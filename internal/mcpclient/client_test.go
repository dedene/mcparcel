package mcpclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/mcpclient"
	"github.com/dedene/mcparcel/internal/output"
	"github.com/dedene/mcparcel/internal/testutil"
)

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

func code(t *testing.T, err error, want string) {
	t.Helper()
	var e *output.Error
	if !errors.As(err, &e) || e.Code != want {
		t.Fatalf("error = %v, want %s", err, want)
	}
}

func httpSession(t *testing.T, server *mcp.Server, wrap func(http.Handler) http.Handler) mcpclient.Session {
	t.Helper()
	h := http.Handler(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	if wrap != nil {
		h = wrap(h)
	}
	hs := httptest.NewServer(h)
	t.Cleanup(hs.Close)
	s, err := mcpclient.Connect(ctx(t), mcpclient.ConnectOptions{Connection: config.Connection{Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal(hs.URL)}}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(ctx(t)); err != nil {
			t.Error(err)
		}
	})
	return s
}

func call(t *testing.T, s mcpclient.Session, name string, args map[string]any) mcpclient.Result {
	t.Helper()
	r, e := s.Call(ctx(t), name, args, nil)
	if e != nil {
		t.Fatal(e)
	}
	if !r.Dispatched {
		t.Fatal("not dispatched")
	}
	return r
}

func structured(t *testing.T, r mcpclient.Result) map[string]any {
	t.Helper()
	var v struct {
		Structured map[string]any `json:"structuredContent"`
	}
	if e := json.Unmarshal(r.JSON, &v); e != nil {
		t.Fatal(e)
	}
	return v.Structured
}

func listNames(t *testing.T, items []json.RawMessage) []string {
	t.Helper()
	names := []string{}
	for _, raw := range items {
		var v struct{ Name string }
		if e := json.Unmarshal(raw, &v); e != nil {
			t.Fatal(e)
		}
		names = append(names, v.Name)
	}
	return names
}

func TestStdioAdapterPersistentState(t *testing.T) {
	s := stdioSession(t, nil)
	for n := 1; n <= 2; n++ {
		if structured(t, call(t, s, "counter", nil))["count"] != float64(n) {
			t.Fatal("lost state")
		}
	}
}

func TestHTTPAdapter(t *testing.T) {
	server := testutil.NewFixtureServer()
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/alive" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer hs.Close()
	s, e := mcpclient.Connect(ctx(t), mcpclient.ConnectOptions{Connection: config.Connection{Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal(hs.URL)}}}})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = s.Close(ctx(t)) })
	if structured(t, call(t, s, "echo", map[string]any{"text": "héllo"}))["text"] != "héllo" {
		t.Fatal("echo mismatch")
	}
	if e := s.Close(ctx(t)); e != nil {
		t.Fatal(e)
	}
	req, e := http.NewRequestWithContext(ctx(t), http.MethodGet, hs.URL+"/alive", nil)
	if e != nil {
		t.Fatal(e)
	}
	probe := &http.Client{Transport: &http.Transport{Proxy: nil}}
	defer probe.CloseIdleConnections()
	resp, e := probe.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatal("external listener stopped")
	}
}

func TestToolsPagination(t *testing.T) {
	var pages atomic.Int64
	server := testutil.NewFixtureServerWithOptions(testutil.FixtureOptions{PageSize: 2})
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(c context.Context, m string, r mcp.Request) (mcp.Result, error) {
			if m == "tools/list" {
				pages.Add(1)
			}
			return next(c, m, r)
		}
	})
	s := httpSession(t, server, nil)
	items, e := s.Tools(ctx(t))
	if e != nil {
		t.Fatal(e)
	}
	want := []string{"counter", "echo", "echo.dotted", "env", "fail", "rich", "typed", "wait", "write_drop"}
	if strings.Join(listNames(t, items), ",") != strings.Join(want, ",") || pages.Load() != 5 {
		t.Fatalf("names/pages %v/%d", listNames(t, items), pages.Load())
	}
}

