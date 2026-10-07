package cli_test

import (
	"syscall"
	"testing"
	"time"
)

// A SIGTERM to an auto-started daemon is the same forced shutdown as one to
// runtime serve: the dispatched call reports outcome_unknown, not canceled.
func TestDaemonSIGTERMReportsUnknown(t *testing.T) {
	r := newRig(t)
	a := r.start(binaryA, "", "call", "fixture.wait", "--json")
	r.waitFile(r.root+"/started", "wait", a)
	pid := r.status().PID
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	inflight := r.check(r.finish(a), 6, "outcome_unknown")
	if d := inflight.envelope.Error.Details; d["dispatched"] != true || d["outcome"] != "unknown" {
		t.Fatal(inflight.stdout)
	}
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) == nil {
		t.Fatal("daemon survived SIGTERM")
	}
}

// runtime restart against serve stops nothing: only the supervisor restarts
// a supervised runtime, so no auto-started daemon can take its lock.
func TestServeRefusesRestart(t *testing.T) {
	r := newRig(t)
	p, pid := r.serve()
	r.check(r.run("runtime", "restart", "--json"), 6, "runtime_supervised")
	r.check(r.run("runtime", "restart", "--force", "--json"), 6, "runtime_supervised")
	select {
	case <-p.done:
		t.Fatal("serve exited on restart")
	default:
	}
	if daemons := r.daemonPIDs(); len(daemons) != 0 || r.status().PID != pid {
		t.Fatalf("restart replaced the served runtime: %v", daemons)
	}
}

// While serve is down (a supervisor restart, start ordering, a crash), a
// call waits for it instead of auto-starting a daemon that would keep the
// supervised runtime out with runtime_busy.
func TestCLIWaitsForSupervisedRuntime(t *testing.T) {
	r := newRig(t)
	p, _ := r.serve()
	if v := r.stopServe(p); v.code != 0 {
		t.Fatalf("serve exit %d: %s", v.code, v.stderr)
	}
	a := r.start(binaryA, "", "call", "fixture.counter", "--json")
	time.Sleep(500 * time.Millisecond)
	select {
	case <-a.done:
		v := r.finish(a)
		t.Fatalf("call did not wait for serve: %d %s", v.code, v.stdout)
	default:
	}
	if daemons := r.daemonPIDs(); len(daemons) != 0 {
		t.Fatalf("call auto-started a runtime beside the supervisor: %v", daemons)
	}
	_, pid := r.serve()
	if structured(t, r.check(r.finish(a), 0, ""))["count"] != float64(1) || r.status().PID != pid {
		t.Fatal("call did not run on the restarted serve")
	}
	if daemons := r.daemonPIDs(); len(daemons) != 0 {
		t.Fatalf("auto-started runtime beside serve: %v", daemons)
	}
}
