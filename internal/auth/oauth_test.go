package auth_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
	"github.com/dedene/mcparcel/internal/testutil"
)

const account = "personal/demo"

type fixture struct {
	as     *testutil.AuthServer
	mcpURL string
	kr     *testutil.MemKeyring
}

func newFixture(t *testing.T, o testutil.AuthServerOptions) *fixture {
	t.Helper()
	as := testutil.NewAuthServer(t, o)
	hs := httptest.NewServer(as.Protect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }), "/mcp"))
	t.Cleanup(hs.Close)
	return &fixture{as: as, mcpURL: hs.URL + "/mcp", kr: &testutil.MemKeyring{}}
}

func (f *fixture) handler(t *testing.T, a *config.OAuth, client auth.OAuthClient, login *auth.LoginOptions, state *auth.OAuthState) *auth.OAuthHandler {
	t.Helper()
	h := auth.NewOAuthHandler(auth.OAuthOptions{Account: account, Name: "demo", Label: "Demo", URL: f.mcpURL, Auth: a, Client: client, State: state, Keyring: f.kr, Login: login})
	t.Cleanup(h.Close)
	return h
}

func (f *fixture) stored(t *testing.T) auth.OAuthState {
	t.Helper()
	s, err := auth.LoadOAuth(context.Background(), f.kr, account)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// session builds a session-mode handler from the stored item.
func (f *fixture) session(t *testing.T, client auth.OAuthClient) *auth.OAuthHandler {
	t.Helper()
	s := f.stored(t)
	return f.handler(t, nil, client, nil, &s)
}

// send emulates the SDK transport: token on every request, one Authorize
// and one resend on 401/403.
func send(ctx context.Context, h *auth.OAuthHandler, u string) (int, error) {
	for attempt := 0; ; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
		ts, err := h.TokenSource(ctx)
		if err != nil {
			return 0, err
		}
		if ts != nil {
			tok, err := ts.Token()
			if err != nil {
				return 0, err
			}
			req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err
		}
		if attempt == 1 || resp.StatusCode != 401 && resp.StatusCode != 403 {
			resp.Body.Close()
			return resp.StatusCode, nil
		}
		if err := h.Authorize(ctx, req, resp); err != nil {
			return 0, err
		}
	}
}

type page struct {
	status int
	body   string
}

// browser follows the authorization URL like a user agent.
type browser struct {
	mu    sync.Mutex
	urls  []string
	pages chan page
	visit func(string) page
}

func newBrowser() *browser {
	return &browser{pages: make(chan page, 4), visit: get}
}

func get(u string) page {
	resp, err := http.Get(u)
	if err != nil {
		return page{body: err.Error()}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return page{resp.StatusCode, string(b)}
}

func (b *browser) login() *auth.LoginOptions {
	return &auth.LoginOptions{ShowURL: func(u string) error {
		b.mu.Lock()
		b.urls = append(b.urls, u)
		b.mu.Unlock()
		go func() { b.pages <- b.visit(u) }()
		return nil
	}}
}

func (b *browser) shown() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.urls...)
}

func (b *browser) page(t *testing.T) page {
	t.Helper()
	select {
	case p := <-b.pages:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("no browser page")
		return page{}
	}
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

func code(t *testing.T, err error, want string) *output.Error {
	t.Helper()
	var e *output.Error
	if !errors.As(err, &e) || e.Code != want {
		t.Fatalf("error = %v, want %s", err, want)
	}
	return e
}

func signIn(t *testing.T, f *fixture, client auth.OAuthClient) {
	t.Helper()
	b := newBrowser()
	h := f.handler(t, nil, client, b.login(), nil)
	if status, err := send(ctx(t), h, f.mcpURL); err != nil || status != 200 {
		t.Fatal(status, err)
	}
	b.page(t)
}

func TestLoginDynamicRegistrationStoresBinding(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true})
	b := newBrowser()
	h := f.handler(t, nil, auth.OAuthClient{}, b.login(), nil)
	if h.SignedIn() {
		t.Fatal("signed in before login")
	}
	status, err := send(ctx(t), h, f.mcpURL)
	if err != nil || status != 200 {
		t.Fatal(status, err)
	}
	if p := b.page(t); p.status != 200 || !strings.Contains(p.body, "Signed in") {
		t.Fatalf("page %d %q", p.status, p.body)
	}
	s := f.stored(t)
	if !h.SignedIn() || s.URL != f.mcpURL || s.Issuer != f.as.URL || s.Resource != f.mcpURL || s.TokenURL != f.as.URL+"/token" ||
		s.ClientID == "" || s.ClientSecret == "" || s.RefreshToken != f.as.RefreshToken() || s.AccessExpiry == 0 || s.Failure != nil {
		t.Fatalf("stored %+v", s)
	}
	if r, x, _ := f.as.Counts(); r != 1 || x != 1 {
		t.Fatal("registrations/exchanges", r, x)
	}
}

