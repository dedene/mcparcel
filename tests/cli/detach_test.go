package cli_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type pipeEnd struct {
	data []byte
	at   time.Time
}

// drainPipe reads f to EOF and reports when EOF arrived.
func drainPipe(f *os.File) <-chan pipeEnd {
	done := make(chan pipeEnd, 1)
	go func() {
		defer f.Close()
		b, _ := io.ReadAll(f)
		done <- pipeEnd{b, time.Now()}
	}()
	return done
}

// assertDetached checks that daemon left the caller's session and process
// group and holds /dev/null, not the caller's descriptors, as 0-2.
func assertDetached(t *testing.T, daemon, callerSession, callerGroup int) {
	t.Helper()
	session, group, err := sessionAndGroup(daemon)
	if err != nil {
		t.Fatal(err)
	}
	if session == callerSession || group == callerGroup || session != daemon || group != daemon {
		t.Fatalf("daemon %d session %d group %d; caller session %d group %d", daemon, session, group, callerSession, callerGroup)
	}
	targets, err := stdioTargets(daemon)
	if err != nil {
		t.Fatal(err)
	}
	for fd, target := range targets {
		if target != os.DevNull {
			t.Fatalf("daemon fd %d is %q, want %s", fd, target, os.DevNull)
		}
	}
}

// TestDaemonSurvivesCallerGroupKill mimics claw-wrap pipe mode: the CLI runs
// in its own process group with piped stdout and stderr, and the wrapper
// kills that group after every run.
func TestDaemonSurvivesCallerGroupKill(t *testing.T) {
	r := newRig(t)
	r.stdio("fixture", "op://Fixture/api/key")
	cli := exec.Command(r.bin+"/cli-a", "call", "fixture.counter", "--json")
	cli.Env, cli.Dir = append([]string(nil), r.env...), r.paths.Home
	cli.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// os/exec's StdoutPipe is an os.Pipe whose write end becomes the child's
	// fd 1; the pipes are made by hand so Wait can run while they drain.
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cli.Stdout, cli.Stderr = outW, errW
	if err = cli.Start(); err != nil {
		t.Fatal(err)
	}
	_ = outW.Close()
	_ = errW.Close()
	stdout, stderr := drainPipe(outR), drainPipe(errR)
	waitErr := cli.Wait()
	exited := time.Now()
	var ends [2]pipeEnd
	for i, ch := range []<-chan pipeEnd{stdout, stderr} {
		select {
		case ends[i] = <-ch:
		case <-time.After(2 * time.Second):
			t.Fatalf("pipe %d still open 2s after the CLI exited", i+1)
		}
		if lag := ends[i].at.Sub(exited); lag > 2*time.Second {
			t.Fatalf("pipe %d reached EOF %v after exit", i+1, lag)
		}
	}
	code := 0
	if waitErr != nil {
		code = cli.ProcessState.ExitCode()
	}
	first := r.check(result{code: code, stdout: string(ends[0].data), stderr: string(ends[1].data)}, 0, "")
	if structured(t, first)["count"] != float64(1) || r.countEvents("bootstrap") != 1 {
		t.Fatal(first.stdout)
	}
	daemon := r.status().PID
	callerSession, _, err := sessionAndGroup(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	assertDetached(t, daemon, callerSession, cli.Process.Pid)
	// The group is empty once the CLI is gone (ESRCH); nothing of ours may be in it.
	if err = syscall.Kill(-cli.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		t.Fatal(err)
	}
	if syscall.Kill(daemon, 0) != nil {
		t.Fatal("group kill reached the daemon")
	}
	second := r.call("fixture.counter")
	if structured(t, second)["count"] != float64(2) || r.status().PID != daemon || r.countEvents("bootstrap") != 1 {
		t.Fatalf("second call did not reuse the daemon: %s", second.stdout)
	}
}

// TestDaemonSurvivesPtySessionHangup mimics claw-wrap PTY mode: the CLI is
// a session leader with the pty as its controlling terminal, and the master
// closes after it exits.
func TestDaemonSurvivesPtySessionHangup(t *testing.T) {
	r := newRig(t)
	master, slavePath, err := openPTY()
	if err != nil {
		t.Fatal(err)
	}
	var masterOpen atomic.Bool
	masterOpen.Store(true)
	t.Cleanup(func() {
		if masterOpen.Load() {
			_ = unix.Close(master)
		}
	})
	slave, err := os.OpenFile(slavePath, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	cli := exec.Command(r.bin+"/cli-a", "call", "fixture.counter", "--json")
	cli.Env, cli.Dir = append([]string(nil), r.env...), r.paths.Home
	cli.Stdin, cli.Stdout, cli.Stderr = slave, slave, slave
	cli.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err = cli.Start(); err != nil {
		t.Fatal(err)
	}
	_ = slave.Close()
	var stop atomic.Bool
	var out bytes.Buffer
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		buf := make([]byte, 4096)
		for !stop.Load() {
			ready, err := unix.Poll([]unix.PollFd{{Fd: int32(master), Events: unix.POLLIN}}, 50)
			if err != nil && !errors.Is(err, unix.EINTR) {
				return
			}
			if ready == 0 {
				continue
			}
			n, err := unix.Read(master, buf)
			if n <= 0 || err != nil {
				return
			}
			out.Write(buf[:n])
		}
	}()
	waitErr := cli.Wait()
	// Collect output already queued in the pty, then hang up the terminal.
	time.Sleep(100 * time.Millisecond)
	stop.Store(true)
	<-readerDone
	masterOpen.Store(false)
	if err = unix.Close(master); err != nil {
		t.Fatal(err)
	}
	if waitErr != nil || !strings.Contains(out.String(), `"count":1`) {
		t.Fatalf("pty call: %v %q", waitErr, out.String())
	}
	daemon := r.status().PID
	// The CLI led its own session and group, both numbered by its PID.
	assertDetached(t, daemon, cli.Process.Pid, cli.Process.Pid)
	time.Sleep(200 * time.Millisecond)
	second := r.call("fixture.counter")
	if structured(t, second)["count"] != float64(2) || r.status().PID != daemon {
		t.Fatalf("daemon did not survive the hangup: %s", second.stdout)
	}
}
