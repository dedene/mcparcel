package mcpclient_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/mcpclient"
)

// rmcpLike answers like an rmcp stdio server: a first request other than
// initialize is logged on stderr, then rejected (rmcp), ignored (rmcp_silent)
// or ends the process (rmcp_exit).
func rmcpLike(mode string) int {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<20)
	out := json.NewEncoder(os.Stdout)
	first := true
	for in.Scan() {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"params"`
		}
		if json.Unmarshal(in.Bytes(), &msg) != nil {
			return 1
		}
		if first && msg.Method != "initialize" {
			first = false
			fmt.Fprintf(os.Stderr, "expect initialized request, but received: %s\n", in.Bytes())
			switch mode {
			case "rmcp_exit":
				return 1
			case "rmcp":
				_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": -32600, "message": "expect initialized request"}})
			}
			continue
		}
		first = false
		switch msg.Method {
		case "initialize":
			if path := os.Getenv("MCP_TEST_INIT"); path != "" {
				_ = os.WriteFile(path, in.Bytes(), 0o600)
			}
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"protocolVersion": msg.Params.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "rmcp", "version": "0"}}})
		case "tools/list":
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"tools": []any{map[string]any{"name": "js", "inputSchema": map[string]any{"type": "object"}}}}})
		case "tools/call":
			rawCall(msg.ID)
		}
	}
	return 0
}

// rawCall answers a tools/call with hand-written bytes: the exact contents of
// the file MCP_TEST_CALL_RAW as the result, MCP_TEST_CALL_ERROR as the error,
// or a text result padding the line to MCP_TEST_CALL_LINE_BYTES bytes.
func rawCall(id json.RawMessage) {
	head := `{"jsonrpc":"2.0","id":` + string(id)
	var line []byte
	switch {
	case os.Getenv("MCP_TEST_CALL_RAW") != "":
		b, err := os.ReadFile(os.Getenv("MCP_TEST_CALL_RAW"))
		if err != nil {
			os.Exit(3)
		}
		line = append(append([]byte(head+`,"result":`), bytes.TrimRight(b, "\n")...), '}')
	case os.Getenv("MCP_TEST_CALL_ERROR") != "":
		line = []byte(head + `,"error":` + os.Getenv("MCP_TEST_CALL_ERROR") + `}`)
	default:
		n, _ := strconv.Atoi(os.Getenv("MCP_TEST_CALL_LINE_BYTES"))
		prefix, suffix := head+`,"result":{"content":[{"type":"text","text":"`, `"}]}}`
		line = []byte(prefix + strings.Repeat("x", max(0, n-len(prefix)-len(suffix))) + suffix)
	}
	_, _ = os.Stdout.Write(append(line, '\n'))
}

func TestHandshakeFallsBackFromRejectedDiscover(t *testing.T) {
	opts := stdioOpts(t, map[string]string{"MCP_TEST_MODE": "rmcp"})
	opts.Env["MCP_TEST_INIT"] = opts.Home + "/init"
	s, e := mcpclient.Connect(ctx(t), opts)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close(ctx(t))
	items, e := s.Tools(ctx(t))
	if e != nil || strings.Join(listNames(t, items), ",") != "js" {
		t.Fatal(items, e)
	}
	init, e := os.ReadFile(opts.Home + "/init")
	if e != nil || !strings.Contains(string(init), `"elicitation":{"form":{}}`) {
		t.Fatalf("initialize did not advertise form elicitation: %s %v", init, e)
	}
}

// A stdio server may ignore a first request other than initialize, or exit on
// it (the real rmcp-based cua_repl does). Stdio connections therefore start
// with the classic initialize and never send server/discover.
func TestStdioHandshakeIsClassicInitialize(t *testing.T) {
	for _, mode := range []string{"rmcp_silent", "rmcp_exit"} {
		t.Run(mode, func(t *testing.T) {
			s, e := mcpclient.Connect(ctx(t), stdioOpts(t, map[string]string{"MCP_TEST_MODE": mode}))
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close(ctx(t))
			if items, e := s.Tools(ctx(t)); e != nil || strings.Join(listNames(t, items), ",") != "js" {
				t.Fatal(items, e)
			}
		})
	}
}
