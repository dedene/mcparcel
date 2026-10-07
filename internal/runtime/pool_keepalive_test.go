package runtime

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

// keepAliveRig is an OAuth pool rig on a fake clock shared by the pool, the
// health log and the fake authorization server.
type keepAliveRig struct {
	*poolRig
	as    *testutil.AuthServer
	kr    *testutil.MemKeyring
	clock *testutil.Clock
}

func newKeepAliveRig(t *testing.T, o testutil.AuthServerOptions, marked bool) *keepAliveRig {
	t.Helper()
	clock := testutil.NewClock()
	o.Now = clock.Now
	r, as, kr := oauthRig(t, o, marked)
	r.opts.Now = clock.Now
	r.opts.Health = auth.NewHealth(r.paths.StateDir, clock.Now)
	return &keepAliveRig{poolRig: r, as: as, kr: kr, clock: clock}
}

func (k *keepAliveRig) pool() *pool { return k.h.(*pool) }

func (k *keepAliveRig) sweep(t *testing.T, trigger string) {
	t.Helper()
	k.pool().keep.Sweep(testCtx(t), trigger)
}

func (k *keepAliveRig) refreshes() int { _, _, n := k.as.Counts(); return n }

func (k *keepAliveRig) signIn(t *testing.T) {
	t.Helper()
	b := &loginBrowser{visit: true}
	success(t, k.login(testCtx(t), b, false))
	b.wg.Wait()
}

// storeGrant stores a session for the preconfigured client, as a daemon
// that signed in earlier would have left it.
func (k *keepAliveRig) storeGrant(t *testing.T) {
	t.Helper()
	u := *k.personal.Connections["a"].Transport.HTTP.URL.Literal
	storeState(t, k.kr, auth.OAuthState{Version: 1, URL: u, Issuer: k.as.URL, Resource: u, TokenURL: k.as.URL + "/token", RefreshToken: k.as.Grant()})
}

// restart replaces the pool, as a daemon restart does; the Keychain and the
// health file stay.
func (k *keepAliveRig) restart(t *testing.T) {
	t.Helper()
	if e := k.h.Shutdown(testCtx(t), true); e != nil {
		t.Fatal(e)
	}
	k.h = NewPool(k.opts)
}

func (k *keepAliveRig) events(t *testing.T) []auth.HealthEvent { return healthEvents(t, k.poolRig) }

func noTerminal(t *testing.T, events []auth.HealthEvent) {
	t.Helper()
	for _, e := range events {
		if e.Terminal {
			t.Fatalf("terminal event: %+v", e)
		}
	}
}

func TestKeepAliveUsesPooledHandler(t *testing.T) {
	k := newKeepAliveRig(t, testutil.AuthServerOptions{ClientID: "pre-id", RotateRefresh: true}, true)
	k.start()
	k.signIn(t)
	count(t, k.call(testCtx(t), "a", "counter"))
	before := k.refreshes()
	k.clock.Advance(25 * time.Hour)
	k.sweep(t, auth.TriggerKeepAlive)
	if k.refreshes() != before+1 || k.connects.Load() != 1 {
		t.Fatal("refreshes", k.refreshes()-before, "connects", k.connects.Load())
	}
	// The pooled session uses the access token the keep-alive minted.
	count(t, k.call(testCtx(t), "a", "counter"))
	if k.refreshes() != before+1 {
		t.Fatal("refreshed again", k.refreshes()-before)
	}
	// Its next refresh presents the rotated refresh token.
	k.clock.Advance(2 * time.Hour)
	count(t, k.call(testCtx(t), "a", "counter"))
	if k.refreshes() != before+2 || k.connects.Load() != 1 {
		t.Fatal("refreshes", k.refreshes()-before, "connects", k.connects.Load())
	}
	events := k.events(t)
	noTerminal(t, events)
	var keepAlive int
	for _, e := range events {
		if e.Kind == auth.HealthRefreshed && e.Trigger == auth.TriggerKeepAlive && e.Rotated {
			keepAlive++
		}
	}
	if keepAlive != 1 {
		t.Fatalf("%+v", events)
	}
	if s, e := auth.LoadOAuth(testCtx(t), k.kr, "local:a"); e != nil || s.RefreshToken != k.as.RefreshToken() {
		t.Fatal("rotated refresh token not stored", e)
	}
}

