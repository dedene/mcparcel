package mcpclient_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/mcpclient"
	"github.com/dedene/mcparcel/internal/testutil"
)

func TestHTTPRedirectNoCredentialForward(t *testing.T) {
	var requests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(500) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(307)
	}))
	defer source.Close()
	_, e := mcpclient.Connect(ctx(t), mcpclient.ConnectOptions{Connection: config.Connection{Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal(source.URL)}}}, Headers: map[string]string{"Authorization": "SENTINEL"}})
	code(t, e, "connection_failed")
	if requests.Load() != 0 || strings.Contains(e.Error(), "SENTINEL") {
		t.Fatal("credential forwarded")
	}
}

func TestIdempotencyHeadersDoNotReplayToolCalls(t *testing.T) {
	for _, header := range []string{"Idempotency-Key", "X-Idempotency-Key"} {
		t.Run(header, func(t *testing.T) {
			var effects atomic.Int32
			server := testutil.NewFixtureServer()
			next := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
			hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					b, _ := io.ReadAll(r.Body)
					r.Body = io.NopCloser(strings.NewReader(string(b)))
					var msg struct{ Method string }
					_ = json.Unmarshal(b, &msg)
					if msg.Method == "tools/call" {
						effects.Add(1)
						conn, _, e := w.(http.Hijacker).Hijack()
						if e == nil {
							conn.Close()
						}
						return
					}
				}
				next.ServeHTTP(w, r)
			}))
			defer hs.Close()
			s, e := mcpclient.Connect(ctx(t), mcpclient.ConnectOptions{Connection: config.Connection{Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal(hs.URL)}}}, Headers: map[string]string{header: "fixture"}})
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close(ctx(t))
			if _, e := s.Tools(ctx(t)); e != nil {
				t.Fatal(e)
			}
			result, e := s.Call(ctx(t), "counter", nil, nil, nil)
			code(t, e, "outcome_unknown")
			if !result.Dispatched || effects.Load() != 1 {
				t.Fatalf("tool effects=%d", effects.Load())
			}
		})
	}
}

func TestOversizedHTTPResponses(t *testing.T) {
	for _, method := range []string{"tools/list", "tools/call"} {
		t.Run(method, func(t *testing.T) {
			s := httpSession(t, testutil.NewFixtureServer(), func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == "POST" {
						b, _ := io.ReadAll(r.Body)
						r.Body = io.NopCloser(strings.NewReader(string(b)))
						var msg struct {
							Method string
							ID     json.RawMessage
						}
						_ = json.Unmarshal(b, &msg)
						if msg.Method == method {
							w.Header().Set("Content-Type", "application/json")
							huge := strings.Repeat("x", 16*1024*1024)
							if method == "tools/list" {
								fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"%s","inputSchema":{}}]}}`, msg.ID, huge)
							} else {
								fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"%s"}]}}`, msg.ID, huge)
							}
							return
						}
					}
					next.ServeHTTP(w, r)
				})
			})
			if method == "tools/list" {
				_, e := s.Tools(ctx(t))
				code(t, e, "protocol_error")
			} else {
				r, e := s.Call(ctx(t), "counter", nil, nil, nil)
				code(t, e, "result_too_large")
				if !r.Dispatched || !r.Retire {
					t.Fatal("not dispatched or not retired")
				}
			}
		})
	}
}

func TestOversizedHTTPErrorDoesNotFallBack(t *testing.T) {
	server := testutil.NewFixtureServer()
	next := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			b, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(strings.NewReader(string(b)))
			var msg struct{ Method string }
			_ = json.Unmarshal(b, &msg)
			if msg.Method == "server/discover" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(404)
				_, _ = io.WriteString(w, strings.Repeat("x", 16*1024*1024+1))
				return
			}
		}
		next.ServeHTTP(w, r)
	}))
	defer hs.Close()
	s, e := mcpclient.Connect(ctx(t), mcpclient.ConnectOptions{Connection: config.Connection{Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal(hs.URL)}}}})
	if s != nil {
		defer s.Close(ctx(t))
	}
	code(t, e, "protocol_error")
}

