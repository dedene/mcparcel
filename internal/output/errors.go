package output

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/elicit"
)

type Details struct {
	SyncReport   json.RawMessage `json:"syncReport,omitempty"`
	ImportReport json.RawMessage `json:"importReport,omitempty"`
	Candidates   []string        `json:"candidates,omitempty"`
	RequestID    string          `json:"requestId,omitempty"`
	Dispatched   bool            `json:"dispatched,omitempty"`
	Outcome      string          `json:"outcome,omitempty"`
	// RPCCode is a server_error's JSON-RPC error code; 0 is a legal code.
	RPCCode *int `json:"rpcCode,omitempty"`
}
type Error struct {
	Code       string   `json:"code"`
	Message    string   `json:"message"`
	NextAction string   `json:"nextAction,omitempty"`
	Details    *Details `json:"details,omitempty"`
}

type errorSpec struct {
	exit            int
	message, action string
}

var registry = map[string]errorSpec{
	"catalog_offline":           {6, "Could not fetch the catalog.", "Use the saved snapshot or retry sync when online."},
	"catalog_auth_required":     {3, "GitHub catalog access is not authorized.", "Check gh authentication and repository access, then retry."},
	"catalog_gh_required":       {3, "Private catalog access requires authenticated gh.", "Install gh and run gh auth login, then retry."},
	"catalog_rate_limited":      {6, "GitHub catalog requests are rate limited.", "Keep using the saved snapshot and retry later."},
	"catalog_unavailable":       {4, "The catalog repository is unavailable.", "Check its name and access; the saved snapshot remains active."},
	"catalog_renamed":           {4, "The catalog repository name changed.", "Review the move, remove the old registration and add the verified new name."},
	"catalog_identity_changed":  {4, "The repository name now identifies a different repository.", "Review it before explicitly removing and re-adding the source."},
	"catalog_source_conflict":   {2, "This repository already has a different catalog registration.", "Remove the existing registration before choosing another path or ref."},
	"invalid_catalog":           {2, "The fetched catalog is invalid or is not a regular file.", "Fix the catalog path or content in the repository, then retry."},
	"invalid_repository":        {2, "Invalid repository argument.", "Use a repository name in owner/repo form."},
	"catalog_too_large":         {2, "The catalog response exceeds its size limit.", "Keep catalog files at or below 2 MiB and API responses within the supported bounds."},
	"catalog_bindings_conflict": {2, "The catalog update conflicts with saved local bindings.", "Review the candidate and reconcile selections.json bindings before retrying sync."},

	"import_blocked":      {2, "Selected import entries require changes.", "Review the import report, provide bindings, or choose an applicable --only subset."},
	"alias_collision":     {2, "The alias already names another connection.", "Use the existing alias or choose a different personal ID."},
	"config_write_failed": {1, "Configuration may have changed, but its durable save could not be confirmed.", "Reload the configuration before retrying."},
	"connection_disabled": {4, "The connection is disabled.", "Enable the connection before calling it."},
	"review_required":     {4, "The connection requires review.", "Review its definition and accept it by ID."},
	"tool_denied":         {4, "The tool is denied by connection policy.", "Check the source policy and personal tool selection."},
	"ambiguous_id":        {2, "The connection name is ambiguous.", "Use a canonical connection ID."},
	"config_conflict":     {7, "The configuration changed since it was loaded.", "Reload the configuration and reapply your changes."},
	"runtime_unsupported": {2, "This connection requires a runtime feature that is not implemented yet.", "Use a supported connection or wait for its runtime stage."},

	"internal_error":           {1, "An internal error occurred.", "Report this error with the mcparcel version."},
	"invalid_arguments":        {2, "Invalid call arguments.", "Check the tool schema and argument syntax."},
	"invalid_config":           {2, "Invalid personal configuration.", "Check personal.json and config.json."},
	"config_changed":           {2, "The connection configuration changed before dispatch.", "Review the current definition and invoke the command again."},
	"config_required":          {2, "Configuration is required.", "Create personal.json and bind the required local profile."},
	"unsafe_local_path":        {2, "A local runtime or configuration path is unsafe.", "Make the mcparcel runtime directory mode 700 and owned by you; config files must be owned by you and not writable by others."},
	"auth_required":            {3, "Credential authorization is required.", "Run the command interactively with 1Password desktop integration enabled."},
	"auth_expired":             {3, "The credential session expired.", "Run the command interactively to authorize again."},
	"auth_account_conflict":    {3, "This daemon already uses another 1Password account; run mcparcel runtime restart.", ""},
	"auth_failed":              {3, "Credential resolution failed.", "Check the profile, desktop integration and vault access."},
	"connection_unavailable":   {4, "The connection is not defined.", "Check the connection ID in personal.json."},
	"tool_error":               {5, "The MCP tool returned an error result.", ""},
	"connection_failed":        {6, "Could not connect to the MCP server.", "Check the connection configuration and prerequisites."},
	"timeout":                  {6, "The operation timed out before tool dispatch.", "Check the server and timeout."},
	"outcome_unknown":          {6, "The tool may have executed; its outcome is unknown.", "Inspect remote state before deciding whether to call again."},
	"runtime_config_mismatch":  {6, "The running daemon uses a different configuration directory.", "Run mcparcel runtime restart."},
	"runtime_version_mismatch": {6, "The CLI and daemon versions differ.", "Run mcparcel runtime restart."},
	"runtime_busy":             {6, "The daemon has active work.", "Wait for completion or use mcparcel runtime restart --force."},
	"runtime_start_failed":     {6, "The daemon did not become ready.", "Check runtime status and the daemon log."},
	"schema_cache_miss":        {6, "No cached tool schema is available.", "Run mcparcel tools without --cached."},
	"invalid_schema":           {6, "The server returned an invalid tool schema.", "Check the MCP server implementation."},
	"tool_not_found":           {2, "The tool is not advertised by this connection.", "Run mcparcel tools for this connection."},
	"protocol_error":           {6, "The runtime or MCP response is invalid.", "Check the daemon log and server compatibility."},
	"elicitation_declined":     {3, "The server asked for approval and MCParcel declined it.", "Approve this in the server's own app, then retry."},
	"server_error":             {6, "The MCP server returned an error for this call.", "Check the arguments with mcparcel tools <mcp>. If the tool may have changed remote state, inspect it before calling again."},
	"result_too_large":         {6, "The tool's result exceeds MCParcel's size limit and was not received.", "The tool may have run. Inspect remote state, then ask for a smaller result (filters, pagination)."},
	"schema_unchecked":         {1, "MCParcel could not check the arguments against this tool's schema; the server checks them.", ""},
	"export_failed":            {1, "The call finished, but MCParcel could not save its image or audio blocks.", "The full result is in data.result; fix the output directory. Do not call the tool again just to export."},
	"input_required":           {6, "The MCP server requires an unsupported interactive response.", "Use a client that supports this server interaction."},
	"canceled":                 {130, "The operation was canceled.", ""},

	"keychain_unavailable":      {3, "The macOS Keychain could not store or read the sign-in.", "Unlock the login keychain, then try again."},
	"auth_callback_unavailable": {3, "The sign-in callback address is in use.", "Close the program using that port, then try again."},
}

