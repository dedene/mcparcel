package runtime

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

func cleanupDaemon(t *testing.T, c *Client) {
	t.Helper()
	conn, e := dialSocket(testCtx(t), c.Paths)
	if e != nil {
		return
	}
	ack, e := ClientHandshake(conn, c.Version, "restart", newRequestID(), configRoot(c.Paths.ConfigDir))
	_ = conn.Close()
	if e != nil {
		t.Error(e)
		return
	}
	if ack.PID == os.Getpid() {
		t.Fatal("refusing test-process signal")
	}
	_ = syscall.Kill(ack.PID, syscall.SIGTERM)
	ctx := testCtx(t)
	if e = waitLock(ctx, c.Paths); e != nil {
		t.Error(e)
	}
}

func spawned(t *testing.T, extra ...string) *Client {
	t.Helper()
	t.Setenv("SHELL", "/bin/sh")
	p, env := testutil.IsolatedPaths(t)
	exe, _ := os.Executable()
	c := &Client{Paths: p, Version: "dev", Executable: exe}
	for _, v := range extra {
		if strings.HasPrefix(v, "MCP_TEST_VERSION=") {
			c.Version = strings.TrimPrefix(v, "MCP_TEST_VERSION=")
		}
	}
	t.Cleanup(func() { cleanupDaemon(t, c) })
	if _, e := StartDaemon(testCtx(t), p, exe, append(env, extra...)); e != nil {
		t.Fatal(e)
	}
	waitStatus(t, c)
	return c
}

func TestConcurrentDaemonStartup(t *testing.T) {
	p, env := testutil.IsolatedPaths(t)
	marker := filepath.Join(p.Home, "starts")
	barrier := filepath.Join(p.Home, "barrier")
	if e := unix.Mkfifo(barrier, 0o600); e != nil {
		t.Fatal(e)
	}
	capture := filepath.Join(p.Home, "captures")
	shell := filepath.Join(p.Home, "fixture-shell")
	script := "#!/bin/sh\nprintf 'capture\\n' >> '" + capture + "'\n/bin/cat < '" + barrier + "' > /dev/null\nexec /bin/sh -c \"$3\"\n"
	if e := os.WriteFile(shell, []byte(script), 0o700); e != nil {
		t.Fatal(e)
	}
	env = append(env, "MCP_TEST_START="+marker, "SHELL="+shell)
	exe, _ := os.Executable()
	cl := &Client{Paths: p, Version: "dev", Executable: exe}
	t.Cleanup(func() { cleanupDaemon(t, cl) })
	var wg sync.WaitGroup
	out := make(chan string, 20)
	fail := make(chan error, 20)
	for range 20 {
		wg.Go(func() {
			cmd := exec.Command(exe, "join-helper")
			cmd.Env = env
			b, e := cmd.Output()
			if e != nil {
				fail <- e
			}
			out <- strings.TrimSpace(string(b))
		})
	}
	ctx := testCtx(t)
	for {
		if _, e := os.Stat(marker); e == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("no start")
		case <-time.After(5 * time.Millisecond):
		}
	}
	f, e := os.OpenFile(barrier, os.O_WRONLY, 0)
	if e != nil {
		t.Fatal(e)
	}
	_ = f.Close()
	wg.Wait()
	close(out)
	close(fail)
	for e := range fail {
		if v, ok := e.(*exec.ExitError); ok {
			t.Log(string(v.Stderr))
		}
		t.Error(e)
	}
	pid := ""
	for v := range out {
		if pid == "" {
			pid = v
		}
		if v != pid {
			t.Fatalf("%q vs %q", v, pid)
		}
	}
	b, _ := os.ReadFile(marker)
	if len(strings.Fields(string(b))) != 1 {
		t.Fatalf("starts: %s", b)
	}
	b, e = os.ReadFile(capture)
	if e != nil || len(strings.Fields(string(b))) != 1 {
		t.Fatalf("captures %s %v", b, e)
	}
}

