package runtime

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/testutil"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestAcquireDaemonLock(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	lock, ok, e := AcquireDaemonLock(testCtx(t), p)
	if e != nil || !ok || lock == nil {
		t.Fatal(ok, e)
	}
	if held, e := lockHeld(p); e != nil || !held {
		t.Fatal("lock not held", held, e)
	}
	again, ok, e := AcquireDaemonLock(testCtx(t), p)
	if e != nil || ok || again != nil {
		t.Fatal("second acquire", ok, e)
	}
	_ = lock.Close()
	if held, e := lockHeld(p); e != nil || held {
		t.Fatal("lock survived close", held, e)
	}
}

func TestAcquireDaemonLockClearsStaleSocket(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	stale(t, p)
	lock, ok, e := AcquireDaemonLock(testCtx(t), p)
	if e != nil || !ok {
		t.Fatal(ok, e)
	}
	defer func() { _ = lock.Close() }()
	if _, e := os.Lstat(p.SocketFile); !os.IsNotExist(e) {
		t.Fatal("stale socket kept", e)
	}
}

func TestServeNoIdleExit(t *testing.T) {
	c, stopped := serviceWith(t, DaemonOptions{Handler: &daemonHandler{}, IdleTimeout: 50 * time.Millisecond, NoIdleExit: true})
	select {
	case e := <-stopped:
		t.Fatal("idle exit despite NoIdleExit", e)
	case <-time.After(500 * time.Millisecond):
	}
	if s, e := c.Status(testCtx(t)); e != nil || !s.Running {
		t.Fatal("not running", e)
	}
}

func TestServeWarnsWhenPID1(t *testing.T) {
	for _, pid1 := range []bool{false, true} {
		log := &lockedBuffer{}
		if pid1 {
			processID = func() int { return 1 }
		}
		c, _ := serviceWith(t, DaemonOptions{Handler: &daemonHandler{}, Log: log})
		processID = os.Getpid
		waitStatus(t, c)
		got := strings.Count(log.String(), `{"event":"pid1_no_reaper"}`)
		if want := map[bool]int{false: 0, true: 1}[pid1]; got != want {
			t.Fatalf("pid1=%v: %d warnings in %q", pid1, got, log.String())
		}
	}
}