func (e *Error) Error() string { return e.Message }

func CommandUsageError() *Error {
	err := NewError("invalid_arguments", nil)
	err.Message = "Invalid command arguments."
	err.NextAction = "Run 'mcparcel --help' for command usage."
	return err
}

func SourceNotRegisteredError() *Error {
	err := NewError("catalog_unavailable", nil)
	err.Message = "This catalog is not registered."
	err.NextAction = "Run 'mcparcel add <owner/repo>' to register a catalog."
	return err
}

// ElicitationDeclined reports an elicitation that was not accepted, with the
// server's sanitized message, worded by reason: unavailable (no prompt was
// possible), unsupported, declined (by the user) or canceled.
func ElicitationDeclined(reason, text string) *Error {
	err := NewError("elicitation_declined", nil)
	switch reason {
	case "unsupported":
		err.Message = "The server asked for input MCParcel cannot show, so MCParcel declined it: " + text
	case "declined":
		err.Message = "You declined the server's request: " + text
		err.NextAction = "Retry the call if you meant to allow it."
	case "canceled":
		err.Message = "The server's request was canceled without an answer: " + text
		err.NextAction = "Retry the call and answer the prompt."
	default:
		err.Message = "The server asked for approval and MCParcel declined it because no prompt was possible: " + text
		err.NextAction = "Run the call in a terminal without --no-input or --json, or set runtime.approvalDialog in config.json, or approve it in the server's own app, then retry."
	}
	return err
}

// ServerError reports a JSON-RPC error answer to a call: the server's
// sanitized message and its code, never the error's data.
func ServerError(rpcCode int, text string) *Error {
	err := NewError("server_error", &Details{RPCCode: &rpcCode})
	text = elicit.Clean(text, 300)
	if text == "" {
		text = "(no message)"
	}
	err.Message = "The MCP server returned an error: " + text
	return err
}

func NewError(code string, details *Details) *Error {
	spec, ok := registry[code]
	if !ok {
		code = "internal_error"
		spec = registry[code]
	}
	var clone *Details
	if details != nil {
		value := *details
		value.SyncReport = append(json.RawMessage(nil), details.SyncReport...)
		value.ImportReport = append(json.RawMessage(nil), details.ImportReport...)
		value.Candidates = append([]string(nil), details.Candidates...)
		if details.RPCCode != nil {
			rpcCode := *details.RPCCode
			value.RPCCode = &rpcCode
		}
		clone = &value
	}
	return &Error{Code: code, Message: spec.message, NextAction: spec.action, Details: clone}
}

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var boundary *Error
	if errors.As(err, &boundary) {
		if boundary == nil {
			return 1
		}
		if spec, ok := registry[boundary.Code]; ok {
			return spec.exit
		}
		switch boundary.Code {
		case "config_conflict":
			return 7
		case "review_required", "tool_denied":
			return 4
		}
		return 1
	}
	if errors.Is(err, config.ErrConfigConflict) {
		return 7
	}
	if errors.Is(err, config.ErrRevisionExhausted) {
		return 2
	}
	if errors.Is(err, context.Canceled) {
		return 130
	}
	return 1
}