func TestJoinDaemonStillStarting(t *testing.T) {
	p, env := testutil.IsolatedPaths(t)
	barrier := filepath.Join(p.Home, "barrier")
	_ = unix.Mkfifo(barrier, 0o600)
	exe, _ := os.Executable()
	started, e := StartDaemon(testCtx(t), p, exe, append(env, "MCP_TEST_BARRIER="+barrier))
	if e != nil || !started {
		t.Fatal(e)
	}
	c := &Client{Paths: p, Version: "dev", Executable: exe}
	t.Cleanup(func() { cleanupDaemon(t, c) })
	done := make(chan error, 1)
	go func() { done <- c.Ensure(testCtx(t)) }()
	select {
	case e := <-done:
		t.Fatal("did not join startup", e)
	case <-time.After(300 * time.Millisecond):
	}
	f, e := os.OpenFile(barrier, os.O_WRONLY, 0)
	if e != nil {
		t.Fatal(e)
	}
	_ = f.Close()
	if e = <-done; e != nil {
		t.Fatal(e)
	}
}

func TestSocketOwnerAndSymlink(t *testing.T) {
	for _, mode := range []string{"symlink", "file", "socket", "dir"} {
		t.Run(mode, func(t *testing.T) {
			p, env := testutil.IsolatedPaths(t)
			target := filepath.Join(p.Home, "target")
			_ = os.WriteFile(target, []byte("keep"), 0o600)
			switch mode {
			case "symlink":
				_ = os.Symlink(target, p.SocketFile)
			case "file":
				_ = os.WriteFile(p.SocketFile, []byte("keep"), 0o600)
			case "socket":
				l, e := net.ListenUnix("unix", &net.UnixAddr{Name: p.SocketFile, Net: "unix"})
				if e != nil {
					t.Fatal(e)
				}
				t.Cleanup(func() { _ = l.Close() })
				_ = os.Chmod(p.SocketFile, 0o666)
			case "dir":
				_ = os.Chmod(p.RuntimeDir, 0o755)
			}
			_, e := StartDaemon(testCtx(t), p, "/absent", env)
			if !errors.Is(e, config.ErrUnsafePath) {
				t.Fatal(e)
			}
			b, _ := os.ReadFile(target)
			if string(b) != "keep" {
				t.Fatal("target changed")
			}
		})
	}
	st := &unix.Stat_t{Mode: unix.S_IFSOCK | 0o600, Uid: uint32(os.Getuid() + 1)}
	if e := safeSocketStat(st); !errors.Is(e, config.ErrUnsafePath) {
		t.Fatal(e)
	}
}

func stale(t *testing.T, p config.Paths) {
	t.Helper()
	l, e := net.ListenUnix("unix", &net.UnixAddr{Name: p.SocketFile, Net: "unix"})
	if e != nil {
		t.Fatal(e)
	}
	l.SetUnlinkOnClose(false)
	_ = os.Chmod(p.SocketFile, 0o600)
	_ = l.Close()
}

func TestStaleSocketRecovery(t *testing.T) {
	p, env := testutil.IsolatedPaths(t)
	stale(t, p)
	exe, _ := os.Executable()
	c := &Client{Paths: p, Version: "dev", Executable: exe}
	t.Cleanup(func() { cleanupDaemon(t, c) })
	if b, e := StartDaemon(testCtx(t), p, exe, env); e != nil || !b {
		t.Fatal(b, e)
	}
	waitStatus(t, c)
}

