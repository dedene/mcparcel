package testutil

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// AuthServerOptions configures the fake OAuth 2.1 authorization server.
// ClientID registers a preconfigured client (public when ClientSecret is
// empty). DenyWith makes /authorize answer with that error code.
// UnadvertisedIss keeps sending iss in the callback but drops the metadata flag.
// IssuerPath makes the server its own protected MCP endpoint at that path, with
// URL+IssuerPath as issuer, endpoints under it, metadata at the path-inserted
// and root well-known URLs, and no protected-resource metadata.
// NoRefreshToken issues no refresh token at all; RefreshExpiresIn adds
// refresh_token_expires_in to every token response. Now is the clock for
// access-token expiry and refresh-token idle time (nil means time.Now);
// RefreshIdleTTL rejects a refresh token unused for longer with invalid_grant.
type AuthServerOptions struct {
	UnadvertisedIss  bool
	IssuerPath       string
	Registration     bool
	ClientID         string
	ClientSecret     string
	AccessTTL        time.Duration
	RotateRefresh    bool
	DenyWith         string
	TokenPrefix      string
	NoRefreshToken   bool
	RefreshExpiresIn time.Duration
	Now              func() time.Time
	RefreshIdleTTL   time.Duration
}

// AuthServer is a loopback-only fake authorization server with PKCE S256,
// dynamic client registration, RFC 9207 iss and refresh-token rotation.
type AuthServer struct {
	URL    string
	issuer string
	o      AuthServerOptions

	mu            sync.Mutex
	clients       map[string]asClient
	codes         map[string]asCode
	access        map[string]time.Time
	refresh       map[string]string    // refresh token -> client ID
	refreshUsed   map[string]time.Time // refresh token -> issued or last used
	latestRefresh string
	prmIssuer     string
	registrations int
	exchanges     int
	refreshes     int
	requests      map[string]int // path -> count
	scope         string         // last scope sent to /authorize
	grantTypes    []string       // grant types of the last registration
	noRegister    bool           // StopRegistration was called
}

type asClient struct{ secret, redirect string }

type asCode struct{ client, redirect, challenge, resource string }

func NewAuthServer(t testing.TB, o AuthServerOptions) *AuthServer {
	t.Helper()
	if o.AccessTTL == 0 {
		o.AccessTTL = time.Hour
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	a := &AuthServer{o: o, clients: map[string]asClient{}, codes: map[string]asCode{}, access: map[string]time.Time{}, refresh: map[string]string{}, refreshUsed: map[string]time.Time{}, requests: map[string]int{}}
	if o.ClientID != "" {
		a.clients[o.ClientID] = asClient{secret: o.ClientSecret}
	}
	p := o.IssuerPath
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/oauth-authorization-server"+p, a.metadata)
	if p != "" {
		mux.HandleFunc("GET /.well-known/oauth-authorization-server", a.metadata)
		mux.Handle(p, a.Protect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }), p))
	}
	mux.HandleFunc("POST "+p+"/register", a.register)
	mux.HandleFunc("GET "+p+"/authorize", a.authorize)
	mux.HandleFunc("POST "+p+"/token", a.token)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.requests[r.URL.Path]++
		a.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(hs.Close)
	a.URL = hs.URL
	a.issuer = hs.URL + p
	a.prmIssuer = hs.URL
	return a
}

// Protect serves the protected-resource metadata for path and admits only
// requests to path that carry a valid access token.
func (a *AuthServer) Protect(next http.Handler, path string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metadata := "/.well-known/oauth-protected-resource" + path
		if r.URL.Path == metadata && a.o.IssuerPath == "" {
			a.mu.Lock()
			issuer := a.prmIssuer
			a.mu.Unlock()
			writeJSON(w, 200, map[string]any{"resource": "http://" + r.Host + path, "authorization_servers": []string{issuer}})
			return
		}
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		a.mu.Lock()
		expiry, ok := a.access[token]
		a.mu.Unlock()
		if !ok || a.o.Now().After(expiry) {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="http://`+r.Host+metadata+`"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Revoke invalidates every access and refresh token.
func (a *AuthServer) Revoke() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.access = map[string]time.Time{}
	a.refresh = map[string]string{}
	a.refreshUsed = map[string]time.Time{}
}

// Counts reports registrations, code exchanges and refresh attempts.
func (a *AuthServer) Counts() (registrations, exchanges, refreshes int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.registrations, a.exchanges, a.refreshes
}

// Requests counts requests to path, or to every path when path is empty.
func (a *AuthServer) Requests(path string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if path != "" {
		return a.requests[path]
	}
	n := 0
	for _, c := range a.requests {
		n += c
	}
	return n
}

// Scopes returns the scope parameter of the last /authorize request.
func (a *AuthServer) Scopes() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.scope
}

// GrantTypes returns the grant types of the last client registration.
func (a *AuthServer) GrantTypes() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.grantTypes...)
}

// DropClients forgets every dynamically registered client: /authorize then
// answers 400 without redirecting and /token answers invalid_client.
func (a *AuthServer) DropClients() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id := range a.clients {
		if id != a.o.ClientID {
			delete(a.clients, id)
		}
	}
}

// StopRegistration removes dynamic client registration from the metadata and
// refuses new registrations; clients registered earlier keep working.
func (a *AuthServer) StopRegistration() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.noRegister = true
}

func (a *AuthServer) registers() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.o.Registration && !a.noRegister
}

// RefreshToken returns the latest issued refresh token.
func (a *AuthServer) RefreshToken() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.latestRefresh
}

