package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/mcpclient"
	"github.com/dedene/mcparcel/internal/output"
	"github.com/dedene/mcparcel/internal/testutil"
)

// oauthRig serves the fixture MCP server at /mcp behind a fake authorization
// server. marked adds auth with the preconfigured client "pre-id".
func oauthRig(t *testing.T, o testutil.AuthServerOptions, marked bool) (*poolRig, *testutil.AuthServer, *testutil.MemKeyring) {
	t.Helper()
	r := newRig(t)
	as := testutil.NewAuthServer(t, o)
	server := testutil.NewFixtureServer()
	hs := httptest.NewServer(as.Protect(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil), "/mcp"))
	t.Cleanup(func() {
		if r.h != nil {
			_ = r.h.Shutdown(testCtx(t), true)
		}
		hs.Close()
	})
	c := config.Connection{Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal(hs.URL + "/mcp"), AllowInsecureHTTP: "loopback"}}}
	if marked {
		id := config.Literal("pre-id")
		c.Auth = &config.OAuth{Type: "oauth", ClientID: &id}
	}
	r.personal.Connections["a"] = c
	kr := &testutil.MemKeyring{}
	r.opts.Keyring = kr
	return r, as, kr
}

// loginBrowser follows each authorization URL like a user agent; with
// visit false it only records them.
type loginBrowser struct {
	mu    sync.Mutex
	urls  []string
	visit bool
	wg    sync.WaitGroup
}

func (b *loginBrowser) send(u string) error {
	b.mu.Lock()
	b.urls = append(b.urls, u)
	b.mu.Unlock()
	if b.visit {
		b.wg.Go(func() {
			if resp, e := http.Get(u); e == nil {
				_ = resp.Body.Close()
			}
		})
	}
	return nil
}

func (b *loginBrowser) count() int { b.mu.Lock(); defer b.mu.Unlock(); return len(b.urls) }

func (r *poolRig) login(ctx context.Context, b *loginBrowser, noInput bool) Response {
	if b != nil {
		ctx = withAuthURLSender(ctx, b.send)
	}
	return r.h.Handle(ctx, testID, Request{Method: "login", Connection: "a", Arguments: emptyArgs(), NoInput: noInput}, nil)
}

func (r *poolRig) logout(ctx context.Context) Response {
	return r.h.Handle(ctx, testID, Request{Method: "logout", Connection: "local:a", Arguments: emptyArgs()}, nil)
}

func signInAction(t *testing.T, res Response) {
	t.Helper()
	responseCode(t, res, "auth_required", false)
	if res.Error.NextAction != "mcparcel auth login a" {
		t.Fatal(res.Error.NextAction)
	}
}

func storeState(t *testing.T, kr auth.Keyring, s auth.OAuthState) {
	t.Helper()
	if e := auth.SaveOAuth(testCtx(t), kr, "local:a", s); e != nil {
		t.Fatal(e)
	}
}

func TestPoolOAuthNotSignedInFailsFast(t *testing.T) {
	r, _, _ := oauthRig(t, testutil.AuthServerOptions{ClientID: "pre-id"}, true)
	r.opts.Connect = func(context.Context, mcpclient.ConnectOptions) (mcpclient.Session, error) {
		r.connects.Add(1)
		return nil, output.NewError("connection_failed", nil)
	}
	r.start()
	res := r.call(testCtx(t), "a", "counter")
	signInAction(t, res)
	if res.Error.Message != "Sign-in required for a." || r.connects.Load() != 0 {
		t.Fatal(res.Error.Message, r.connects.Load())
	}
}

func TestPoolOAuthKeyringMissingIsUnavailable(t *testing.T) {
	r, _, _ := oauthRig(t, testutil.AuthServerOptions{ClientID: "pre-id"}, true)
	r.opts.Keyring = nil
	r.start()
	responseCode(t, r.call(testCtx(t), "a", "counter"), "keychain_unavailable", false)
	if r.connects.Load() != 0 {
		t.Fatal("connected")
	}
}

func TestPoolOAuthRecordedFailureFailsFast(t *testing.T) {
	r, as, kr := oauthRig(t, testutil.AuthServerOptions{ClientID: "pre-id"}, true)
	u := *r.personal.Connections["a"].Transport.HTTP.URL.Literal
	storeState(t, kr, auth.OAuthState{Version: 1, URL: u, Issuer: as.URL, Resource: u, TokenURL: as.URL + "/token", RefreshToken: as.Grant(), Failure: &auth.OAuthFailure{At: 1, Code: "invalid_grant"}})
	r.start()
	signInAction(t, r.call(testCtx(t), "a", "counter"))
	if _, _, n := as.Counts(); n != 0 || r.connects.Load() != 0 {
		t.Fatal("network before auth_required", n, r.connects.Load())
	}
}