func TestToolsIgnoresSDKTTL(t *testing.T) {
	var pages atomic.Int64
	server := testutil.NewFixtureServerWithOptions(testutil.FixtureOptions{PageSize: 2})
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(c context.Context, m string, r mcp.Request) (mcp.Result, error) {
			out, e := next(c, m, r)
			if v, ok := out.(*mcp.ListToolsResult); ok {
				v.TTLMs = 60000
				pages.Add(1)
			}
			return out, e
		}
	})
	s := httpSession(t, server, nil)
	if _, e := s.Tools(ctx(t)); e != nil {
		t.Fatal(e)
	}
	server.AddTool(&mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"new": map[string]any{"type": "string"}}}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	})
	items, e := s.Tools(ctx(t))
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, raw := range items {
		if strings.Contains(string(raw), `"new"`) {
			found = true
		}
	}
	if !found || pages.Load() != 10 {
		t.Fatalf("schema changed=%v pages=%d", found, pages.Load())
	}
}

func TestToolsCursorLoop(t *testing.T) {
	var pages atomic.Int64
	server := testutil.NewFixtureServer()
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(c context.Context, m string, r mcp.Request) (mcp.Result, error) {
			if m == "tools/list" {
				pages.Add(1)
				return &mcp.ListToolsResult{NextCursor: "x"}, nil
			}
			return next(c, m, r)
		}
	})
	items, e := httpSession(t, server, nil).Tools(ctx(t))
	code(t, e, "protocol_error")
	if len(items) != 0 || pages.Load() != 2 {
		t.Fatal("loop not bounded")
	}
}

func TestTypedFixture(t *testing.T) {
	s := httpSession(t, testutil.NewFixtureServer(), nil)
	r := call(t, s, "typed", map[string]any{"limit": int64(5), "enabled": true, "queries": []map[string]any{{"a": 1}}})
	v := structured(t, r)
	if v["limit"] != float64(5) || v["enabled"] != true || len(v["queries"].([]any)) != 1 {
		t.Fatal("typed mismatch")
	}
	r = call(t, s, "typed", map[string]any{"limit": json.Number("9007199254740993"), "enabled": true, "queries": []any{}})
	if !strings.Contains(string(r.JSON), "limit-received=9007199254740993") {
		t.Fatal("integer precision lost")
	}
}

func TestAdapterToolErrorAndRich(t *testing.T) {
	s := httpSession(t, testutil.NewFixtureServer(), nil)
	if !call(t, s, "fail", nil).IsError {
		t.Fatal("missing IsError")
	}
	r := call(t, s, "rich", nil)
	for _, field := range []string{`"_meta"`, `"structuredContent"`, `"content"`} {
		if !strings.Contains(string(r.JSON), field) {
			t.Fatal("missing", field)
		}
	}
}

func TestNoAutomaticMultiRoundTrip(t *testing.T) {
	var calls atomic.Int64
	server := testutil.NewFixtureServer()
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(c context.Context, m string, r mcp.Request) (mcp.Result, error) {
			if m == "tools/call" {
				calls.Add(1)
				var out mcp.CallToolResult
				if e := json.Unmarshal([]byte(`{"resultType":"input_required","inputRequests":{},"content":[]}`), &out); e != nil {
					return nil, e
				}
				return &out, nil
			}
			return next(c, m, r)
		}
	})
	s := httpSession(t, server, nil)
	r, e := s.Call(ctx(t), "echo", nil, nil)
	code(t, e, "input_required")
	if !r.NeedsInput || !r.Dispatched || len(r.JSON) == 0 || calls.Load() != 1 {
		t.Fatal("input required replayed or lost")
	}
}

func TestNoCallReplay(t *testing.T) {
	paths, _ := testutil.IsolatedPaths(t)
	marker := paths.Home + "/marker"
	s := stdioSession(t, map[string]string{"MCP_TEST_MARKER": marker})
	r, e := s.Call(ctx(t), "write_drop", nil, nil)
	code(t, e, "outcome_unknown")
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "1" || !r.Dispatched {
		t.Fatalf("marker/dispatched: %q %v %v", data, err, r.Dispatched)
	}
}

