package doctor

import (
	"os"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
)

func TestRuntimeVersionRows(t *testing.T) {
	running := &runtimeclient.Status{Running: true, PID: 42, BinaryVersion: "1.2.3"}
	for _, tc := range []struct {
		name       string
		probe      runtimeclient.Probe
		probeErr   error
		supervised bool
		status     string
		code       string
		message    string
		next       string
	}{
		{"stopped", runtimeclient.Probe{State: runtimeclient.ProbeStopped}, nil, false, OK, "", "Not running. The next runtime command starts 1.2.3.", ""},
		{"stopped supervised", runtimeclient.Probe{State: runtimeclient.ProbeStopped}, nil, true, Warn, "", "Not running; its supervisor (runtime serve) starts it.", "Start or restart it through its supervisor"},
		{"stale", runtimeclient.Probe{State: runtimeclient.ProbeStaleSocket}, nil, false, OK, "", "A stale socket is left", ""},
		{"starting", runtimeclient.Probe{State: runtimeclient.ProbeStarting}, nil, false, Warn, "runtime_start_failed", "did not answer within 2 s", "daemon.log"},
		{"running", runtimeclient.Probe{State: runtimeclient.ProbeRunning, DaemonVersion: "1.2.3", PID: 42, Status: running}, nil, false, OK, "", "Running 1.2.3 (PID 42).", ""},
		{"mismatch", runtimeclient.Probe{State: runtimeclient.ProbeVersionMismatch, DaemonVersion: "1.2.2", PID: 7}, nil, false, Fail, "runtime_version_mismatch", "The runtime runs 1.2.2 (PID 7); this CLI is 1.2.3.", "Run mcparcel runtime restart when no calls are active."},
		{"mismatch supervised", runtimeclient.Probe{State: runtimeclient.ProbeVersionMismatch, DaemonVersion: "1.2.2", PID: 7}, nil, true, Fail, "runtime_version_mismatch", "1.2.2", "Restart the runtime through its supervisor."},
		{"config mismatch", runtimeclient.Probe{State: runtimeclient.ProbeConfigMismatch, DaemonVersion: "1.2.3", PID: 7}, nil, false, Fail, "runtime_config_mismatch", "another configuration directory", "runtime restart"},
		{"unreadable", runtimeclient.Probe{State: runtimeclient.ProbeUnreadable}, nil, false, Fail, "runtime_version_mismatch", "could not be read", "runtime stop with the MCParcel version that started it"},
		{"unsafe", runtimeclient.Probe{}, config.ErrUnsafePath, false, Fail, "unsafe_local_path", "not safe", "mode 700"},
		{"daemon version cleaned", runtimeclient.Probe{State: runtimeclient.ProbeVersionMismatch, DaemonVersion: "1.0\x1b[31m", PID: 7}, nil, false, Fail, "runtime_version_mismatch", `"1.0\x1b[31m"`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := inputFor(t, docs{personal: stdioPaper, selections: enabledPaper})
			in.Probe, in.ProbeErr, in.Supervised = tc.probe, tc.probeErr, tc.supervised
			c := find(t, Offline(in), "runtime.version", "")
			want(t, c, tc.status, tc.code)
			if !strings.Contains(c.Message, tc.message) || !strings.Contains(c.NextAction, tc.next) || strings.ContainsRune(c.Message, 0x1b) {
				t.Fatalf("%+v", c)
			}
		})
	}
}

func TestRuntimeModeUnsupportedDesktopFails(t *testing.T) {
	in := inputFor(t, docs{personal: stdioPaper, selections: enabledPaper})
	in.DesktopSupported = false
	checks := Offline(in)
	c := find(t, checks, "runtime.mode", "")
	want(t, c, Fail, "runtime_unsupported")
	if c.Message != "Desktop mode is not supported on this platform." {
		t.Fatal(c.Message)
	}
	want(t, find(t, checks, "runtime.version", ""), Skip, "")
	// Headless mode runs there.
	h := headlessInput(inputFor(t, docs{personal: stdioPaper, selections: enabledPaper}))
	h.DesktopSupported, h.Supervised = false, true
	c = find(t, Offline(h), "runtime.mode", "")
	want(t, c, OK, "")
	if !strings.HasPrefix(c.Message, "Headless mode (state root /") || !strings.HasSuffix(c.Message, "). Supervised.") || Mode(h) != "headless" {
		t.Fatal(c.Message)
	}
}

