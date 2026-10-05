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
		{[]string{"enable", "--all", "CANARY"}, 2, "invalid_arguments", "Invalid command arguments.", "Run 'mcparcel --help' for command usage."},
		{[]string{"sync", "--accept", "CANARY"}, 2, "invalid_arguments", "Invalid command arguments.", "Run 'mcparcel --help' for command usage."},
		{[]string{"add", "CANARY/bad/repo"}, 2, "invalid_repository", "Invalid repository argument.", "Use a repository name in owner/repo form."},
		{[]string{"remove", "CANARY/bad/repo"}, 2, "invalid_repository", "Invalid repository argument.", "Use a repository name in owner/repo form."},
		{[]string{"sync", "CANARY/bad/repo"}, 2, "invalid_repository", "Invalid repository argument.", "Use a repository name in owner/repo form."},
		{[]string{"remove", "fixture/missing"}, 4, "catalog_unavailable", "This catalog is not registered.", "Run 'mcparcel add <owner/repo>' to register a catalog."},
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

func TestCallUsageDiagnosticUnchanged(t *testing.T) {
	_, stdout, _ := run(t, "call", "--json")
	if !strings.Contains(stdout, `"message":"Invalid call arguments."`) || !strings.Contains(stdout, `"nextAction":"Check the tool schema and argument syntax."`) {
		t.Fatal(stdout)
	}
}
