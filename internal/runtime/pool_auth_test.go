package runtime

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
	"github.com/dedene/mcparcel/internal/testutil"
)

// orderResolver records the order of Resolve and Invalidate calls.
type orderResolver struct {
	inner auth.Resolver
	mu    sync.Mutex
	log   []string
}

func (o *orderResolver) record(call string) { o.mu.Lock(); o.log = append(o.log, call); o.mu.Unlock() }
func (o *orderResolver) calls() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.log)
}

func (o *orderResolver) Resolve(ctx context.Context, id string, p config.Profile, refs []string, noInput bool) (auth.Lease, error) {
	o.record("resolve")
	return o.inner.Resolve(ctx, id, p, refs, noInput)
}

func (o *orderResolver) Invalidate(id string, refs []string) {
	o.record("invalidate")
	o.inner.Invalidate(id, refs)
}
func (o *orderResolver) Lock()                        { o.inner.Lock() }
func (o *orderResolver) Sessions() []auth.SessionInfo { return o.inner.Sessions() }
func (o *orderResolver) Close() error                 { return o.inner.Close() }

// A service-account profile is read again without a prompt, also under
// --no-input and in headless mode: the values are dropped, then read.
func TestServiceAccountAuthRereads(t *testing.T) {
	for _, headless := range []bool{false, true} {
		r, vault := saRig(t, headless)
		spy := &orderResolver{inner: r.opts.Credentials}
		r.opts.Credentials = spy
		r.start()
		d := authData(t, r.h.Handle(testCtx(t), testID, authReq("a", true), nil))
		if d.SecretsRefreshed == nil || vault.Boots() != 1 || vault.Reads(secretRef("a")) != 1 || r.connects.Load() != 0 {
			t.Fatal(headless, d, vault.Boots(), r.connects.Load())
		}
		if got := spy.calls(); !slices.Equal(got, []string{"invalidate", "resolve"}) {
			t.Fatal(headless, got)
		}
	}
}

// Headless mode refuses a sign-in before any effect, also when the
// connection's client secret is a service-account 1Password reference.
func TestHeadlessAuthSignInRefused(t *testing.T) {
	for name, withRef := range map[string]bool{"oauth": false, "oauth with op:// secret": true} {
		t.Run(name, func(t *testing.T) {
			r, as, _ := oauthRig(t, testutil.AuthServerOptions{ClientID: "pre-id"}, true)
			r.opts.Headless = true
			r.opts.Keyring = spyKeyring{t}
			spy := &orderResolver{inner: &spyResolver{}}
			r.opts.Credentials = spy
			if withRef {
				c := r.personal.Connections["a"]
				c.CredentialProfile = "shared"
				c.Auth.ClientSecret = &config.Value{Secret: &config.SecretRef{Secret: "op://vault/a/secret"}}
				r.personal.Connections["a"] = c
				r.personal.CredentialProfiles["shared"] = config.ProfileRequirement{}
				r.local.CredentialProfiles["shared"] = config.Profile{Mode: config.ProfileModeServiceAccount, TokenEnv: "OP_SERVICE_ACCOUNT_TOKEN"}
			}
			r.start()
			res := r.h.Handle(testCtx(t), testID, authReq("a", false), nil)
			responseCode(t, res, "auth_required", false)
			if res.Error.Message != "This server needs sign-in, which headless mode cannot do." || r.connects.Load() != 0 || as.Requests("") != 0 || len(spy.calls()) != 0 {
				t.Fatal(res.Error, r.connects.Load(), spy.calls())
			}
		})
	}
}

// Desktop mode without the 1Password app refuses a desktop-app profile with
// config_required, also under --no-input, before dropping anything.
func TestNoDesktopAppAuthNoInput(t *testing.T) {
	r, vault := saRig(t, false)
	r.opts.NoDesktopApp = true
	r.local.CredentialProfiles["shared"] = config.Profile{Mode: config.ProfileModeDesktop, Account: "a"}
	spy := &orderResolver{inner: r.opts.Credentials}
	r.opts.Credentials = spy
	r.start()
	res := r.h.Handle(testCtx(t), testID, authReq("a", true), nil)
	responseCode(t, res, "config_required", false)
	if want := output.DesktopAppUnavailableError("shared"); res.Error.Message != want.Message {
		t.Fatal(res.Error)
	}
	if len(spy.calls()) != 0 || vault.Boots() != 0 {
		t.Fatal(spy.calls(), vault.Boots())
	}
}

