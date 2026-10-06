package mcpclient_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/mcpclient"
	"github.com/dedene/mcparcel/internal/output"
	"github.com/dedene/mcparcel/internal/testutil"
)

func sample(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "protocol", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// rawStdio starts the hand-written stdio server answering tools/call with env.
func rawStdio(t *testing.T, env map[string]string, limit int) mcpclient.Session {
	t.Helper()
	extra := map[string]string{"MCP_TEST_MODE": "rmcp_raw"}
	for k, v := range env {
		extra[k] = v
	}
	opts := stdioOpts(t, extra)
	opts.MaxMessageBytes = limit
	s, e := mcpclient.Connect(ctx(t), opts)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = s.Close(ctx(t)) })
	return s
}

func rawFile(t *testing.T, b []byte) string {
	t.Helper()
	paths, _ := testutil.IsolatedPaths(t)
	path := paths.Home + "/raw"
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// rawHTTPServer serves the fixture over HTTP but answers tools/call with
// answer; a JSON-RPC response POST from the client gets 202.
func rawHTTPServer(t *testing.T, limit int, answer func(id json.RawMessage) (int, string, []byte)) mcpclient.Session {
	t.Helper()
	server := testutil.NewFixtureServer()
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, id := rpcMethod(r)
		if method == "" && len(id) > 0 && string(id) != "null" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if method == "tools/call" {
			status, contentType, body := answer(id)
			w.Header().Set("Content-Type", contentType)
			w.WriteHeader(status)
			_, _ = w.Write(body)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(hs.Close)
	s, err := mcpclient.Connect(ctx(t), mcpclient.ConnectOptions{Connection: config.Connection{Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal(hs.URL)}}}, MaxMessageBytes: limit})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(ctx(t)) })
	return s
}

func jsonAnswer(result []byte) func(json.RawMessage) (int, string, []byte) {
	return func(id json.RawMessage) (int, string, []byte) {
		return 200, "application/json", []byte(`{"jsonrpc":"2.0","id":` + string(id) + `,"result":` + string(result) + `}`)
	}
}

func TestRawResultPreservedStdio(t *testing.T) {
	for _, name := range []string{"unknown_block.json", "bigint_meta.json", "bad_base64.json"} {
		t.Run(name, func(t *testing.T) {
			want := sample(t, name)
			s := rawStdio(t, map[string]string{"MCP_TEST_CALL_RAW": rawFile(t, want)}, 0)
			r := call(t, s, "js", nil)
			if !bytes.Equal(r.JSON, want) || r.IsError || r.Retire {
				t.Fatalf("got %s", r.JSON)
			}
		})
	}
}

func TestRawResultPreservedHTTPJSON(t *testing.T) {
	for _, name := range []string{"unknown_block.json", "bigint_meta.json", "bad_base64.json"} {
		t.Run(name, func(t *testing.T) {
			want := sample(t, name)
			r := call(t, rawHTTPServer(t, 0, jsonAnswer(want)), "echo", nil)
			if !bytes.Equal(r.JSON, want) {
				t.Fatalf("got %s", r.JSON)
			}
		})
	}
}

func TestRawResultPreservedHTTPSSE(t *testing.T) {
	want := sample(t, "unknown_block.json")
	// Split the result over several data lines; JSON allows the newlines.
	split := strings.ReplaceAll(string(want), `,"extraField"`, "\ndata: ,\"extraField\"")
	// The SDK's own SSE reader ends lines at LF only; tap_internal_test pins that.
	for _, eol := range []string{"\n", "\r\n"} {
		t.Run(strconv.Quote(eol), func(t *testing.T) {
			s := rawHTTPServer(t, 0, func(id json.RawMessage) (int, string, []byte) {
				events := []string{
					`event: message` + "\n" + `data: {"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info","data":"x"}}`,
					`event: other` + "\n" + `data: {"jsonrpc":"2.0","id":` + string(id) + `,"result":{"content":[]}}`,
					`data: {"jsonrpc":"2.0","id":99,"method":"elicitation/create","params":{"message":"Allow?","requestedSchema":{"type":"object","properties":{}}}}`,
					`data: {"jsonrpc":"2.0","id":` + string(id) + `,"result":` + split + `}`,
				}
				body := strings.ReplaceAll(strings.Join(events, "\n\n")+"\n\n", "\n", eol)
				return 200, "text/event-stream", []byte(body)
			})
			r := call(t, s, "echo", nil)
			if string(r.JSON) != strings.ReplaceAll(split, "\ndata: ", "\n") {
				t.Fatalf("got %q", r.JSON)
			}
			var v map[string]any
			if json.Unmarshal(r.JSON, &v) != nil || v["extraField"] == nil {
				t.Fatalf("got %s", r.JSON)
			}
		})
	}
}

