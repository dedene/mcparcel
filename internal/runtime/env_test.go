package runtime_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/mcpclient"
	"github.com/dedene/mcparcel/internal/output"
	runtime "github.com/dedene/mcparcel/internal/runtime"
	"github.com/dedene/mcparcel/internal/testutil"
)

func shell(t *testing.T, body string) (string, config.Paths) {
	t.Helper()
	paths, env := testutil.IsolatedPaths(t)
	for _, entry := range env {
		k, v, _ := strings.Cut(entry, "=")
		t.Setenv(k, v)
	}
	file := paths.Home + "/shell"
	if e := os.WriteFile(file, []byte("#!/bin/sh\n"+body+"\n"), 0o700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("SHELL", file)
	return file, paths
}

func errCode(t *testing.T, err error, want string) {
	t.Helper()
	var e *output.Error
	if !errors.As(err, &e) || e.Code != want {
		t.Fatalf("error=%v want %s", err, want)
	}
}

func TestCaptureLoginEnv(t *testing.T) {
	_, p := shell(t, "printf 'banner\\n'; export PATH=/fixture/bin; export FEATURE='line1\nline2=value'; /bin/sh -c \"$3\"; printf 'after\\n'")
	got, e := runtime.CaptureLoginEnv(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if got["PATH"] != "/fixture/bin" || got["HOME"] != p.Home || got["FEATURE"] != "line1\nline2=value" {
		t.Fatal("dump not preserved")
	}
}

func TestCaptureIgnoresFakeBanner(t *testing.T) {
	shell(t, "printf 'PATH=/wrong\\n\\000MCP-BEGIN-static\\000'; export PATH=/correct; /bin/sh -c \"$3\"")
	got, e := runtime.CaptureLoginEnv(context.Background())
	if e != nil || got["PATH"] != "/correct" {
		t.Fatal("banner parsed")
	}
}

func TestCaptureFailureFallback(t *testing.T) {
	for name, body := range map[string]string{"exit": "exit 1", "malformed": "printf '\\000bad\\000'", "overflow": "/usr/bin/head -c 1048577 /dev/zero"} {
		t.Run(name, func(t *testing.T) {
			shell(t, body)
			t.Setenv("PATH", "/caller/path")
			t.Setenv("SECRET", "MUST-NOT-LEAK")
			got, e := runtime.CaptureLoginEnv(context.Background())
			if !errors.Is(e, runtime.ErrLoginEnvUnavailable) || got["PATH"] != "/caller/path" || got["SECRET"] != "" || strings.Contains(e.Error(), "MUST-NOT-LEAK") {
				t.Fatal("unsafe fallback")
			}
		})
	}
}

func TestCaptureCancellation(t *testing.T) {
	_, p := shell(t, "/bin/sleep 60 &\nprintf '%s' \"$!\" > \"$HOME/child-pid\"\nwait")
	c, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		env map[string]string
		err error
	}
	done := make(chan result, 1)
	go func() { env, err := runtime.CaptureLoginEnv(c); done <- result{env, err} }()
	readyDeadline := time.NewTimer(3 * time.Second)
	defer readyDeadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	var raw []byte
	for len(raw) == 0 {
		raw, _ = os.ReadFile(p.Home + "/child-pid")
		if len(raw) > 0 {
			break
		}
		select {
		case r := <-done:
			t.Fatalf("capture ended before barrier: %v", r.err)
		case <-readyDeadline.C:
			t.Fatal("shell did not reach barrier")
		case <-tick.C:
		}
	}
	start := time.Now()
	timer := time.AfterFunc(100*time.Millisecond, cancel)
	defer timer.Stop()
	r := <-done
	if !errors.Is(r.err, runtime.ErrLoginEnvUnavailable) || r.env["HOME"] != p.Home || time.Since(start) > time.Second {
		t.Fatal("unbounded cancellation")
	}
	pid, _ := strconv.Atoi(string(raw))
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for syscall.Kill(pid, 0) != syscall.ESRCH {
		select {
		case <-deadline.C:
			t.Fatal("capture descendant survived")
		case <-tick.C:
		}
	}
}

func TestBuildChildBase(t *testing.T) {
	login := map[string]string{"PATH": "/login/bin", "HOME": "/disposable", "LANG": "C", "SECRET": "no"}
	got, e := runtime.BuildChildEnv(login, config.Connection{Transport: config.Transport{Stdio: &config.Stdio{}}}, nil)
	if e != nil || !reflect.DeepEqual(got, map[string]string{"PATH": "/login/bin", "HOME": "/disposable", "LANG": "C"}) {
		t.Fatal("incorrect base")
	}
	got, e = runtime.BuildChildEnv(login, config.Connection{Transport: config.Transport{HTTP: &config.HTTP{}}}, nil)
	if e != nil || len(got) != 0 {
		t.Fatal("HTTP child environment")
	}
}

func TestExplicitInheritance(t *testing.T) {
	for _, tt := range []struct{ name, code string }{{"FEATURE", ""}, {"MISSING", "config_required"}, {"GH_TOKEN", "invalid_config"}} {
		t.Run(tt.name, func(t *testing.T) {
			got, e := runtime.BuildChildEnv(map[string]string{"FEATURE": "enabled", "GH_TOKEN": "no"}, config.Connection{Transport: config.Transport{Stdio: &config.Stdio{InheritEnv: []string{tt.name}}}}, nil)
			if tt.code != "" {
				errCode(t, e, tt.code)
			} else if e != nil || got["FEATURE"] != "enabled" {
				t.Fatal("inheritance failed")
			}
		})
	}
}

func TestNoTokenInChildEnvironment(t *testing.T) {
	login := map[string]string{"HOME": "/temp", "OP_SERVICE_ACCOUNT_TOKEN": "no", "GH_TOKEN": "no", "SECRET": "no"}
	before := map[string]string{}
	for k, v := range login {
		before[k] = v
	}
	got, e := runtime.BuildChildEnv(login, config.Connection{Transport: config.Transport{Stdio: &config.Stdio{}}}, nil)
	if e != nil || len(got) != 1 || !reflect.DeepEqual(login, before) {
		t.Fatal("secret inherited or mutated")
	}
	v := "forbidden"
	_, e = runtime.BuildChildEnv(login, config.Connection{Transport: config.Transport{Stdio: &config.Stdio{Env: map[string]config.Value{"OP_SERVICE_ACCOUNT_TOKEN": {Literal: &v}}}}}, nil)
	errCode(t, e, "invalid_config")
}

func TestConnectionSecretIsolation(t *testing.T) {
	login := map[string]string{"PATH": "/bin"}
	a := config.Connection{Transport: config.Transport{Stdio: &config.Stdio{Env: map[string]config.Value{"API_KEY": {Secret: &config.SecretRef{Secret: "op://a/b/c", Prefix: "Bearer ", Suffix: "!"}}}}}}
	got, e := runtime.BuildChildEnv(login, a, map[string]string{"op://a/b/c": "value"})
	if e != nil || got["API_KEY"] != "Bearer value!" {
		t.Fatal("binding failed")
	}
	b, e := runtime.BuildChildEnv(login, config.Connection{Transport: config.Transport{Stdio: &config.Stdio{}}}, nil)
	if e != nil || b["API_KEY"] != "" || login["API_KEY"] != "" {
		t.Fatal("cross connection leak")
	}
	_, e = runtime.BuildChildEnv(login, a, nil)
	errCode(t, e, "auth_failed")
}

func TestChildPathFromLoginShell(t *testing.T) {
	_, p := shell(t, "export PATH=\"$HOME/bin\"; /bin/sh -c \"$3\"")
	dir := p.Home + "/bin"
	if e := os.Mkdir(dir, 0o700); e != nil {
		t.Fatal(e)
	}
	file := dir + "/fixture"
	if e := os.WriteFile(file, []byte("#!/bin/sh\nexit 0\n"), 0o700); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{"/harness/a", "/harness/b"} {
		t.Setenv("PATH", path)
		login, e := runtime.CaptureLoginEnv(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		child, e := runtime.BuildChildEnv(login, config.Connection{Transport: config.Transport{Stdio: &config.Stdio{}}}, nil)
		if e != nil {
			t.Fatal(e)
		}
		found, e := mcpclient.Executable("fixture", child["PATH"], p.Home)
		if e != nil || found != filepath.Join(dir, "fixture") {
			t.Fatal("harness PATH used")
		}
	}
}

func TestCaptureReapsDescendantsOnEveryCompletion(t *testing.T) {
	for _, mode := range []string{"success", "exit", "wait-delay"} {
		t.Run(mode, func(t *testing.T) {
			redirect := ">/dev/null 2>&1"
			if mode == "wait-delay" {
				redirect = ""
			}
			body := "/bin/sleep 60 " + redirect + " &\nprintf '%s' \"$!\" > \"$HOME/child-pid\"\n"
			if mode == "exit" {
				body += "exit 1"
			} else {
				body += "/bin/sh -c \"$3\""
			}
			_, p := shell(t, body)
			_, err := runtime.CaptureLoginEnv(context.Background())
			if mode == "success" && err != nil {
				t.Fatal(err)
			}
			raw, e := os.ReadFile(p.Home + "/child-pid")
			if e != nil {
				t.Fatal(e)
			}
			pid, _ := strconv.Atoi(string(raw))
			defer syscall.Kill(pid, syscall.SIGKILL)
			tick := time.NewTicker(time.Millisecond)
			defer tick.Stop()
			deadline := time.NewTimer(time.Second)
			defer deadline.Stop()
			for syscall.Kill(pid, 0) != syscall.ESRCH {
				select {
				case <-tick.C:
				case <-deadline.C:
					t.Fatal("capture descendant survived completion")
				}
			}
		})
	}
}
