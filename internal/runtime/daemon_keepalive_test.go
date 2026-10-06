package runtime

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// stayHandler is a daemon handler that runs a keep-alive and answers
// stay-alive as told.
type stayHandler struct {
	*daemonHandler
	stay    atomic.Bool
	ran     chan struct{}
	stopped chan struct{}
}

func newStayHandler(stay bool) *stayHandler {
	h := &stayHandler{daemonHandler: &daemonHandler{}, ran: make(chan struct{}), stopped: make(chan struct{})}
	h.stay.Store(stay)
	return h
}

func (h *stayHandler) StayAlive() bool { return h.stay.Load() }

func (h *stayHandler) RunKeepAlive(ctx context.Context) error {
	close(h.ran)
	<-ctx.Done()
	close(h.stopped)
	return nil
}

func waitStopped(t *testing.T, stopped <-chan error, within time.Duration) {
	t.Helper()
	select {
	case e := <-stopped:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(within):
		t.Fatal("daemon did not stop")
	}
}

func TestKeepAliveDisablesIdleExit(t *testing.T) {
	h := newStayHandler(true)
	c, stopped := service(t, h, 50*time.Millisecond)
	select {
	case <-h.ran:
	case <-time.After(5 * time.Second):
		t.Fatal("keep-alive not started")
	}
	select {
	case e := <-stopped:
		t.Fatal("idle exit with stay-alive", e)
	case <-time.After(400 * time.Millisecond):
	}
	if s, e := c.Status(testCtx(t)); e != nil || !s.Running {
		t.Fatal(s, e)
	}
	h.stay.Store(false)
	waitStopped(t, stopped, 5*time.Second)
	select {
	case <-h.stopped:
	default:
		t.Fatal("daemon stopped before its keep-alive returned")
	}
}

func TestIdleExitWithoutOAuthSessions(t *testing.T) {
	h := newStayHandler(false)
	_, stopped := service(t, h, 50*time.Millisecond)
	waitStopped(t, stopped, 5*time.Second)
	<-h.stopped
}

func TestStatusReportsStayAlive(t *testing.T) {
	h := newStayHandler(true)
	c, _ := service(t, h, 0)
	if s := waitStatus(t, c); !s.StayAlive {
		t.Fatalf("%+v", s)
	}
	h.stay.Store(false)
	if s := waitStatus(t, c); s.StayAlive {
		t.Fatalf("%+v", s)
	}
	// A handler without stay-alive reports it off.
	plain, _ := service(t, &daemonHandler{}, 0)
	if s := waitStatus(t, plain); s.StayAlive {
		t.Fatalf("%+v", s)
	}
}
