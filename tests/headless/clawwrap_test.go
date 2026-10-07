//go:build linux && headless_e2e

package headless_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The image links /usr/local/bin/mcparcel to claw-wrap at build time (the
// root fs is read-only, so `claw-wrap install` cannot run here), as the
// hermes container does. The real mcparcel is /opt/mcparcel/mcparcel.
const (
	clawWrap = "/usr/local/bin/claw-wrap"
	wrapped  = "/usr/local/bin/mcparcel"
	openclaw = "/run/openclaw"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// startClawWrap writes the env-file credentials and starts `claw-wrap
// daemon` as the same uid; it is stopped when the test ends. The returned
// buffer holds its stdout and stderr.
func startClawWrap(t *testing.T) *lockedBuffer {
	t.Helper()
	if err := os.Mkdir(openclaw, 0o700); err != nil {
		t.Fatal(err)
	}
	creds := "FRONT_CLIENT_ID=" + clientID + "\nFRONT_CLIENT_SECRET=" + clientSecret + "\n"
	if err := os.WriteFile(filepath.Join(openclaw, "env"), []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	daemon := exec.Command(clawWrap, "daemon")
	daemon.Env, daemon.Dir, daemon.Stdout, daemon.Stderr = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + home}, home, logs, logs
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = daemon.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = daemon.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = daemon.Process.Kill()
			<-done
		}
		record(logs.String())
		t.Logf("claw-wrap daemon output:\n%s", logs.String())
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		sock, errSock := os.Stat(filepath.Join(openclaw, "secrets.sock"))
		auth, errAuth := os.Stat(filepath.Join(openclaw, "auth"))
		if errSock == nil && sock.Mode()&os.ModeSocket != 0 && errAuth == nil && auth.Size() > 0 {
			return logs
		}
		if time.Now().After(deadline) {
			t.Fatalf("claw-wrap daemon not ready:\n%s", logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// cliAlive reports whether a `mcparcel call` process (the real binary, not
// the daemon) exists. A zombie has no exe link and counts as exited.
func cliAlive() bool {
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		dir := filepath.Join("/proc", e.Name())
		if exe, err := os.Readlink(dir + "/exe"); err != nil || exe != mcparcel {
			continue
		}
		cmdline, err := os.ReadFile(dir + "/cmdline")
		if argv := strings.Split(string(cmdline), "\x00"); err == nil && len(argv) > 1 && argv[1] == "call" {
			return true
		}
	}
	return false
}

// wrappedCall runs `mcparcel call` through the claw-wrap symlink without any
// secret in the caller's environment, watching /proc for the real CLI. It
// returns the result and how long the client took after the CLI exited.
func wrappedCall(t *testing.T, args ...string) (result, time.Duration) {
	t.Helper()
	stop := make(chan struct{})
	last := make(chan time.Time, 1)
	go func() {
		var seen time.Time
		for {
			select {
			case <-stop:
				last <- seen
				return
			default:
			}
			if cliAlive() {
				seen = time.Now()
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	r, err := runE(wrapped, []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + home}, append([]string{"call"}, args...)...)
	returned := time.Now()
	close(stop)
	seen := <-last
	if err != nil {
		t.Fatal(err)
	}
	if seen.IsZero() {
		t.Fatalf("never saw the CLI run under claw-wrap: %+v", r)
	}
	return r, returned.Sub(seen)
}

func wrappedDaemonPID(t *testing.T) int {
	t.Helper()
	r := check(t, run(t, wrapped, []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + home}, "runtime", "status", "--json"), 0, "")
	var status struct {
		Running bool `json:"running"`
		PID     int  `json:"pid"`
	}
	if err := json.Unmarshal(r.env.Data, &status); err != nil || !status.Running || status.PID <= 1 {
		t.Fatalf("runtime status %s (%v)", r.stdout, err)
	}
	return status.PID
}

// TestE2EThroughClawWrap drives mcparcel the way hermes does: through the
// claw-wrap client symlink, with claw-wrap holding the credentials (env
// file), pipe mode, and a SIGKILL to the CLI's process group after every
// run. The daemon the first call starts must survive that and serve the
// second call; the client must return as soon as the CLI exits.
func TestE2EThroughClawWrap(t *testing.T) {
	if _, err := os.Stat(clawWrap); err != nil {
		t.Fatal("claw-wrap not built: set CLAW_WRAP_DIR")
	}
	if target, err := os.Readlink(wrapped); err != nil || target != "claw-wrap" {
		t.Fatalf("%s -> %q (%v), want claw-wrap", wrapped, target, err)
	}
	logs := startClawWrap(t)
	stopRuntime(t) // the first call through claw-wrap starts the daemon
	t.Cleanup(func() { stopRuntime(t) })
	before := front.counts()
	pid := 0
	var gaps []time.Duration
	for range 2 {
		r, after := wrappedCall(t, "front.read_conversation", "id=1", "--json")
		check(t, r, 0, "")
		if after > 2*time.Second {
			t.Fatalf("claw-wrap client returned %v after the CLI exited", after)
		}
		gaps = append(gaps, after)
		if len(gaps) == 1 {
			pid = wrappedDaemonPID(t)
		}
	}
	if again := wrappedDaemonPID(t); again != pid {
		t.Fatalf("daemon PID %d, then %d: the group kill reached the daemon", pid, again)
	}

	r, _ := wrappedCall(t, "front.send_message", "to=customer", "body=hi", "--json")
	check(t, r, 4, "tool_denied")
	delta(t, before, 1, 0, map[string]int{"read_conversation": 2, "send_message": 0})
	if out := logs.String(); strings.Contains(out, "still holds its std") {
		t.Fatalf("a descendant of the CLI held its stdio:\n%s", out)
	}
	t.Logf("daemon PID %d survived the group kills; client returned %v after the CLI exited", pid, gaps)
}
