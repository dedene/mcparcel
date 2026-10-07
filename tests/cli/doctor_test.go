package cli_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
	"github.com/dedene/mcparcel/internal/testutil"
)

func doctorData(t *testing.T, v result) output.DoctorData {
	t.Helper()
	var d output.DoctorData
	if err := json.Unmarshal(v.envelope.Data, &d); err != nil || len(d.Checks) == 0 {
		t.Fatalf("doctor data: %s %v", v.stdout, err)
	}
	return d
}

func doctorRow(t *testing.T, d output.DoctorData, id, subject string) output.DoctorCheck {
	t.Helper()
	for _, c := range d.Checks {
		if c.ID == id && c.Subject == subject {
			return c
		}
	}
	t.Fatalf("no %s %q row in %+v", id, subject, d.Checks)
	return output.DoctorCheck{}
}

// tree records every entry below root with its mode, size and mtime.
func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out[path] = fmt.Sprintf("%v %d %d", info.Mode(), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameTree(t *testing.T, before, after map[string]string) {
	t.Helper()
	for path, v := range after {
		if before[path] != v {
			t.Errorf("doctor changed %s: %q -> %q", path, before[path], v)
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			t.Errorf("doctor removed %s", path)
		}
	}
}

func TestDoctorOfflineBlackBox(t *testing.T) {
	r := newRig(t)
	d := doctorData(t, r.check(r.run("doctor", "--json"), 0, ""))
	if d.Version != "stage2-test-a" || d.Mode != "desktop" || d.Live || d.Summary.Fail != 0 {
		t.Fatalf("%+v", d)
	}
	if c := doctorRow(t, d, "config.connection", "local:fixture"); c.Status != "ok" {
		t.Fatal(c)
	}
	if c := doctorRow(t, d, "prereq.command", "local:fixture"); c.Status != "ok" || c.Message != "Found "+fixtureBinary+"." {
		t.Fatal(c)
	}
	if c := doctorRow(t, d, "runtime.version", ""); c.Status != "ok" || !strings.HasPrefix(c.Message, "Not running.") {
		t.Fatal(c)
	}
	v := r.run("doctor")
	if v.code != 0 || v.stderr != "" || !strings.HasPrefix(v.stdout, "Status  Check") || !strings.Contains(v.stdout, "\nok      config.connection") {
		t.Fatalf("%d %q %q", v.code, v.stdout, v.stderr)
	}
	if _, err := os.Lstat(r.paths.SocketFile); !errors.Is(err, os.ErrNotExist) || len(r.daemonPIDs()) != 0 {
		t.Fatal("doctor started a runtime", err)
	}
	r.check(r.run("doctor", "--live", "--json"), 2, "invalid_arguments")
	r.check(r.run("doctor", "nope", "--json"), 4, "connection_unavailable")
}

func TestDoctorFreshHomeWritesNothingBlackBox(t *testing.T) {
	r := newRig(t)
	before := tree(t, r.root)
	r.check(r.run("doctor", "--json"), 0, "")
	if v := r.run("doctor", "fixture"); v.code != 0 {
		t.Fatal(v.stdout, v.stderr)
	}
	sameTree(t, before, tree(t, r.root))
}

func TestDoctorVersionMismatchBlackBox(t *testing.T) {
	r := newRig(t)
	r.check(r.run("runtime", "restart", "--json"), 0, "")
	pid := r.status().PID
	v := r.finish(r.start(binaryB, "", "doctor", "--json"))
	d := doctorData(t, r.check(v, 8, "doctor_failed"))
	c := doctorRow(t, d, "runtime.version", "")
	if c.Status != "fail" || c.Code != "runtime_version_mismatch" || c.Message != fmt.Sprintf("The runtime runs stage2-test-a (PID %d); this CLI is stage2-test-b.", pid) {
		t.Fatal(c)
	}
	if c.NextAction != "Run mcparcel runtime restart when no calls are active." || d.Summary.Fail != 1 {
		t.Fatal(c, d.Summary)
	}
	// Human output: rows on stdout, the failure on stderr, exit 8.
	v = r.finish(r.start(binaryB, "", "doctor"))
	if v.code != 8 || !strings.Contains(v.stdout, "fail    runtime.version") || v.stderr != "1 check failed.\nRun mcparcel runtime restart when no calls are active.\n" {
		t.Fatalf("%d %q %q", v.code, v.stdout, v.stderr)
	}
	if s := r.status(); !s.Running || s.PID != pid {
		t.Fatal("doctor disturbed the daemon", s)
	}
}

func TestDoctorStaleSocketBlackBox(t *testing.T) {
	r := newRig(t)
	r.check(r.run("runtime", "restart", "--json"), 0, "")
	pid := r.status().PID
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		<-time.After(10 * time.Millisecond)
	}
	if _, err := os.Lstat(r.paths.SocketFile); err != nil {
		t.Fatal("socket gone", err)
	}
	d := doctorData(t, r.check(r.run("doctor", "--json"), 0, ""))
	if c := doctorRow(t, d, "runtime.version", ""); c.Status != "ok" || c.Message != "Not running. A stale socket is left; the next start removes it." {
		t.Fatal(c)
	}
	r.check(r.run("tools", "fixture", "--json"), 0, "")
}