func TestKeepAliveNeverRefreshesLoggedOut(t *testing.T) {
	k := newKeepAliveRig(t, testutil.AuthServerOptions{ClientID: "pre-id", RotateRefresh: true}, true)
	k.local.Runtime = &config.RuntimeDefaults{KeepAlive: true}
	k.start()
	k.signIn(t)
	k.sweep(t, auth.TriggerStart)
	if !k.pool().StayAlive() {
		t.Fatal("no stay-alive while signed in")
	}
	k.clock.Advance(time.Minute)
	success(t, k.logout(testCtx(t)))
	if k.pool().StayAlive() {
		t.Fatal("stay-alive after logout")
	}
	before, n := k.refreshes(), len(k.events(t))
	for range 3 {
		k.clock.Advance(25 * time.Hour)
		k.sweep(t, auth.TriggerKeepAlive)
	}
	if k.refreshes() != before || len(k.events(t)) != n {
		t.Fatal("logged-out connection refreshed", k.refreshes()-before, k.events(t))
	}
	if _, e := auth.LoadOAuth(testCtx(t), k.kr, "local:a"); e != auth.ErrNoSession {
		t.Fatal("item came back", e)
	}
}

// A client secret held in 1Password is kept alive only through a pooled
// session: resolving it in the background could prompt.
func TestKeepAliveSkipsSecretRefClient(t *testing.T) {
	k := newKeepAliveRig(t, testutil.AuthServerOptions{ClientID: "pre-id", ClientSecret: "pre-secret"}, true)
	c := k.personal.Connections["a"]
	c.Auth.ClientSecret = &config.Value{Secret: &config.SecretRef{Secret: "op://vault/a/secret"}}
	c.CredentialProfile = "shared"
	k.personal.Connections["a"] = c
	k.personal.CredentialProfiles["shared"] = config.ProfileRequirement{}
	k.local.CredentialProfiles["shared"] = config.Profile{Mode: "desktop-service-account", Account: "fixture", BootstrapRef: "op://vault/bootstrap/token", SessionDuration: "24h"}
	var resolves atomic.Int32
	k.opts.Credentials = auth.NewResolver(auth.ResolverOptions{Now: k.clock.Now, Provider: testutil.FakeProvider{BootstrapFunc: func(context.Context, config.Profile) (auth.SecretClient, error) {
		resolves.Add(1)
		return testutil.FakeSecretClient{ResolveFunc: func(context.Context, string) (string, error) { return "pre-secret", nil }}, nil
	}}})
	k.storeGrant(t)
	k.start()
	k.sweep(t, auth.TriggerStart)
	if resolves.Load() != 0 || k.refreshes() != 0 {
		t.Fatal("resolved", resolves.Load(), "refreshes", k.refreshes())
	}
	if k.pool().keep.HasSessions() {
		t.Fatal("unreachable session counted")
	}
}

func TestKeepAliveEnvClientFromLoginEnvOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		env       bool
		refreshes int
	}{"exported": {true, 1}, "missing": {false, 0}} {
		t.Run(name, func(t *testing.T) {
			k := newKeepAliveRig(t, testutil.AuthServerOptions{ClientID: "pre-id", ClientSecret: "pre-secret"}, true)
			c := k.personal.Connections["a"]
			c.Auth.ClientSecret = &config.Value{Secret: &config.SecretRef{Secret: "env:FIXTURE_CLIENT_SECRET"}}
			k.personal.Connections["a"] = c
			if tc.env {
				k.opts.LoginEnv["FIXTURE_CLIENT_SECRET"] = "pre-secret"
			}
			var lookups atomic.Int32
			k.opts.Keychain = func(context.Context, string) (string, error) {
				lookups.Add(1)
				return "pre-secret", nil
			}
			k.storeGrant(t)
			k.start()
			k.sweep(t, auth.TriggerStart)
			if lookups.Load() != 0 || k.refreshes() != tc.refreshes {
				t.Fatal("keychain lookups", lookups.Load(), "refreshes", k.refreshes())
			}
		})
	}
}

