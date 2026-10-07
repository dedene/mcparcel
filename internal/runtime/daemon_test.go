package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dedene/mcparcel/internal/args"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/mcpclient"
	"github.com/dedene/mcparcel/internal/output"
	"github.com/dedene/mcparcel/internal/testutil"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "mcp-lock-fixture" {
		os.Exit(runLockFixture())
	}
	if len(os.Args) > 1 && os.Args[1] == "mcp-fixture" {
		if path := os.Getenv("MCP_TEST_CHILD_PID"); path != "" {
			_ = os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600)
		}
		if e := testutil.NewFixtureServer().Run(context.Background(), &mcp.StdioTransport{}); e != nil {
			os.Exit(2)
		}
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "daemon" {
		p, e := config.ResolvePaths(os.Getenv, os.Getenv("HOME"), testutil.TempRoot(), os.Getuid())
		if e != nil {
			os.Exit(2)
		}
		lock, e := AdoptLock(p, 3)
		if e != nil {
			os.Exit(2)
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
		defer stop()
		if marker := os.Getenv("MCP_TEST_START"); marker != "" {
			f, _ := os.OpenFile(marker, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if f != nil {
				_, _ = fmt.Fprintln(f, os.Getpid())
				_ = f.Close()
			}
		}
		if barrier := os.Getenv("MCP_TEST_BARRIER"); barrier != "" {
			f, e := os.Open(barrier)
			if e != nil {
				os.Exit(2)
			}
			_, _ = io.Copy(io.Discard, f)
			_ = f.Close()
		}
		login, e := CaptureLoginEnv(ctx)
		log, le := OpenLog(p)
		if le != nil {
			os.Exit(2)
		}
		defer func() { _ = log.Close() }()
		v := os.Getenv("MCP_TEST_VERSION")
		if v == "" {
			v = "dev"
		}
		h := &daemonHandler{}
		if marker := os.Getenv("MCP_TEST_CHILD_PID"); marker != "" {
			env := map[string]string{}
			for _, key := range []string{"HOME", "TMPDIR", "SHELL", "PATH", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "MCPARCEL_RUNTIME_DIR", "MCP_TEST_CHILD_PID"} {
				env[key] = os.Getenv(key)
			}
			exe, _ := os.Executable()
			session, e := mcpclient.Connect(ctx, mcpclient.ConnectOptions{Connection: config.Connection{Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal(exe), Args: []config.Value{config.Literal("mcp-lock-fixture")}}}}, Env: env, Home: p.Home, ShutdownTimeout: 100 * time.Millisecond})
			if e != nil {
				os.Exit(2)
			}
			defer func() {
				closeCtx, c := context.WithTimeout(context.Background(), time.Second)
				defer c()
				_ = session.Close(closeCtx)
			}()
		}
		self, _ := os.Executable()
		if e = Serve(ctx, DaemonOptions{Paths: p, Version: v, Executable: self, Lock: lock, LoginEnv: login, EnvFallback: e != nil, Handler: h, Log: log}); e != nil {
			os.Exit(2)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "join-helper" {
		p, e := config.ResolvePaths(os.Getenv, os.Getenv("HOME"), testutil.TempRoot(), os.Getuid())
		if e != nil {
			os.Exit(2)
		}
		ctx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		exe, _ := os.Executable()
		if _, e = startPlain(ctx, p, exe, os.Environ()); e != nil {
			_, _ = fmt.Fprintf(os.Stderr, "helper start: %T\n", e)
			os.Exit(2)
		}
		cl := Client{Paths: p, Version: "dev", Executable: exe}
		if e = cl.Ensure(ctx); e != nil {
			_, _ = fmt.Fprintf(os.Stderr, "helper ensure: %T\n", e)
			os.Exit(2)
		}
		s, e := cl.Status(ctx)
		if e != nil {
			_, _ = fmt.Fprintf(os.Stderr, "helper status: %T\n", e)
			os.Exit(2)
		}
		_, _ = fmt.Fprintln(os.Stdout, s.PID)
		return
	}
	os.Exit(m.Run())
}

type daemonHandler struct {
	active  atomic.Int32
	started chan struct{}
	release chan struct{}
	big     bool
	mu      sync.Mutex
	cancels []context.CancelFunc
	shut    atomic.Int32
}

func (h *daemonHandler) Active() int { return int(h.active.Load()) }
func (h *daemonHandler) Handle(ctx context.Context, id string, r Request, before func() error) Response {
	ctx, c := context.WithCancel(ctx)
	defer c()
	h.mu.Lock()
	h.cancels = append(h.cancels, c)
	h.mu.Unlock()
	h.active.Add(1)
	defer h.active.Add(-1)
	if e := before(); e != nil {
		return Response{Error: output.NewError("canceled", nil)}
	}
	if h.started != nil {
		select {
		case h.started <- struct{}{}:
		case <-ctx.Done():
		}
	}
	if h.release != nil {
		select {
		case <-h.release:
		case <-ctx.Done():
			return Response{Dispatched: true, Error: output.NewError("canceled", &output.Details{RequestID: id, Dispatched: true, Outcome: "unknown"})}
		}
	}
	result := json.RawMessage(`{"content":[]}`)
	if h.big && r.Tool == "rich" {
		result, _ = json.Marshal(map[string]string{"text": strings.Repeat("x", MaxFrameBytes-2048)})
	}
	b, _ := json.Marshal(output.CallData{Connection: r.Connection, Tool: r.Tool, Result: result})
	return Response{Data: b, Dispatched: true}
}

func (h *daemonHandler) Shutdown(ctx context.Context, force bool) error {
	if h.Active() > 0 && !force {
		return output.NewError("runtime_busy", nil)
	}
	h.mu.Lock()
	for _, c := range h.cancels {
		c()
	}
	h.mu.Unlock()
	h.shut.Add(1)
	return nil
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, c := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(c)
	return ctx
}

func lockFile(t *testing.T, p config.Paths) *os.File {
	t.Helper()
	dir, e := config.OpenPrivateDir(p.RuntimeDir, true)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = dir.Close() }()
	f, e := config.OpenPrivateFile(dir, "daemon.lock", true)
	if e != nil {
		t.Fatal(e)
	}
	if e = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		t.Fatal(e)
	}
	return f
}

