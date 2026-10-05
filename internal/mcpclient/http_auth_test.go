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
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/mcpclient"
	"github.com/dedene/mcparcel/internal/testutil"
)

// rpcMethod reads the JSON-RPC method of a POST and restores its body.
func rpcMethod(r *http.Request) (string, json.RawMessage) {
	if r.Method != http.MethodPost {
		return "", nil
	}
	b, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(strings.NewReader(string(b)))
	var msg struct {
		Method string
		ID     json.RawMessage
	}
	_ = json.Unmarshal(b, &msg)
	return msg.Method, msg.ID
}

func connectHTTP(t *testing.T, u string, h *auth.OAuthHandler) (mcpclient.Session, error) {
	t.Helper()
	opts := mcpclient.ConnectOptions{Connection: config.Connection{Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal(u)}}}, Headers: map[string]string{"X-Api-Key": "fixture"}}
	if h != nil {
		opts.OAuth = h
	}
	s, err := mcpclient.Connect(ctx(t), opts)
	if err == nil {
		t.Cleanup(func() { _ = s.Close(context.Background()) })
	}
	return s, err
}

func TestHTTP401InitializePromptAuthRequired(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"all": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) },
		"discover-method-not-found": func(w http.ResponseWriter, r *http.Request) {
			method, id := rpcMethod(r)
			if method == "server/discover" {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"method not found"}}`, id)
				return
			}
			w.WriteHeader(401)
		},
	} {
		t.Run(name, func(t *testing.T) {
			hs := httptest.NewServer(handler)
			defer hs.Close()
			start := time.Now()
			_, err := connectHTTP(t, hs.URL, nil)
			code(t, err, "auth_required")
			if time.Since(start) > time.Second {
				t.Fatal("slow auth_required", time.Since(start))
			}
		})
	}
}

func TestHTTP401ToolsListAuthRequired(t *testing.T) {
	s := httpSession(t, testutil.NewFixtureServer(), func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if method, _ := rpcMethod(r); method == "tools/list" {
				w.WriteHeader(401)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	_, err := s.Tools(ctx(t))
	code(t, err, "auth_required")
}

func TestHTTP401ToolCallNotOutcomeUnknown(t *testing.T) {
	var calls atomic.Int64
	s := httpSession(t, testutil.NewFixtureServer(), func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if method, _ := rpcMethod(r); method == "tools/call" {
				calls.Add(1)
				w.WriteHeader(401)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	r, err := s.Call(context.Background(), "counter", nil, nil, nil)
	code(t, err, "auth_required")
	if !r.Dispatched || calls.Load() != 1 {
		t.Fatal("call replay")
	}
}

// oauthServer serves the fixture MCP server behind the fake authorization
// server; wrap sees requests before the bearer check.
func oauthServer(t *testing.T, as *testutil.AuthServer, wrap func(http.Handler) http.Handler) string {
	t.Helper()
	server := testutil.NewFixtureServer()
	h := as.Protect(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil), "/mcp")
	if wrap != nil {
		h = wrap(h)
	}
	hs := httptest.NewServer(h)
	t.Cleanup(hs.Close)
	return hs.URL + "/mcp"
}

func sessionHandler(t *testing.T, as *testutil.AuthServer, u string) *auth.OAuthHandler {
	t.Helper()
	state := &auth.OAuthState{Version: 1, URL: u, Issuer: as.URL, Resource: u, TokenURL: as.URL + "/token", AuthStyle: 1, RefreshToken: as.Grant()}
	h := auth.NewOAuthHandler(auth.OAuthOptions{Account: "personal/demo", Name: "demo", URL: u, Client: auth.OAuthClient{ID: "pre-id"}, State: state, Keyring: &testutil.MemKeyring{}})
	t.Cleanup(h.Close)
	return h
}

func TestOAuthModeAttachesAndRefreshes(t *testing.T) {
	as := testutil.NewAuthServer(t, testutil.AuthServerOptions{ClientID: "pre-id", AccessTTL: 33 * time.Second})
	u := oauthServer(t, as, nil)
	s, err := connectHTTP(t, u, sessionHandler(t, as, u))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Tools(ctx(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Tools(ctx(t)); err != nil {
		t.Fatal(err)
	}
	if _, _, n := as.Counts(); n != 1 {
		t.Fatal("refreshes", n)
	}
	time.Sleep(4 * time.Second)
	if _, err := s.Tools(ctx(t)); err != nil {
		t.Fatal(err)
	}
	if _, _, n := as.Counts(); n != 2 {
		t.Fatal("refreshes after expiry", n)
	}
}

func TestOAuthRefreshAfter401Resends(t *testing.T) {
	as := testutil.NewAuthServer(t, testutil.AuthServerOptions{ClientID: "pre-id"})
	var rejected atomic.Bool
	u := oauthServer(t, as, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if method, _ := rpcMethod(r); method == "tools/list" && rejected.CompareAndSwap(false, true) {
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="http://`+r.Host+`/.well-known/oauth-protected-resource/mcp"`)
				w.WriteHeader(401)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	s, err := connectHTTP(t, u, sessionHandler(t, as, u))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Tools(ctx(t)); err != nil {
		t.Fatal(err)
	}
	if _, _, n := as.Counts(); n != 2 || !rejected.Load() {
		t.Fatal("refreshes", n)
	}
}

func TestOAuth401DoesNotReplayTool(t *testing.T) {
	as := testutil.NewAuthServer(t, testutil.AuthServerOptions{ClientID: "pre-id"})
	var calls atomic.Int64
	u := oauthServer(t, as, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if method, _ := rpcMethod(r); method == "tools/call" {
				calls.Add(1)
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="http://`+r.Host+`/.well-known/oauth-protected-resource/mcp"`)
				w.WriteHeader(401)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	s, err := connectHTTP(t, u, sessionHandler(t, as, u))
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Call(ctx(t), "counter", nil, nil, nil)
	code(t, err, "auth_expired")
	if !r.Dispatched || calls.Load() != 1 {
		t.Fatal("tool call replayed", calls.Load())
	}
}
