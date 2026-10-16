package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/output"
)

func TestCommandDiagnosticsDistinguishInputAndRegistration(t *testing.T) {
	metadataEnv(t)
	for _, tc := range []struct {
		args                  []string
		exit                  int
		code, message, action string
	}{
		{[]string{"enable", "--all", "CANARY"}, 2, "invalid_arguments", "Unknown flag --all.", "Run mcparcel enable --help."},
		{[]string{"sync", "--accept", "CANARY"}, 2, "invalid_arguments", "The --accept flag needs --apply.", "Run mcparcel sync --help."},
		{[]string{"add", "CANARY/bad/repo"}, 2, "invalid_repository", "Invalid repository argument.", "Use a repository name in owner/repo form."},
		{[]string{"remove", "CANARY/bad/repo"}, 2, "invalid_repository", "Invalid repository argument.", "Use a repository name in owner/repo form."},
		{[]string{"sync", "CANARY/bad/repo"}, 2, "invalid_repository", "Invalid repository argument.", "Use a repository name in owner/repo form."},
		{[]string{"remove", "fixture/missing"}, 4, "catalog_unavailable", "This catalog is not registered.", "Run mcparcel add <owner/repo> to register a catalog."},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			exit, stdout, stderr := run(t, append(tc.args, "--json")...)
			var result struct {
				Error output.Error `json:"error"`
			}
			if err := json.Unmarshal([]byte(stdout), &result); err != nil {
				t.Fatal(err, stdout)
			}
			if exit != tc.exit || stderr != "" || result.Error.Code != tc.code || result.Error.Message != tc.message || result.Error.NextAction != tc.action || strings.Contains(stdout, "CANARY") {
				t.Fatalf("exit=%d stdout=%s stderr=%q", exit, stdout, stderr)
			}
		})
	}
}

// call without <mcp>.<tool> says how to name the tool, with or without a
// target, and never echoes what was typed.
func TestCallTargetDiagnostic(t *testing.T) {
	for _, argv := range [][]string{{"call"}, {"call", "foo"}, {"call", "CANARY"}, {"call", "github:acme/tools"}, {"call", "echo."}} {
		_, stdout, _ := run(t, append(argv, "--json")...)
		if !strings.Contains(stdout, `"message":"Name the tool as \u003cmcp\u003e.\u003ctool\u003e, for example context7.resolve-library-id."`) || strings.Contains(stdout, "CANARY") {
			t.Fatal(argv, stdout)
		}
	}
}