func TestKeepAliveNoSessionGoesDormant(t *testing.T) {
	k := newKeepAliveRig(t, testutil.AuthServerOptions{ClientID: "pre-id"}, true)
	k.local.Runtime = &config.RuntimeDefaults{KeepAlive: true}
	k.start()
	if !k.pool().StayAlive() {
		t.Fatal("stay-alive before the first sweep")
	}
	k.sweep(t, auth.TriggerStart)
	gets := k.kr.Gets()
	if gets != 1 || k.pool().StayAlive() {
		t.Fatal("gets", gets, "stay-alive", k.pool().StayAlive())
	}
	for range 3 {
		k.clock.Advance(25 * time.Hour)
		k.sweep(t, auth.TriggerKeepAlive)
	}
	if k.kr.Gets() != gets {
		t.Fatal("dormant connection read the Keychain again", k.kr.Gets()-gets)
	}
	// Signing in wakes it.
	k.signIn(t)
	if !k.pool().StayAlive() {
		t.Fatal("no stay-alive after sign-in")
	}
}

func TestKeepAliveOff(t *testing.T) {
	k := newKeepAliveRig(t, testutil.AuthServerOptions{ClientID: "pre-id"}, true)
	c := k.personal.Connections["a"]
	c.Lifecycle = &config.Lifecycle{KeepAlive: "off"}
	k.personal.Connections["a"] = c
	k.storeGrant(t)
	k.local.Runtime = &config.RuntimeDefaults{KeepAlive: true}
	k.start()
	gets := k.kr.Gets()
	k.sweep(t, auth.TriggerStart)
	if k.refreshes() != 0 || k.kr.Gets() != gets {
		t.Fatal("refreshed a connection with keep-alive off")
	}
	if k.pool().StayAlive() {
		t.Fatal("stay-alive for a connection with keep-alive off")
	}
	// Calls still work and refresh as usual.
	count(t, k.call(testCtx(t), "a", "counter"))
}

