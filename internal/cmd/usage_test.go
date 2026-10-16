package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

func TestUsageReasonKeepsOnlySafeNames(t *testing.T) {
	for _, tc := range []struct {
		kong, reason, argument, suggestion string
	}{
		{`unknown flag --frobnicate`, "Unknown flag --frobnicate.", "--frobnicate", ""},
		{`unknown flag --jsn, did you mean "--json"?`, "Unknown flag --jsn. Did you mean --json?", "--jsn", "--json"},
		{`unknown flag --x, did you mean one of "--xa", "--xb"?`, "Unknown flag --x. Did you mean --xa or --xb?", "--x", ""},
		// A token that does not look like a flag name may be a pasted secret.
		{`unknown flag --CANARY`, "Unknown flag.", "", ""},
		{`unknown flag --sk_CANARY, did you mean "--json"?`, "Unknown flag. Did you mean --json?", "", "--json"},
		{`unexpected argument CANARY`, "Unexpected argument.", "", ""},
		{`unexpected argument inptu, did you mean "input"?`, "Unexpected argument. Did you mean input?", "", "input"},
		{`expected "<mcp>"`, "Missing argument <mcp>.", "<mcp>", ""},
		{`expected "<mcp> ..."`, "Missing argument <mcp>.", "<mcp>", ""},
		{`expected "<mcp> <name> <value>"`, "Missing arguments <mcp> <name> <value>.", "<mcp>", ""},
		{`expected "set"`, "Missing subcommand set.", "", ""},
		// A branching argument is listed by its summary among subcommands.
		{`expected one of "tools <mcp> [flags]", "list", "enable", "disable"`, "Missing argument <mcp>.", "<mcp>", ""},
		{`expected one of "auth <mcp>", "status", "logout", "lock"`, "Missing argument <mcp>.", "<mcp>", ""},
		{`expected one of "tools <mcp> CANARY", "list"`, "Invalid command arguments.", "", ""},
		{`expected one of "a <x>", "b <y>", "list"`, "Invalid command arguments.", "", ""},
		{`expected one of "validate", "input", "profile"`, "Missing subcommand: choose validate, input or profile.", "", ""},
		{`missing flags: --file=STRING`, "Missing flag --file.", "--file", ""},
		{`--timeout: expected string value but got "EOL" (<EOL>)`, "The --timeout flag needs a value.", "--timeout", ""},
		{`--accept: missing value, expecting "<arg>"`, "The --accept flag needs a value.", "--accept", ""},
		{`--json: bool value must be true, 1, yes, false, 0 or no but got "CANARY"`, "Invalid value for the --json flag.", "--json", ""},
		{`something else "CANARY"`, "Invalid command arguments.", "", ""},
		// A hidden flag is never suggested.
		{`unknown flag --cache, did you mean "--cached"?`, "Unknown flag --cache.", "--cache", ""},
		{`unknown flag --x, did you mean one of "--cached", "--xb"?`, "Unknown flag --x. Did you mean --xb?", "--x", "--xb"},
	} {
		reason, argument, suggestion := usageReason(tc.kong, map[string]bool{"--cached": true})
		if reason != tc.reason || argument != tc.argument || suggestion != tc.suggestion {
			t.Errorf("%s: got %q %q %q", tc.kong, reason, argument, suggestion)
		}
	}
}

