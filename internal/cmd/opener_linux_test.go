//go:build !mcparceltest

package cmd

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/config"
)

// stubOpener points lookPath at path and getenv at env for the test.
func stubOpener(t *testing.T, env map[string]string, path string, err error) {
	t.Helper()
	savedLook, savedEnv := lookPath, getenv
	t.Cleanup(func() { lookPath, getenv = savedLook, savedEnv })
	lookPath = func(string) (string, error) { return path, err }
	getenv = func(k string) string { return env[k] }
}

// A browser opens only with a display and an absolute xdg-open.
func TestBrowserOpens(t *testing.T) {
	display := map[string]string{"DISPLAY": ":0"}
	for _, tc := range []struct {
		env  map[string]string
		path string
		err  error
		want bool
	}{
		{nil, "/usr/bin/xdg-open", nil, false},
		{display, "/usr/bin/xdg-open", nil, true},
		{map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, "/usr/bin/xdg-open", nil, true},
		{display, "", exec.ErrNotFound, false},
		{display, "bin/xdg-open", nil, false},
	} {
		stubOpener(t, tc.env, tc.path, tc.err)
		if got := browserOpens(); got != tc.want {
			t.Error(tc.env, tc.path, got)
		}
	}
	stubOpener(t, nil, "/usr/bin/xdg-open", nil)
	if err := newBrowser(config.Paths{})(context.Background(), "https://as.example/"); !errors.Is(err, errNotAvailable) {
		t.Fatal(err)
	}
}

// Linux has no approval dialog: it reports errNotAvailable and runs nothing.
func TestDialogNotAvailable(t *testing.T) {
	if out, err := newDialog(config.Paths{})(context.Background(), []string{"MCParcel", "Allow?"}); out != "" || !errors.Is(err, errNotAvailable) {
		t.Fatal(out, err)
	}
}

// xdg-open gets the URL as its only argument, /dev/null as stdin, no
// protected variable and its own session; a browser that keeps running does
// not hold up the sign-in. Unsafe URLs never run it.
func TestBrowserRunsXDGOpen(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	script := filepath.Join(dir, "xdg-open")
	body := "#!/bin/sh\n" +
		"{ echo \"pid=$$\"; echo \"argc=$#\"; echo \"arg=$1\"; echo \"stdin=$(readlink /proc/self/fd/0)\"; echo \"sid=$(cut -d' ' -f6 /proc/$$/stat)\"; env; } > " + out + ".tmp\n" +
		"mv " + out + ".tmp " + out + "\n" +
		"exec sleep 30\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	stubOpener(t, map[string]string{"DISPLAY": ":0"}, script, nil)
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "sa-canary")
	t.Setenv("GH_TOKEN", "gh-canary")
	open := newBrowser(config.Paths{})
	if err := open(context.Background(), "https://as.example/$(touch "+filepath.Join(dir, "pwned")+")"); err == nil {
		t.Fatal("unsafe URL accepted")
	}
	const raw = "https://as.example/authorize?state=a-b&redirect_uri=http%3A%2F%2F127.0.0.1%3A1%2F"
	start := time.Now()
	if err := open(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("waited for the browser")
	}
	var data []byte
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		var err error
		if data, err = os.ReadFile(out); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("xdg-open did not run")
		}
	}
	lines := strings.Split(string(data), "\n")
	for _, kv := range lines {
		if k, v, ok := strings.Cut(kv, "="); ok && k == "pid" {
			if pid, err := strconv.Atoi(v); err == nil {
				t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
				if sid := "sid=" + strconv.Itoa(pid); !slices.ContainsFunc(lines, func(l string) bool { return strings.ReplaceAll(l, " ", "") == sid }) {
					t.Error("not its own session", lines)
				}
			}
		}
	}
	for _, want := range []string{"argc=1", "arg=" + raw, "stdin=/dev/null"} {
		if !slices.Contains(lines, want) {
			t.Error("missing", want, lines)
		}
	}
	if strings.Contains(string(data), "sa-canary") || strings.Contains(string(data), "gh-canary") {
		t.Fatal("protected variable passed to xdg-open")
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
		t.Fatal("unsafe URL ran")
	}
}