func TestKeepAliveShutdownWaitsForRefresh(t *testing.T) {
	k := newKeepAliveRig(t, testutil.AuthServerOptions{ClientID: "pre-id", RotateRefresh: true}, true)
	k.storeGrant(t)
	k.start()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	k.kr.BeforeSet = func() { once.Do(func() { close(entered); <-release }) }
	swept := make(chan struct{})
	go func() { defer close(swept); k.sweep(t, auth.TriggerStart) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh never saved")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- k.h.Shutdown(context.Background(), false) }()
	select {
	case e := <-stopped:
		t.Fatal("shutdown returned during the save", e)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	select {
	case e := <-stopped:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown hung")
	}
	<-swept
	if s, e := auth.LoadOAuth(testCtx(t), k.kr, "local:a"); e != nil || s.RefreshToken != k.as.RefreshToken() {
		t.Fatal("rotated refresh token lost", e)
	}
}

// The stage gate: refresh tokens rotate and expire after 14 idle days, access
// tokens last an hour, and nothing calls the connection for 20 days. Daily
// keep-alive refreshes, across a daemon restart, need no new sign-in.
func TestSimulatedIdleWeekNoReLogin(t *testing.T) {
	k := newKeepAliveRig(t, testutil.AuthServerOptions{ClientID: "pre-id", RotateRefresh: true, RefreshIdleTTL: 14 * 24 * time.Hour, AccessTTL: time.Hour}, true)
	k.start()
	k.signIn(t)
	for hour := 1; hour <= 20*24; hour++ {
		k.clock.Advance(time.Hour)
		if hour == 10*24 {
			// A restarted daemon refreshes from the Keychain without a pooled session.
			k.restart(t)
			k.sweep(t, auth.TriggerStart)
			continue
		}
		if hour%6 == 0 {
			k.sweep(t, auth.TriggerKeepAlive)
		}
	}
	count(t, k.call(testCtx(t), "a", "counter"))
	events := k.events(t)
	noTerminal(t, events)
	refreshed := 0
	for _, e := range events {
		if e.Kind == auth.HealthRefreshed && (e.Trigger == auth.TriggerKeepAlive || e.Trigger == auth.TriggerStart) {
			refreshed++
		}
	}
	if refreshed < 19 || refreshed > 21 {
		t.Fatalf("%d keep-alive refreshes: %+v", refreshed, events)
	}
}

// Without sweeps (the daemon was not running) the 14-day idle window passes,
// and auth status names the cause.
func TestDaemonOffPastIdleWindowExplained(t *testing.T) {
	k := newKeepAliveRig(t, testutil.AuthServerOptions{ClientID: "pre-id", RotateRefresh: true, RefreshIdleTTL: 14 * 24 * time.Hour, AccessTTL: time.Hour}, true)
	k.start()
	k.signIn(t)
	if e := k.h.Shutdown(testCtx(t), true); e != nil {
		t.Fatal(e)
	}
	k.clock.Advance(15 * 24 * time.Hour)
	k.h = NewPool(k.opts)
	k.sweep(t, auth.TriggerStart)
	signInAction(t, k.call(testCtx(t), "a", "counter"))
	events := k.events(t)
	last := events[len(events)-1]
	if last.Kind != auth.HealthRefreshFailed || last.Code != "invalid_grant" || !last.Terminal || last.Trigger != auth.TriggerStart || last.Idle != 15*24*3600 {
		t.Fatalf("%+v", events)
	}
	state, e := auth.LoadOAuth(testCtx(t), k.kr, "local:a")
	u := *k.personal.Connections["a"].Transport.HTTP.URL.Literal
	m, _ := auth.ReadHealth(k.paths.StateDir)
	report := auth.ExplainSession(auth.SessionInput{Connection: "local:a", Name: "a", URL: u, State: state, Found: e == nil, Health: m["local:a"], KeepAlive: "24h", Now: k.clock.Now()})
	if report.State != auth.StateSignInRequired || report.Cause == nil || report.Cause.Code != "refresh_expired_or_revoked" || !strings.Contains(report.Cause.Message, "for 15 days") {
		t.Fatalf("%+v %+v", report, report.Cause)
	}
}

// Stay-alive keeps the daemon, not credential sessions: a 1Password-backed
// session still ends when its lease does, and the keep-alive never renews it.
func TestKeepAliveDoesNotExtendCredentialSession(t *testing.T) {
	k := newKeepAliveRig(t, testutil.AuthServerOptions{ClientID: "pre-id", ClientSecret: "pre-secret"}, true)
	c := k.personal.Connections["a"]
	c.Auth.ClientSecret = &config.Value{Secret: &config.SecretRef{Secret: "op://vault/a/secret"}}
	c.CredentialProfile = "shared"
	c.Lifecycle = &config.Lifecycle{KeepAlive: "12h"}
	k.personal.Connections["a"] = c
	k.personal.CredentialProfiles["shared"] = config.ProfileRequirement{}
	k.local.CredentialProfiles["shared"] = config.Profile{Mode: "desktop-service-account", Account: "fixture", BootstrapRef: "op://vault/bootstrap/token", SessionDuration: "24h"}
	k.local.Runtime = &config.RuntimeDefaults{KeepAlive: true}
	var resolves atomic.Int32
	k.opts.Credentials = auth.NewResolver(auth.ResolverOptions{Now: k.clock.Now, Provider: testutil.FakeProvider{BootstrapFunc: func(context.Context, config.Profile) (auth.SecretClient, error) {
		resolves.Add(1)
		return testutil.FakeSecretClient{ResolveFunc: func(context.Context, string) (string, error) { return "pre-secret", nil }}, nil
	}}})
	k.opts.ExpiryCheck = 10 * time.Millisecond
	k.storeGrant(t)
	k.start()
	count(t, k.call(testCtx(t), "a", "counter"))
	resolved, before := resolves.Load(), k.refreshes()
	// While the lease lasts, the pooled session is refreshed in place.
	k.clock.Advance(13 * time.Hour)
	k.sweep(t, auth.TriggerKeepAlive)
	if k.refreshes() != before+1 || resolves.Load() != resolved {
		t.Fatal("refreshes", k.refreshes()-before, "resolves", resolves.Load()-resolved)
	}
	k.clock.Advance(11 * time.Hour)
	deadline := time.Now().Add(5 * time.Second)
	for k.closed.Load() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("expired session not closed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	k.clock.Advance(25 * time.Hour)
	k.sweep(t, auth.TriggerKeepAlive)
	if k.refreshes() != before+1 || resolves.Load() != resolved || k.connects.Load() != 1 {
		t.Fatal("keep-alive renewed the credential session", k.refreshes()-before, resolves.Load()-resolved, k.connects.Load())
	}
}

// A temporary handler whose save of a rotated refresh token failed retries
// the save before it closes, so a background refresh never drops the only
// copy of the new token.
func TestKeepAliveTempHandlerRetriesFailedSave(t *testing.T) {
	old := keepAliveSaveRetries
	keepAliveSaveRetries = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { keepAliveSaveRetries = old })
	k := newKeepAliveRig(t, testutil.AuthServerOptions{ClientID: "pre-id", RotateRefresh: true}, true)
	k.storeGrant(t)
	k.start()
	k.kr.FailSets = 2 // the refresh's save and the first retry
	k.sweep(t, auth.TriggerStart)
	if s, e := auth.LoadOAuth(testCtx(t), k.kr, "local:a"); e != nil || s.RefreshToken != k.as.RefreshToken() {
		t.Fatal("rotated refresh token lost", e)
	}
	events := k.events(t)
	if last := events[len(events)-1]; last.Kind != auth.HealthRefreshed || !last.Rotated {
		t.Fatalf("%+v", events)
	}
	count(t, k.call(testCtx(t), "a", "counter"))
	noTerminal(t, k.events(t))
}

// Shutdown empties the pool without taking gates. A keep-alive refresh that
// already holds the gate must not then refresh from the Keychain while the
// pooled handler may still be saving a rotated token.
func TestKeepAliveStopsWhenShutdownEmptiesPool(t *testing.T) {
	k := newKeepAliveRig(t, testutil.AuthServerOptions{ClientID: "pre-id", RotateRefresh: true}, true)
	k.start()
	k.signIn(t)
	count(t, k.call(testCtx(t), "a", "counter"))
	p := k.pool()
	load := p.opts.Load
	var loads atomic.Int32
	stopped := make(chan error, 1)
	p.opts.Load = func(paths config.Paths) (config.Snapshot, error) {
		if loads.Add(1) == 2 { // refreshStored, holding the gate
			go func() { stopped <- k.h.Shutdown(context.Background(), true) }()
			deadline := time.Now().Add(5 * time.Second)
			for {
				p.mu.Lock()
				emptied := p.closed && len(p.entries) == 0
				p.mu.Unlock()
				if emptied {
					break
				}
				if time.Now().After(deadline) {
					t.Error("shutdown never emptied the pool")
					break
				}
				time.Sleep(time.Millisecond)
			}
		}
		return load(paths)
	}
	before := k.refreshes()
	k.clock.Advance(25 * time.Hour)
	k.sweep(t, auth.TriggerKeepAlive)
	if e := <-stopped; e != nil {
		t.Fatal(e)
	}
	if k.refreshes() != before {
		t.Fatal("refreshed from the Keychain during shutdown", k.refreshes()-before)
	}
}
