package auth_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/testutil"
)

// A server without protected-resource metadata whose authorization server is
// the MCP URL itself: metadata at the origin names the MCP URL as issuer.
func TestLoginIssuerIsMCPURLWithoutPRM(t *testing.T) {
	as := testutil.NewAuthServer(t, testutil.AuthServerOptions{Registration: true, RotateRefresh: true, IssuerPath: "/mcp"})
	f := &fixture{as: as, mcpURL: as.URL + "/mcp", kr: &testutil.MemKeyring{}}
	signIn(t, f, auth.OAuthClient{})
	s := f.stored(t)
	if s.Issuer != f.mcpURL || s.Resource != f.mcpURL || s.TokenURL != f.mcpURL+"/token" || s.ClientID == "" || s.RefreshToken == "" {
		t.Fatalf("stored %+v", s)
	}
	if as.Requests("/.well-known/oauth-protected-resource/mcp") == 0 {
		t.Fatal("protected-resource metadata never tried")
	}

	h := f.session(t, auth.OAuthClient{})
	if _, err := h.Token(); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(ctx(t), http.MethodPost, f.mcpURL, nil)
	req.Header.Set("Authorization", "Bearer stale")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 401 {
		t.Fatal(resp, err)
	}
	h = f.session(t, auth.OAuthClient{})
	if err := h.Authorize(ctx(t), req, resp); err != nil {
		t.Fatal(err)
	}
	if status, err := send(ctx(t), h, f.mcpURL); err != nil || status != 200 {
		t.Fatal(status, err)
	}
	if s := f.stored(t); s.Failure != nil || s.RefreshToken != as.RefreshToken() {
		t.Fatalf("stored %+v", s.Failure)
	}
	if _, _, n := as.Counts(); n != 2 {
		t.Fatal("refreshes", n)
	}
}

// Metadata at the path-inserted URL must still name the MCP URL as issuer.
func TestLoginIssuerPathMismatchRejected(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issuer := srv.URL + "/mcp"
		switch r.URL.Path {
		case "/mcp":
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+srv.URL+`/.well-known/oauth-protected-resource/mcp"`)
			w.WriteHeader(401)
			return
		case "/.well-known/oauth-authorization-server/mcp":
			issuer = srv.URL + "/other"
		case "/.well-known/oauth-authorization-server":
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token",
			"registration_endpoint": issuer + "/register", "code_challenge_methods_supported": []string{"S256"},
		})
	}))
	t.Cleanup(srv.Close)
	f := &fixture{mcpURL: srv.URL + "/mcp", kr: &testutil.MemKeyring{}}
	b := newBrowser()
	h := f.handler(t, nil, auth.OAuthClient{}, b.login(), nil)
	_, err := send(ctx(t), h, f.mcpURL)
	if e := code(t, err, "auth_failed"); !strings.Contains(e.Message, "Could not discover") || len(b.shown()) != 0 {
		t.Fatal(e.Message, b.shown())
	}
	if _, err := f.kr.Get(auth.KeyringService, account); err != auth.ErrNoSession {
		t.Fatal("stored", err)
	}
}