func TestHeldLockRefusedSocket(t *testing.T) {
	p, env := testutil.IsolatedPaths(t)
	stale(t, p)
	lock := lockFile(t, p)
	defer func() { _ = lock.Close() }()
	before, _ := os.Stat(p.SocketFile)
	if b, e := StartDaemon(testCtx(t), p, "/absent", env); e != nil || b {
		t.Fatal(b, e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	e := (&Client{Paths: p, Version: "dev"}).Ensure(ctx)
	wantCode(t, e, "runtime_start_failed")
	after, _ := os.Stat(p.SocketFile)
	if !os.SameFile(before, after) {
		t.Fatal("unlinked held socket")
	}
}

func TestStartFailureReleasesLock(t *testing.T) {
	p, env := testutil.IsolatedPaths(t)
	_, e := StartDaemon(testCtx(t), p, "/absent", env)
	wantCode(t, e, "runtime_start_failed")
	exe, _ := os.Executable()
	c := &Client{Paths: p, Version: "dev", Executable: exe}
	t.Cleanup(func() { cleanupDaemon(t, c) })
	if b, e := StartDaemon(testCtx(t), p, exe, env); e != nil || !b {
		t.Fatal(e)
	}
	waitStatus(t, c)
}

func TestDaemonVersionMismatch(t *testing.T) {
	c := spawned(t)
	pid := waitStatus(t, c).PID
	c.Version = "new"
	e := c.Ensure(testCtx(t))
	wantCode(t, e, "runtime_version_mismatch")
	if e = syscall.Kill(pid, 0); e != nil {
		t.Fatal("old died")
	}
	c.Version = "dev"
}

func TestRestartMismatchExplicit(t *testing.T) {
	c := spawned(t, "MCP_TEST_VERSION=old")
	c.Version = "old"
	old := waitStatus(t, c).PID
	c.Version = "dev"
	r, e := c.Restart(testCtx(t), false)
	if e != nil || !r.Restarted || r.Status.PID == old || r.Status.BinaryVersion != "dev" {
		t.Fatalf("%+v %v", r, e)
	}
}

func TestLockNotInheritedByMCPChild(t *testing.T) {
	p, env := testutil.IsolatedPaths(t)
	exe, _ := os.Executable()
	c := &Client{Paths: p, Version: "dev", Executable: exe}
	marker := filepath.Join(p.Home, "child-pid")
	t.Cleanup(func() { cleanupDaemon(t, c) })
	if started, e := StartDaemon(testCtx(t), p, exe, append(env, "MCP_TEST_CHILD_PID="+marker)); e != nil || !started {
		t.Fatal(started, e)
	}
	old := waitStatus(t, c).PID
	b, e := os.ReadFile(marker)
	if e != nil {
		t.Fatal(e)
	}
	child, e := strconv.Atoi(string(b))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(child, syscall.SIGTERM)
		ctx := testCtx(t)
		for syscall.Kill(child, 0) != syscall.ESRCH {
			select {
			case <-ctx.Done():
				t.Error("MCP fixture child did not stop")
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	})
	if e = syscall.Kill(old, syscall.SIGKILL); e != nil {
		t.Fatal(e)
	}
	if e = waitLock(testCtx(t), p); e != nil {
		t.Fatal("MCP child retained lifetime lock", e)
	}
	if e = syscall.Kill(child, 0); e != nil {
		t.Fatal("child was not alive for lock proof", e)
	}
	if started, e := StartDaemon(testCtx(t), p, exe, env); e != nil || !started {
		t.Fatal(started, e)
	}
	if waitStatus(t, c).PID == old {
		t.Fatal("old daemon survived")
	}
}

func TestDaemonStartupRetriesTransientCreate(t *testing.T) {
	p, env := testutil.IsolatedPaths(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{Paths: p, Version: "dev", Executable: exe}
	t.Cleanup(func() { cleanupDaemon(t, c) })
	for _, joining := range []bool{false, true} {
		attempts := 0
		open := func(dir *os.File, name string, create bool) (*os.File, error) {
			if name != "daemon.lock" || !create {
				t.Fatal("unexpected lock open")
			}
			attempts++
			if attempts <= 3 {
				return nil, os.ErrNotExist
			}
			return config.OpenPrivateFile(dir, name, create)
		}
		started, err := startDaemon(testCtx(t), p, exe, env, open)
		if err != nil || started == joining || attempts < 4 {
			t.Fatalf("joining=%v started=%v attempts=%d err=%v", joining, started, attempts, err)
		}
		waitStatus(t, c)
	}
}

func TestDaemonStartupCreateRetryCancellation(t *testing.T) {
	p, env := testutil.IsolatedPaths(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	attempts := 0
	_, err := startDaemon(ctx, p, "/absent", env, func(*os.File, string, bool) (*os.File, error) {
		attempts++
		return nil, os.ErrNotExist
	})
	if !errors.Is(err, context.DeadlineExceeded) || attempts < 2 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}
