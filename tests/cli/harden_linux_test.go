package cli_test

import (
	"errors"
	"fmt"
	"os"
	"testing"
)

// A headless CLI carries the tokenEnv token it passes to the daemon it
// starts, so the CLI is non-dumpable too (D20): while a call waits, a
// same-uid process cannot read /proc/<cli>/environ.
func TestCLINotDumpableBlackBox(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any process's environ")
	}
	r := newRig(t)
	a := r.start(binaryA, "", "call", "fixture.wait", "--json")
	r.waitFile(r.root+"/started", "wait")
	if _, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", a.cmd.Process.Pid)); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("CLI environ readable (%v): PR_SET_DUMPABLE not cleared", err)
	}
	if err := a.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	r.check(r.finish(a), 130, "canceled")
}
