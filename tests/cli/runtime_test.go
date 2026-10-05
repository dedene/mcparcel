package cli_test

import (
	"debug/buildinfo"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/config"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
)

func TestTwoProcessesShareDaemon(t *testing.T) {
	r := newRig(t)
	a := r.call("fixture.counter")
	pid := r.status().PID
	b := r.call("fixture.counter")
	if structured(t, a)["count"] != float64(1) || structured(t, b)["count"] != float64(2) || r.status().PID != pid {
		t.Fatalf("counter/session: %s %s", a.stdout, b.stdout)
	}
}

func TestConcurrentDaemonStartupBlackBox(t *testing.T) {
	r := newRig(t)
	r.write(r.root+"/shell", "#!/bin/sh\nprintf shell-started > '"+r.root+"/shell-started'\nwhile [ ! -f '"+r.root+"/shell-release' ]; do /bin/sleep 0.01; done\nexec /bin/sh -c \"$3\"\n", 0o700)
	a := r.start(binaryA, "", "call", "fixture.counter", "--json")
	r.waitFile(r.root+"/shell-started", "shell-started")
	b := r.start(binaryA, "", "call", "fixture.counter", "--json")
	r.write(r.root+"/shell-release", "release", 0o600)
	av := r.check(r.finish(a), 0, "")
	bv := r.check(r.finish(b), 0, "")
	ac, bc := structured(t, av)["count"], structured(t, bv)["count"]
	if !((ac == float64(1) && bc == float64(2)) || (ac == float64(2) && bc == float64(1))) {
		t.Fatalf("two singleton sessions: %s %s", av.stdout, bv.stdout)
	}
	s := r.status()
	if !s.Running || s.PID <= 0 {
		t.Fatal(s)
	}
	children := ownedChildren(t, s.PID)
	if len(children) != 1 {
		t.Fatalf("daemon children: %v", children)
	}
}

func ownedChildren(t *testing.T, pid int) []int {
	t.Helper()
	out, e := exec.Command("/bin/ps", "-axo", "pid=,ppid=").Output()
	if e != nil {
		t.Error("cannot observe owned child processes:", e)
		return nil
	}
	var ids []int
	for _, line := range strings.Split(string(out), "\n") {
		var child, parent int
		if _, e := fmt.Sscanf(line, "%d %d", &child, &parent); e == nil && parent == pid {
			ids = append(ids, child)
		}
	}
	return ids
}

func TestChildPathFromLoginShellBlackBox(t *testing.T) {
	r := newRig(t)
	cpath := r.root + "/c:/usr/bin:/bin"
	r.write(r.root+"/shell", "#!/bin/sh\nexport PATH='"+cpath+"'\nexec /bin/sh -c \"$3\"\n", 0o700)
	if e := os.Mkdir(r.root+"/c", 0o700); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(fixtureBinary, r.root+"/c/fixture-command"); e != nil {
		t.Fatal(e)
	}
	c := r.personal.Connections["fixture"]
	c.Transport.Stdio.Command = config.Literal("fixture-command")
	r.personal.Connections["fixture"] = c
	r.save()
	for _, path := range []string{r.root + "/a", r.root + "/b"} {
		r.env = replaceEnv(r.env, "PATH", path)
		v := r.call("fixture.env", "name=PATH")
		if structured(t, v)["value"] != cpath {
			t.Fatal(v.stdout)
		}
	}
	if r.status().CapturedPath != cpath {
		t.Fatal("incorrect captured PATH")
	}
}

