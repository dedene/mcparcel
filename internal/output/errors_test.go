package output

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

func TestErrorExitRegistry(t *testing.T) {
	rows := []struct {
		code            string
		exit            int
		message, action string
	}{
		{"connection_disabled", 4, "The connection is disabled.", "Enable the connection before calling it."},
		{"review_required", 4, "The connection requires review.", "Review its definition and accept it by ID."},
		{"tool_denied", 4, "The tool is denied by connection policy.", "Check the source policy and personal tool selection."},
		{"ambiguous_id", 2, "The connection name is ambiguous.", "Use a canonical connection ID."},
		{"config_write_failed", 1, "Configuration may have changed, but its durable save could not be confirmed.", "Reload the configuration before retrying."},
		{"config_conflict", 7, "The configuration changed since it was loaded.", "Reload the configuration and reapply your changes."},
		{"runtime_unsupported", 2, "This connection requires a runtime feature that is not implemented yet.", "Use a supported connection or wait for its runtime stage."},
		{"internal_error", 1, "An internal error occurred.", "Report this error with the mcparcel version."},
		{"invalid_arguments", 2, "Invalid call arguments.", "Check the tool schema and argument syntax."},
		{"invalid_config", 2, "Invalid personal configuration.", "Check personal.json and config.json."},
		{"config_changed", 2, "The connection configuration changed before dispatch.", "Review the current definition and invoke the command again."},
		{"config_required", 2, "Configuration is required.", "Create personal.json and bind the required local profile."},
		{"unsafe_local_path", 2, "A local runtime or configuration path is unsafe.", "Make the mcparcel runtime directory mode 700 and owned by you; config files must be owned by you and not writable by others."},
		{"auth_required", 3, "Credential authorization is required.", "Run the command interactively with 1Password desktop integration enabled."},
		{"auth_expired", 3, "The credential session expired.", "Run the command interactively to authorize again."},
		{"auth_failed", 3, "Credential resolution failed.", "Check the profile, desktop integration and vault access."},
		{"keychain_unavailable", 3, "The macOS Keychain could not store or read the sign-in.", "Unlock the login keychain, then try again."},
		{"auth_callback_unavailable", 3, "The sign-in callback address is in use.", "Close the program using that port, then try again."},
		{"connection_unavailable", 4, "The connection is not defined.", "Check the connection ID in personal.json."},
		{"tool_error", 5, "The MCP tool returned an error result.", ""},
		{"connection_failed", 6, "Could not connect to the MCP server.", "Check the connection configuration and prerequisites."},
		{"timeout", 6, "The operation timed out before tool dispatch.", "Check the server and timeout."},
		{"outcome_unknown", 6, "The tool may have executed; its outcome is unknown.", "Inspect remote state before deciding whether to call again."},
		{"runtime_version_mismatch", 6, "The CLI and daemon versions differ.", "Run mcparcel runtime restart."},
		{"runtime_busy", 6, "The daemon has active work.", "Wait for completion or use mcparcel runtime restart --force."},
		{"runtime_start_failed", 6, "The daemon did not become ready.", "Check runtime status and the daemon log."},
		{"schema_cache_miss", 6, "No cached tool schema is available.", "Run mcparcel tools without --cached."},
		{"invalid_schema", 6, "The server returned an invalid tool schema.", "Check the MCP server implementation."},
		{"tool_not_found", 2, "The tool is not advertised by this connection.", "Run mcparcel tools for this connection."},
		{"protocol_error", 6, "The runtime or MCP response is invalid.", "Check the daemon log and server compatibility."},
		{"input_required", 6, "The MCP server requires an unsupported interactive response.", "Use a client that supports this server interaction."},
		{"canceled", 130, "The operation was canceled.", ""},
		{"terminal_required", 2, "Setup needs an interactive terminal; it does not run with --json, --no-input or without a terminal.", "Use mcparcel catalog, enable, disable, tools enable/disable, config input set, config profile bind and local add/update instead."},
	}
	for _, row := range rows {
		t.Run(row.code, func(t *testing.T) {
			e := NewError(row.code, nil)
			if e.Code != row.code || e.Message != row.message || e.NextAction != row.action || e.Error() != row.message || ExitCode(e) != row.exit || ExitCode(fmt.Errorf("wrapped: %w", e)) != row.exit {
				t.Fatal(e, ExitCode(e))
			}
		})
	}
	for code, exit := range map[string]int{"config_conflict": 7, "review_required": 4, "tool_denied": 4} {
		if ExitCode(&Error{Code: code}) != exit {
			t.Fatal(code)
		}
	}
	if ExitCode(nil) != 0 || ExitCode(context.Canceled) != 130 || ExitCode(fmt.Errorf("wrap: %w", context.Canceled)) != 130 {
		t.Fatal("nil/canceled mapping")
	}
}