// OAuth with a 1Password client ID: the value is read again before the
// authorization URL is shown.
func TestAuthBothOrderOAuth(t *testing.T) {
	r, _, _ := oauthRig(t, testutil.AuthServerOptions{ClientID: "pre-id"}, true)
	vault := &testutil.FakeVault{}
	vault.Set(secretRef("a"), "pre-id")
	r.opts.Credentials = auth.NewResolver(auth.ResolverOptions{Provider: vault})
	c := r.personal.Connections["a"]
	c.CredentialProfile = "shared"
	c.Auth.ClientID = &config.Value{Secret: &config.SecretRef{Secret: secretRef("a")}}
	r.personal.Connections["a"] = c
	r.personal.CredentialProfiles["shared"] = config.ProfileRequirement{}
	r.local.CredentialProfiles["shared"] = config.Profile{Mode: config.ProfileModeDesktopServiceAccount, Account: "fixture", BootstrapRef: "op://vault/bootstrap/token", SessionDuration: "24h"}
	r.start()
	signInAction(t, r.call(testCtx(t), "a", "counter"))
	if vault.Reads(secretRef("a")) != 1 {
		t.Fatal("reads", vault.Reads(secretRef("a")))
	}
	b := &loginBrowser{visit: true}
	readsAtURL := -1
	ctx := withAuthURLSender(testCtx(t), func(u string) error {
		readsAtURL = vault.Reads(secretRef("a"))
		return b.send(u)
	})
	res := r.h.Handle(ctx, testID, authReq("a", false), nil)
	b.wg.Wait()
	if string(res.Data) != `{"connection":"local:a","secretsRefreshed":true,"signedIn":true}` {
		t.Fatalf("%s %+v", res.Data, res.Error)
	}
	if readsAtURL != 2 {
		t.Fatal("not re-read before the sign-in", readsAtURL)
	}
}

// A config change while auth waits for the gate that changes its steps is
// config_changed, not a half-done renewal.
func TestAuthConfigChangedBetweenPlanAndGate(t *testing.T) {
	r, s := ccRig(t, false)
	// The config may change only after the request planned: Active counts
	// it before its first load.
	var armed atomic.Bool
	planned := make(chan struct{})
	load := r.opts.Load
	r.opts.Load = func(paths config.Paths) (config.Snapshot, error) {
		snapshot, err := load(paths)
		if armed.CompareAndSwap(true, false) {
			close(planned)
		}
		return snapshot, err
	}
	r.start()
	gate := r.h.(*pool).gate("local:a")
	<-gate
	armed.Store(true)
	ch := make(chan Response, 1)
	go func() { ch <- r.h.Handle(testCtx(t), testID, authReq("a", false), nil) }()
	select {
	case <-planned:
	case <-testCtx(t).Done():
		t.Fatal("auth did not load")
	}
	awaitActive(t, r.h, 1)
	c := r.personal.Connections["a"]
	c.Auth = nil
	r.personal.Connections["a"] = c
	r.save()
	gate <- struct{}{}
	responseCode(t, response(t, ch), "config_changed", false)
	if s.grants() != 0 || r.connects.Load() != 0 {
		t.Fatal(s.grants(), r.connects.Load())
	}
}

// A connection named like an auth subcommand is pointed at by its canonical
// ID, which mcparcel auth reaches.
func TestNotSignedInShadowedName(t *testing.T) {
	r, _, _ := oauthRig(t, testutil.AuthServerOptions{ClientID: "pre-id"}, true)
	r.personal.Connections["status"] = r.personal.Connections["a"]
	r.start()
	res := r.call(testCtx(t), "status", "counter")
	responseCode(t, res, "auth_required", false)
	if res.Error.NextAction != "mcparcel auth local:status" || res.Error.Message != "Sign-in required for status." {
		t.Fatal(res.Error)
	}
}

// --no-input refuses a sign-in before any effect, also when the connection's
// client secret is a 1Password reference: nothing is dropped or read again
// by a request that can only fail.
func TestAuthBothNoInputRefusedFirst(t *testing.T) {
	r, as, _ := oauthRig(t, testutil.AuthServerOptions{ClientID: "pre-id"}, true)
	r.opts.Keyring = spyKeyring{t}
	spy := &orderResolver{inner: &spyResolver{}}
	r.opts.Credentials = spy
	c := r.personal.Connections["a"]
	c.CredentialProfile = "shared"
	c.Auth.ClientSecret = &config.Value{Secret: &config.SecretRef{Secret: "op://vault/a/secret"}}
	r.personal.Connections["a"] = c
	r.personal.CredentialProfiles["shared"] = config.ProfileRequirement{}
	r.local.CredentialProfiles["shared"] = config.Profile{Mode: config.ProfileModeServiceAccount, TokenEnv: "OP_SERVICE_ACCOUNT_TOKEN"}
	r.start()
	res := r.h.Handle(testCtx(t), testID, authReq("a", true), nil)
	responseCode(t, res, "auth_required", false)
	if res.Error.Message != "Signing in to a opens a browser, which --no-input does not allow." || r.connects.Load() != 0 || as.Requests("") != 0 || len(spy.calls()) != 0 {
		t.Fatal(res.Error, r.connects.Load(), spy.calls())
	}
}
