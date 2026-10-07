package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

// spyKeyring fails the test on any use: headless mode has no Keychain.
type spyKeyring struct{ t *testing.T }

func (k spyKeyring) Get(string, string) (string, error) {
	k.t.Error("keyring read in headless mode")
	return "", auth.ErrNoSession
}

func (k spyKeyring) Set(string, string, string) error {
	k.t.Error("keyring written in headless mode")
	return nil
}

func (k spyKeyring) Delete(string, string) error {
	k.t.Error("keyring deleted in headless mode")
	return nil
}

// headlessRig is a pool rig in headless mode whose Keychain lookup, keyring
// and 1Password provider fail the test when used.
func headlessRig(t *testing.T) *poolRig {
	t.Helper()
	r := newRig(t)
	r.opts.Headless = true
	r.opts.Keychain = func(context.Context, string) (string, error) {
		t.Error("keychain consulted in headless mode")
		return "keychain-canary", nil
	}
	r.opts.Keyring = spyKeyring{t}
	r.opts.Credentials = auth.NewResolver(auth.ResolverOptions{Provider: testutil.FakeProvider{BootstrapFunc: func(context.Context, config.Profile) (auth.SecretClient, error) {
		t.Error("1Password provider used in headless mode")
		return nil, auth.ErrProvider
	}}})
	r.local.Runtime = &config.RuntimeDefaults{Mode: config.ModeHeadless, StateRoot: "/var/lib/mcparcel"}
	return r
}

func headerRef(name string) map[string]config.Value {
	return map[string]config.Value{"Authorization": {Secret: &config.SecretRef{Secret: "env:" + name, Prefix: "Bearer "}}}
}

func TestHeadlessNoKeychainFallback(t *testing.T) {
	r := headlessRig(t)
	delete(r.opts.LoginEnv, "FRONT_CLIENT_SECRET")
	r.http("front", testutil.FixtureOptions{}, false)
	r.personal.Connections["front"].Transport.HTTP.Headers = headerRef("FRONT_CLIENT_SECRET")
	r.start()
	res := r.call(testCtx(t), "front", "counter")
	responseCode(t, res, "config_required", false)
	if res.Error.Message != "Environment variable FRONT_CLIENT_SECRET is not set." || res.Error.NextAction != "Set FRONT_CLIENT_SECRET in the environment that starts mcparcel (headless mode reads no Keychain), then run mcparcel runtime restart." || res.Error.Details == nil || !slices.Equal(res.Error.Details.Variables, []string{"FRONT_CLIENT_SECRET"}) {
		t.Fatalf("%+v %+v", res.Error, res.Error.Details)
	}
	if r.connects.Load() != 0 {
		t.Fatal("connected without the variable")
	}
	b, _ := json.Marshal(res.Error)
	if strings.Contains(string(b), "canary") {
		t.Fatal(string(b))
	}
}

// A missing variable is not remembered: once the daemon restarts with it in
// its environment, the call succeeds.
func TestHeadlessMissingVarNotCached(t *testing.T) {
	r := headlessRig(t)
	r.http("front", testutil.FixtureOptions{}, false)
	r.personal.Connections["front"].Transport.HTTP.Headers = headerRef("FRONT_CLIENT_SECRET")
	r.start()
	responseCode(t, r.call(testCtx(t), "front", "counter"), "config_required", false)
	responseCode(t, r.call(testCtx(t), "front", "counter"), "config_required", false)
	if e := r.h.Shutdown(testCtx(t), false); e != nil {
		t.Fatal(e)
	}
	// runtime restart: a new daemon reads its own environment again.
	snap, e := config.Load(r.paths)
	if e != nil {
		t.Fatal(e)
	}
	environ := append(os.Environ(), "FRONT_CLIENT_SECRET=secret-canary", "AWS_SECRET_ACCESS_KEY=aws-canary")
	r.opts.LoginEnv = HeadlessEnv(environ, ForwardedNames(snap))
	if _, ok := r.opts.LoginEnv["AWS_SECRET_ACCESS_KEY"]; ok {
		t.Fatal("unreferenced variable reached the daemon environment")
	}
	r.h = NewPool(r.opts)
	if n := count(t, r.call(testCtx(t), "front", "counter")); n != 1 {
		t.Fatal(n)
	}
	o := <-r.captured
	if o.Headers["Authorization"] != "Bearer secret-canary" {
		t.Fatal("header not built from the restarted environment")
	}
}

