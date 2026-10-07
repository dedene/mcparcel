package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// policyPersonal: front allows read_conversation and send_message but also
// denies send_message; open has no policy; off is disabled in selections.
const policyPersonal = `{"schemaVersion":1,"connections":{
"front":{"transport":{"type":"http","url":"https://mcp.example/mcp"},"toolPolicy":{"allow":["read_conversation","send_message"],"deny":["send_message"]}},
"open":{"transport":{"type":"http","url":"https://open.example/mcp"}},
"off":{"transport":{"type":"http","url":"https://off.example/mcp"}}}}`

const policySelections = `{"schemaVersion":1,"revision":1,"connections":{"local:front":{"enabled":true},"local:open":{"enabled":true},"local:off":{"enabled":false}}}`

// policyEnv writes a headless config with policyPersonal and
// policySelections; runtime.approvalDialog is on.
func policyEnv(t *testing.T) config.Paths {
	t.Helper()
	_, root := headlessEnv(t)
	paths, err := runtimePaths()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"schemaVersion": 1, "runtime": map[string]any{"mode": "headless", "stateRoot": root, "approvalDialog": true}})
	if err != nil {
		t.Fatal(err)
	}
	for file, body := range map[string][]byte{paths.ConfigFile: raw, paths.PersonalFile: []byte(policyPersonal), paths.SelectionsFile: []byte(policySelections)} {
		if err = os.WriteFile(file, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return paths
}

func TestCheckCallOffline(t *testing.T) {
	paths := policyEnv(t)
	for _, tc := range []struct{ target, id, code string }{
		{"front.read_conversation", "local:front", ""},
		{"local:front.read_conversation", "local:front", ""},
		{"open.anything", "local:open", ""},
		{"front.create_draft", "", "tool_denied"},
		{"off.anything", "", "connection_disabled"},
		{"missing.anything", "", "connection_unavailable"},
		{"front", "", "invalid_arguments"},
	} {
		id, tool, err := checkCallOffline(context.Background(), paths, tc.target)
		var e *output.Error
		switch {
		case tc.code == "" && (err != nil || id != tc.id || tool == ""):
			t.Errorf("%s: %q %q %v", tc.target, id, tool, err)
		case tc.code != "" && (!errors.As(err, &e) || e.Code != tc.code || id != ""):
			t.Errorf("%s: %q %v, want %s", tc.target, id, err, tc.code)
		}
	}
}

// deny wins over allow, and the CLI answers before any runtime exists.
func TestDenyWinsOverAllow(t *testing.T) {
	paths := policyEnv(t)
	code, stdout, stderr := run(t, "call", "front.send_message", "--json")
	if e := envelopeError(t, stdout); code != 4 || e.Code != "tool_denied" || stderr != "" {
		t.Fatal(code, stdout, stderr)
	}
	noRuntime(t, paths)
	code, stdout, _ = run(t, "call", "off.anything", "--json")
	if e := envelopeError(t, stdout); code != 4 || e.Code != "connection_disabled" {
		t.Fatal(code, stdout)
	}
	noRuntime(t, paths)
}

// Headless implies --no-input (D10): no terminal prompt, no approval
// dialog, even with runtime.approvalDialog set.
func TestHeadlessApprovalDialogIgnored(t *testing.T) {
	paths := policyEnv(t)
	shown := false
	saved := newDialogFactory
	t.Cleanup(func() { newDialogFactory = saved })
	newDialogFactory = func(config.Paths) func(context.Context, []string) (string, error) {
		shown = true
		return nil
	}
	s := &Streams{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}
	if kind, ask := promptFor(context.Background(), s, &CommandOptions{}, paths, "front"); kind != "" || ask != nil || shown {
		t.Fatal(kind, ask != nil, shown)
	}
	client, err := newRuntimeClient(&CommandOptions{})
	if err != nil || !client.NoInput {
		t.Fatal("headless client allows input", err)
	}
	// The desktop path still shows the dialog when it is enabled.
	desktop := paths
	desktop.StateRoot = ""
	if kind, _ := promptFor(context.Background(), s, &CommandOptions{}, desktop, "front"); kind != "dialog" || !shown {
		t.Fatal("desktop dialog", kind, shown)
	}
}
