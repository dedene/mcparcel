package runtime

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
	"github.com/dedene/mcparcel/internal/testutil"
)

func probe(t *testing.T, c *Client) Probe {
	t.Helper()
	p, e := c.Probe(testCtx(t))
	if e != nil {
		t.Fatal(e)
	}
	return p
}

// listing names everything below root, so a test can see that nothing was created.
func listing(t *testing.T, root string) []string {
	t.Helper()
	var names []string
	if e := filepath.WalkDir(root, func(path string, _ os.DirEntry, err error) error {
		names = append(names, path)
		return err
	}); e != nil {
		t.Fatal(e)
	}
	return names
}

func TestProbeStopped(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	root := filepath.Dir(p.RuntimeDir)
	before := listing(t, root)
	c := &Client{Paths: p, Version: "dev"}
	if got := probe(t, c); got.State != ProbeStopped || got.Status != nil {
		t.Fatal(got)
	}
	if s, e := c.Status(testCtx(t)); e != nil || s.Running || s.BinaryVersion != "dev" || s.Socket != p.SocketFile {
		t.Fatal(s, e)
	}
	// No runtime directory at all is stopped too.
	if e := os.Remove(p.RuntimeDir); e != nil {
		t.Fatal(e)
	}
	if got := probe(t, c); got.State != ProbeStopped {
		t.Fatal(got)
	}
	if after := listing(t, root); len(after) != len(before)-1 {
		t.Fatalf("probe created files: %v", after)
	}
}

func TestProbeStaleSocket(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	stale(t, p)
	c := &Client{Paths: p, Version: "dev"}
	if got := probe(t, c); got.State != ProbeStaleSocket {
		t.Fatal(got)
	}
	if s, e := c.Status(testCtx(t)); e != nil || s.Running {
		t.Fatal(s, e)
	}
	if _, e := os.Lstat(p.SocketFile); e != nil {
		t.Fatal("probe removed the stale socket", e)
	}
}

func TestProbeStartingLockHeld(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	lock := lockFile(t, p)
	defer func() { _ = lock.Close() }()
	c := &Client{Paths: p, Version: "dev"}
	start := time.Now()
	if got := probe(t, c); got.State != ProbeStarting {
		t.Fatal(got)
	}
	if elapsed := time.Since(start); elapsed < probeBudget-100*time.Millisecond || elapsed > probeBudget+time.Second {
		t.Fatal("probe budget", elapsed)
	}
	_, e := c.Status(testCtx(t))
	wantCode(t, e, "runtime_start_failed")
}

func TestProbeReportsMismatchedDaemonVersion(t *testing.T) {
	running, _ := service(t, &daemonHandler{}, time.Minute)
	pid := waitStatus(t, running).PID
	got := probe(t, running)
	if got.State != ProbeRunning || got.DaemonVersion != "dev" || got.PID != pid || got.Status == nil || got.Status.CapturedPath != "/fixture" {
		t.Fatal(got)
	}
	other := &Client{Paths: running.Paths, Version: "0.9.0"}
	got = probe(t, other)
	if got.State != ProbeVersionMismatch || got.DaemonVersion != "dev" || got.PID != pid || got.Status != nil {
		t.Fatal(got)
	}
	_, e := other.Status(testCtx(t))
	wantCode(t, e, "runtime_version_mismatch")
	if s := waitStatus(t, running); s.PID != pid {
		t.Fatal("the probe disturbed the daemon")
	}
}

func TestProbeConfigMismatch(t *testing.T) {
	running, _ := service(t, &daemonHandler{}, time.Minute)
	paths := running.Paths
	paths.ConfigDir = filepath.Join(filepath.Dir(paths.ConfigDir), "other")
	other := &Client{Paths: paths, Version: "dev"}
	got := probe(t, other)
	if got.State != ProbeConfigMismatch || got.DaemonVersion != "dev" || got.PID == 0 {
		t.Fatal(got)
	}
	_, e := other.Status(testCtx(t))
	wantCode(t, e, "runtime_config_mismatch")
}

// fakeDaemon answers each hello on p's socket with reply; nil never answers.
func fakeDaemon(t *testing.T, p config.Paths, reply func(*net.UnixConn, Frame)) {
	t.Helper()
	l, e := net.ListenUnix("unix", &net.UnixAddr{Name: p.SocketFile, Net: "unix"})
	if e != nil {
		t.Fatal(e)
	}
	_ = os.Chmod(p.SocketFile, 0o600)
	done := make(chan struct{})
	t.Cleanup(func() { _ = l.Close(); <-done })
	go func() {
		defer close(done)
		for {
			conn, e := l.AcceptUnix()
			if e != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				hello, e := ReadFrame(conn)
				if e != nil {
					return
				}
				if reply == nil {
					_, _ = ReadFrame(conn) // hold the connection until the client gives up
					return
				}
				reply(conn, hello)
			}()
		}
	}()
}