func service(t *testing.T, h Handler, idle time.Duration) (*Client, <-chan error) {
	t.Helper()
	return serviceWith(t, DaemonOptions{Handler: h, IdleTimeout: idle})
}

// serviceWith serves o's handler, idle and prompt timeouts in-process.
func serviceWith(t *testing.T, o DaemonOptions) (*Client, <-chan error) {
	t.Helper()
	t.Setenv("SHELL", "/bin/sh")
	p, _ := testutil.IsolatedPaths(t)
	ctx, c := context.WithCancel(context.Background())
	done := make(chan error, 1)
	o.Paths, o.Version, o.Lock, o.LoginEnv, o.ShutdownTimeout = p, "dev", lockFile(t, p), map[string]string{"PATH": "/fixture"}, time.Second
	go func() {
		done <- Serve(ctx, o)
		close(done)
	}()
	cl := &Client{Paths: p, Version: "dev"}
	waitStatus(t, cl)
	t.Cleanup(func() {
		c()
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(5 * time.Second):
			t.Error("service cleanup timeout")
		}
	})
	return cl, done
}

func waitStatus(t *testing.T, c *Client) Status {
	t.Helper()
	ctx := testCtx(t)
	for {
		s, e := c.Status(ctx)
		if e == nil && s.Running {
			return s
		}
		if e != nil {
			var oe *output.Error
			if !errorsAs(e, &oe) || oe.Code != "runtime_start_failed" {
				t.Fatal(e)
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("not ready")
		case <-time.After(5 * time.Millisecond):
		}
	}
}
func errorsAs(e error, dst any) bool { return errors.As(e, dst) }
func callReq() CallRequest {
	return CallRequest{Connection: "local:fixture", Tool: "wait", Arguments: args.Raw{Values: map[string]args.Value{}}}
}

func TestStatusDoesNotStart(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	p.RuntimeDir = filepath.Join(p.Home, "absent")
	p.SocketFile = filepath.Join(p.RuntimeDir, "daemon.sock")
	p.LockFile = filepath.Join(p.RuntimeDir, "daemon.lock")
	s, e := (&Client{Paths: p, Version: "dev"}).Status(testCtx(t))
	if e != nil || s.Running || s.PID != 0 || !s.Compatible || s.StartedAt != nil {
		t.Fatalf("%+v %v", s, e)
	}
	if _, e = os.Stat(p.RuntimeDir); !os.IsNotExist(e) {
		t.Fatal("status created directory")
	}
}

func TestRestartRefusesActive(t *testing.T) {
	h := &daemonHandler{started: make(chan struct{}, 1), release: make(chan struct{})}
	c, _ := service(t, h, 0)
	pid := waitStatus(t, c).PID
	done := make(chan error, 1)
	go func() { _, e := c.Call(testCtx(t), callReq()); done <- e }()
	<-h.started
	_, e := c.Restart(testCtx(t), false)
	wantCode(t, e, "runtime_busy")
	close(h.release)
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if waitStatus(t, c).PID != pid {
		t.Fatal("changed pid")
	}
}

func TestRestartForceUnknown(t *testing.T) {
	h := &daemonHandler{started: make(chan struct{}, 1), release: make(chan struct{})}
	c, _ := service(t, h, 0)
	c.Executable, _ = os.Executable()
	done := make(chan error, 1)
	go func() { _, e := c.Call(testCtx(t), callReq()); done <- e }()
	<-h.started
	r, e := c.Restart(testCtx(t), true)
	if e != nil || !r.Restarted {
		t.Fatalf("%+v %v", r, e)
	}
	wantCode(t, <-done, "outcome_unknown")
	if h.shut.Load() != 1 {
		t.Fatal("shutdown not complete")
	}
	cleanupDaemon(t, c)
}

func TestDaemonIdleExit(t *testing.T) {
	h := &daemonHandler{started: make(chan struct{}, 1), release: make(chan struct{})}
	c, stopped := service(t, h, 50*time.Millisecond)
	done := make(chan error, 1)
	go func() { _, e := c.Call(testCtx(t), callReq()); done <- e }()
	<-h.started
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	<-timer.C
	if s, e := c.Status(testCtx(t)); e != nil || !s.Running {
		t.Fatal("exited during work", e)
	}
	close(h.release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-stopped:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("idle did not stop")
	}
}

func TestLogBoundAndPrivacy(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	w, e := OpenLog(p)
	if e != nil {
		t.Fatal(e)
	}
	for range 20000 {
		if e = WriteLog(w, "daemon_started", ptrString(strings.Repeat("x", 100))); e != nil {
			t.Fatal(e)
		}
	}
	for _, event := range []string{"SECRET-upstream-error", "auth_failed"} {
		e = WriteLog(w, event, ptrString("secret"))
		if e == nil {
			t.Fatal("invalid log accepted")
		}
	}
	if e = w.Close(); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(p.LogFile)
	if e != nil || len(b) > 1<<20 || bytes.Contains(b, []byte("secret")) {
		t.Fatalf("log %d %v", len(b), e)
	}
	st, _ := os.Stat(p.LogFile)
	if st.Mode().Perm() != 0o600 {
		t.Fatal(st.Mode())
	}
}
func ptrString(s string) *string { return &s }
func TestSlowIPCReader(t *testing.T) {
	h := &daemonHandler{big: true, started: make(chan struct{}, 2)}
	c, _ := service(t, h, 0)
	conn, e := net.DialUnix("unix", nil, &net.UnixAddr{Name: c.Paths.SocketFile, Net: "unix"})
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = conn.Close() }()
	_, e = ClientHandshake(conn, "dev", "work", testID, configRoot(c.Paths.ConfigDir))
	if e != nil {
		t.Fatal(e)
	}
	_ = WriteFrame(conn, frame("request", Request{Method: "call", Connection: "fixture", Tool: "rich", Arguments: args.Raw{Values: map[string]args.Value{}}}))
	if f, e := ReadFrame(conn); e != nil || f.Kind != "dispatch" {
		t.Fatal(f, e)
	}
	<-h.started
	var header [4]byte
	if _, e = io.ReadFull(conn, header[:]); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Call(testCtx(t), CallRequest{Connection: "fixture", Tool: "counter", Arguments: emptyArgs()}); e != nil {
		t.Fatal(e)
	}

	s, e := c.Status(testCtx(t))
	if e != nil || !s.Running {
		t.Fatal(e)
	}
	c.Executable, _ = os.Executable()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if _, e = c.Restart(ctx, true); e != nil {
		t.Fatal(e)
	}
	cleanupDaemon(t, c)
}

