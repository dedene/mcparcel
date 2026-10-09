package auth_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/output"
	"github.com/dedene/mcparcel/internal/testutil"
)

const (
	ccID     = "cc-client-canary"
	ccSecret = "cc-secret-canary"
)

func ccServer(t *testing.T, o testutil.AuthServerOptions) *testutil.AuthServer {
	t.Helper()
	o.ClientCredentials, o.ClientID, o.ClientSecret = true, ccID, ccSecret
	return testutil.NewAuthServer(t, o)
}

func newCC(t *testing.T, cfg auth.ClientCredentialsConfig) *auth.ClientCredentials {
	t.Helper()
	if cfg.ClientID == "" {
		cfg.ClientID, cfg.Secret = ccID, ccSecret
	}
	h, err := auth.NewClientCredentials(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	return h
}

func ccToken(h *auth.ClientCredentials) (*oauth2.Token, error) {
	ts, err := h.TokenSource(context.Background())
	if err != nil {
		return nil, err
	}
	if ts == nil {
		return nil, errors.New("nil token source")
	}
	return ts.Token()
}

func mustToken(t *testing.T, h *auth.ClientCredentials) string {
	t.Helper()
	tok, err := ccToken(h)
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken == "" || !strings.EqualFold(tok.TokenType, "bearer") {
		t.Fatal("unusable token", tok.TokenType)
	}
	return tok.AccessToken
}

// ccReject answers a request that carried token with status, as the SDK's
// transport does for 401 and 403.
func ccReject(ctx context.Context, h *auth.ClientCredentials, status int, token string) error {
	req := httptest.NewRequest(http.MethodPost, "https://mcp.example.invalid/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp := &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":"invalid_token"}`))}
	return h.Authorize(ctx, req, resp)
}

func errCode(err error) string {
	var oe *output.Error
	if errors.As(err, &oe) {
		return oe.Code
	}
	return ""
}

func grants(as *testutil.AuthServer) int { return as.GrantCounts().ClientCredentials }

func TestMintOnFirstToken(t *testing.T) {
	as := ccServer(t, testutil.AuthServerOptions{CCExpiresIn: 900})
	h := newCC(t, auth.ClientCredentialsConfig{TokenURL: as.URL + "/token"})
	if grants(as) != 0 {
		t.Fatal("minted before the first Token")
	}
	first := mustToken(t, h)
	if second := mustToken(t, h); second != first || grants(as) != 1 {
		t.Fatal("token not reused", grants(as))
	}
}

func TestReuseUntilRefreshPoint(t *testing.T) {
	clock := testutil.NewClock()
	as := ccServer(t, testutil.AuthServerOptions{CCExpiresIn: 900, Now: clock.Now})
	h := newCC(t, auth.ClientCredentialsConfig{TokenURL: as.URL + "/token", Now: clock.Now})
	first := mustToken(t, h)
	clock.Advance(719 * time.Second)
	if mustToken(t, h) != first || grants(as) != 1 {
		t.Fatal("re-minted before 180 s remained")
	}
	clock.Advance(2 * time.Second)
	if mustToken(t, h) == first || grants(as) != 2 {
		t.Fatal("not re-minted at 179 s left")
	}
}

func TestShortLifetimeCap(t *testing.T) {
	clock := testutil.NewClock()
	as := ccServer(t, testutil.AuthServerOptions{CCExpiresIn: 6, Now: clock.Now})
	h := newCC(t, auth.ClientCredentialsConfig{TokenURL: as.URL + "/token", Now: clock.Now})
	first := mustToken(t, h)
	clock.Advance(2 * time.Second)
	if mustToken(t, h) != first || grants(as) != 1 {
		t.Fatal("re-minted with 4 s left")
	}
	clock.Advance(time.Second)
	if mustToken(t, h) == first || grants(as) != 2 {
		t.Fatal("not re-minted at 3 s left")
	}
}

func TestUnknownExpiryUsedUntil401(t *testing.T) {
	clock := testutil.NewClock()
	as := ccServer(t, testutil.AuthServerOptions{Now: clock.Now, AccessTTL: 1000 * time.Hour})
	h := newCC(t, auth.ClientCredentialsConfig{TokenURL: as.URL + "/token", Now: clock.Now})
	first := mustToken(t, h)
	clock.Advance(240 * time.Hour)
	if mustToken(t, h) != first || grants(as) != 1 {
		t.Fatal("token without expires_in re-minted")
	}
	if err := ccReject(context.Background(), h, http.StatusUnauthorized, first); err != nil {
		t.Fatal(err)
	}
	if mustToken(t, h) == first || grants(as) != 2 {
		t.Fatal("401 did not re-mint")
	}
}

// formServer answers like Front and hands each request to check.
func formServer(t *testing.T, check func(*http.Request)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		check(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"at-1","token_type":"bearer","expires_in":900}`)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/oauth/token"
}