func TestCachedDoesNotStartBlackBox(t *testing.T) {
	r := newRig(t)
	r.env = replaceEnv(r.env, "HOME", r.root+"/absent-home")
	r.env = replaceEnv(r.env, "XDG_CONFIG_HOME", r.root+"/absent-config")
	r.env = replaceEnv(r.env, "XDG_STATE_HOME", r.root+"/absent-state")
	r.env = replaceEnv(r.env, "MCPARCEL_RUNTIME_DIR", r.root+"/absent-run")
	r.check(r.run("tools", "fixture", "--cached", "--json"), 6, "schema_cache_miss")
	for _, name := range []string{"absent-home", "absent-config", "absent-state", "absent-run"} {
		if _, e := os.Stat(r.root + "/" + name); !os.IsNotExist(e) {
			t.Fatalf("cached touched %s: %v", name, e)
		}
	}
	r.env = replaceEnv(r.env, "HOME", r.paths.Home)
	r.env = replaceEnv(r.env, "XDG_CONFIG_HOME", filepath.Dir(r.paths.ConfigDir))
	r.env = replaceEnv(r.env, "XDG_STATE_HOME", filepath.Dir(r.paths.StateDir))
	r.env = replaceEnv(r.env, "MCPARCEL_RUNTIME_DIR", r.paths.RuntimeDir)
	r.check(r.run("tools", "fixture", "--json"), 0, "")
	pid := r.status().PID
	childrenBefore := ownedChildren(t, pid)
	r.check(r.run("tools", "fixture", "--cached", "--json"), 6, "schema_cache_miss")
	if r.status().PID != pid || r.countEvents("bootstrap") != 0 || !reflect.DeepEqual(ownedChildren(t, pid), childrenBefore) {
		t.Fatal("cached started/authenticated")
	}
}

func TestRuntimeStatusBlackBox(t *testing.T) {
	r := newRig(t)
	s := r.status()
	if s.Running || s.PID != 0 {
		t.Fatal(s)
	}
	for _, p := range []string{r.paths.RuntimeDir, r.paths.StateDir} {
		if _, e := os.Stat(p); !os.IsNotExist(e) {
			t.Fatalf("status created %s", p)
		}
	}
	r.call("fixture.counter")
	s = r.status()
	if !s.Running || s.PID <= 0 || s.Socket != r.paths.SocketFile || s.Log != r.paths.LogFile || s.BinaryVersion != "stage2-test-a" || s.CapturedPath == "" {
		t.Fatal(s)
	}
	v := r.run("runtime", "status")
	want := []string{"Runtime: running", "PID: ", "Version: stage2-test-a", "Active calls: 0", "PATH: ", "Environment: login shell", "Socket: ", "Log: "}
	lines := strings.Split(strings.TrimSuffix(v.stdout, "\n"), "\n")
	if v.code != 0 || len(lines) != len(want) {
		t.Fatalf("human status %q %q", v.stdout, v.stderr)
	}
	for i, prefix := range want {
		if !strings.HasPrefix(lines[i], prefix) {
			t.Fatal(v.stdout)
		}
	}
}

func TestVersionMismatchBlackBox(t *testing.T) {
	r := newRig(t)
	r.call("fixture.counter")
	pid := r.status().PID
	r.check(r.finish(r.start(binaryB, "", "call", "fixture.counter", "--json")), 6, "runtime_version_mismatch")
	if syscall.Kill(pid, 0) != nil {
		t.Fatal("old daemon died")
	}
	v := r.check(r.finish(r.start(binaryB, "", "runtime", "restart", "--json")), 0, "")
	var d runtimeclient.RestartData
	if e := json.Unmarshal(v.envelope.Data, &d); e != nil {
		t.Fatal(e)
	}
	r.pids[d.Status.PID] = true
	if !d.Restarted || d.Status.PID == pid || d.Status.BinaryVersion != "stage2-test-b" {
		t.Fatal(v.stdout)
	}
}

