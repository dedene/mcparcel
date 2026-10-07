package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

const (
	ccClientID = "cc-client-canary"
	ccSecret   = "cc-secret-canary"
	ccPrefix   = "CC-TOKEN-CANARY-"
)

// ccServer is a client_credentials authorization server in front of the
// fixture MCP server. reject answers every MCP request with 401 and forbid
// with 403, whatever token it carries.
type ccServer struct {
	as             *testutil.AuthServer
	reject, forbid atomic.Bool
	// tokenDown makes the token endpoint answer 503.
	tokenDown atomic.Bool

	mu            sync.Mutex
	events        []string
	rejectedCalls []string // "<HTTP method> <JSON-RPC method>" of each 401
}

func (s *ccServer) rejected() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.rejectedCalls...)
}

func (s *ccServer) grants() int { return s.as.GrantCounts().ClientCredentials }

func (s *ccServer) logged() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...)
}

// ccRig is a pool rig with client_credentials connection a whose client ID
// and secret come from env: references. headless picks the mode; in desktop
// mode the keyring and the Keychain fail the test when used.
func ccRig(t *testing.T, headless bool) (*poolRig, *ccServer) {
	t.Helper()
	var r *poolRig
	if headless {
		r = headlessRig(t)
	} else {
		r = newRig(t)
		r.opts.Keyring = spyKeyring{t}
		r.opts.Keychain = func(context.Context, string) (string, error) {
			t.Error("keychain consulted for a variable that is set")
			return "", errKeychain
		}
	}
	s := &ccServer{as: testutil.NewAuthServer(t, testutil.AuthServerOptions{ClientCredentials: true, ClientID: ccClientID, ClientSecret: ccSecret, CCExpiresIn: 900, TokenPrefix: ccPrefix})}
	fixture := testutil.NewFixtureServer()
	protected := s.as.Protect(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return fixture }, nil), "/mcp")
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case s.forbid.Load():
			w.WriteHeader(http.StatusForbidden)
		case s.reject.Load():
			var msg struct{ Method string }
			_ = json.NewDecoder(req.Body).Decode(&msg)
			s.mu.Lock()
			s.rejectedCalls = append(s.rejectedCalls, req.Method+" "+msg.Method)
			s.mu.Unlock()
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			w.WriteHeader(http.StatusUnauthorized)
		default:
			protected.ServeHTTP(w, req)
		}
	}))
	target, _ := url.Parse(s.as.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if s.tokenDown.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		proxy.ServeHTTP(w, req)
	}))
	t.Cleanup(func() {
		if r.h != nil {
			_ = r.h.Shutdown(testCtx(t), true)
		}
		hs.Close()
		ts.Close()
	})
	r.opts.LoginEnv["CC_ID"] = ccClientID
	r.opts.LoginEnv["CC_SECRET"] = ccSecret
	r.opts.TokenLog = func(event string, fields map[string]any) {
		b, _ := json.Marshal(fields)
		s.mu.Lock()
		s.events = append(s.events, event+" "+string(b))
		s.mu.Unlock()
	}
	r.personal.Connections["a"] = config.Connection{
		Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal(hs.URL + "/mcp"), AllowInsecureHTTP: "loopback"}},
		Auth: &config.OAuth{
			Type: "oauth", Grant: config.GrantClientCredentials, TokenURL: ts.URL + "/token",
			ClientID:     &config.Value{Secret: &config.SecretRef{Secret: "env:CC_ID"}},
			ClientSecret: &config.Value{Secret: &config.SecretRef{Secret: "env:CC_SECRET"}},
		},
	}
	return r, s
}

// callBefore is a call whose dispatch hook runs before right before the
// tools/call request is sent.
func callBefore(r *poolRig, before func()) Response {
	return r.h.Handle(testCtx(r.t), testID, Request{Method: "call", Connection: "a", Tool: "counter", Arguments: emptyArgs()}, func() error {
		before()
		return nil
	})
}