func TestClientSecretPostForm(t *testing.T) {
	var got url.Values
	var header, ctype string
	tokenURL := formServer(t, func(r *http.Request) {
		_ = r.ParseForm()
		got, header, ctype = r.PostForm, r.Header.Get("Authorization"), r.Header.Get("Content-Type")
	})
	h := newCC(t, auth.ClientCredentialsConfig{TokenURL: tokenURL, ClientID: "id with&chars", Secret: "secret:/+=", Scopes: []string{"read", "write"}})
	mustToken(t, h)
	want := url.Values{"grant_type": {"client_credentials"}, "client_id": {"id with&chars"}, "client_secret": {"secret:/+="}, "scope": {"read write"}}
	if fmt.Sprint(got) != fmt.Sprint(want) || header != "" || !strings.HasPrefix(ctype, "application/x-www-form-urlencoded") {
		t.Fatal(got, header, ctype)
	}
}

func TestClientSecretBasic(t *testing.T) {
	var got url.Values
	var header string
	tokenURL := formServer(t, func(r *http.Request) {
		_ = r.ParseForm()
		got, header = r.PostForm, r.Header.Get("Authorization")
	})
	h := newCC(t, auth.ClientCredentialsConfig{TokenURL: tokenURL, ClientID: "id with&chars", Secret: "secret:/+=", AuthStyle: oauth2.AuthStyleInHeader})
	mustToken(t, h)
	basic := base64.StdEncoding.EncodeToString([]byte(url.QueryEscape("id with&chars") + ":" + url.QueryEscape("secret:/+=")))
	if fmt.Sprint(got) != fmt.Sprint(url.Values{"grant_type": {"client_credentials"}}) || header != "Basic "+basic {
		t.Fatal(got, header)
	}
}

func TestRejectsNonBearerAndEmptyToken(t *testing.T) {
	for _, tt := range []struct{ name, body, code string }{
		{"mac", `{"access_token":"at-1","token_type":"mac","expires_in":900}`, "auth_failed"},
		{"missingType", `{"access_token":"at-1","expires_in":900}`, "auth_failed"},
		{"emptyToken", `{"access_token":"","token_type":"bearer","expires_in":900}`, "auth_failed"},
		{"notJSON", `<html>sign in</html>`, "auth_failed"},
		{"upperBearer", `{"access_token":"at-1","token_type":"BEARER","expires_in":900}`, ""},
		{"negativeExpiry", `{"access_token":"at-1","token_type":"Bearer","expires_in":-5}`, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()
			_, err := ccToken(newCC(t, auth.ClientCredentialsConfig{TokenURL: srv.URL}))
			if errCode(err) != tt.code || tt.code == "" && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMintErrorCodes(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	for _, tt := range []struct {
		name, oauthErr string
		status         int
		url            string
		delay          time.Duration
		code           string
	}{
		{name: "invalidClient400", status: 400, oauthErr: "invalid_client", code: "auth_failed"},
		{name: "invalidClient401", status: 401, oauthErr: "invalid_client", code: "auth_failed"},
		{name: "unauthorizedClient", status: 400, oauthErr: "unauthorized_client", code: "auth_failed"},
		{name: "invalidScope", status: 400, oauthErr: "invalid_scope", code: "auth_failed"},
		{name: "unavailable", status: 503, code: "connection_failed"},
		{name: "rateLimited", status: 429, oauthErr: "slow_down", code: "connection_failed"},
		{name: "refused", url: closed.URL, code: "connection_failed"},
		{name: "timeout", status: 200, delay: time.Second, code: "connection_failed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tokenURL := tt.url
			if tokenURL == "" {
				release := make(chan struct{})
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					select {
					case <-time.After(tt.delay):
					case <-release:
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tt.status)
					_, _ = fmt.Fprintf(w, `{"error":%q,"error_description":"denied"}`, tt.oauthErr)
				}))
				defer srv.Close()
				defer close(release)
				tokenURL = srv.URL
			}
			h := newCC(t, auth.ClientCredentialsConfig{TokenURL: tokenURL, HTTPClient: &http.Client{Timeout: 100 * time.Millisecond}})
			_, err := ccToken(h)
			if errCode(err) != tt.code {
				t.Fatal(err)
			}
			if tt.oauthErr != "" && tt.code == "auth_failed" && !strings.Contains(err.Error(), tt.oauthErr) {
				t.Fatal("OAuth error code missing:", err)
			}
		})
	}
}

