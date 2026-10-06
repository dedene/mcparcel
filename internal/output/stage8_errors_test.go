package output

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStage8ErrorCodes(t *testing.T) {
	for _, row := range []struct {
		code            string
		exit            int
		message, action string
	}{
		{"server_error", 6, "The MCP server returned an error for this call.", "Check the arguments with mcparcel tools <mcp>. If the tool may have changed remote state, inspect it before calling again."},
		{"result_too_large", 6, "The tool's result exceeds MCParcel's size limit and was not received.", "The tool may have run. Inspect remote state, then ask for a smaller result (filters, pagination)."},
		{"schema_unchecked", 1, "MCParcel could not check the arguments against this tool's schema; the server checks them.", ""},
	} {
		e := NewError(row.code, nil)
		if e.Code != row.code || e.Message != row.message || e.NextAction != row.action || ExitCode(e) != row.exit {
			t.Fatal(row.code, e, ExitCode(e))
		}
	}
}

func TestServerErrorDetails(t *testing.T) {
	e := ServerError(-32602, "bad \x1b]52;c;SGVsbG8=\x07limit "+strings.Repeat("x", 400))
	if e.Code != "server_error" || ExitCode(e) != 6 || e.Details == nil || e.Details.RPCCode == nil || *e.Details.RPCCode != -32602 {
		t.Fatal(e)
	}
	text := strings.TrimPrefix(e.Message, "The MCP server returned an error: ")
	if text == e.Message || len([]rune(text)) > 300 || strings.ContainsAny(text, "\x1b\x07") || !strings.HasPrefix(text, "bad limit") {
		t.Fatalf("%q", e.Message)
	}
	if e.NextAction != NewError("server_error", nil).NextAction {
		t.Fatal(e.NextAction)
	}
	if e = ServerError(0, ""); e.Message != "The MCP server returned an error: (no message)" || *e.Details.RPCCode != 0 {
		t.Fatal(e)
	}
	b, _ := json.Marshal(e)
	if !strings.Contains(string(b), `"rpcCode":0`) {
		t.Fatal(string(b))
	}
	copied := NewError("server_error", e.Details)
	*e.Details.RPCCode = 7
	if *copied.Details.RPCCode != 0 {
		t.Fatal("NewError shares RPCCode")
	}
	if b, _ = json.Marshal(NewError("protocol_error", &Details{RequestID: "x"})); strings.Contains(string(b), "rpcCode") {
		t.Fatal(string(b))
	}
}