// noCCLeak fails when a response carries the client ID, the secret or a token.
func noCCLeak(t *testing.T, texts ...string) {
	t.Helper()
	for _, text := range texts {
		for _, secret := range []string{ccClientID, ccSecret, ccPrefix} {
			if strings.Contains(text, secret) {
				t.Fatalf("%q leaks %q", text, secret)
			}
		}
	}
}

func responseText(r Response) string {
	b, _ := json.Marshal(r)
	return string(b)
}

func TestCCCallMintsOnce(t *testing.T) {
	r, s := ccRig(t, true)
	r.start()
	for want := 1; want <= 2; want++ {
		res := r.call(testCtx(t), "a", "counter")
		if n := count(t, res); n != want {
			t.Fatal(n, want)
		}
		noCCLeak(t, responseText(res))
	}
	if s.grants() != 1 || r.connects.Load() != 1 {
		t.Fatal("grants", s.grants(), "connects", r.connects.Load())
	}
	logged := s.logged()
	if len(logged) != 1 || logged[0] != `oauth_token_minted {"trigger":"first","ttl":900}` {
		t.Fatal(logged)
	}
}

func TestCCConcurrentCallsShareToken(t *testing.T) {
	r, s := ccRig(t, true)
	r.start()
	a, b := asyncCall(r, testCtx(t), "a", "counter"), asyncCall(r, testCtx(t), "a", "counter")
	if n := count(t, response(t, a)) + count(t, response(t, b)); n != 3 {
		t.Fatal("counts", n)
	}
	if s.grants() != 1 {
		t.Fatal("grants", s.grants())
	}
}

// A 401 answer to tools/call is the resource server's authentication answer,
// given before the tool runs (D7): one new token, one resend, and the result
// is the resend's answer.
func TestCC401ResendsToolCallOnce(t *testing.T) {
	r, s := ccRig(t, true)
	r.start()
	res := callBefore(r, s.as.Revoke)
	if n := count(t, res); n != 1 {
		t.Fatal("tool invocations", n)
	}
	if !res.Dispatched || res.Error != nil {
		t.Fatalf("%+v", res)
	}
	var d struct {
		Result json.RawMessage `json:"result"`
	}
	if e := json.Unmarshal(res.Data, &d); e != nil || !strings.Contains(string(d.Result), `"count":1`) {
		t.Fatal(string(res.Data), e)
	}
	if c := s.as.GrantCounts(); c.ClientCredentials != 2 || c.Unauthorized != 1 {
		t.Fatalf("%+v", c)
	}
	logged := s.logged()
	if len(logged) != 2 || logged[1] != `oauth_token_minted {"trigger":"401","ttl":900}` {
		t.Fatal(logged)
	}
	noCCLeak(t, responseText(res))
}

func TestCCSecond401IsAuthFailed(t *testing.T) {
	t.Run("tools/call", func(t *testing.T) {
		r, s := ccRig(t, true)
		r.start()
		res := callBefore(r, func() { s.reject.Store(true) })
		responseCode(t, res, "auth_failed", true)
		if res.Error.Details == nil || res.Error.Details.Outcome != "" || !strings.Contains(res.Error.Message, "token_rejected") {
			t.Fatalf("%+v %+v", res.Error, res.Error.Details)
		}
		// One resend; the DELETE that closes the retired session follows.
		if got := s.rejected(); s.grants() != 2 || !slices.Equal(got, []string{"POST tools/call", "POST tools/call", "DELETE "}) {
			t.Fatal("grants", s.grants(), "401s", got)
		}
		s.reject.Store(false)
		if n := count(t, r.call(testCtx(t), "a", "counter")); n != 1 {
			t.Fatal("the rejected call ran", n-1, "times")
		}
		noCCLeak(t, responseText(res))
	})
	t.Run("connect", func(t *testing.T) {
		r, s := ccRig(t, true)
		s.reject.Store(true)
		r.start()
		res := r.call(testCtx(t), "a", "counter")
		responseCode(t, res, "auth_failed", false)
		// The discover probe is resent once with a new token; the initialize
		// fallback's 401 for that same token is token_rejected, not resent.
		if got := s.rejected(); s.grants() != 2 || len(got) != 3 || got[0] != got[1] || got[2] != "POST initialize" {
			t.Fatal("grants", s.grants(), "401s", got)
		}
		// No failure outlives the call.
		s.reject.Store(false)
		if n := count(t, r.call(testCtx(t), "a", "counter")); n != 1 {
			t.Fatal(n)
		}
		noCCLeak(t, responseText(res))
	})
}

