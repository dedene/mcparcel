package mcpclient_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/mcpclient"
	"github.com/dedene/mcparcel/internal/output"
	"github.com/dedene/mcparcel/internal/testutil"
)

func TestMain(m *testing.M) {
	if os.Getenv("MCP_TEST_STDIO") == "1" {
		mode := os.Getenv("MCP_TEST_MODE")
		if mode == "stall" {
			if path := os.Getenv("MCP_TEST_PID"); path != "" {
				_ = os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600)
			}
			signals := make(chan os.Signal, 1)
			signal.Notify(signals, syscall.SIGTERM)
			<-signals
			return
		}
		if mode == "group" || mode == "group_exit" {
			if mode == "group" {
				signal.Ignore(syscall.SIGTERM)
			}
			readyReader, readyWriter, err := os.Pipe()
			if err != nil {
				os.Exit(2)
			}
			child := exec.Command("/bin/sh", "-c", "trap '' TERM; printf x >&3; exec /bin/sleep 60")
			child.ExtraFiles = []*os.File{readyWriter}
			child.Env = []string{}
			for _, name := range []string{"HOME", "TMPDIR", "SHELL", "PATH", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "MCPARCEL_RUNTIME_DIR"} {
				child.Env = append(child.Env, name+"="+os.Getenv(name))
			}
			if err := child.Start(); err != nil {
				os.Exit(2)
			}
			_ = readyWriter.Close()
			var mark [1]byte
			if _, err := io.ReadFull(readyReader, mark[:]); err != nil {
				os.Exit(2)
			}
			_ = readyReader.Close()
			_ = os.WriteFile(os.Getenv("MCP_TEST_PID"), []byte(strconv.Itoa(child.Process.Pid)), 0o600)
			go func() { _ = child.Wait() }()
		}
		fmt.Fprintln(os.Stderr, "DIAGNOSTIC-SENTINEL")
		opts := testutil.FixtureOptions{OnWrite: func() {
			path := os.Getenv("MCP_TEST_MARKER")
			previous, _ := os.ReadFile(path)
			count, _ := strconv.Atoi(string(previous))
			_ = os.WriteFile(path, []byte(strconv.Itoa(count+1)), 0o600)
		}, Crash: func() { os.Exit(0) }}
		if e := testutil.NewFixtureServerWithOptions(opts).Run(context.Background(), &mcp.StdioTransport{}); e != nil {
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}

func stdioOpts(t *testing.T, extra map[string]string) mcpclient.ConnectOptions {
	t.Helper()
	paths, env := testutil.IsolatedPaths(t)
	values := map[string]string{}
	for _, entry := range env {
		k, v, _ := strings.Cut(entry, "=")
		values[k] = v
	}
	values["MCP_TEST_STDIO"] = "1"
	for k, v := range extra {
		values[k] = v
	}
	return mcpclient.ConnectOptions{Connection: config.Connection{Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal(os.Args[0])}}}, Home: paths.Home, Env: values, ShutdownTimeout: 50 * time.Millisecond}
}

func stdioSession(t *testing.T, extra map[string]string) mcpclient.Session {
	t.Helper()
	s, e := mcpclient.Connect(ctx(t), stdioOpts(t, extra))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = s.Close(ctx(t)) })
	return s
}