func TestProbeUnreadableAck(t *testing.T) {
	ack := func(protocol int, body string) func(*net.UnixConn, Frame) {
		return func(conn *net.UnixConn, hello Frame) {
			_ = WriteFrame(conn, Frame{protocol, "hello_ack", hello.RequestID, json.RawMessage(body)})
		}
	}
	mismatchBody := `{"binaryVersion":"9.0","pid":1,"compatible":false,"error":{"code":"runtime_version_mismatch","message":"m"}}`
	for _, tc := range []struct {
		name   string
		reply  func(*net.UnixConn, Frame)
		status string // Status's error code, as before Probe
	}{
		{"newer ack field", ack(ProtocolVersion, `{"binaryVersion":"9.0","pid":1,"compatible":false,"error":null,"newField":1}`), "runtime_start_failed"},
		{"other frame protocol", ack(ProtocolVersion+1, mismatchBody), "runtime_version_mismatch"},
		{"never answers", nil, "runtime_start_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := testutil.IsolatedPaths(t)
			fakeDaemon(t, p, tc.reply)
			c := &Client{Paths: p, Version: "dev"}
			start := time.Now()
			got := probe(t, c)
			if got.State != ProbeUnreadable || got.DaemonVersion != "" {
				t.Fatal(got)
			}
			if elapsed := time.Since(start); elapsed > probeBudget+time.Second {
				t.Fatal("probe exceeded its budget", elapsed)
			}
			_, e := c.Status(testCtx(t))
			wantCode(t, e, tc.status)
		})
	}
}

// Probe never starts a runtime and never waits for a supervised one.
func TestProbeNeverStartsOrWaitsSupervised(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	if e := MarkSupervised(p); e != nil {
		t.Fatal(e)
	}
	p.Supervised = true
	before := listing(t, filepath.Dir(p.RuntimeDir))
	exe, _ := os.Executable()
	c := &Client{Paths: p, Version: "dev", Executable: exe}
	start := time.Now()
	if got := probe(t, c); got.State != ProbeStopped {
		t.Fatal(got)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatal("probe waited for the supervisor", elapsed)
	}
	if after := listing(t, filepath.Dir(p.RuntimeDir)); !slices.Equal(before, after) {
		t.Fatalf("probe created files: %v", after)
	}
}

func TestProbeUnsafeRuntimeDir(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	if e := os.Chmod(p.RuntimeDir, 0o755); e != nil {
		t.Fatal(e)
	}
	c := &Client{Paths: p, Version: "dev"}
	if _, e := c.Probe(testCtx(t)); !errors.Is(e, config.ErrUnsafePath) {
		t.Fatal(e)
	}
	if _, e := c.Status(testCtx(t)); !errors.Is(e, config.ErrUnsafePath) {
		t.Fatal(e)
	}
}

// The handshake is what lets any CLI version stop any daemon (restart and
// stop are version-exempt), but only while both sides can decode it: acks
// are decoded strictly and frames must share ProtocolVersion. Changing this
// shape means bumping ProtocolVersion on purpose and accepting that older
// CLIs can no longer stop the daemon (doctor then reports "unreadable").
func TestHelloWireShapeIsFrozen(t *testing.T) {
	if ProtocolVersion != 1 {
		t.Fatal("ProtocolVersion changed:", ProtocolVersion)
	}
	hello, _ := json.Marshal(Hello{ConfigRoot: "r", BinaryVersion: "v", Intent: "stop"})
	if string(hello) != `{"configRoot":"r","binaryVersion":"v","intent":"stop"}` {
		t.Fatal(string(hello))
	}
	ack, _ := json.Marshal(HelloAck{BinaryVersion: "v", PID: 1, Compatible: false, Error: output.NewError("runtime_version_mismatch", nil)})
	if string(ack) != `{"binaryVersion":"v","pid":1,"compatible":false,"error":{"code":"runtime_version_mismatch","message":"The CLI and daemon versions differ.","nextAction":"Run mcparcel runtime restart."}}` {
		t.Fatal(string(ack))
	}
	frame, _ := json.Marshal(Frame{ProtocolVersion, "hello", "id", json.RawMessage(`{}`)})
	if string(frame) != `{"protocolVersion":1,"kind":"hello","requestId":"id","body":{}}` {
		t.Fatal(string(frame))
	}
}
