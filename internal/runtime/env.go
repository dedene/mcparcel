package runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
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
		out["PATH"] = "/usr/bin:/bin:/usr/sbin:/sbin"
	}
	return out
}

func CaptureLoginEnv(ctx context.Context) (map[string]string, error) {
	fallback := fallbackEnv()
	fail := func() (map[string]string, error) { return fallback, ErrLoginEnvUnavailable }
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
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
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "SHELL=" + shell}
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