func TestBeforeDispatchFailure(t *testing.T) {
	var calls atomic.Int64
	server := testutil.NewFixtureServer()
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(c context.Context, m string, r mcp.Request) (mcp.Result, error) {
			if m == "tools/call" {
				calls.Add(1)
			}
			return next(c, m, r)
		}
	})
	s := httpSession(t, server, nil)
	r, e := s.Call(ctx(t), "counter", nil, func() error { return output.NewError("canceled", nil) })
	code(t, e, "canceled")
	if r.Dispatched || calls.Load() != 0 {
		t.Fatal("dispatched rejected request")
	}
}

func TestToolsSorted(t *testing.T) {
	items, e := httpSession(t, testutil.NewFixtureServer(), nil).Tools(ctx(t))
	if e != nil {
		t.Fatal(e)
	}
	if !sort.StringsAreSorted(listNames(t, items)) {
		t.Fatal("unsorted")
	}
}

func TestCallCancellationBoundaries(t *testing.T) {
	started := make(chan string, 1)
	release := make(chan struct{})
	s := httpSession(t, testutil.NewFixtureServerWithOptions(testutil.FixtureOptions{Started: started, Release: release}), nil)
	t.Cleanup(func() { close(release) })
	for _, mode := range []string{"before", "callback", "after", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			c, cancel := context.WithCancel(ctx(t))
			defer cancel()
			if mode == "before" {
				cancel()
				r, e := s.Call(c, "wait", nil, nil)
				code(t, e, "canceled")
				if r.Dispatched {
					t.Fatal("canceled before dispatch")
				}
				return
			}
			if mode == "callback" {
				r, e := s.Call(c, "wait", nil, func() error { cancel(); return nil })
				code(t, e, "canceled")
				if r.Dispatched {
					t.Fatal("canceled callback dispatched")
				}
				return
			}
			if mode == "deadline" {
				var stop context.CancelFunc
				c, stop = context.WithTimeout(c, 100*time.Millisecond)
				defer stop()
			}
			type answer struct {
				r   mcpclient.Result
				err error
			}
			out := make(chan answer, 1)
			go func() { r, e := s.Call(c, "wait", nil, nil); out <- answer{r, e} }()
			select {
			case <-started:
			case <-ctx(t).Done():
				t.Fatal("wait did not start")
			}
			if mode == "after" {
				cancel()
			}
			a := <-out
			want := "canceled"
			if mode == "deadline" {
				want = "outcome_unknown"
			}
			code(t, a.err, want)
			if !a.r.Dispatched {
				t.Fatal("dispatch lost")
			}
		})
	}
}

func TestToolsRejectsMalformedPages(t *testing.T) {
	for _, mode := range []string{"duplicate", "name", "schema"} {
		t.Run(mode, func(t *testing.T) {
			server := testutil.NewFixtureServer()
			server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
				return func(c context.Context, m string, r mcp.Request) (mcp.Result, error) {
					if m != "tools/list" {
						return next(c, m, r)
					}
					tool := &mcp.Tool{Name: "x", InputSchema: map[string]any{"type": "object"}}
					tools := []*mcp.Tool{tool}
					if mode == "duplicate" {
						tools = append(tools, tool)
					}
					if mode == "name" {
						tool.Name = ""
					}
					if mode == "schema" {
						tool.InputSchema = "bad"
					}
					return &mcp.ListToolsResult{Tools: tools}, nil
				}
			})
			items, e := httpSession(t, server, nil).Tools(ctx(t))
			want := "protocol_error"
			if mode == "schema" {
				want = "invalid_schema"
			}
			code(t, e, want)
			if len(items) != 0 {
				t.Fatal("partial page returned")
			}
		})
	}
}

func TestBeforeDispatchErrorsAreSafe(t *testing.T) {
	s := httpSession(t, testutil.NewFixtureServer(), nil)
	for _, tt := range []struct {
		err  error
		code string
	}{{context.Canceled, "canceled"}, {context.DeadlineExceeded, "timeout"}, {errors.New("SECRET-CANARY"), "internal_error"}} {
		r, e := s.Call(ctx(t), "counter", nil, func() error { return tt.err })
		code(t, e, tt.code)
		if r.Dispatched || strings.Contains(e.Error(), "SECRET-CANARY") {
			t.Fatal("unsafe callback error")
		}
	}
}