func TestLoginPreconfiguredClient(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{ClientID: "pre-id", ClientSecret: "pre-secret"})
	signIn(t, f, auth.OAuthClient{ID: "pre-id", Secret: "pre-secret"})
	s := f.stored(t)
	if s.ClientID != "" || s.ClientSecret != "" || s.RefreshToken == "" {
		t.Fatal("preconfigured client stored")
	}
	h := f.session(t, auth.OAuthClient{ID: "pre-id", Secret: "pre-secret"})
	if _, err := h.Token(); err != nil {
		t.Fatal(err)
	}
	if r, _, n := f.as.Counts(); r != 0 || n != 1 {
		t.Fatal("registrations/refreshes", r, n)
	}
}

func TestRefreshRotationSavedBeforeUse(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true, RotateRefresh: true})
	signIn(t, f, auth.OAuthClient{})
	old := f.stored(t).RefreshToken
	var logs []string
	s := f.stored(t)
	h := auth.NewOAuthHandler(auth.OAuthOptions{Account: account, Name: "demo", URL: f.mcpURL, State: &s, Keyring: f.kr, Log: func(m string) { logs = append(logs, m) }})
	defer h.Close()
	if _, err := h.Token(); err != nil {
		t.Fatal(err)
	}
	if got := f.stored(t).RefreshToken; got == old || got != f.as.RefreshToken() {
		t.Fatal("rotated refresh token not saved")
	}
	if strings.Join(logs, ",") != "oauth_refreshed" {
		t.Fatal(logs)
	}
}

func TestRefreshSaveFailureKeepsToken(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true, RotateRefresh: true})
	signIn(t, f, auth.OAuthClient{})
	h := f.session(t, auth.OAuthClient{})
	f.kr.FailSets = 1
	_, err := h.Token()
	code(t, err, "keychain_unavailable")
	tok, err := h.Token()
	if err != nil || tok.AccessToken == "" {
		t.Fatal(err)
	}
	if _, _, n := f.as.Counts(); n != 1 || f.stored(t).RefreshToken != f.as.RefreshToken() {
		t.Fatal("save not retried", n)
	}
}

func TestConcurrentCallersRefreshOnce(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true, RotateRefresh: true})
	signIn(t, f, auth.OAuthClient{})
	h := f.session(t, auth.OAuthClient{})
	var wg sync.WaitGroup
	tokens := make(chan string, 20)
	for range 20 {
		wg.Go(func() {
			tok, err := h.Token()
			if err != nil {
				t.Error(err)
				return
			}
			tokens <- tok.AccessToken
		})
	}
	wg.Wait()
	close(tokens)
	seen := map[string]bool{}
	for tok := range tokens {
		seen[tok] = true
	}
	if _, _, n := f.as.Counts(); n != 1 || len(seen) != 1 {
		t.Fatal("refreshes", n, len(seen))
	}
	if f.stored(t).RefreshToken != f.as.RefreshToken() {
		t.Fatal("rotated refresh token not stored")
	}
}

func TestRefreshInvalidGrantRecordsFailure(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true})
	signIn(t, f, auth.OAuthClient{})
	var logs []string
	s := f.stored(t)
	h := auth.NewOAuthHandler(auth.OAuthOptions{Account: account, Name: "demo", URL: f.mcpURL, State: &s, Keyring: f.kr, Log: func(m string) { logs = append(logs, m) }})
	defer h.Close()
	f.as.Revoke()
	_, err := h.Token()
	if e := code(t, err, "auth_required"); e.NextAction != "mcparcel auth demo" || e.Message != "Sign-in required for demo." {
		t.Fatal(e)
	}
	stored := f.stored(t)
	if stored.Failure == nil || stored.Failure.Code != "invalid_grant" || stored.RefreshToken != "" || stored.Failure.At == 0 {
		t.Fatalf("stored %+v", stored.Failure)
	}
	_, err = h.Token()
	code(t, err, "auth_required")
	if _, _, n := f.as.Counts(); n != 1 || strings.Join(logs, ",") != "oauth_refresh_failed" {
		t.Fatal("refresh after terminal failure", n, logs)
	}
}