func TestHelpPathDropsPlaceholders(t *testing.T) {
	for in, want := range map[string]string{
		"tools <mcp>":                           "tools",
		"tools enable <mcp> <tool>":             "tools enable",
		"config input set <mcp> <name> <value>": "config input set",
		"auth <mcp>":                            "auth",
		"":                                      "",
		"list CANARY!":                          "",
	} {
		if got := helpPath(in); got != want {
			t.Errorf("helpPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// Product usage errors name the flag, argument or subcommand and point at the
// command's own help; nothing typed as a value is echoed.
func TestUsageErrorsNameWhatFailed(t *testing.T) {
	metadataEnv(t)
	for _, tc := range []struct {
		argv            []string
		message, action string
	}{
		{[]string{"list", "--frobnicate"}, "Unknown flag --frobnicate.", "Run mcparcel list --help."},
		{[]string{"list", "--jsn"}, "Unknown flag --jsn. Did you mean --json?", "Run mcparcel list --help."},
		{[]string{"tools"}, "Missing argument <mcp>.", "Run mcparcel tools --help."},
		// --cached is hidden, so no hint names it.
		{[]string{"tools", "echo", "--cachd"}, "Unknown flag --cachd.", "Run mcparcel tools --help."},
		{[]string{"tools", "--cache", "x"}, "Unknown flag --cache.", "Run mcparcel tools --help."},
		{[]string{"tools", "enable", "echo", "alpha", "--cache"}, "Unknown flag --cache.", "Run mcparcel tools enable --help."},
		{[]string{"tools", "echo", "disable", "CANARY"}, "Unexpected argument.", "Run mcparcel tools --help."},
		{[]string{"tools", "list"}, "Missing argument <mcp>.", "Run mcparcel tools list --help."},
		{[]string{"tools", "list", "echo", "CANARY"}, "Unexpected argument.", "Run mcparcel tools list --help."},
		{[]string{"tools", "disable", "echo"}, "Missing argument <tool>.", "Run mcparcel tools disable --help."},
		{[]string{"tools", "enable"}, "Missing arguments <mcp> <tool>.", "Run mcparcel tools enable --help."},
		{[]string{"tools", "enable", "echo", "x", "--cached"}, "Unknown flag --cached.", "Run mcparcel tools enable --help."},
		{[]string{"auth"}, "Missing argument <mcp>.", "Run mcparcel auth --help."},
		{[]string{"config"}, "Missing subcommand: choose validate, input or profile.", "Run mcparcel config --help."},
		{[]string{"config", "input", "set"}, "Missing arguments <mcp> <name> <value>.", "Run mcparcel config input set --help."},
		{[]string{"list", "CANARY"}, "Unexpected argument.", "Run mcparcel list --help."},
		{[]string{"local", "add"}, "Missing flag --file.", "Run mcparcel local add --help."},
		{[]string{"local", "add", "--file="}, "The --file flag needs a value.", "Run mcparcel local add --help."},
		{[]string{"catalog", "--domain", "a", "--domain", "b"}, "Give the --domain flag only once.", "Run mcparcel catalog --help."},
		{[]string{"call", "echo.x", "--timeout", "CANARY"}, "The --timeout flag needs a positive duration, such as 120s.", "Run mcparcel call --help."},
		{[]string{"call", "echo.x", "a=1", "--args", "{}"}, "Give the arguments one way: key=value assignments, --args or --args-file.", "Run mcparcel call --help."},
		{[]string{"call", "echo.x", "a=1", "a=2"}, "Duplicate key.", "Run mcparcel call --help."},
		{[]string{"call", "echo.x", "--meta", "[]"}, "--meta must be one JSON object.", "Run mcparcel call --help."},
	} {
		t.Run(strings.Join(tc.argv, " "), func(t *testing.T) {
			code, stdout, stderr := run(t, append(tc.argv, "--json")...)
			e := envelopeError(t, stdout)
			if code != ExitUsage || stderr != "" || e.Code != "invalid_arguments" || e.Message != tc.message || e.NextAction != tc.action || strings.Contains(stdout, "CANARY") || strings.Contains(stdout, `"suggestion":"--cached"`) {
				t.Fatalf("exit=%d stdout=%s stderr=%q", code, stdout, stderr)
			}
		})
	}
	// Human mode prints the same two lines on stderr.
	for argv, want := range map[string]string{
		"list --frobnicate":     "Unknown flag --frobnicate.\nRun mcparcel list --help.\n",
		"call echo.x --timeout": "The --timeout flag needs a value.\nRun mcparcel call --help.\n",
	} {
		code, stdout, stderr := run(t, strings.Fields(argv)...)
		if code != ExitUsage || stdout != "" || stderr != want {
			t.Fatalf("%s: exit=%d stdout=%q stderr=%q", argv, code, stdout, stderr)
		}
	}
}

// An unknown connection is named, with the closest known name, offline and
// without starting the runtime.
func TestUnknownConnectionSuggestsClosest(t *testing.T) {
	paths := ccEnv(t, false)
	for _, tc := range []struct {
		argv    []string
		message string
	}{
		{[]string{"call", "fornt.list"}, "No connection is named fornt. Did you mean front?"},
		{[]string{"inspect", "cod"}, "No connection is named cod. Did you mean code?"},
		{[]string{"auth", "frnt"}, "No connection is named frnt. Did you mean front?"},
		{[]string{"inspect", "zzzzzz"}, "No connection is named zzzzzz."},
		{[]string{"inspect", "local:fron"}, "No connection is named local:fron. Did you mean local:front?"},
	} {
		code, stdout, stderr := run(t, append(tc.argv, "--json")...)
		e := envelopeError(t, stdout)
		if code != 4 || stderr != "" || e.Code != "connection_unavailable" || e.Message != tc.message ||
			e.NextAction != "Run mcparcel list to see enabled connections, or mcparcel catalog for all of them." || e.Details == nil || e.Details.Connection != tc.argv[1] && !strings.HasPrefix(tc.argv[1], e.Details.Connection+".") {
			t.Fatalf("%v: exit=%d stdout=%s stderr=%q", tc.argv, code, stdout, stderr)
		}
	}
	noRuntime(t, paths)
}

// A fresh install has nothing to call: config_required says so in plain
// words and points at setup.
func TestFreshInstallConfigRequired(t *testing.T) {
	paths := metadataEnv(t)
	code, stdout, stderr := run(t, "call", "nope.x", "--json")
	e := envelopeError(t, stdout)
	if code != 2 || stderr != "" || e.Code != "config_required" || e.Message != "No connections are set up yet." || e.NextAction != "Run mcparcel setup, or mcparcel add <owner/repo>." {
		t.Fatalf("exit=%d stdout=%s stderr=%q", code, stdout, stderr)
	}
	noRuntime(t, paths)
}

func TestContextualFailureNamesDisabledConnection(t *testing.T) {
	metadataEnv(t)
	cli := &CLI{Inspect: InspectCmd{MCP: "front"}}
	err := contextualFailure(context.Background(), cli, "inspect", "inspect", config.ErrDisabled)
	e, ok := err.(*output.Error)
	if !ok || e.Code != "connection_disabled" || e.Message != "Connection front is disabled." || e.NextAction != "Run mcparcel enable front." {
		t.Fatalf("%#v", err)
	}
	// Several names, or a customized message, stay as they were.
	cli = &CLI{Enable: EnableCmd{MCPs: []string{"a", "b"}}}
	if err := contextualFailure(context.Background(), cli, "enable", "enable", config.ErrNotFound); err != config.ErrNotFound {
		t.Fatalf("%#v", err)
	}
	custom := output.NewError("connection_unavailable", nil)
	custom.Message = "Custom."
	if err := contextualFailure(context.Background(), &CLI{Inspect: InspectCmd{MCP: "front"}}, "inspect", "inspect", custom); err != custom {
		t.Fatalf("%#v", err)
	}
}

// A connection the install still lists but cannot use says its definition
// is gone, offline; local update and local remove look only at personal IDs
// and suggest only those.
func TestUnavailableConnectionIsNotMissing(t *testing.T) {
	paths, _, _ := metadataSeed(t)
	for name, argv := range map[string][]string{
		"removed":       {"call", "removed.search"},
		"local:removed": {"enable", "local:removed"},
	} {
		code, stdout, stderr := run(t, append(argv, "--json")...)
		e := envelopeError(t, stdout)
		if code != 4 || stderr != "" || e.Code != "connection_unavailable" || e.Message != "Connection "+name+" is unavailable: its definition is gone." ||
			e.Details == nil || e.Details.Connection != name || e.Details.Suggestion != "" {
			t.Fatalf("%v: exit=%d stdout=%s stderr=%q", argv, code, stdout, stderr)
		}
	}
	for argv, want := range map[string]string{
		"local remove pape":       "No personal connection is named pape. Did you mean paper?",
		"local remove local:pape": "No personal connection is named local:pape. Did you mean local:paper?",
		"local remove removed":    "No personal connection is named removed.",
	} {
		code, stdout, _ := run(t, append(strings.Fields(argv), "--json")...)
		if e := envelopeError(t, stdout); code != 4 || e.Code != "connection_unavailable" || e.Message != want {
			t.Fatalf("%s: exit=%d stdout=%s", argv, code, stdout)
		}
	}
	cli := &CLI{Local: LocalCmd{Update: LocalUpdateCmd{ID: "pape"}}}
	err := contextualFailure(context.Background(), cli, "local", "local update", config.ErrNotFound)
	if e, ok := err.(*output.Error); !ok || e.Message != "No personal connection is named pape. Did you mean paper?" {
		t.Fatalf("%#v", err)
	}
	noRuntime(t, paths)
}

// A generic config_required names the connection the command line gave.
func TestConfigRequiredNamesTheConnection(t *testing.T) {
	paths, _, _ := metadataSeed(t)
	code, stdout, stderr := run(t, "call", "missing.search", "--json")
	e := envelopeError(t, stdout)
	if code != 2 || stderr != "" || e.Code != "config_required" || e.Details == nil || e.Details.Connection != "missing" ||
		e.Message != "Connection missing needs configuration." || e.NextAction != "Run mcparcel inspect missing to see what the connection needs, then mcparcel setup." {
		t.Fatalf("exit=%d stdout=%s stderr=%q", code, stdout, stderr)
	}
	err := contextualFailure(context.Background(), &CLI{Tools: ToolsCmd{MCP: ToolsListCmd{MCP: "missing"}}}, "tools", "tools", config.ErrConfigRequired)
	if e, ok := err.(*output.Error); !ok || e.Message != "Connection missing needs configuration." {
		t.Fatalf("%#v", err)
	}
	noRuntime(t, paths)
}
