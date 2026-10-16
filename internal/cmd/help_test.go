package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
)

// Bare mcparcel is a short help on stdout, not a usage error.
func TestNoArgsPrintsShortHelp(t *testing.T) {
	for _, argv := range [][]string{nil, {"--no-input"}} {
		code, stdout, stderr := run(t, argv...)
		if code != 0 || stderr != "" {
			t.Fatalf("%q: code=%d stderr=%q", argv, code, stderr)
		}
		for _, want := range []string{
			appDescription,
			"  mcparcel tools context7\n",
			"  mcparcel call context7.resolve-library-id libraryName=react\n",
			"  mcparcel setup\n",
			"Use MCP tools                      tools, call\n",
			"Diagnose and help                  doctor, runtime, version, help, completion\n",
			"Run mcparcel --help for all commands.\n",
		} {
			if !strings.Contains(stdout, want) {
				t.Errorf("%q: short help misses %q:\n%s", argv, want, stdout)
			}
		}
		if strings.Contains(stdout, "daemon") || strings.Contains(stdout, "signal-wait") {
			t.Errorf("short help names a hidden command:\n%s", stdout)
		}
	}
	_, text, _ := run(t)
	code, stdout, stderr := run(t, "--json")
	var envelope struct {
		OK   bool     `json:"ok"`
		Data helpData `json:"data"`
	}
	if code != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &envelope) != nil || !envelope.OK || envelope.Data.Help != text {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

// Every top-level command sits in a help group, so the short help and the
// grouped --help list it.
func TestEveryCommandHasAGroup(t *testing.T) {
	parser, err := kong.New(&CLI{}, kong.ExplicitGroups(commandGroups))
	if err != nil {
		t.Fatal(err)
	}
	for _, child := range parser.Model.Children {
		if !child.Hidden && child.ClosestGroup() == nil {
			t.Errorf("%s has no group", child.Name)
		}
	}
	_, stdout, _ := run(t, "--help")
	last := -1
	for _, group := range commandGroups {
		at := strings.Index(stdout, "\n"+group.Title+"\n")
		if at < last {
			t.Fatalf("group %q missing or out of order:\n%s", group.Title, stdout)
		}
		last = at
	}
	if strings.Contains(stdout, "\nCommands:\n") {
		t.Errorf("an ungrouped command:\n%s", stdout)
	}
}

func TestHelpCommandMatchesHelpFlag(t *testing.T) {
	for _, path := range [][]string{nil, {"tools"}, {"auth", "status"}, {"call"}, {"tools", "paper"}} {
		_, want, _ := run(t, append(path, "--help")...)
		code, stdout, stderr := run(t, append([]string{"help"}, path...)...)
		if code != 0 || stderr != "" || stdout != want {
			t.Fatalf("help %q: code=%d stderr=%q\n%s\nwant\n%s", path, code, stderr, stdout, want)
		}
	}
	for _, argv := range [][]string{{"help", "bogus-CANARY"}, {"help", "auth", "status", "a", "b-CANARY"}} {
		code, stdout, stderr := run(t, argv...)
		if code != 2 || stdout != "" || !strings.Contains(stderr, "Run mcparcel") || strings.Contains(stderr, "CANARY") {
			t.Fatalf("%q: code=%d stdout=%q stderr=%q", argv, code, stdout, stderr)
		}
	}
	code, stdout, _ := run(t, "help", "bogus-CANARY", "--json")
	if e := envelopeError(t, stdout); code != 2 || e.Code != "invalid_arguments" || e.Message != "Unknown command." || strings.Contains(stdout, "CANARY") {
		t.Fatal(code, stdout)
	}
}

func TestMostUsedCommandsHaveExamples(t *testing.T) {
	for _, tc := range []struct{ command, example string }{
		{"call", "mcparcel call context7.resolve-library-id libraryName=react"},
		{"tools", "mcparcel tools context7"},
		{"enable", "mcparcel enable context7"},
		{"add", "mcparcel add acme/mcp-catalog"},
		{"join", "mcparcel join acme/mcp-catalog"},
		{"auth", "mcparcel auth status"},
	} {
		_, stdout, _ := run(t, tc.command, "--help")
		if !strings.Contains(stdout, "Examples:") || !strings.Contains(stdout, "    "+tc.example+"\n") {
			t.Errorf("%s help misses the example %q:\n%s", tc.command, tc.example, stdout)
		}
	}
}

// --json reaches every path: usage errors outside the product surface,
// version (command and flag) and help.
func TestJSONOnEveryPath(t *testing.T) {
	for _, argv := range [][]string{{"--json", "bogus-CANARY"}, {"bogus-CANARY", "--json"}, {"--json", "--bogus"}, {"version", "extra-CANARY", "--json"}} {
		code, stdout, stderr := run(t, argv...)
		if e := envelopeError(t, stdout); code != 2 || stderr != "" || e.Code != "invalid_arguments" || strings.Count(stdout, "\n") != 1 || strings.Contains(stdout, "CANARY") {
			t.Fatalf("%q: code=%d stdout=%q stderr=%q", argv, code, stdout, stderr)
		}
	}
	if _, stdout, _ := run(t, "--json", "bogus"); !strings.Contains(stdout, `"message":"Unknown command."`) || !strings.Contains(stdout, `"nextAction":"Run mcparcel --help."`) {
		t.Fatalf("unknown command: %s", stdout)
	}
	defer func(v, c, d string) { version, commit, date = v, c, d }(version, commit, date)
	for _, argv := range [][]string{{"version", "--json"}, {"--json", "version"}, {"--json", "--version"}, {"--version", "--json"}, {"-V", "--json"}} {
		code, stdout, stderr := run(t, argv...)
		if code != 0 || stderr != "" || stdout != `{"schemaVersion":1,"ok":true,"data":{"version":"dev"},"error":null}`+"\n" {
			t.Fatalf("%q: code=%d stdout=%q stderr=%q", argv, code, stdout, stderr)
		}
	}
	version, commit, date = "1.2.3", "abc1234", "2026-10-06"
	if _, stdout, _ := run(t, "version", "--json"); !strings.Contains(stdout, `"data":{"version":"1.2.3","commit":"abc1234","date":"2026-10-06"}`) {
		t.Fatalf("version: %s", stdout)
	}
	for _, argv := range [][]string{{"--json", "--help"}, {"tools", "--help", "--json"}, {"help", "auth", "--json"}} {
		code, stdout, stderr := run(t, argv...)
		var envelope struct {
			OK   bool     `json:"ok"`
			Data helpData `json:"data"`
		}
		if code != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &envelope) != nil || !strings.HasPrefix(envelope.Data.Help, "Usage: mcparcel") {
			t.Fatalf("%q: code=%d stdout=%q stderr=%q", argv, code, stdout, stderr)
		}
	}
}