func TestRefreshServerErrorNotRecorded(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true})
	signIn(t, f, auth.OAuthClient{})
	for _, tc := range []struct {
		status int
		body   string
	}{{502, ""}, {503, `{"error":"temporarily_unavailable"}`}, {500, `{"error":"server_error"}`}, {429, `{"error":"slow_down"}`}} {
		broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, tc.body)
		}))
		s := f.stored(t)
		s.TokenURL = broken.URL + "/token"
		h := f.handler(t, nil, auth.OAuthClient{}, nil, &s)
		_, err := h.Token()
		broken.Close()
		code(t, err, "connection_failed")
		if got := f.stored(t); got.Failure != nil || got.RefreshToken == "" {
			t.Fatal("transient failure recorded", tc.status)
		}
	}
}

func TestEmptyRefreshTokenNotSignedIn(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true})
	h := f.handler(t, nil, auth.OAuthClient{}, nil, &auth.OAuthState{Version: 1, URL: f.mcpURL, TokenURL: f.as.URL + "/token"})
	_, err := h.Token()
	code(t, err, "auth_required")
	if _, _, n := f.as.Counts(); n != 0 {
		t.Fatal("refresh without refresh token")
	}
}

func TestChangedIssuerRejectsOldToken(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true})
	other := testutil.NewAuthServer(t, testutil.AuthServerOptions{Registration: true})
	signIn(t, f, auth.OAuthClient{})
	h := f.session(t, auth.OAuthClient{})
	if status, err := send(ctx(t), h, f.mcpURL); err != nil || status != 200 {
		t.Fatal(status, err)
	}
	f.as.SetIssuerForPRM(other.URL)
	f.as.Revoke()
	_, err := send(ctx(t), h, f.mcpURL)
	code(t, err, "auth_required")
	if s := f.stored(t); s.Failure == nil || s.Failure.Code != "issuer_changed" || s.RefreshToken != "" {
		t.Fatalf("stored %+v", s.Failure)
	}
	if _, _, n := f.as.Counts(); n != 1 {
		t.Fatal("refreshed against changed issuer", n)
	}
}

func TestClosedHandlerNeverRefreshes(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true})
	signIn(t, f, auth.OAuthClient{})
	h := f.session(t, auth.OAuthClient{})
	h.Close()
	_, err := h.Token()
	code(t, err, "auth_required")
	if _, _, n := f.as.Counts(); n != 0 {
		t.Fatal("closed handler refreshed")
	}
}

func TestKeychainUnavailableAtLogin(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true})
	f.kr.Err = errors.New("keychain locked")
	b := newBrowser()
	h := f.handler(t, nil, auth.OAuthClient{}, b.login(), nil)
	_, err := send(ctx(t), h, f.mcpURL)
	code(t, err, "keychain_unavailable")
	if p := b.page(t); !strings.Contains(p.body, "Sign-in failed") {
		t.Fatal(p.body)
	}
}

func TestNoSecretsInErrors(t *testing.T) {
	const canary = "CANARY"
	var errs []error
	f := newFixture(t, testutil.AuthServerOptions{ClientID: "pre-id", ClientSecret: canary + "-secret", TokenPrefix: canary})
	b := newBrowser()
	h := f.handler(t, nil, auth.OAuthClient{ID: "pre-id", Secret: "wrong-" + canary}, b.login(), nil)
	_, err := send(ctx(t), h, f.mcpURL)
	code(t, err, "auth_failed")
	b.page(t)
	errs = append(errs, err)

	signIn(t, f, auth.OAuthClient{ID: "pre-id", Secret: canary + "-secret"})
	s := f.stored(t)
	f.as.Revoke()
	h = f.handler(t, nil, auth.OAuthClient{ID: "pre-id", Secret: canary + "-secret"}, nil, &s)
	_, err = h.Token()
	errs = append(errs, err)

	for _, err := range errs {
		text := fmt.Sprintf("%v %+v %#v", err, err, err)
		if err == nil || strings.Contains(text, canary) {
			t.Fatalf("secret in error: %q", text)
		}
	}
	if text := fmt.Sprintf("%v %+v %#v %s", s, s, s, s); strings.Contains(text, canary) {
		t.Fatal("state not redacted", text)
	}
}
