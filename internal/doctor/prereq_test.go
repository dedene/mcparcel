package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
)

func commandDocs(command, env string) docs {
	if env != "" {
		env = `,"env":` + env
	}
	return docs{
		personal:   `{"schemaVersion":1,"connections":{"paper":{"transport":{"type":"stdio","command":"` + command + `"` + env + `}}}}`,
		selections: enabledPaper,
	}
}

func TestCommandFoundOnRuntimePATH(t *testing.T) {
	in := inputFor(t, commandDocs("paper-mcp", ""))
	runtimeBin := mkdir(t, filepath.Join(filepath.Dir(in.Paths.Home), "runtime-bin"))
	found := executable(t, runtimeBin, "paper-mcp")
	in.Probe = runtimeclient.Probe{State: runtimeclient.ProbeRunning, DaemonVersion: "1.2.3", PID: 1, Status: &runtimeclient.Status{CapturedPath: "relative:" + runtimeBin}}
	c := find(t, Offline(in), "prereq.command", "local:paper")
	want(t, c, OK, "")
	if c.Message != "Found paper-mcp at "+found+" on the runtime's PATH." {
		t.Fatal(c.Message)
	}
}

func TestCommandMissingRuntimePATHFails(t *testing.T) {
	in := inputFor(t, commandDocs("paper-mcp", ""))
	executable(t, mkdir(t, in.PATH), "paper-mcp") // this shell has it; the runtime does not
	in.Probe = runtimeclient.Probe{State: runtimeclient.ProbeRunning, Status: &runtimeclient.Status{CapturedPath: "/nonexistent"}}
	c := find(t, Offline(in), "prereq.command", "local:paper")
	want(t, c, Fail, "connection_failed")
	if c.Message != "paper-mcp was not found on the runtime's PATH." || c.NextAction != "Install paper-mcp or add it to your PATH, then run mcparcel runtime restart." {
		t.Fatal(c)
	}
	// Headless: this process's PATH, and a miss fails.
	h := headlessInput(inputFor(t, commandDocs("paper-mcp", "")))
	c = find(t, Offline(h), "prereq.command", "local:paper")
	want(t, c, Fail, "connection_failed")
	if c.Message != "paper-mcp was not found on this process's PATH." || c.NextAction != "Install paper-mcp or add it to your PATH." {
		t.Fatal(c)
	}
}

func TestCommandMissingShellPATHWarns(t *testing.T) {
	in := inputFor(t, commandDocs("paper-mcp", ""))
	c := find(t, Offline(in), "prereq.command", "local:paper")
	want(t, c, Warn, "connection_failed")
	if c.Message != "paper-mcp was not found on this shell's PATH." {
		t.Fatal(c.Message)
	}
	executable(t, mkdir(t, in.PATH), "paper-mcp")
	if c = find(t, Offline(in), "prereq.command", "local:paper"); c.Status != OK || !strings.HasSuffix(c.Message, " on this shell's PATH.") {
		t.Fatal(c)
	}
}

func TestConnectionEnvPATHWins(t *testing.T) {
	in := inputFor(t, commandDocs("paper-mcp", ""))
	own := mkdir(t, filepath.Join(filepath.Dir(in.Paths.Home), "own-bin"))
	executable(t, own, "paper-mcp")
	in = inputFor(t, commandDocs("paper-mcp", `{"PATH":"`+own+`"}`))
	in.Probe = runtimeclient.Probe{State: runtimeclient.ProbeRunning, Status: &runtimeclient.Status{CapturedPath: "/nonexistent"}}
	c := find(t, Offline(in), "prereq.command", "local:paper")
	if c.Status != OK || !strings.HasSuffix(c.Message, " on the connection's PATH.") {
		t.Fatal(c)
	}
	in = inputFor(t, commandDocs("paper-mcp", `{"PATH":{"secret":"env:MY_PATH"}}`))
	want(t, find(t, Offline(in), "prereq.command", "local:paper"), Skip, "")
}

func TestAbsoluteCommand(t *testing.T) {
	in := inputFor(t, commandDocs("/bin/sh", ""))
	c := find(t, Offline(in), "prereq.command", "local:paper")
	if c.Status != OK || c.Message != "Found /bin/sh." {
		t.Fatal(c)
	}
	in = inputFor(t, commandDocs("/nonexistent/paper-mcp", ""))
	c = find(t, Offline(in), "prereq.command", "local:paper")
	want(t, c, Fail, "connection_failed")
	if c.NextAction != "Install the program at this path or fix the definition, then run mcparcel runtime restart." {
		t.Fatal(c.NextAction)
	}
	in = inputFor(t, docs{personal: `{"schemaVersion":1,"connections":{"paper":{"transport":{"type":"http","url":"https://a.example.invalid/mcp"}}}}`, selections: enabledPaper})
	if c = find(t, Offline(in), "prereq.command", "local:paper"); c.Status != Skip || c.Message != "Remote connection; use --live to check it." {
		t.Fatal(c)
	}
}

func TestInstallHints(t *testing.T) {
	for command, hint := range map[string]string{
		"npx": "Install Node.js 20 or newer", "node": "Install Node.js 20 or newer", "uvx": "Install uv", "uv": "Install uv",
		"docker": "Install and start Docker", "/opt/x/bin/tool": "Install the program at this path or fix the definition", "gh": "Install gh or add it to your PATH",
	} {
		if got := installHint(command, false); got != hint+"." {
			t.Fatalf("%s: %s", command, got)
		}
		if got := installHint(command, true); got != hint+", then run mcparcel runtime restart." {
			t.Fatalf("%s: %s", command, got)
		}
	}
}

func TestOnePasswordAppPresence(t *testing.T) {
	in := inputFor(t, opDocs("op://v/i/f", ""))
	c := find(t, Offline(in), "prereq.onepassword", "")
	want(t, c, Fail, "auth_failed")
	if err := os.MkdirAll(filepath.Join(in.AppDirs[0], "1Password.app"), 0o700); err != nil {
		t.Fatal(err)
	}
	if c = find(t, Offline(in), "prereq.onepassword", ""); c.Status != OK || !strings.Contains(c.Message, `does not check that "Integrate with other apps" is on`) {
		t.Fatal(c)
	}
	absent(t, Offline(inputFor(t, commandDocs("/bin/sh", ""))), "prereq.onepassword")
	absent(t, Offline(headlessInput(inputFor(t, opDocs("op://v/i/f", "")))), "prereq.onepassword")
}