func TestHeadlessOnePasswordRefused(t *testing.T) {
	for _, mode := range []string{config.ModeHeadless, config.ModeDesktop} {
		t.Run(mode, func(t *testing.T) {
			r := headlessRig(t)
			// The desktop case is a daemon started headless whose config.json
			// changed since: the pool still never resolves a 1Password ref.
			if mode == config.ModeDesktop {
				r.local.Runtime = nil
			}
			r.http("front", testutil.FixtureOptions{}, true)
			r.start()
			res := r.call(testCtx(t), "front", "counter")
			responseCode(t, res, "config_required", false)
			if res.Error.Message != "1Password references need the desktop app and are not available in headless mode." {
				t.Fatal(res.Error.Message)
			}
			if r.connects.Load() != 0 {
				t.Fatal("connected")
			}
		})
	}
}

// An unmarked HTTP connection answered with 401 cannot sign in headless; no
// URL-only Keychain item is written for auth status.
func TestHeadlessUnmarked401CannotSignIn(t *testing.T) {
	r, _, _ := oauthRig(t, testutil.AuthServerOptions{Registration: true}, false)
	r.opts.Headless = true
	r.opts.Keyring = spyKeyring{t}
	r.start()
	res := r.call(testCtx(t), "a", "counter")
	responseCode(t, res, "auth_required", false)
	if res.Error.Message != "This server needs sign-in, which headless mode cannot do." || strings.Contains(res.Error.NextAction, "auth login") {
		t.Fatalf("%+v", res.Error)
	}
	if o := <-r.captured; o.OAuth != nil {
		t.Fatal("OAuth handler in headless mode")
	}
}

// A marked (authorization code) connection fails the same way, without a
// keyring lookup, and logout finds nothing to remove.
func TestHeadlessMarkedNeedsSignIn(t *testing.T) {
	r, _, _ := oauthRig(t, testutil.AuthServerOptions{ClientID: "pre-id"}, true)
	r.opts.Headless = true
	r.opts.Keyring = spyKeyring{t}
	r.start()
	res := r.call(testCtx(t), "a", "counter")
	responseCode(t, res, "auth_required", false)
	if res.Error.Message != "This server needs sign-in, which headless mode cannot do." || strings.Contains(res.Error.NextAction, "auth login") || r.connects.Load() != 0 {
		t.Fatalf("%+v %d", res.Error, r.connects.Load())
	}
	res = r.logout(testCtx(t))
	var d LogoutData
	if res.Error != nil || json.Unmarshal(res.Data, &d) != nil || d.Removed {
		t.Fatalf("%+v %s", res.Error, res.Data)
	}
}

// Headless mode stores no OAuth session, so keep-alive never runs and never
// keeps the daemon up.
func TestHeadlessKeepAliveIdle(t *testing.T) {
	r := headlessRig(t)
	r.local.Runtime.KeepAlive = true
	r.opts.Health = auth.NewHealth(r.paths.StateDir, time.Now)
	r.http("front", testutil.FixtureOptions{}, false)
	r.start()
	k := r.h.(interface {
		RunKeepAlive(context.Context) error
		StayAlive() bool
	})
	ctx, cancel := context.WithTimeout(testCtx(t), 2*time.Second)
	defer cancel()
	if err := k.RunKeepAlive(ctx); err != nil || ctx.Err() != nil || k.StayAlive() {
		t.Fatal("keep-alive ran in headless mode", err, ctx.Err())
	}
}

func TestHeadlessHeaderKey401NamesVariables(t *testing.T) {
	r := headlessRig(t)
	r.opts.LoginEnv["FRONT_TOKEN"] = "stale-canary"
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	t.Cleanup(hs.Close)
	r.personal.Connections["a"] = config.Connection{Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal(hs.URL), AllowInsecureHTTP: "loopback", Headers: map[string]config.Value{"X-Api-Key": {Secret: &config.SecretRef{Secret: "env:FRONT_TOKEN"}}}}}}
	r.start()
	res := r.call(testCtx(t), "a", "counter")
	responseCode(t, res, "auth_required", false)
	if res.Error.Message != "The server rejected the credentials from environment variable FRONT_TOKEN." || res.Error.NextAction != "Check FRONT_TOKEN in the environment that starts mcparcel, update the value, then run mcparcel runtime restart." {
		t.Fatalf("%+v", res.Error)
	}
	b, _ := json.Marshal(res.Error)
	if strings.Contains(string(b), "canary") {
		t.Fatal(string(b))
	}
}