func TestDoctorLiveBlackBox(t *testing.T) {
	r := newRig(t)
	d := doctorData(t, r.check(r.run("doctor", "fixture", "--live", "--json"), 0, ""))
	c := doctorRow(t, d, "live.tools", "local:fixture")
	if !d.Live || c.Status != "ok" || !strings.HasPrefix(c.Message, "Connected and listed ") || d.Checks[len(d.Checks)-1].ID != "live.tools" {
		t.Fatal(c, d.Live)
	}

	o := newOAuthRig(t, testutil.AuthServerOptions{Registration: true}, &config.OAuth{Type: "oauth"})
	d = doctorData(t, o.check(o.run("doctor", "n", "--live", "--no-input", "--json"), 8, "doctor_failed"))
	c = doctorRow(t, d, "live.tools", "local:n")
	if c.Status != "fail" || c.Code != "auth_required" {
		t.Fatal(c)
	}
	if o.page() != "" || o.as.Requests("") != 0 {
		t.Fatal("doctor --live --no-input opened a browser or contacted the authorization server")
	}
}

func TestDoctorLiveHeadlessBlackBox(t *testing.T) {
	r := newFrontRig(t, map[string]any{"approvalDialog": true})
	d := doctorData(t, r.check(r.run("doctor", "front", "--live", "--json"), 8, "doctor_failed"))
	if d.Mode != "headless" {
		t.Fatal(d.Mode)
	}
	c := doctorRow(t, d, "live.tools", "local:front")
	if c.Status != "fail" || c.Code != "" || c.NextAction != "Start the runtime through claw-wrap (any allowed call), then run doctor --live again." {
		t.Fatal(c)
	}
	if e := doctorRow(t, d, "credentials.env", "local:front"); e.Status != "ok" {
		t.Fatal(e)
	}
	r.noRuntimeContact()
	// A runtime started by a regular call (with the wrapper's variables) is used.
	r.check(r.run("tools", "front", "--json"), 0, "")
	d = doctorData(t, r.check(r.run("doctor", "front", "--live", "--json"), 0, ""))
	if c = doctorRow(t, d, "live.tools", "local:front"); c.Status != "ok" {
		t.Fatal(c)
	}
	if _, err := os.Stat(filepath.Join(r.paths.StateDir, "fixture-dialog-args")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("headless doctor showed a dialog", err)
	}
}

func TestDoctorHeadlessReadOnlyConfigBlackBox(t *testing.T) {
	r := newFrontRig(t, nil)
	if err := os.Chmod(r.paths.ConfigDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(r.paths.ConfigDir, 0o700) })
	before := tree(t, r.root)
	d := doctorData(t, r.check(r.run("doctor", "--json"), 0, ""))
	if d.Mode != "headless" || doctorRow(t, d, "runtime.mode", "").Message != "Headless mode (state root "+r.paths.StateRoot+")." {
		t.Fatalf("%+v", d.Checks)
	}
	if c := doctorRow(t, d, "config.connection", "local:front"); c.Status != "ok" {
		t.Fatal(c)
	}
	sameTree(t, before, tree(t, r.root))
	r.noRuntimeContact()
}