func TestExecutableUsesChildPATH(t *testing.T) {
	paths, _ := testutil.IsolatedPaths(t)
	a, b := paths.Home+"/a", paths.Home+"/b"
	for _, dir := range []string{a, b} {
		if e := os.Mkdir(dir, 0o700); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.WriteFile(a+"/fixture", []byte("#!/bin/sh\nexit 1\n"), 0o700); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(os.Args[0], b+"/fixture"); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", a)
	opts := stdioOpts(t, nil)
	opts.Connection.Transport.Stdio.Command = config.Literal("fixture")
	opts.Env["PATH"] = b
	s, e := mcpclient.Connect(ctx(t), opts)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close(ctx(t))
	call(t, s, "counter", nil)
	for _, command := range []string{"./fixture", "missing"} {
		_, e := mcpclient.Executable(command, ".:"+a, paths.Home)
		code(t, e, "connection_failed")
	}
	got, e := mcpclient.Executable("fixture", ":"+b, paths.Home)
	if e != nil || got != filepath.Join(b, "fixture") {
		t.Fatal("lookup mismatch")
	}
}

func TestOwnedProcessShutdown(t *testing.T) {
	for _, mode := range []string{"group", "group_exit"} {
		t.Run(mode, func(t *testing.T) {
			paths, env := testutil.IsolatedPaths(t)
			other := exec.Command("/bin/sleep", "60")
			other.Env = env
			if e := other.Start(); e != nil {
				t.Fatal(e)
			}
			defer func() { _ = other.Process.Kill(); _ = other.Wait() }()
			pidfile := paths.Home + "/pid"
			s := stdioSession(t, map[string]string{"MCP_TEST_MODE": mode, "MCP_TEST_PID": pidfile})
			data, e := os.ReadFile(pidfile)
			if e != nil {
				t.Fatal(e)
			}
			pid, _ := strconv.Atoi(string(data))
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
			start := time.Now()
			done := make(chan error, 2)
			for range 2 {
				go func() { done <- s.Close(ctx(t)) }()
			}
			for range 2 {
				if e := <-done; e != nil {
					t.Fatal(e)
				}
			}
			if time.Since(start) > time.Second {
				t.Fatal("slow shutdown")
			}
			if e := syscall.Kill(other.Process.Pid, 0); e != nil {
				t.Fatal("unrelated process stopped")
			}
			deadline := time.NewTimer(time.Second)
			defer deadline.Stop()
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for syscall.Kill(pid, 0) != syscall.ESRCH {
				select {
				case <-deadline.C:
					t.Fatal("descendant survived")
				case <-ticker.C:
				}
			}
		})
	}
}

func TestConnectTimeout(t *testing.T) {
	opts := stdioOpts(t, map[string]string{"MCP_TEST_MODE": "stall"})
	opts.Env["MCP_TEST_PID"] = opts.Home + "/pid"
	opts.ConnectTimeout = 100 * time.Millisecond
	start := time.Now()
	_, e := mcpclient.Connect(ctx(t), opts)
	code(t, e, "timeout")
	if data, err := os.ReadFile(opts.Env["MCP_TEST_PID"]); err == nil {
		pid, _ := strconv.Atoi(string(data))
		if syscall.Kill(pid, 0) != syscall.ESRCH {
			t.Fatal("initialization child survived")
		}
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("timeout not bounded")
	}
}

func TestAdapterDiagnosticsDiscarded(t *testing.T) {
	s := stdioSession(t, nil)
	r, e := s.Call(ctx(t), "write_drop", nil, nil)
	code(t, e, "outcome_unknown")
	if strings.Contains(e.Error()+string(r.JSON), "DIAGNOSTIC-SENTINEL") {
		t.Fatal("stderr leaked")
	}
}

func TestOwnedProcessShutdownUsesCallerDeadline(t *testing.T) {
	opts := stdioOpts(t, map[string]string{"MCP_TEST_MODE": "group"})
	opts.Env["MCP_TEST_PID"] = opts.Home + "/pid"
	opts.ShutdownTimeout = 2 * time.Second
	s, err := mcpclient.Connect(ctx(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx(t))
	deadline, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stop()
	start := time.Now()
	_ = s.Close(deadline)
	if time.Since(start) > time.Second {
		t.Fatal("owned process ignored shared shutdown deadline")
	}
}

func TestAdapterRejectsUnresolvedExecutable(t *testing.T) {
	paths, _ := testutil.IsolatedPaths(t)
	marker := paths.Home + "/spawned"
	if err := os.WriteFile(paths.Home+"/launch", []byte("#!/bin/sh\n touch "+marker+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	mixedCommand := config.Literal(paths.Home + "/launch")
	mixedCommand.Input = &config.InputRef{Input: "command"}
	mixedURL := config.Literal(server.URL)
	mixedURL.Input = &config.InputRef{Input: "url"}
	for _, transport := range []config.Transport{
		{Stdio: &config.Stdio{Command: config.Value{Input: &config.InputRef{Input: "command"}}}},
		{HTTP: &config.HTTP{URL: config.Value{Input: &config.InputRef{Input: "url"}}}},
		{Stdio: &config.Stdio{Command: mixedCommand}},
		{HTTP: &config.HTTP{URL: mixedURL}},
	} {
		_, err := mcpclient.Connect(context.Background(), mcpclient.ConnectOptions{Connection: config.Connection{Transport: transport}, Home: paths.Home})
		var safe *output.Error
		if !errors.As(err, &safe) || safe.Code != "config_required" {
			t.Fatalf("%v", err)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("HTTP request sent")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("process spawned")
	}
}