func TestMintFailureNotCached(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":"invalid_client"}`)
			return
		}
		_, _ = io.WriteString(w, `{"access_token":"at-2","token_type":"bearer","expires_in":900}`)
	}))
	defer srv.Close()
	h := newCC(t, auth.ClientCredentialsConfig{TokenURL: srv.URL})
	if _, err := ccToken(h); errCode(err) != "auth_failed" {
		t.Fatal(err)
	}
	if mustToken(t, h) != "at-2" || calls.Load() != 2 {
		t.Fatal("failure cached", calls.Load())
	}
}

func TestNoRedirectFollowed(t *testing.T) {
	var hit atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit.Store(true)
		_, _ = io.WriteString(w, `{"access_token":"at-1","token_type":"bearer"}`)
	}))
	defer target.Close()
	srv := httptest.NewServer(http.RedirectHandler(target.URL+"/token", http.StatusFound))
	defer srv.Close()
	for _, client := range []*http.Client{nil, {}} {
		h := newCC(t, auth.ClientCredentialsConfig{TokenURL: srv.URL, HTTPClient: client})
		if _, err := ccToken(h); errCode(err) != "connection_failed" || hit.Load() {
			t.Fatal(err, hit.Load())
		}
	}
}

func TestForbiddenNoMint(t *testing.T) {
	as := ccServer(t, testutil.AuthServerOptions{CCExpiresIn: 900})
	h := newCC(t, auth.ClientCredentialsConfig{TokenURL: as.URL + "/token"})
	tok := mustToken(t, h)
	if err := ccReject(context.Background(), h, http.StatusForbidden, tok); errCode(err) != "auth_failed" || grants(as) != 1 {
		t.Fatal(err, grants(as))
	}
	if mustToken(t, h) != tok {
		t.Fatal("403 dropped the token")
	}
}

// A 401 for the token a 401 just minted is token_rejected, not another mint;
// the rejected token is dropped so the next call mints afresh.
func TestRejectedRightAfterMint(t *testing.T) {
	as := ccServer(t, testutil.AuthServerOptions{CCExpiresIn: 900})
	h := newCC(t, auth.ClientCredentialsConfig{TokenURL: as.URL + "/token"})
	first := mustToken(t, h)
	if err := ccReject(context.Background(), h, http.StatusUnauthorized, first); err != nil {
		t.Fatal(err)
	}
	second := mustToken(t, h)
	err := ccReject(context.Background(), h, http.StatusUnauthorized, second)
	if errCode(err) != "auth_failed" || !strings.Contains(err.Error(), "token_rejected") || grants(as) != 2 {
		t.Fatal(err, grants(as))
	}
	if third := mustToken(t, h); third == second || grants(as) != 3 {
		t.Fatal("rejected token kept", grants(as))
	}
}

