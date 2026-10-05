package auth_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/testutil"
)

// storedSession starts from a granted refresh token for the preconfigured
// client "pre-id" against an MCP server at mcpURL.
func storedSession(t *testing.T, as *testutil.AuthServer, mcpURL string) *auth.OAuthHandler {
	t.Helper()
	s := auth.OAuthState{Version: 1, URL: mcpURL, Issuer: as.URL, Resource: mcpURL, TokenURL: as.URL + "/token", RefreshToken: as.Grant()}
	h := auth.NewOAuthHandler(auth.OAuthOptions{Account: account, Name: "demo", URL: mcpURL, Client: auth.OAuthClient{ID: "pre-id"}, State: &s, Keyring: &testutil.MemKeyring{}})
	t.Cleanup(h.Close)
	return h
}

func TestPRMOutageDuring401Refreshes(t *testing.T) {
	as := testutil.NewAuthServer(t, testutil.AuthServerOptions{Registration: true})
	protected := as.Protect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }), "/mcp")
	var prmDown atomic.Bool
	var reject atomic.Int32
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case prmDown.Load() && strings.HasPrefix(r.URL.Path, "/.well-known/oauth-protected-resource"):
			w.WriteHeader(http.StatusServiceUnavailable)
		case r.URL.Path == "/mcp" && reject.Add(-1) >= 0:
			w.WriteHeader(http.StatusUnauthorized)
		default:
			protected.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(hs.Close)
	f := &fixture{as: as, mcpURL: hs.URL + "/mcp", kr: &testutil.MemKeyring{}}
	signIn(t, f, auth.OAuthClient{})
	h := f.session(t, auth.OAuthClient{})
	if status, err := send(ctx(t), h, f.mcpURL); err != nil || status != 200 {
		t.Fatal(status, err)
	}
	prmDown.Store(true)
	reject.Store(1)
	if status, err := send(ctx(t), h, f.mcpURL); err != nil || status != 200 {
		t.Fatal(status, err)
	}
	if s := f.stored(t); s.Failure != nil || s.RefreshToken == "" {
		t.Fatalf("stored %+v", s.Failure)
	}
	if _, _, n := as.Counts(); n != 2 {
		t.Fatal("refreshes", n)
	}
}

func TestRejectedFreshTokenStopsRefreshing(t *testing.T) {
	as := testutil.NewAuthServer(t, testutil.AuthServerOptions{ClientID: "pre-id"})
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	t.Cleanup(hs.Close)
	h := storedSession(t, as, hs.URL+"/mcp")
	if status, err := send(ctx(t), h, hs.URL+"/mcp"); err != nil || status != 401 {
		t.Fatal(status, err)
	}
	_, _, before := as.Counts()
	_, err := send(ctx(t), h, hs.URL+"/mcp")
	code(t, err, "auth_required")
	_, err = h.Token()
	code(t, err, "auth_required")
	if _, _, after := as.Counts(); before != 2 || after != before {
		t.Fatal("refreshes", before, after)
	}
}

func TestForbiddenIsAuthFailed(t *testing.T) {
	as := testutil.NewAuthServer(t, testutil.AuthServerOptions{ClientID: "pre-id"})
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	t.Cleanup(hs.Close)
	h := storedSession(t, as, hs.URL+"/mcp")
	_, err := send(ctx(t), h, hs.URL+"/mcp")
	code(t, err, "auth_failed")
}

func TestTokenRedirectNotFollowed(t *testing.T) {
	as := testutil.NewAuthServer(t, testutil.AuthServerOptions{ClientID: "pre-id", ClientSecret: "pre-secret"})
	var collected atomic.Int32
	collector := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { collected.Add(1) }))
	t.Cleanup(collector.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, collector.URL+"/collect", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)
	kr := &testutil.MemKeyring{}
	s := auth.OAuthState{Version: 1, URL: "http://127.0.0.1:1/mcp", Issuer: as.URL, Resource: "http://127.0.0.1:1/mcp", TokenURL: redirect.URL + "/token", RefreshToken: as.Grant()}
	h := auth.NewOAuthHandler(auth.OAuthOptions{Account: account, Name: "demo", URL: s.URL, Client: auth.OAuthClient{ID: "pre-id", Secret: "pre-secret"}, State: &s, Keyring: kr})
	t.Cleanup(h.Close)
	_, err := h.Token()
	code(t, err, "connection_failed")
	if collected.Load() != 0 {
		t.Fatal("token request body sent to the redirect target")
	}
}