func TestCC403NoRemint(t *testing.T) {
	r, s := ccRig(t, true)
	r.start()
	res := callBefore(r, func() { s.forbid.Store(true) })
	responseCode(t, res, "auth_failed", true)
	if s.grants() != 1 {
		t.Fatal("grants", s.grants())
	}
	s.forbid.Store(false)
	if n := count(t, r.call(testCtx(t), "a", "counter")); n != 1 {
		t.Fatal("the forbidden call ran", n-1, "times")
	}
}

// Neither mode touches the keyring or the Keychain for a client_credentials
// connection, and nothing about its token reaches the health file.
func TestCCNoKeychainNoHealthFile(t *testing.T) {
	for _, headless := range []bool{true, false} {
		t.Run(fmt.Sprint("headless=", headless), func(t *testing.T) {
			r, s := ccRig(t, headless)
			withHealth(r)
			r.start()
			count(t, callBefore(r, s.as.Revoke))
			if _, e := os.Stat(filepath.Join(r.paths.StateDir, "oauth-health.json")); !os.IsNotExist(e) {
				t.Fatal("health file written", e)
			}
		})
	}
}

func TestCCKeepAliveSkips(t *testing.T) {
	r, _ := ccRig(t, false)
	withHealth(r)
	r.start()
	targets, e := r.h.(*pool).keepAliveTargets(testCtx(t))
	if e != nil || len(targets) != 0 {
		t.Fatal(targets, e)
	}
}

func TestCCConfigChangeDropsToken(t *testing.T) {
	r, s := ccRig(t, true)
	r.opts.LoginEnv["CC_SECRET_NEW"] = ccSecret
	r.start()
	count(t, r.call(testCtx(t), "a", "counter"))
	c := r.personal.Connections["a"]
	c.Auth.ClientSecret = &config.Value{Secret: &config.SecretRef{Secret: "env:CC_SECRET_NEW"}}
	r.personal.Connections["a"] = c
	r.save()
	count(t, r.call(testCtx(t), "a", "counter"))
	if s.grants() != 2 || r.connects.Load() != 2 {
		t.Fatal("grants", s.grants(), "connects", r.connects.Load())
	}
}

func TestCCWrongSecretIsAuthFailed(t *testing.T) {
	r, s := ccRig(t, true)
	r.opts.LoginEnv["CC_SECRET"] = "wrong-canary"
	r.start()
	res := r.call(testCtx(t), "a", "counter")
	responseCode(t, res, "auth_failed", false)
	if !strings.Contains(res.Error.Message, "invalid_client") {
		t.Fatalf("%+v", res.Error)
	}
	noCCLeak(t, responseText(res))
	if strings.Contains(responseText(res), "wrong-canary") {
		t.Fatal(responseText(res))
	}
	// No failure is cached: the connect's discover probe and its initialize
	// fallback each ask the token endpoint.
	logged := s.logged()
	if len(logged) == 0 || len(logged) > 2 {
		t.Fatal(logged)
	}
	for _, line := range logged {
		if line != `oauth_token_mint_failed {"code":"invalid_client","status":401}` {
			t.Fatal(logged)
		}
	}
}

func TestCCLoginRefused(t *testing.T) {
	r, s := ccRig(t, false)
	r.start()
	res := r.login(testCtx(t), &loginBrowser{}, false)
	responseCode(t, res, "invalid_arguments", false)
	if res.Error.Message != "a uses client credentials; there is nothing to sign in to." || r.connects.Load() != 0 || s.grants() != 0 {
		t.Fatalf("%+v", res.Error)
	}
}
