package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestClosestSuggestsLikelyTypos(t *testing.T) {
	ids := []string{"context7", "echo", "github", "linear"}
	for _, tc := range []struct{ name, want string }{
		{"ech", "echo"},
		{"contxt7", "context7"},
		{"githbu", "github"},
		{"echo", ""}, // an exact match is no suggestion
		{"zzzz", ""}, // nothing near
		{"x", ""},    // one letter is never a typo of a four-letter name
		{"linera", "linear"},
	} {
		if got := Closest(tc.name, ids); got != tc.want {
			t.Errorf("Closest(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := Closest("ab", []string{"ac", "ad"}); got != "ac" {
		t.Fatalf("tie = %q, want the earlier candidate", got)
	}
}

func TestSafeNamesRejectValues(t *testing.T) {
	for name, want := range map[string]bool{
		"echo": true, "context7": true, "local:paper": true, "github:acme/tools#paper": true,
		"Echo": false, "ech o": false, "a/b": false, "local:": false, "op://vault/item": false, "": false,
		strings.Repeat("a", 65): false,
	} {
		if got := SafeConnectionName(name); got != want {
			t.Errorf("SafeConnectionName(%q) = %v", name, got)
		}
	}
	for name, want := range map[string]bool{
		"resolve-library-id": true, "get_issue": true, "ns.tool": true, "a/b": true,
		"bad name": false, "x\x1b[31m": false, "": false, strings.Repeat("t", 129): false,
	} {
		if got := SafeToolName(name); got != want {
			t.Errorf("SafeToolName(%q) = %v", name, got)
		}
	}
}

func TestConnectionUnavailableNamesTheConnection(t *testing.T) {
	err := ConnectionUnavailableError("ech", "echo")
	if err.Code != "connection_unavailable" || ExitCode(err) != 4 ||
		err.Message != "No connection is named ech. Did you mean echo?" ||
		err.NextAction != "Run mcparcel list to see enabled connections, or mcparcel catalog for all of them." {
		t.Fatalf("%+v", err)
	}
	if err.Details == nil || err.Details.Connection != "ech" || err.Details.Suggestion != "echo" {
		t.Fatalf("details %+v", err.Details)
	}
	if err := ConnectionUnavailableError("ech", ""); err.Message != "No connection is named ech." || err.Details.Suggestion != "" {
		t.Fatalf("%+v", err)
	}
	// A name outside the grammar may be a pasted value: generic, no details.
	if err := ConnectionUnavailableError("CANARY-token", "echo"); err.Message != "The connection is not defined." || err.Details != nil {
		t.Fatalf("%+v", err)
	}
}

// A connection the install lists but cannot use, a name local update or
// local remove does not know, and a connection that needs configuration are
// named; a name outside the grammar gives the generic message, no details.
func TestConnectionStateErrorsNameTheConnection(t *testing.T) {
	gone := ConnectionGoneError("paper")
	if gone.Code != "connection_unavailable" || ExitCode(gone) != 4 || gone.Message != "Connection paper is unavailable: its definition is gone." ||
		gone.NextAction != "Run mcparcel inspect paper to see it, or mcparcel catalog for connections you can use." ||
		gone.Details == nil || gone.Details.Connection != "paper" || gone.Details.Suggestion != "" {
		t.Fatalf("%+v %+v", gone, gone.Details)
	}
	personal := PersonalConnectionUnavailableError("pape", "paper")
	if personal.Code != "connection_unavailable" || ExitCode(personal) != 4 || personal.Message != "No personal connection is named pape. Did you mean paper?" ||
		personal.NextAction != "Run mcparcel catalog to see your personal connections, listed as local:<id>." ||
		personal.Details == nil || personal.Details.Connection != "pape" || personal.Details.Suggestion != "paper" {
		t.Fatalf("%+v %+v", personal, personal.Details)
	}
	if e := PersonalConnectionUnavailableError("pape", "Bad Name"); e.Message != "No personal connection is named pape." || e.Details.Suggestion != "" {
		t.Fatalf("%+v", e)
	}
	required := ConfigRequiredError("notion")
	if required.Code != "config_required" || ExitCode(required) != 2 || required.Message != "Connection notion needs configuration." ||
		required.NextAction != "Run mcparcel inspect notion to see what the connection needs, then mcparcel setup." ||
		required.Details == nil || required.Details.Connection != "notion" {
		t.Fatalf("%+v %+v", required, required.Details)
	}
	for _, e := range []*Error{ConnectionGoneError("CANARY token"), PersonalConnectionUnavailableError("CANARY token", "paper"), ConfigRequiredError("CANARY token")} {
		if e.Details != nil || e.Message != NewError(e.Code, nil).Message || strings.Contains(e.Message+e.NextAction, "CANARY") {
			t.Fatalf("%+v", e)
		}
	}
}

// tools list, enable and disable shadow a connection of that name in tools
// <mcp>, so the command that lists its tools goes through tools list.
func TestToolsCommandAvoidsTheSubcommands(t *testing.T) {
	for connection, want := range map[string]string{
		"echo": "mcparcel tools echo", "list": "mcparcel tools list list", "enable": "mcparcel tools list enable",
		"disable": "mcparcel tools list disable", "local:enable": "mcparcel tools local:enable",
	} {
		if got := ToolsCommand(connection); got != want {
			t.Errorf("ToolsCommand(%q) = %q, want %q", connection, got, want)
		}
	}
	if e := ToolNotFoundError("enable", "x", ""); e.NextAction != "Run mcparcel tools list enable." {
		t.Fatalf("%+v", e)
	}
}

func TestToolNotFoundNamesConnectionAndTool(t *testing.T) {
	err := ToolNotFoundError("echo", "ech", "echo")
	if err.Code != "tool_not_found" || ExitCode(err) != 2 ||
		err.Message != "Connection echo has no tool named ech. Did you mean echo?" || err.NextAction != "Run mcparcel tools echo." {
		t.Fatalf("%+v", err)
	}
	if d := err.Details; d == nil || d.Connection != "echo" || d.Tool != "ech" || d.Suggestion != "echo" {
		t.Fatalf("details %+v", err.Details)
	}
	err = ToolNotFoundError("echo", "bad name CANARY", "")
	if err.Message != "Connection echo has no tool by that name." || err.Details.Tool != "" || strings.Contains(fmt.Sprint(err.Details), "CANARY") {
		t.Fatalf("%+v %+v", err, err.Details)
	}
	if err := ToolNotFoundError("Bad", "x", ""); err.Message != "The tool is not advertised by this connection." || err.Details != nil {
		t.Fatalf("%+v", err)
	}
}

func TestIdentityDetailsReachTheEnvelope(t *testing.T) {
	var b bytes.Buffer
	if err := WriteJSON(&b, nil, ToolNotFoundError("echo", "ech", "echo")); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Error struct {
			Code    string
			Details map[string]any
		}
	}
	if err := json.Unmarshal(b.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Error.Code != "tool_not_found" || v.Error.Details["connection"] != "echo" || v.Error.Details["tool"] != "ech" || v.Error.Details["suggestion"] != "echo" {
		t.Fatal(b.String())
	}
	if _, ok := v.Error.Details["argument"]; ok {
		t.Fatal("empty fields are omitted:", b.String())
	}
}

func TestArgumentsMessageIsASentence(t *testing.T) {
	for in, want := range map[string]string{
		`invalid arguments: argument "times" must be integer`:                     `Argument "times" must be integer.`,
		`invalid arguments: Arguments do not match the tool schema at /x (type).`: `Arguments do not match the tool schema at /x (type).`,
		`invalid arguments: --meta must be one JSON object`:                       `--meta must be one JSON object.`,
		`invalid arguments`: "Invalid call arguments.",
	} {
		if got := ArgumentsMessage(errors.New(in)); got != want {
			t.Errorf("ArgumentsMessage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUsageAndFreshInstallMessages(t *testing.T) {
	err := UsageError("tools", "Unknown flag --jsn. Did you mean --json?", "--jsn", "--json")
	if err.Code != "invalid_arguments" || ExitCode(err) != 2 || err.NextAction != "Run mcparcel tools --help." || err.Details.Argument != "--jsn" || err.Details.Suggestion != "--json" {
		t.Fatalf("%+v", err)
	}
	if err := CommandUsageError(""); err.Message != "Invalid command arguments." || err.NextAction != "Run mcparcel --help." || err.Details != nil {
		t.Fatalf("%+v", err)
	}
	if err := NoConnectionsError(); err.Code != "config_required" || err.Message != "No connections are set up yet." || err.NextAction != "Run mcparcel setup, or mcparcel add <owner/repo>." {
		t.Fatalf("%+v", err)
	}
	if err := CallTargetError(); err.Message != "Name the tool as <mcp>.<tool>, for example context7.resolve-library-id." {
		t.Fatalf("%+v", err)
	}
}

func TestNameCallTimeout(t *testing.T) {
	err := NewError("outcome_unknown", &Details{RequestID: "r", Dispatched: true})
	NameCallTimeout(err, "echo", "wait", 2*time.Second)
	if err.Message != "echo.wait did not answer within 2s; it may have run." || err.Details.Connection != "echo" || err.Details.Tool != "wait" || !err.Details.Dispatched || ExitCode(err) != 6 || err.NextAction != "Inspect remote state before deciding whether to call again." {
		t.Fatalf("%+v %+v", err, err.Details)
	}
	err = NewError("timeout", nil)
	NameCallTimeout(err, "echo", "wait", 120*time.Second)
	if err.Message != "echo.wait timed out after 120s before it was sent." {
		t.Fatal(err.Message)
	}
	// A name outside its grammar could be a pasted secret: not shown.
	err = NewError("outcome_unknown", nil)
	NameCallTimeout(err, "echo", "sk-secret value", 1500*time.Millisecond)
	if err.Message != "The call did not answer within 1.5s; it may have run." || err.Details != nil {
		t.Fatalf("%+v", err)
	}
	// Another code is left as it is.
	err = NewError("tool_error", nil)
	NameCallTimeout(err, "echo", "wait", time.Second)
	if err.Message != NewError("tool_error", nil).Message {
		t.Fatal(err.Message)
	}
}
