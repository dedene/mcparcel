package runtime

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

// A supervised runtime's restart is the supervisor's job: a restart request
// stops nothing, so no CLI can auto-start a runtime in its place.
func TestSupervisedRuntimeRefusesRestart(t *testing.T) {
	h := &daemonHandler{}
	c, _ := serviceWith(t, DaemonOptions{Handler: h, NoIdleExit: true, Supervised: true})
	pid := waitStatus(t, c).PID
	for _, force := range []bool{false, true} {
		_, e := c.Restart(testCtx(t), force)
		wantCode(t, e, "runtime_supervised")
	}
	if s := waitStatus(t, c); s.PID != pid || h.shut.Load() != 0 {
		t.Fatalf("restart touched the supervised runtime: %+v shut=%d", s, h.shut.Load())
	}
}

func TestMarkSupervised(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	if on, e := Supervised(p); e != nil || on {
		t.Fatal("fresh runtime dir supervised", on, e)
	}
	if e := MarkSupervised(p); e != nil {
		t.Fatal(e)
	}
	if e := MarkSupervised(p); e != nil {
		t.Fatal("second mark", e)
	}
	if on, e := Supervised(p); e != nil || !on {
		t.Fatal("marker not seen", on, e)
	}
	st, e := os.Lstat(p.RuntimeDir + "/supervised")
	if e != nil || st.Mode().Perm() != 0o600 || !st.Mode().IsRegular() {
		t.Fatal("marker file", st, e)
	}
}

// With the marker present, a missing runtime is the supervisor's to start:
// Ensure waits for it and never starts a daemon of its own.
func TestEnsureNeverStartsSupervisedRuntime(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	p, _ := testutil.IsolatedPaths(t)
	if e := MarkSupervised(p); e != nil {
		t.Fatal(e)
	}
	ensureTimeout = 300 * time.Millisecond
	t.Cleanup(func() { ensureTimeout = 15 * time.Second })
	exe, _ := os.Executable()
	c := &Client{Paths: p, Version: "dev", Executable: exe}
	wantCode(t, c.Ensure(testCtx(t)), "runtime_supervised")
	if held, e := lockHeld(p); e != nil || held {
		t.Fatal("Ensure started a runtime beside the supervisor", held, e)
	}
	if _, e := os.Lstat(p.SocketFile); !os.IsNotExist(e) {
		t.Fatal("socket created", e)
	}
	// The supervisor starting the runtime meanwhile lets Ensure join it.
	ensureTimeout = 5 * time.Second
	done := make(chan error, 1)
	go func() { done <- c.Ensure(testCtx(t)) }()
	time.Sleep(100 * time.Millisecond)
	serveAt(t, p, &daemonHandler{})
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}

// serveAt serves h on p in-process until the test ends.
func serveAt(t *testing.T, p config.Paths, h Handler) {
	t.Helper()
	lock, ok, e := AcquireDaemonLock(testCtx(t), p)
	if e != nil || !ok {
		t.Fatal("lock", ok, e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, DaemonOptions{Paths: p, Version: "dev", Lock: lock, LoginEnv: map[string]string{"PATH": "/fixture"}, Handler: h, ShutdownTimeout: time.Second, NoIdleExit: true, Supervised: true})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("serve cleanup timeout")
		}
	})
}