// Grant issues a refresh token for the preconfigured client without a
// browser flow, for tests that start from a stored session.
func (a *AuthServer) Grant() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	rt := a.o.TokenPrefix + rand.Text()
	a.refresh[rt] = a.o.ClientID
	a.refreshUsed[rt] = a.o.Now()
	a.latestRefresh = rt
	return rt
}

// SetIssuerForPRM changes the issuer that protected-resource metadata names.
func (a *AuthServer) SetIssuerForPRM(issuer string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.prmIssuer = issuer
}

func (a *AuthServer) metadata(w http.ResponseWriter, _ *http.Request) {
	m := map[string]any{
		"issuer":                                         a.issuer,
		"authorization_endpoint":                         a.issuer + "/authorize",
		"token_endpoint":                                 a.issuer + "/token",
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"client_secret_post", "client_secret_basic", "none"},
		"scopes_supported":                               []string{"mcp", "offline_access"},
		"response_types_supported":                       []string{"code"},
		"authorization_response_iss_parameter_supported": !a.o.UnadvertisedIss,
	}
	if a.registers() {
		m["registration_endpoint"] = a.issuer + "/register"
	}
	writeJSON(w, 200, m)
}

func (a *AuthServer) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RedirectURIs            []string `json:"redirect_uris"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
		GrantTypes              []string `json:"grant_types"`
	}
	if !a.registers() || json.NewDecoder(r.Body).Decode(&req) != nil || len(req.RedirectURIs) != 1 || !loopbackRedirect(req.RedirectURIs[0]) {
		writeJSON(w, 400, map[string]string{"error": "invalid_client_metadata"})
		return
	}
	method := req.TokenEndpointAuthMethod
	if method == "" {
		method = "client_secret_basic"
	}
	c := asClient{redirect: req.RedirectURIs[0]}
	resp := map[string]any{"client_id": "client-" + rand.Text(), "redirect_uris": req.RedirectURIs, "token_endpoint_auth_method": method}
	if method != "none" {
		c.secret = a.o.TokenPrefix + rand.Text()
		resp["client_secret"] = c.secret
	}
	a.mu.Lock()
	a.registrations++
	a.grantTypes = req.GrantTypes
	a.clients[resp["client_id"].(string)] = c
	a.mu.Unlock()
	writeJSON(w, 201, resp)
}

func (a *AuthServer) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	a.mu.Lock()
	c, ok := a.clients[q.Get("client_id")]
	a.scope = q.Get("scope")
	a.mu.Unlock()
	redirect := q.Get("redirect_uri")
	if !ok || !loopbackRedirect(redirect) || c.redirect != "" && c.redirect != redirect || q.Get("response_type") != "code" ||
		q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("resource") == "" {
		http.Error(w, "invalid authorization request", http.StatusBadRequest)
		return
	}
	back := url.Values{"state": {q.Get("state")}}
	if a.o.DenyWith != "" {
		back.Set("error", a.o.DenyWith)
		back.Set("error_description", "The fixture denied the request.")
	} else {
		code := a.o.TokenPrefix + rand.Text()
		a.mu.Lock()
		a.codes[code] = asCode{client: q.Get("client_id"), redirect: redirect, challenge: q.Get("code_challenge"), resource: q.Get("resource")}
		a.mu.Unlock()
		back.Set("code", code)
		back.Set("iss", a.issuer)
	}
	sep := "?"
	if strings.Contains(redirect, "?") {
		sep = "&"
	}
	http.Redirect(w, r, redirect+sep+back.Encode(), http.StatusFound)
}

func (a *AuthServer) token(w http.ResponseWriter, r *http.Request) {
	if r.ParseForm() != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid_request"})
		return
	}
	id, secret, basic := r.BasicAuth()
	if basic {
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.clients[id]
	if !ok || c.secret != secret {
		writeJSON(w, 401, map[string]string{"error": "invalid_client"})
		return
	}
	rotate := true
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		a.exchanges++
		code, ok := a.codes[r.PostForm.Get("code")]
		delete(a.codes, r.PostForm.Get("code"))
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if !ok || code.client != id || code.redirect != r.PostForm.Get("redirect_uri") || code.resource != r.PostForm.Get("resource") ||
			base64.RawURLEncoding.EncodeToString(sum[:]) != code.challenge {
			writeJSON(w, 400, map[string]string{"error": "invalid_grant"})
			return
		}
	case "refresh_token":
		a.refreshes++
		rt := r.PostForm.Get("refresh_token")
		if a.refresh[rt] != id || rt == "" || a.o.RefreshIdleTTL > 0 && a.o.Now().Sub(a.refreshUsed[rt]) > a.o.RefreshIdleTTL {
			writeJSON(w, 400, map[string]string{"error": "invalid_grant"})
			return
		}
		rotate = a.o.RotateRefresh
		if rotate {
			delete(a.refresh, rt)
			delete(a.refreshUsed, rt)
		} else {
			a.refreshUsed[rt] = a.o.Now()
		}
	default:
		writeJSON(w, 400, map[string]string{"error": "unsupported_grant_type"})
		return
	}
	access := a.o.TokenPrefix + rand.Text()
	a.access[access] = a.o.Now().Add(a.o.AccessTTL)
	resp := map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": int(a.o.AccessTTL / time.Second)}
	if a.o.RefreshExpiresIn > 0 {
		resp["refresh_token_expires_in"] = int(a.o.RefreshExpiresIn / time.Second)
	}
	if rotate && !a.o.NoRefreshToken {
		rt := a.o.TokenPrefix + rand.Text()
		a.refresh[rt] = id
		a.refreshUsed[rt] = a.o.Now()
		a.latestRefresh = rt
		resp["refresh_token"] = rt
	}
	writeJSON(w, 200, resp)
}

func loopbackRedirect(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "::1" || u.Hostname() == "localhost")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
