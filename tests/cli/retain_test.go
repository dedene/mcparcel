package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// With retention on (StateDir/fixture-retain; release builds always retain),
// the CLI starts the daemon from <data>/runtime/<version>/mcparcel, status
// reports that file, and doctor's runtime.binary is ok.
func TestRetainedBinaryBlackBox(t *testing.T) {
	r := newRig(t)
	r.write(filepath.Join(r.paths.StateDir, "fixture-retain"), "", 0o600)
	d := doctorData(t, r.check(r.run("doctor", "--json"), 0, ""))
	retained := filepath.Join(r.paths.DataDir, "runtime", "stage2-test-a", "mcparcel")
	if c := doctorRow(t, d, "runtime.binary", ""); c.Status != "ok" || c.Message != "The next start copies this binary to "+retained+"." {
		t.Fatal(c)
	}
	r.check(r.run("runtime", "restart", "--json"), 0, "")
	if s := r.status(); s.Executable != retained {
		t.Fatalf("daemon runs from %q, want %q", s.Executable, retained)
	}
	info, err := os.Lstat(retained)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o700 {
		t.Fatal(info, err)
	}
	a, _ := os.ReadFile(binaryA)
	b, _ := os.ReadFile(retained)
	if !bytes.Equal(a, b) {
		t.Fatal("the retained copy differs from the CLI binary")
	}
	d = doctorData(t, r.check(r.run("doctor", "--json"), 0, ""))
	if c := doctorRow(t, d, "runtime.binary", ""); c.Status != "ok" || c.Message != "Runs from its retained copy "+retained+"." {
		t.Fatal(c)
	}
}