func TestCallIPCDisconnectUnknown(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	l, e := net.ListenUnix("unix", &net.UnixAddr{Name: p.SocketFile, Net: "unix"})
	if e != nil {
		t.Fatal(e)
	}
	_ = os.Chmod(p.SocketFile, 0o600)
	defer func() { _ = l.Close() }()
	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 2 {
			conn, e := l.AcceptUnix()
			if e != nil {
				return
			}
			_, _, e = ServerHandshake(conn, "dev", 1, configRoot(p.ConfigDir))
			if e == nil {
				f, e := ReadFrame(conn)
				if e == nil && f.Kind == "request" {
					calls.Add(1)
				}
			}
			_ = conn.Close()
		}
	}()
	c := Client{Paths: p, Version: "dev"}
	r, e := c.Call(testCtx(t), callReq())
	wantCode(t, e, "outcome_unknown")
	var oe *output.Error
	_ = errors.As(e, &oe)
	if len(r.RequestID) != 32 || oe.Details == nil || !oe.Details.Dispatched {
		t.Fatal("missing uncertainty", r, e)
	}
	<-done
	if calls.Load() != 1 {
		t.Fatal("replayed")
	}
}

func TestStatusHeldUnresponsive(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	lock := lockFile(t, p)
	defer func() { _ = lock.Close() }()
	l, e := net.ListenUnix("unix", &net.UnixAddr{Name: p.SocketFile, Net: "unix"})
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = l.Close() }()
	_ = os.Chmod(p.SocketFile, 0o600)
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, e := l.AcceptUnix()
		if e != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if _, _, e = ServerHandshake(conn, "dev", 1, configRoot(p.ConfigDir)); e != nil {
			return
		}
		for {
			if _, e = ReadFrame(conn); e != nil {
				return
			}
		}
	}()
	_, e = (&Client{Paths: p, Version: "dev"}).Status(testCtx(t))
	wantCode(t, e, "runtime_start_failed")
	<-done
}
