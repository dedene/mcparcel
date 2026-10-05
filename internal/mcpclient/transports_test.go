package mcpclient_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestHTTP401AfterDispatch(t *testing.T) {
	var calls atomic.Int64
	s := httpSession(t, testutil.NewFixtureServer(), func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" {
				data, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(strings.NewReader(string(data)))
				var msg struct{ Method string }
				_ = json.Unmarshal(data, &msg)
				if msg.Method == "tools/call" {
					calls.Add(1)
					w.WriteHeader(401)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	})
	r, e := s.Call(context.Background(), "counter", nil, nil)
	code(t, e, "outcome_unknown")
	if !r.Dispatched || calls.Load() != 1 {
		t.Fatal("call replay")
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
			result, e := s.Call(ctx(t), "counter", nil, nil)
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
				r, e := s.Call(ctx(t), "counter", nil, nil)
				code(t, e, "outcome_unknown")
				if !r.Dispatched {
					t.Fatal("not dispatched")
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
