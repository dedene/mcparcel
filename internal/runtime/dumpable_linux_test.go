package runtime

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// TestHardenProcess runs in a child test binary, so the flag does not leak
// into this package's other tests. The hardened child then starts a
// grandchild that reports its own flag: execve resets it.
func TestHardenProcess(t *testing.T) {
	dumpable := func() int {
		v, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	run := func(mode string) string {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHardenProcess$", "-test.v")
		cmd.Env = append(os.Environ(), "MCPARCEL_TEST_HARDEN="+mode)
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "--- PASS: TestHardenProcess") {
			t.Fatalf("%s: %v\n%s", mode, err, out)
		}
		return string(out)
	}
	switch os.Getenv("MCPARCEL_TEST_HARDEN") {
	case "":
		run("harden")
	case "harden":
		if v := dumpable(); v != 1 {
			t.Fatal("dumpable before:", v)
		}
		HardenProcess()
		if v := dumpable(); v != 0 {
			t.Fatal("dumpable after:", v)
		}
		if out := run("report"); !strings.Contains(out, "child dumpable=1") {
			t.Fatalf("child:\n%s", out)
		}
	case "report":
		fmt.Printf("child dumpable=%d\n", dumpable())
	}
}