func TestShortVersionFlag(t *testing.T) {
	_, want, _ := run(t, "version")
	for _, argv := range [][]string{{"-V"}, {"tools", "-V"}} {
		if code, stdout, stderr := run(t, argv...); code != 0 || stdout != want || stderr != "" {
			t.Fatalf("%q: code=%d stdout=%q stderr=%q", argv, code, stdout, stderr)
		}
	}
}

// help suggests a near miss of a subcommand as the command line does, calls
// any stray word an unknown command, and never documents a hidden command.
func TestHelpUnknownAndHiddenCommands(t *testing.T) {
	code, stdout, _ := run(t, "--json", "help", "runtime", "stauts")
	e := envelopeError(t, stdout)
	if code != 2 || e.Code != "invalid_arguments" || e.Message != "Unknown command. Did you mean status?" || e.NextAction != "Run mcparcel runtime --help." || !strings.Contains(stdout, `"suggestion":"status"`) {
		t.Fatal(code, stdout)
	}
	code, stdout, stderr := run(t, "help", "runtime", "CANARY")
	if code != 2 || stdout != "" || stderr != "Unknown command.\nRun mcparcel runtime --help.\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	for _, argv := range [][]string{{"help", "daemon"}, {"help", "signal-wait"}, {"help", completeCommand}} {
		code, stdout, stderr := run(t, argv...)
		if code != 2 || stdout != "" || stderr != "Unknown command.\nRun mcparcel --help.\n" || strings.Contains(stderr, "lock-fd") {
			t.Fatalf("%q: code=%d stdout=%q stderr=%q", argv, code, stdout, stderr)
		}
	}
	_, stdout, _ = run(t, "help", "completion")
	if strings.Contains(stdout, "in zsh, after compinit runs") || !strings.Contains(stdout, "~/.zshrc   # after compinit\n") {
		t.Fatalf("completion help:\n%s", stdout)
	}
}