func TestClosedHandlerFails(t *testing.T) {
	as := ccServer(t, testutil.AuthServerOptions{CCExpiresIn: 900})
	h := newCC(t, auth.ClientCredentialsConfig{TokenURL: as.URL + "/token"})
	mustToken(t, h)
	h.Close()
	if _, err := ccToken(h); err == nil || grants(as) != 1 {
		t.Fatal("closed handler served a token", err)
	}
}

func TestNewClientCredentialsRequiresConfig(t *testing.T) {
	for _, cfg := range []auth.ClientCredentialsConfig{
		{ClientID: ccID, Secret: ccSecret},
		{TokenURL: "https://ws.example.invalid/oauth/token", Secret: ccSecret},
		{TokenURL: "https://ws.example.invalid/oauth/token", ClientID: ccID},
		{TokenURL: "::not a url", ClientID: ccID, Secret: ccSecret},
	} {
		if _, err := auth.NewClientCredentials(cfg); errCode(err) != "config_required" || strings.Contains(err.Error(), ccSecret) || strings.Contains(err.Error(), ccID) {
			t.Fatal(err)
		}
	}
}

func TestSecretNeverInErrorsOrLogs(t *testing.T) {
	var mu sync.Mutex
	var logs []string
	logf := func(event string, fields map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, fmt.Sprint(event, fields))
	}
	var issued []string
	var mode atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch mode.Load() {
		case 0:
			mu.Lock()
			tok := fmt.Sprintf("issued-token-%d", len(issued))
			issued = append(issued, tok)
			mu.Unlock()
			_, _ = fmt.Fprintf(w, `{"access_token":%q,"token_type":"bearer","expires_in":900}`, tok)
		case 1: // echoes the secret, also as a plausible error code
			w.WriteHeader(401)
			_, _ = fmt.Fprintf(w, `{"error":%q,"error_description":"bad secret %s for %s"}`, ccSecret, ccSecret, ccID)
		case 2:
			w.WriteHeader(400)
			_, _ = fmt.Fprintf(w, `{"error":"invalid_client","error_description":"bad secret %s for %s"}`, ccSecret, ccID)
		default:
			w.WriteHeader(503)
			_, _ = fmt.Fprintf(w, `upstream said %s`, ccSecret)
		}
	}))
	defer srv.Close()
	h := newCC(t, auth.ClientCredentialsConfig{TokenURL: srv.URL, Log: logf})
	var errs []error
	tok := mustToken(t, h)
	errs = append(errs, ccReject(context.Background(), h, http.StatusUnauthorized, tok))
	tok = mustToken(t, h)
	errs = append(errs, ccReject(context.Background(), h, http.StatusForbidden, tok), ccReject(context.Background(), h, http.StatusUnauthorized, tok))
	for _, m := range []int32{1, 2, 3} {
		mode.Store(m)
		_, err := ccToken(h)
		errs = append(errs, err)
	}
	var texts []string
	for _, err := range errs {
		if err != nil {
			texts = append(texts, err.Error(), fmt.Sprintf("%+v", err))
			var oe *output.Error
			if errors.As(err, &oe) {
				texts = append(texts, oe.NextAction, fmt.Sprint(oe.Details))
			}
		}
	}
	// A mint logs after it hands over its result, so a log can still arrive:
	// read logs only under the lock.
	mu.Lock()
	texts = append(texts, logs...)
	logged := len(logs)
	secrets := append([]string{ccSecret, ccID}, issued...)
	mu.Unlock()
	if logged == 0 || len(secrets) != 4 {
		t.Fatal("nothing logged or wrong mint count", logged, len(secrets))
	}
	for _, text := range texts {
		for _, secret := range secrets {
			if strings.Contains(text, secret) {
				t.Fatalf("%q leaks %q", text, secret)
			}
		}
	}
	if !slices.ContainsFunc(texts, func(s string) bool { return strings.Contains(s, "invalid_client") }) {
		t.Fatal("OAuth error code not reported", texts)
	}
}