func TestUnknownErrorIsSafe(t *testing.T) {
	raw := errors.New("bootstrap-token-fake https://user:password@fixture.invalid Authorization: Bearer header-fake")
	if ExitCode(raw) != 1 {
		t.Fatal("unknown exit")
	}
	e := NewError(raw.Error(), nil)
	if e.Code != "internal_error" || strings.Contains(e.Error(), "bootstrap-token-fake") {
		t.Fatal(e)
	}
	var b bytes.Buffer
	if err := WriteHuman(&b, raw); err != nil {
		t.Fatal(err)
	}
	if b.String() != "An internal error occurred.\nReport this error with the mcparcel version.\n" {
		t.Fatal(b.String())
	}
	b.Reset()
	if err := WriteJSON(&b, "not retained", e); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "bootstrap-token-fake") || strings.Contains(b.String(), "not retained") {
		t.Fatal(b.String())
	}
}

func TestCandidatesCopied(t *testing.T) {
	d := &Details{Candidates: []string{"local:paper"}}
	e := NewError("ambiguous_id", d)
	d.Candidates[0] = "changed"
	if e.Details.Candidates[0] != "local:paper" {
		t.Fatal(e)
	}
}

func TestStoreErrorExitCodes(t *testing.T) {
	for _, row := range []struct {
		err  error
		exit int
	}{{config.ErrConfigConflict, 7}, {config.ErrRevisionExhausted, 2}, {config.ErrDurability, 1}, {errors.Join(config.ErrConfigWrite, config.ErrDurability), 1}} {
		if ExitCode(row.err) != row.exit || ExitCode(fmt.Errorf("wrapped: %w", row.err)) != row.exit {
			t.Fatal(row)
		}
	}
	var b bytes.Buffer
	failure := NewError("config_write_failed", nil)
	if err := WriteJSON(&b, "discard", failure); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `"data":null`) || strings.Contains(b.String(), "discard") {
		t.Fatal(b.String())
	}
}

func TestImportErrorDetailsCopy(t *testing.T) {
	d := &Details{ImportReport: []byte(`{"entries":[]}`), Candidates: []string{"local:paper"}}
	e := NewError("import_blocked", d)
	d.ImportReport[0] = 'x'
	d.Candidates[0] = "changed"
	if e.Code != "import_blocked" || ExitCode(e) != 2 || string(e.Details.ImportReport) != `{"entries":[]}` || e.Details.Candidates[0] != "local:paper" {
		t.Fatal(e)
	}
	a := NewError("alias_collision", nil)
	if a.Message != "The alias already names another connection." || a.NextAction != "Use the existing alias or choose a different personal ID." || ExitCode(a) != 2 {
		t.Fatal(a)
	}
}

func TestSyncReportCopy(t *testing.T) {
	details := &Details{SyncReport: json.RawMessage(`{"apply":true,"results":[]}`)}
	e := NewError("catalog_offline", details)
	details.SyncReport[0] = '['
	if string(e.Details.SyncReport) != `{"apply":true,"results":[]}` || ExitCode(e) != 6 {
		t.Fatal(e)
	}
}