func TestRestartBusyAndForceBlackBox(t *testing.T) {
	r := newRig(t)
	a := r.start(binaryA, "", "call", "fixture.wait", "--json")
	r.waitFile(r.root+"/started", "wait")
	pid := r.status().PID
	r.check(r.run("runtime", "restart", "--json"), 6, "runtime_busy")
	v := r.check(r.run("runtime", "restart", "--force", "--json"), 0, "")
	r.check(r.finish(a), 6, "outcome_unknown")
	var d runtimeclient.RestartData
	if e := json.Unmarshal(v.envelope.Data, &d); e != nil {
		t.Fatal(e)
	}
	r.pids[d.Status.PID] = true
	if d.Status.PID == pid || !d.Restarted {
		t.Fatal(v.stdout)
	}
}

func TestSIGINTIsolationBlackBox(t *testing.T) {
	r := newRig(t)
	r.stdio("other", "")
	a := r.start(binaryA, "", "call", "fixture.wait", "--json")
	r.waitFile(r.root+"/started", "wait")
	pid := r.status().PID
	b := r.call("other.counter")
	if structured(t, b)["count"] != float64(1) {
		t.Fatal(b.stdout)
	}
	if e := a.cmd.Process.Signal(os.Interrupt); e != nil {
		t.Fatal(e)
	}
	v := r.check(r.finish(a), 130, "canceled")
	requestID, ok := v.envelope.Error.Details["requestId"].(string)
	if !ok || requestID == "" || v.envelope.Error.Details["dispatched"] != true || v.envelope.Error.Details["outcome"] != "unknown" {
		t.Fatal(v.stdout)
	}
	if r.status().PID != pid {
		t.Fatal("SIGINT killed daemon")
	}
}

func TestRuntimeConfigMismatchBlackBox(t *testing.T) {
	r := newRig(t)
	r.call("fixture.counter")
	pid := r.status().PID
	r.env = replaceEnv(r.env, "XDG_CONFIG_HOME", r.root+"/other-config")
	r.check(r.run("call", "fixture.counter", "--json"), 6, "runtime_config_mismatch")
	if syscall.Kill(pid, 0) != nil {
		t.Fatal("mismatch stopped daemon")
	}
	r.env = replaceEnv(r.env, "XDG_CONFIG_HOME", filepath.Dir(r.paths.ConfigDir))
	if structured(t, r.call("fixture.counter"))["count"] != float64(2) {
		t.Fatal("mismatch executed tool")
	}
}

func TestSIGINTDuringRuntimeStartupBlackBox(t *testing.T) {
	r := newRig(t)
	if e := syscall.Mkfifo(r.root+"/shell-release", 0o600); e != nil {
		t.Fatal(e)
	}
	r.write(r.root+"/shell", "#!/bin/sh\nprintf shell-started > '"+r.root+"/shell-started'\n/bin/cat '"+r.root+"/shell-release' >/dev/null\nexec /bin/sh -c \"$3\"\n", 0o700)
	p := r.start(binaryA, "", "call", "fixture.counter", "--json")
	r.waitFile(r.root+"/shell-started", "shell-started")
	if err := p.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	v := r.finish(p)
	r.write(r.root+"/shell-release", "release", 0o600)
	r.check(v, 130, "canceled")
}

func TestBlackBoxBinariesRaceInstrumented(t *testing.T) {
	for _, binary := range []string{binaryA, binaryB} {
		info, err := buildinfo.ReadFile(binary)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, setting := range info.Settings {
			if setting.Key == "-race" && setting.Value == "true" {
				found = true
			}
		}
		if !found {
			t.Fatalf("black-box binary lacks race instrumentation: %s", binary)
		}
	}
}

func TestRuntimeStopBlackBox(t *testing.T) {
	r := newRig(t)
	r.call("fixture.counter")
	pid := r.status().PID
	v := r.check(r.run("runtime", "stop", "--json"), 0, "")
	var data struct {
		Stopped    bool
		WasRunning bool
	}
	if e := json.Unmarshal(v.envelope.Data, &data); e != nil || !data.Stopped || !data.WasRunning {
		t.Fatal(v.stdout, e)
	}
	assertStopped(t, r, pid)
	if structured(t, r.call("fixture.counter"))["count"] != float64(1) || r.status().PID == pid {
		t.Fatal("next call did not start fresh daemon")
	}
	human := r.run("runtime", "stop")
	if human.code != 0 || human.stdout != "Runtime stopped.\n" {
		t.Fatal(human.stdout, human.stderr)
	}
}