func TestPoolLoginSendsAuthURLAndPoolsSession(t *testing.T) {
	for name, tc := range map[string]struct {
		o      testutil.AuthServerOptions
		marked bool
	}{
		"preconfigured": {testutil.AuthServerOptions{ClientID: "pre-id", AccessTTL: 33 * time.Second}, true},
		"unmarked dcr":  {testutil.AuthServerOptions{Registration: true, AccessTTL: 33 * time.Second}, false},
	} {
		t.Run(name, func(t *testing.T) {
			r, as, kr := oauthRig(t, tc.o, tc.marked)
			r.start()
			b := &loginBrowser{visit: true}
			res := r.login(testCtx(t), b, false)
			success(t, res)
			var d LoginData
			if e := json.Unmarshal(res.Data, &d); e != nil || d.Connection != "local:a" || !d.SignedIn {
				t.Fatal(string(res.Data), e)
			}
			if b.count() != 1 {
				t.Fatal("auth urls", b.count())
			}
			b.wg.Wait()
			count(t, r.call(testCtx(t), "a", "counter"))
			if _, _, n := as.Counts(); n != 0 || r.connects.Load() != 1 {
				t.Fatal("refreshes", n, "connects", r.connects.Load())
			}
			if _, e := auth.LoadOAuth(testCtx(t), kr, "local:a"); e != nil {
				t.Fatal("no stored session", e)
			}
			// A restarted daemon uses the stored session with one refresh.
			if e := r.h.Shutdown(testCtx(t), true); e != nil {
				t.Fatal(e)
			}
			r.h = NewPool(r.opts)
			count(t, r.call(testCtx(t), "a", "counter"))
			if _, _, n := as.Counts(); n != 1 {
				t.Fatal("refreshes after restart", n)
			}
		})
	}
}

func TestPoolLoginTimeoutIsAuthRequired(t *testing.T) {
	r, _, kr := oauthRig(t, testutil.AuthServerOptions{ClientID: "pre-id"}, true)
	r.start()
	ctx, cancel := context.WithTimeout(testCtx(t), 500*time.Millisecond)
	defer cancel()
	b := &loginBrowser{}
	res := r.login(ctx, b, false)
	signInAction(t, res)
	if res.Error.Message != "Sign-in was not completed in time." || b.count() != 1 {
		t.Fatal(res.Error.Message, b.count())
	}
	if _, e := auth.LoadOAuth(testCtx(t), kr, "local:a"); e != auth.ErrNoSession {
		t.Fatal("stored", e)
	}
}

func TestPoolLoginHeaderKeyRejected(t *testing.T) {
	r := newRig(t)
	r.opts.Keyring = &testutil.MemKeyring{}
	r.personal.Connections["a"] = config.Connection{Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal("https://fixture.invalid/mcp"), Headers: map[string]config.Value{"X-Api-Key": {Secret: &config.SecretRef{Secret: "env:FIXTURE_TOKEN"}}}}}}
	r.start()
	res := r.login(testCtx(t), &loginBrowser{}, false)
	responseCode(t, res, "invalid_arguments", false)
	if r.connects.Load() != 0 {
		t.Fatal("connected")
	}
}

func TestPoolLoginNoInput(t *testing.T) {
	r, _, _ := oauthRig(t, testutil.AuthServerOptions{Registration: true}, false)
	r.start()
	b := &loginBrowser{}
	signInAction(t, r.login(testCtx(t), b, true))
	if r.connects.Load() != 0 || b.count() != 0 {
		t.Fatal("effects")
	}
}

func TestPoolLogoutRetiresEntryAndDeletes(t *testing.T) {
	// Access tokens expire inside the refresh skew, so any Token() at close
	// would refresh unless the handler is closed first.
	r, as, kr := oauthRig(t, testutil.AuthServerOptions{ClientID: "pre-id", AccessTTL: 20 * time.Second}, true)
	r.start()
	b := &loginBrowser{visit: true}
	success(t, r.login(testCtx(t), b, false))
	b.wg.Wait()
	_, _, before := as.Counts()
	res := r.logout(testCtx(t))
	success(t, res)
	var d LogoutData
	if e := json.Unmarshal(res.Data, &d); e != nil || d.Connection != "local:a" || !d.Removed || d.ProviderRevoked {
		t.Fatal(string(res.Data), e)
	}
	if r.closed.Load() != 1 {
		t.Fatal("entry not retired")
	}
	if _, e := auth.LoadOAuth(testCtx(t), kr, "local:a"); e != auth.ErrNoSession {
		t.Fatal("item kept", e)
	}
	signInAction(t, r.call(testCtx(t), "a", "counter"))
	if _, _, after := as.Counts(); after != before || r.connects.Load() != 1 {
		t.Fatal("refresh after logout", before, after, r.connects.Load())
	}
	res = r.logout(testCtx(t))
	success(t, res)
	if e := json.Unmarshal(res.Data, &d); e != nil || d.Removed {
		t.Fatal("second logout", string(res.Data))
	}
}

