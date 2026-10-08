//go:build !mcparceltest

package cmd

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/dedene/mcparcel/internal/config"
)

// errNotAvailable answers a browser request without a display or xdg-open,
// and every approval dialog request: Linux has no approval dialog, so it
// counts as declined.
var errNotAvailable = errors.New("not available on Linux")

// lookPath and getenv are exec.LookPath and os.Getenv; tests replace them.
var (
	lookPath = exec.LookPath
	getenv   = os.Getenv
)

// xdgOpen returns xdg-open's absolute path when a display is set, so a
// browser can open at all.
func xdgOpen() (string, bool) {
	if getenv("DISPLAY") == "" && getenv("WAYLAND_DISPLAY") == "" {
		return "", false
	}
	path, err := lookPath("xdg-open")
	if err != nil || !filepath.IsAbs(path) {
		return "", false
	}
	return path, true
}

// browserOpens reports whether newBrowser can open a browser at all; auth
// login words its prompt by it.
func browserOpens() bool {
	_, ok := xdgOpen()
	return ok
}

// newBrowser opens a sign-in URL that validSignInURL accepts with xdg-open, a
// shell script that older versions passed to a shell unsafely: the URL is one
// argv entry of a strict byte set. It runs in its own session with stdio on
// /dev/null and without the protected variables, and is reaped in the
// background; a browser that keeps running never blocks the sign-in.
func newBrowser(config.Paths) func(context.Context, string) error {
	return func(_ context.Context, raw string) error {
		if err := validSignInURL(raw); err != nil {
			return err
		}
		path, ok := xdgOpen()
		if !ok {
			return errNotAvailable
		}
		cmd := exec.Command(path, raw)
		cmd.Env = browserEnv(os.Environ())
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { _ = cmd.Wait() }()
		return nil
	}
}

// browserEnv is environ without config.ProtectedEnv names.
func browserEnv(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if !config.ProtectedEnv(name) {
			out = append(out, kv)
		}
	}
	return out
}

func newDialog(config.Paths) func(context.Context, []string) (string, error) {
	return func(context.Context, []string) (string, error) { return "", errNotAvailable }
}