func TestRawIsErrorAndInputRequired(t *testing.T) {
	s := rawStdio(t, map[string]string{"MCP_TEST_CALL_RAW": rawFile(t, []byte(`{"content":[{"type":"text","text":"no"}],"isError":true,"future":1}`))}, 0)
	if r := call(t, s, "js", nil); !r.IsError || !strings.Contains(string(r.JSON), `"future":1`) {
		t.Fatal(string(r.JSON))
	}
	want := sample(t, "input_required.json")
	s = rawStdio(t, map[string]string{"MCP_TEST_CALL_RAW": rawFile(t, want)}, 0)
	r, e := s.Call(ctx(t), "js", nil, nil, nil)
	code(t, e, "input_required")
	if !r.NeedsInput || !bytes.Equal(r.JSON, want) {
		t.Fatal(string(r.JSON))
	}
}

func TestRawResultStrictJSON(t *testing.T) {
	for _, name := range []string{"deep.json", "dupkey.json", "badutf8.bin", "iserror_string.json"} {
		for _, transport := range []string{"stdio", "http"} {
			t.Run(name+"/"+transport, func(t *testing.T) {
				b := sample(t, name)
				var s mcpclient.Session
				if transport == "stdio" {
					s = rawStdio(t, map[string]string{"MCP_TEST_CALL_RAW": rawFile(t, b)}, 0)
				} else {
					s = rawHTTPServer(t, 0, jsonAnswer(b))
				}
				r, e := s.Call(ctx(t), "js", nil, nil, nil)
				code(t, e, "protocol_error")
				if !r.Dispatched || len(r.JSON) != 0 || !strings.Contains(e.Error(), "strict JSON") {
					t.Fatal(string(r.JSON), e)
				}
				if _, e = s.Tools(ctx(t)); e != nil { // the session survives
					t.Fatal(e)
				}
			})
		}
	}
}

func TestRawResultDepthBoundary(t *testing.T) {
	nest := func(levels int) []byte {
		// The result object is one level; x adds levels-1 nested arrays.
		return []byte(`{"content":[],"x":` + strings.Repeat("[", levels-1) + strings.Repeat("]", levels-1) + `}`)
	}
	s := rawHTTPServer(t, 0, jsonAnswer(nest(125)))
	if r := call(t, s, "echo", nil); !bytes.Equal(r.JSON, nest(125)) {
		t.Fatal(string(r.JSON))
	}
	s = rawHTTPServer(t, 0, jsonAnswer(nest(126)))
	_, e := s.Call(ctx(t), "echo", nil, nil, nil)
	code(t, e, "protocol_error")
}

func rpcCode(t *testing.T, err error) int {
	t.Helper()
	var e *output.Error
	if !errors.As(err, &e) || e.Details == nil || e.Details.RPCCode == nil {
		t.Fatalf("no rpcCode: %v", err)
	}
	return *e.Details.RPCCode
}

func TestServerRPCErrorIsServerError(t *testing.T) {
	rpcErr := `{"code":-32602,"message":"fixture rejected \u001b[31mlimit","data":{"secret":"sk-DATA"}}`
	check := func(t *testing.T, r mcpclient.Result, e error) {
		t.Helper()
		code(t, e, "server_error")
		if rpcCode(t, e) != -32602 || e.Error() != "The MCP server returned an error: fixture rejected limit" || strings.Contains(e.Error(), "sk-DATA") || r.Retire || !r.Dispatched {
			t.Fatal(e, r.Retire)
		}
	}
	t.Run("stdio", func(t *testing.T) {
		s := rawStdio(t, map[string]string{"MCP_TEST_CALL_ERROR": rpcErr}, 0)
		r, e := s.Call(ctx(t), "js", nil, nil, nil)
		check(t, r, e)
	})
	t.Run("http", func(t *testing.T) {
		s := rawHTTPServer(t, 0, func(id json.RawMessage) (int, string, []byte) {
			return 200, "application/json", []byte(`{"jsonrpc":"2.0","id":` + string(id) + `,"error":` + rpcErr + `}`)
		})
		r, e := s.Call(ctx(t), "echo", nil, nil, nil)
		check(t, r, e)
		if _, e = s.Tools(ctx(t)); e != nil { // the session survives
			t.Fatal(e)
		}
	})
	t.Run("fixture", func(t *testing.T) {
		server := testutil.NewFixtureServer()
		r, e := httpSession(t, server, nil).Call(ctx(t), "rpcfail", nil, nil, nil)
		code(t, e, "server_error")
		if rpcCode(t, e) != -32602 || !strings.Contains(e.Error(), "fixture rejected limit") || r.Retire {
			t.Fatal(e)
		}
	})
}