func TestPoolUnmarked401RewritesNextAction(t *testing.T) {
	r, _, _ := oauthRig(t, testutil.AuthServerOptions{Registration: true}, false)
	r.start()
	start := time.Now()
	res := r.call(testCtx(t), "a", "counter")
	signInAction(t, res)
	if res.Error.Message != "Sign-in required for a." || time.Since(start) > 2*time.Second {
		t.Fatal(res.Error.Message, time.Since(start))
	}
	if o := <-r.captured; o.OAuth != nil {
		t.Fatal("handler without a stored session")
	}
}

// A login that saw no 401 must not leave its login-mode session pooled: a
// later 401 would otherwise start a sign-in from an ordinary call.
func TestPoolLoginWithoutChallengeNotReused(t *testing.T) {
	r := newRig(t)
	as := testutil.NewAuthServer(t, testutil.AuthServerOptions{Registration: true})
	server := testutil.NewFixtureServer()
	open := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	protected := as.Protect(open, "/mcp")
	var opened atomic.Bool
	opened.Store(true)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if opened.Load() {
			open.ServeHTTP(w, req)
			return
		}
		protected.ServeHTTP(w, req)
	}))
	t.Cleanup(func() {
		if r.h != nil {
			_ = r.h.Shutdown(testCtx(t), true)
		}
		hs.Close()
	})
	r.personal.Connections["a"] = config.Connection{Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal(hs.URL + "/mcp"), AllowInsecureHTTP: "loopback"}}}
	r.opts.Keyring = &testutil.MemKeyring{}
	r.start()
	b := &loginBrowser{}
	res := r.login(testCtx(t), b, false)
	success(t, res)
	var d LoginData
	if e := json.Unmarshal(res.Data, &d); e != nil || d.SignedIn {
		t.Fatal(string(res.Data), e)
	}
	opened.Store(false)
	ctx, cancel := context.WithTimeout(testCtx(t), 5*time.Second)
	defer cancel()
	signInAction(t, r.call(ctx, "a", "counter"))
	if reg, _, _ := as.Counts(); b.count() != 0 || reg != 0 {
		t.Fatal("sign-in started from a call", b.count(), reg)
	}
}

func TestOAuthLogEventsWritten(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	w, e := OpenLog(p)
	if e != nil {
		t.Fatal(e)
	}
	events := []string{"oauth_signed_in", "oauth_refreshed", "oauth_refresh_failed", "oauth_signed_out"}
	for _, event := range events {
		if e = WriteLog(w, event, nil); e != nil {
			t.Fatal(event, e)
		}
	}
	_ = w.Close()
	b, e := os.ReadFile(p.LogFile)
	for _, event := range events {
		if e != nil || !strings.Contains(string(b), `{"event":"`+event+`"}`) {
			t.Fatal(event, string(b), e)
		}
	}
}

func TestPoolHeaderKey401KeepsEnvAction(t *testing.T) {
	r := newRig(t)
	r.opts.Keyring = &testutil.MemKeyring{}
	r.opts.LoginEnv["FIXTURE_TOKEN"] = "stale-canary"
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	t.Cleanup(hs.Close)
	r.personal.Connections["a"] = config.Connection{Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal(hs.URL), AllowInsecureHTTP: "loopback", Headers: map[string]config.Value{"X-Api-Key": {Secret: &config.SecretRef{Secret: "env:FIXTURE_TOKEN"}}}}}}
	r.start()
	start := time.Now()
	res := r.call(testCtx(t), "a", "counter")
	responseCode(t, res, "auth_required", false)
	if !strings.Contains(res.Error.NextAction, "FIXTURE_TOKEN") || strings.Contains(res.Error.NextAction, "auth login") || time.Since(start) > 2*time.Second {
		t.Fatal(res.Error.NextAction, time.Since(start))
	}
	if o := <-r.captured; o.OAuth != nil {
		t.Fatal("handler on a header-key connection")
	}
}