// Linux desktop mode without a desktop session runs once config.json
// exists; without one too, runtime.mode fails as runtime commands do.
func TestRuntimeModeLinuxSession(t *testing.T) {
	in := inputFor(t, docs{personal: stdioPaper, selections: enabledPaper})
	in.DesktopSession = false
	want(t, find(t, Offline(in), "runtime.mode", ""), OK, "")
	in.Files.Files[0].Present = false
	checks := Offline(in)
	c := find(t, checks, "runtime.mode", "")
	want(t, c, Fail, "runtime_unsupported")
	if e := output.LinuxNoSessionError(); c.Message != e.Message || c.NextAction != e.NextAction {
		t.Fatal(c.Message, c.NextAction)
	}
	want(t, find(t, checks, "runtime.version", ""), Skip, "")
	want(t, find(t, checks, "runtime.binary", ""), Skip, "")
	in.DesktopSession = true
	want(t, find(t, Offline(in), "runtime.mode", ""), OK, "")
	// Headless mode never needs a session.
	h := headlessInput(inputFor(t, docs{personal: stdioPaper, selections: enabledPaper}))
	h.DesktopSession, h.Files.Files[0].Present = false, false
	want(t, find(t, Offline(h), "runtime.mode", ""), OK, "")
}

func TestUnreadableConfigSkipsRuntimeRows(t *testing.T) {
	in := inputFor(t, docs{personal: stdioPaper, selections: enabledPaper})
	_, err := config.DecodeLocal([]byte(`{"schemaVersion":1,"runtime":{"mode":"server"}}`))
	in.RuntimeErr = err
	in.Probe = runtimeclient.Probe{State: runtimeclient.ProbeRunning}
	checks := Offline(in)
	c := find(t, checks, "runtime.mode", "")
	want(t, c, Fail, "invalid_config")
	if c.Message != "Cannot read the runtime mode from config.json: runtime.mode: must be desktop or headless." {
		t.Fatal(c.Message)
	}
	want(t, find(t, checks, "runtime.version", ""), Skip, "")
	want(t, find(t, checks, "storage.dir", ""), Skip, "")
	absent(t, checks, "prereq.onepassword")
	if Mode(in) != "unknown" {
		t.Fatal(Mode(in))
	}
	in.RuntimeErr = config.ErrUnsafePath
	want(t, find(t, Offline(in), "runtime.mode", ""), Fail, "unsafe_local_path")
}

func TestStorageRows(t *testing.T) {
	in := inputFor(t, docs{personal: stdioPaper, selections: enabledPaper})
	if err := os.MkdirAll(in.Paths.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(in.Paths.DataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(in.Paths.StateDir, in.Paths.RuntimeDir); err != nil {
		t.Fatal(err)
	}
	checks := Offline(in)
	state := find(t, checks, "storage.dir", "state")
	want(t, state, OK, "")
	if state.Message != "Private: "+in.Paths.StateDir+"." {
		t.Fatal(state.Message)
	}
	want(t, find(t, checks, "storage.dir", "data"), Fail, "unsafe_local_path")
	want(t, find(t, checks, "storage.dir", "runtime"), Fail, "unsafe_local_path")
	if err := os.Remove(in.Paths.RuntimeDir); err != nil {
		t.Fatal(err)
	}
	if c := find(t, Offline(in), "storage.dir", "runtime"); c.Status != OK || !strings.HasPrefix(c.Message, "Absent; created on first use: ") {
		t.Fatal(c)
	}
	if _, err := os.Stat(in.Paths.RuntimeDir); !os.IsNotExist(err) {
		t.Fatal("doctor created the runtime directory")
	}
}