func TestServerErrorNon2xxRetires(t *testing.T) {
	// The SDK reads no body for 500, 502-504 and 429; those stay outcome_unknown.
	for _, status := range []int{400, 404, 409} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			s := rawHTTPServer(t, 0, func(id json.RawMessage) (int, string, []byte) {
				return status, "application/json", []byte(`{"jsonrpc":"2.0","id":` + string(id) + `,"error":{"code":0,"message":"session missing"}}`)
			})
			r, e := s.Call(ctx(t), "echo", nil, nil, nil)
			code(t, e, "server_error")
			if rpcCode(t, e) != 0 || !r.Retire {
				t.Fatal(e, r.Retire)
			}
		})
	}
}

func TestTapIgnoresUnauthorizedBody(t *testing.T) {
	as := testutil.NewAuthServer(t, testutil.AuthServerOptions{ClientID: "pre-id"})
	u := oauthServer(t, as, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if method, id := rpcMethod(r); method == "tools/call" {
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="http://`+r.Host+`/.well-known/oauth-protected-resource/mcp"`)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(401)
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32001,"message":"unauthorized"}}`, id)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	s, err := connectHTTP(t, u, sessionHandler(t, as, u))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Call(ctx(t), "counter", nil, nil, nil)
	var e *output.Error
	if !errors.As(err, &e) || e.Code == "server_error" || !strings.HasPrefix(e.Code, "auth_") {
		t.Fatal(err)
	}
}

func TestResultTooLargeStdio(t *testing.T) {
	const limit = 1 << 20
	s := rawStdio(t, map[string]string{"MCP_TEST_CALL_LINE_BYTES": strconv.Itoa(limit)}, limit)
	if r := call(t, s, "js", nil); len(r.JSON) == 0 {
		t.Fatal("line at the limit rejected")
	}
	s = rawStdio(t, map[string]string{"MCP_TEST_CALL_LINE_BYTES": strconv.Itoa(limit + 1)}, limit)
	r, e := s.Call(ctx(t), "js", nil, nil, nil)
	code(t, e, "result_too_large")
	if !r.Retire || !r.Dispatched || len(r.JSON) != 0 {
		t.Fatal(r)
	}
}

func TestResultTooLargeHTTP(t *testing.T) {
	const limit = 1 << 20
	for _, contentType := range []string{"application/json", "text/event-stream"} {
		t.Run(contentType, func(t *testing.T) {
			s := rawHTTPServer(t, limit, func(id json.RawMessage) (int, string, []byte) {
				body := `{"jsonrpc":"2.0","id":` + string(id) + `,"result":{"content":[{"type":"text","text":"` + strings.Repeat("x", limit) + `"}]}}`
				if contentType == "text/event-stream" {
					body = "data: " + body + "\n\n"
				}
				return 200, contentType, []byte(body)
			})
			r, e := s.Call(ctx(t), "echo", nil, nil, nil)
			code(t, e, "result_too_large")
			if !r.Retire || !r.Dispatched {
				t.Fatal(r)
			}
		})
	}
}

func TestElicitationDeclineBeatsServerError(t *testing.T) {
	s := stdioSession(t, map[string]string{"MCP_TEST_LEGACY": "1"})
	r, e := s.Call(ctx(t), "elicit", map[string]any{"message": "Allow?", "then": "rpc"}, nil, nil)
	code(t, e, "elicitation_declined")
	if r.Retire || r.Declined != "Allow?" {
		t.Fatal(r)
	}
}
