package cli_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
)

// serve starts `runtime serve` and waits until it answers on the socket as
// itself. Cleanup sends SIGTERM and waits, so the served runtime shuts its
// sessions down before the rig checks for leftover processes.
func (r *rig) serve() (*process, int) {
	r.t.Helper()
	p := r.start(binaryA, "", "runtime", "serve")
	pid := p.cmd.Process.Pid
	r.t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			_ = p.cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-p.done:
			case <-time.After(10 * time.Second):
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := runtimeclient.Client{Paths: r.paths, Version: "stage2-test-a"}
	for {
		select {
		case <-p.done:
			v := r.finish(p)
			r.t.Fatalf("serve exited %d: stdout=%s stderr=%s", v.code, v.stdout, v.stderr)
		default:
		}
		if status, err := client.Status(ctx); err == nil && status.Running {
			if status.PID != pid {
				r.t.Fatalf("socket answered by %d, serve is %d", status.PID, pid)
			}
			return p, pid
		}
		select {
		case <-ctx.Done():
			r.t.Fatal("serve never answered")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// lockFree reports whether nothing holds the daemon lock.
func (r *rig) lockFree() bool {
	r.t.Helper()
	f, err := os.OpenFile(r.paths.LockFile, os.O_RDWR, 0)
	if err != nil {
		r.t.Fatal(err)
	}
	defer f.Close()
	err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return false
	}
	if err != nil {
		r.t.Fatal(err)
	}
	return true
}

func (r *rig) stopServe(p *process) result {
	r.t.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		r.t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		r.t.Fatal("serve did not exit after SIGTERM")
	}
	return r.finish(p)
}

func TestServeHoldsLockAndAnswers(t *testing.T) {
	r := newRig(t)
	p, pid := r.serve()
	if r.lockFree() {
		t.Fatal("serve does not hold the daemon lock")
	}
	if structured(t, r.call("fixture.counter"))["count"] != float64(1) || r.status().PID != pid {
		t.Fatal("served runtime did not run the call")
	}
	v := r.stopServe(p)
	if v.code != 0 || v.stdout != "Runtime stopped.\n" {
		t.Fatalf("serve exit %d stdout %q stderr %q", v.code, v.stdout, v.stderr)
	}
	log, err := os.ReadFile(r.paths.LogFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{`{"event":"daemon_started"`, `{"event":"connection_opened"}`, `{"event":"daemon_stopped"}`} {
		if !strings.Contains(v.stderr, event) || !strings.Contains(string(log), event) {
			t.Fatalf("%s missing: stderr=%q log=%q", event, v.stderr, log)
		}
	}
}

func TestServeSecondInstanceBusy(t *testing.T) {
	r := newRig(t)
	r.call("fixture.counter")
	auto := r.status().PID
	r.check(r.run("runtime", "serve", "--json"), 6, "runtime_busy")
	if r.status().PID != auto {
		t.Fatal("busy serve replaced the auto-started runtime")
	}
	r.check(r.run("runtime", "stop", "--json"), 0, "")
	p, pid := r.serve()
	r.check(r.run("runtime", "serve", "--json"), 6, "runtime_busy")
	if r.status().PID != pid {
		t.Fatal("busy serve replaced the served runtime")
	}
	if v := r.stopServe(p); v.code != 0 {
		t.Fatalf("serve exit %d: %s", v.code, v.stderr)
	}
}

func TestServeSIGTERMDrainsAndUnlinksSocket(t *testing.T) {
	r := newRig(t)
	p, pid := r.serve()
	r.call("fixture.counter")
	children := ownedChildren(t, pid)
	if len(children) != 1 {
		t.Fatalf("serve children: %v", children)
	}
	a := r.start(binaryA, "", "call", "fixture.wait", "--json")
	r.waitFile(r.root+"/started", "wait", a)
	v := r.stopServe(p)
	if v.code != 0 {
		t.Fatalf("serve exit %d: %s", v.code, v.stderr)
	}
	// A SIGTERM is a forced runtime shutdown, not the caller's own cancel:
	// the dispatched call may have run, as with runtime stop --force.
	inflight := r.check(r.finish(a), 6, "outcome_unknown")
	if d := inflight.envelope.Error.Details; d["dispatched"] != true || d["outcome"] != "unknown" {
		t.Fatal(inflight.stdout)
	}
	if _, err := os.Lstat(r.paths.SocketFile); !os.IsNotExist(err) {
		t.Fatal("socket survived SIGTERM", err)
	}
	if !r.lockFree() {
		t.Fatal("lock survived SIGTERM")
	}
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(children[0], 0) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if syscall.Kill(children[0], 0) == nil {
		t.Fatal("MCP server survived serve")
	}
}

func TestServeNoIdleExit(t *testing.T) {
	r := newRig(t)
	r.write(r.paths.StateDir+"/fixture-idle-timeout", "200ms", 0o600)
	// Control: the same injected timeout stops an auto-started runtime.
	// The runtime may be gone before a status call could see it, so the
	// evidence is its stop event and an empty daemon process list.
	r.call("fixture.counter")
	r.waitFile(r.paths.LogFile, `{"event":"daemon_stopped"}`)
	deadline := time.Now().Add(5 * time.Second)
	for len(r.daemonPIDs()) != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if daemons := r.daemonPIDs(); len(daemons) != 0 || r.status().Running {
		t.Fatalf("injected idle timeout did not stop the auto-started runtime: %v", daemons)
	}
	p, pid := r.serve()
	r.call("fixture.counter")
	time.Sleep(time.Second)
	select {
	case <-p.done:
		t.Fatal("serve exited while idle")
	default:
	}
	if r.status().PID != pid {
		t.Fatal("served runtime replaced")
	}
}

func TestCLIUsesServedDaemon(t *testing.T) {
	r := newRig(t)
	p, pid := r.serve()
	a, b := r.call("fixture.counter"), r.call("fixture.counter")
	if structured(t, a)["count"] != float64(1) || structured(t, b)["count"] != float64(2) {
		t.Fatal(a.stdout, b.stdout)
	}
	if daemons := r.daemonPIDs(); len(daemons) != 0 || r.status().PID != pid {
		t.Fatalf("auto-started runtime beside serve: %v", daemons)
	}
	r.check(r.run("runtime", "stop", "--json"), 0, "")
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		t.Fatal("serve survived runtime stop")
	}
	if v := r.finish(p); v.code != 0 {
		t.Fatalf("serve exit %d: %s", v.code, v.stderr)
	}
}
