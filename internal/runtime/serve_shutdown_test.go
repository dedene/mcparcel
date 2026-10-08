package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/testutil"
)

// A signal ends the runtime's parent context (SIGTERM to runtime serve, or to
// an auto-started daemon). That is a forced shutdown, not the caller's own
// cancel: dispatched work reports outcome_unknown, never canceled.
func TestServeParentCancelReportsUnknown(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	p, _ := testutil.IsolatedPaths(t)
	h := &daemonHandler{started: make(chan struct{}, 1), release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Take the lock before waitStatus probes it: a probe holds the flock for
	// a moment, and lockFile's non-blocking attempt would then fail.
	lock := lockFile(t, p)
	stopped := make(chan error, 1)
	go func() {
		stopped <- Serve(ctx, DaemonOptions{Paths: p, Version: "dev", Lock: lock, LoginEnv: map[string]string{"PATH": "/fixture"}, Handler: h, ShutdownTimeout: time.Second, NoIdleExit: true})
	}()
	c := &Client{Paths: p, Version: "dev"}
	waitStatus(t, c)
	done := make(chan error, 1)
	go func() { _, e := c.Call(testCtx(t), callReq()); done <- e }()
	<-h.started
	cancel()
	wantCode(t, <-done, "outcome_unknown")
	select {
	case e := <-stopped:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop")
	}
}