func TestAutoModeNamesLegacySSEServer(t *testing.T) {
	for _, mode := range []string{"", "auto", "streamable"} {
		t.Run("mode="+mode, func(t *testing.T) {
			server := testutil.NewFixtureServer()
			sse := mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return server }, nil)
			var gets, calls atomic.Int32
			hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					gets.Add(1)
				}
				if method, _ := rpcMethod(r); method == "tools/call" {
					calls.Add(1)
				}
				sse.ServeHTTP(w, r)
			}))
			defer hs.Close()
			defer hs.CloseClientConnections()
			s, e := mcpclient.Connect(ctx(t), mcpclient.ConnectOptions{Connection: config.Connection{Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal(hs.URL), Mode: mode}}}, Headers: map[string]string{"X-Api-Key": "SENTINEL"}})
			if s != nil {
				_ = s.Close(ctx(t))
				t.Fatal("connected to an SSE-only server")
			}
			if mode == "streamable" {
				code(t, e, "connection_failed")
				if gets.Load() != 0 {
					t.Fatal("streamable mode probed", gets.Load())
				}
				return
			}
			code(t, e, "runtime_unsupported")
			if !strings.Contains(e.Error(), "legacy SSE") || strings.Contains(e.Error(), "SENTINEL") {
				t.Fatal(e)
			}
			if gets.Load() != 1 || calls.Load() != 0 {
				t.Fatal("gets", gets.Load(), "calls", calls.Load())
			}
		})
	}
}

func TestNoSSEProbeOnOtherFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		oauth  bool
		want   string
	}{
		{"500", 500, false, "connection_failed"},
		{"429", 429, false, "connection_failed"},
		{"401", 401, false, "auth_required"},
		{"401-oauth", 401, true, "auth_required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gets atomic.Int32
			hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					// OAuth metadata discovery GETs well-known paths; only the
					// endpoint itself is a transport probe.
					if r.URL.Path != "/" {
						w.WriteHeader(404)
						return
					}
					gets.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "event: endpoint\ndata: /message\n\n")
					return
				}
				w.WriteHeader(tc.status)
			}))
			defer hs.Close()
			opts := mcpclient.ConnectOptions{Connection: config.Connection{Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal(hs.URL), Mode: "auto"}}}, Headers: map[string]string{"X-Api-Key": "SENTINEL"}}
			if tc.oauth {
				as := testutil.NewAuthServer(t, testutil.AuthServerOptions{ClientID: "pre-id"})
				opts.OAuth = sessionHandler(t, as, hs.URL)
			}
			s, e := mcpclient.Connect(ctx(t), opts)
			if s != nil {
				_ = s.Close(ctx(t))
			}
			code(t, e, tc.want)
			if gets.Load() != 0 || strings.Contains(e.Error(), "SENTINEL") {
				t.Fatal("probed or leaked", gets.Load())
			}
		})
	}
}

// A refused server/discover must not license a probe when initialize then
// fails for another reason: only a connect whose every POST was refused with
// 400, 404 or 405 is diagnosed.
func TestNoSSEProbeAfterLaterPostFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int // initialize answer; 0 drops the connection
	}{{"503", 503}, {"429", 429}, {"500", 500}, {"drop", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			var gets atomic.Int32
			var mu sync.Mutex
			var posts []string
			hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					gets.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "event: endpoint\ndata: /message\n\n")
					return
				}
				method, _ := rpcMethod(r)
				mu.Lock()
				posts = append(posts, method)
				mu.Unlock()
				if method == "server/discover" {
					w.WriteHeader(400)
					return
				}
				if tc.status == 0 {
					conn, _, _ := w.(http.Hijacker).Hijack()
					_ = conn.Close()
					return
				}
				w.WriteHeader(tc.status)
			}))
			defer hs.Close()
			s, e := mcpclient.Connect(ctx(t), mcpclient.ConnectOptions{Connection: config.Connection{Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal(hs.URL), Mode: "auto"}}}, Headers: map[string]string{"X-Api-Key": "SENTINEL"}})
			if s != nil {
				_ = s.Close(ctx(t))
			}
			code(t, e, "connection_failed")
			mu.Lock()
			defer mu.Unlock()
			if gets.Load() != 0 || len(posts) < 2 || posts[0] != "server/discover" {
				t.Fatal("gets", gets.Load(), "posts", posts)
			}
		})
	}
}
