package runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"maps"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

var ErrLoginEnvUnavailable = errors.New("login shell environment unavailable")

type captureBuffer struct {
	bytes.Buffer
	overflow bool
}

func (b *captureBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (1 << 20) - b.Len()
	if len(p) > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

func fallbackEnv() map[string]string {
	out := map[string]string{}
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "USER", "LOGNAME", "SHELL", "__CF_USER_TEXT_ENCODING"} {
		if value, ok := os.LookupEnv(key); ok {
			out[key] = value
		}
	}
	if out["PATH"] == "" {
		out["PATH"] = defaultPath
	}
	return out
}

func CaptureLoginEnv(ctx context.Context) (map[string]string, error) {
	fallback := fallbackEnv()
	fail := func() (map[string]string, error) { return fallback, ErrLoginEnvUnavailable }
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = defaultShell
	}
	if !filepath.IsAbs(shell) {
		return fail()
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fail()
	}
	token := hex.EncodeToString(nonce[:])
	begin, end := "MCP-BEGIN-"+token, "MCP-END-"+token
	script := "printf '\\0%s\\0' '" + begin + "'; /usr/bin/env -0; printf '\\0%s\\0' '" + end + "'"
	captureCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(captureCtx, shell, "-l", "-c", script)
	cmd.Dir = fallback["HOME"]
	cmd.Env = []string{"PATH=" + defaultPath, "SHELL=" + shell}
	for _, key := range []string{"HOME", "TMPDIR", "USER", "LOGNAME", "LANG", "LC_ALL"} {
		if value, ok := fallback[key]; ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 100 * time.Millisecond
	var stdout captureBuffer
	cmd.Stdout, cmd.Stderr = &stdout, io.Discard
	err := cmd.Run()
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if err != nil || stdout.overflow {
		return fail()
	}
	bmark, emark := []byte("\x00"+begin+"\x00"), []byte("\x00"+end+"\x00")
	raw := stdout.Bytes()
	if bytes.Count(raw, bmark) != 1 || bytes.Count(raw, emark) != 1 {
		return fail()
	}
	start := bytes.Index(raw, bmark) + len(bmark)
	finish := bytes.Index(raw, emark)
	if finish < start {
		return fail()
	}
	env := map[string]string{}
	for _, entry := range bytes.Split(raw[start:finish], []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		key, value, ok := strings.Cut(string(entry), "=")
		if !ok || key == "" {
			return fail()
		}
		env[key] = value
	}
	if env["PATH"] == "" || env["HOME"] == "" {
		return fail()
	}
	return env, nil
}

func BuildChildEnv(login map[string]string, c config.Connection, resolved map[string]string) (map[string]string, error) {
	out := map[string]string{}
	if c.Transport.Stdio == nil {
		return out, nil
	}
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "USER", "LOGNAME", "SHELL", "__CF_USER_TEXT_ENCODING"} {
		if value, ok := login[key]; ok {
			out[key] = value
		}
	}
	for _, key := range c.Transport.Stdio.InheritEnv {
		if config.ProtectedEnv(key) {
			return nil, output.NewError("invalid_config", nil)
		}
		value, ok := login[key]
		if !ok {
			return nil, output.NewError("config_required", nil)
		}
		out[key] = value
	}
	for key, binding := range c.Transport.Stdio.Env {
		if forbiddenChildEnv(key) {
			return nil, output.NewError("invalid_config", nil)
		}
		if (binding.Literal == nil) == (binding.Secret == nil) {
			return nil, output.NewError("invalid_config", nil)
		}
		if binding.Literal != nil {
			out[key] = *binding.Literal
		} else {
			value, ok := resolved[binding.Secret.Secret]
			if !ok {
				return nil, output.NewError("auth_failed", nil)
			}
			out[key] = binding.Secret.Prefix + value + binding.Secret.Suffix
		}
	}
	return out, nil
}

func forbiddenChildEnv(key string) bool {
	switch key {
	case "OP_SERVICE_ACCOUNT_TOKEN", "OP_CONNECT_TOKEN", "BASH_ENV", "ENV", "ZDOTDIR":
		return true
	}
	return strings.HasPrefix(key, "DYLD_") || strings.HasPrefix(key, "LD_")
}

// envRefValues adds values for env: references from the captured login
// environment, falling back to a Keychain generic password named after the
// variable (temporary bridge; macOS only, keychain is nil elsewhere). In
// headless mode login is the daemon's own environment, the Keychain is never
// consulted and the error names every missing variable. Values never enter
// errors or logs.
func envRefValues(ctx context.Context, login map[string]string, keychain func(context.Context, string) (string, error), headless bool, c config.Connection, resolved map[string]string) (map[string]string, error) {
	names := config.EnvRefs(c)
	if len(names) == 0 {
		return resolved, nil
	}
	out := maps.Clone(resolved)
	if out == nil {
		out = map[string]string{}
	}
	var missing []string
	for _, name := range names {
		value := login[name]
		if headless {
			if value == "" {
				missing = append(missing, name)
			} else {
				out["env:"+name] = value
			}
			continue
		}
		if value == "" && keychain != nil {
			if v, err := keychain(ctx, name); err == nil {
				value = v
			}
		}
		if value == "" && keychain == nil {
			return nil, loginEnvMissing(name)
		}
		if value == "" {
			err := output.NewError("config_required", nil)
			err.Message = "Environment variable " + name + " is not set in the daemon's login environment and the Keychain has no generic password named " + name + "."
			err.NextAction = "Export " + name + " where your login shell reads it (zsh: ~/.zprofile or ~/.zshenv, not ~/.zshrc), or store it with security add-generic-password -a \"$USER\" -s " + name + " -w (prompts for the value), then run mcparcel runtime restart. If mcparcel runtime status shows Environment: caller fallback, the login-shell capture failed; fix that first."
			return nil, err
		}
		out["env:"+name] = value
	}
	if len(missing) > 0 {
		return nil, headlessMissingEnv(missing)
	}
	return out, nil
}

// loginEnvMissing is config_required for an env: reference missing from the
// login environment where no Keychain fallback exists (Linux desktop mode).
func loginEnvMissing(name string) *output.Error {
	err := output.NewError("config_required", nil)
	err.Message = "Environment variable " + name + " is not set in the daemon's login environment."
	err.NextAction = "Export " + name + " where your login shell reads it (bash: ~/.profile or ~/.bash_profile; zsh: ~/.zprofile), then run mcparcel runtime restart. If mcparcel runtime status shows Environment: caller fallback, the login-shell capture failed; fix that first."
	return err
}

var errKeychain = errors.New("keychain lookup failed")

// KeychainLookup reads the generic password with service name and the
// current user as account, as the owner's mcporter wrapper does.
func KeychainLookup(ctx context.Context, name string) (string, error) {
	u, err := user.Current()
	if err != nil || u.Username == "" {
		return "", errKeychain
	}
	return keychainRead(ctx, securityBin, u.Username, name)
}

// securityBin is the Keychain tool. Items it creates trust it, not mcparcel,
// so a new mcparcel binary or signature reads them without a prompt.
const securityBin = "/usr/bin/security"

// keychainRead never surfaces stderr or exit details; only success matters.
func keychainRead(ctx context.Context, bin, account, service string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "find-generic-password", "-a", account, "-s", service, "-w")
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 100 * time.Millisecond
	out, err := cmd.Output()
	if err != nil {
		return "", errKeychain
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}