func assertStopped(t *testing.T, r *rig, pid int) {
	t.Helper()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()
	for syscall.Kill(pid, 0) != syscall.ESRCH {
		select {
		case <-tick.C:
		case <-timeout.C:
			t.Fatal("daemon survived stop")
		}
	}
	if _, e := os.Stat(r.paths.SocketFile); !os.IsNotExist(e) {
		t.Fatal("socket survived", e)
	}
	if r.status().Running {
		t.Fatal("status says running")
	}
}

func TestRuntimeStopBusyAndForceBlackBox(t *testing.T) {
	r := newRig(t)
	a := r.start(binaryA, "", "call", "fixture.wait", "--json")
	r.waitFile(r.root+"/started", "wait", a)
	pid := r.status().PID
	r.check(r.run("runtime", "stop", "--json"), 6, "runtime_busy")
	r.env = replaceEnv(r.env, "XDG_CONFIG_HOME", r.root+"/different-config")
	v := r.check(r.finish(r.start(binaryB, "", "runtime", "stop", "--force", "--json")), 0, "")
	r.check(r.finish(a), 6, "outcome_unknown")
	if !strings.Contains(string(v.envelope.Data), `"wasRunning":true`) {
		t.Fatal(v.stdout)
	}
	assertStopped(t, r, pid)
}

func TestRuntimeStopWhenStoppedCreatesNothing(t *testing.T) {
	r := newRig(t)
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "MCPARCEL_RUNTIME_DIR"} {
		r.env = replaceEnv(r.env, name, r.root+"/absent-"+name)
	}
	for _, force := range []bool{false, true} {
		args := []string{"runtime", "stop", "--json"}
		if force {
			args = append(args, "--force")
		}
		v := r.check(r.run(args...), 0, "")
		var data map[string]bool
		if e := json.Unmarshal(v.envelope.Data, &data); e != nil || !data["stopped"] || data["wasRunning"] {
			t.Fatal(v.stdout, e)
		}
	}
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "MCPARCEL_RUNTIME_DIR"} {
		if _, e := os.Stat(r.root + "/absent-" + name); !os.IsNotExist(e) {
			t.Fatal("stop created files", name, e)
		}
	}
}

func TestSIGINTDuringArgumentReadBlackBox(t *testing.T) {
	r := newRig(t)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	p := r.startReader(binaryA, reader, "call", "fixture.counter", "--args-file", "-", "--json")
	// More than pipe capacity establishes that the command is consuming stdin.
	if _, err := writer.Write([]byte(strings.Repeat(" ", 1024*1024))); err != nil {
		t.Fatal(err)
	}
	if err := p.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	r.check(r.finish(p), 130, "canceled")
	if r.status().Running {
		t.Fatal("argument cancellation started runtime")
	}
}

func TestRuntimeDirThroughSymlinkBlackBox(t *testing.T) {
	r := newRig(t)
	if err := os.Mkdir(r.root+"/linked", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(r.root+"/linked", r.root+"/l"); err != nil {
		t.Fatal(err)
	}
	r.env = replaceEnv(r.env, "MCPARCEL_RUNTIME_DIR", r.root+"/l/run")
	var err error
	if r.paths, err = config.ResolvePaths(func(k string) string { return envValue(r.env, k) }, r.paths.Home, "/private/tmp", os.Getuid()); err != nil {
		t.Fatal(err)
	}
	r.call("fixture.counter")
	if s := r.status(); s.Socket != r.root+"/linked/run/daemon.sock" {
		t.Fatal(s.Socket)
	}
	r.check(r.run("runtime", "stop", "--json"), 0, "")
}
