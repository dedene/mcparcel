package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

const saToken = "sa-token-canary"

// tokenProvider bootstraps a service-account profile as the real provider
// does: it reads the token from env or the token file, and only saToken is
// accepted. The vault then serves the references.
type tokenProvider struct {
	vault *testutil.FakeVault
	env   map[string]string
}

func (p tokenProvider) Bootstrap(ctx context.Context, profile config.Profile) (auth.SecretClient, error) {
	token, err := auth.ServiceAccountToken(profile, p.env)
	if err != nil {
		return nil, err
	}
	if token != saToken {
		return nil, auth.ErrProvider
	}
	return p.vault.Bootstrap(ctx, profile)
}

// saRig is a pool rig whose "a" stdio connection reads a 1Password reference
// through the service-account profile "shared"; the token comes from
// OP_SERVICE_ACCOUNT_TOKEN in the daemon's environment unless the profile is
// changed.
func saRig(t *testing.T, headless bool) (*poolRig, *testutil.FakeVault) {
	t.Helper()
	var r *poolRig
	if headless {
		r = headlessRig(t)
	} else {
		r = newRig(t)
	}
	vault := &testutil.FakeVault{}
	r.opts.LoginEnv["OP_SERVICE_ACCOUNT_TOKEN"] = saToken
	r.opts.Credentials = auth.NewResolver(auth.ResolverOptions{Provider: tokenProvider{vault: vault, env: r.opts.LoginEnv}})
	r.stdio("a", true)
	r.local.CredentialProfiles["shared"] = config.Profile{Mode: config.ProfileModeServiceAccount, TokenEnv: "OP_SERVICE_ACCOUNT_TOKEN"}
	vault.Set(secretRef("a"), "value")
	return r, vault
}

func noToken(t *testing.T, v any) {
	t.Helper()
	if b, _ := json.Marshal(v); strings.Contains(string(b), saToken) {
		t.Fatal("token leaked:", string(b))
	}
}

// A headless pool resolves op:// references through a service-account
// profile, also under --no-input, and the child never sees the token.
func TestHeadlessServiceAccountResolves(t *testing.T) {
	r, vault := saRig(t, true)
	r.start()
	res := noInputCall(r, "a")
	if n := count(t, res); n != 1 || vault.Boots() != 1 {
		t.Fatal(n, vault.Boots())
	}
	o := <-r.captured
	if o.Env["SECRET"] != "value" {
		t.Fatal("secret not delivered")
	}
	noToken(t, o.Env)
	noToken(t, res)
}

// auth lock ends a service-account session but does not bar it: the next
// --no-input call bootstraps again from the token (A1).
func TestServiceAccountLockEndsSession(t *testing.T) {
	for _, headless := range []bool{false, true} {
		r, vault := saRig(t, headless)
		r.start()
		count(t, noInputCall(r, "a"))
		lockPool(t, r)
		if r.closed.Load() != 1 {
			t.Fatal("lock kept the process", r.closed.Load())
		}
		if n := count(t, noInputCall(r, "a")); n != 1 || vault.Boots() != 2 {
			t.Fatal("no new bootstrap after lock", headless, n, vault.Boots())
		}
	}
}

func TestServiceAccountLeaseErrors(t *testing.T) {
	dir, err := os.MkdirTemp(testutil.TempRoot(), "sa-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	unsafe := filepath.Join(dir, "unsafe")
	if err = os.WriteFile(unsafe, []byte(saToken+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing")
	for _, tc := range []struct {
		name      string
		headless  bool
		profile   config.Profile
		env       string
		code      string
		message   string
		next      string
		variables []string
	}{
		{
			"env missing headless", true,
			config.Profile{TokenEnv: "OP_OTHER"},
			saToken, "config_required",
			"Environment variable OP_OTHER, the 1Password service-account token of profile shared, is not set in the runtime's environment.",
			"Set OP_OTHER in the environment that starts mcparcel (Kubernetes: env.valueFrom.secretKeyRef; claw-wrap: map a credential to OP_OTHER in the tool's env:), then restart the runtime.",
			[]string{"OP_OTHER"},
		},
		{
			"env missing desktop", false,
			config.Profile{TokenEnv: "OP_OTHER"},
			saToken, "config_required",
			"Environment variable OP_OTHER, the 1Password service-account token of profile shared, is not set in the runtime's environment.",
			"Export OP_OTHER where your login shell reads it (bash: ~/.profile, zsh: ~/.zprofile) or switch the profile to tokenFile, then run mcparcel runtime restart.",
			[]string{"OP_OTHER"},
		},
		{
			"file missing", true,
			config.Profile{TokenFile: missing},
			saToken, "config_required",
			"The 1Password service-account token file " + missing + " of profile shared is missing, unreadable, empty or not a single token.", "", nil,
		},
		{
			"file unsafe", false,
			config.Profile{TokenFile: unsafe},
			saToken, "unsafe_local_path",
			"The token file " + unsafe + " of profile shared is unsafe: it must be a regular file owned by you or root, never readable by others, and readable or writable by its group only on a read-only mount.", "", nil,
		},
		{
			"rejected headless", true,
			config.Profile{TokenEnv: "OP_SERVICE_ACCOUNT_TOKEN"},
			"wrong-token", "auth_failed",
			"1Password did not accept the service-account token of profile shared (token from OP_SERVICE_ACCOUNT_TOKEN), could not be reached, or the service account cannot read these items.",
			"Check the token and the service account's vault access; after changing OP_SERVICE_ACCOUNT_TOKEN where mcparcel starts, restart the runtime. A rejected token is retried after a short backoff (up to 10 minutes) unless it changes.", nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := saRig(t, tc.headless)
			r.opts.LoginEnv["OP_SERVICE_ACCOUNT_TOKEN"] = tc.env
			tc.profile.Mode = config.ProfileModeServiceAccount
			r.local.CredentialProfiles["shared"] = tc.profile
			r.start()
			res := noInputCall(r, "a")
			responseCode(t, res, tc.code, false)
			var variables []string
			if res.Error.Details != nil {
				variables = res.Error.Details.Variables
			}
			if res.Error.Message != tc.message || tc.next != "" && res.Error.NextAction != tc.next || !slices.Equal(variables, tc.variables) {
				t.Fatalf("%+v %v", res.Error, variables)
			}
			if r.connects.Load() != 0 {
				t.Fatal("connected without a token")
			}
			noToken(t, res)
		})
	}
}

// failingResolver passes to inner until fail is set.
type failingResolver struct {
	auth.Resolver
	fail atomic.Pointer[error]
}

func (f *failingResolver) Resolve(ctx context.Context, id string, p config.Profile, refs []string, noInput bool) (auth.Lease, error) {
	if e := f.fail.Load(); e != nil {
		return auth.Lease{}, *e
	}
	return f.Resolver.Resolve(ctx, id, p, refs, noInput)
}

// A token that can no longer be read stops the pooled process, as a failed
// bootstrap does.
func TestServiceAccountTokenFailureRetires(t *testing.T) {
	for _, failure := range []error{auth.ErrTokenUnavailable, auth.ErrTokenUnsafe} {
		r, _ := saRig(t, true)
		f := &failingResolver{Resolver: r.opts.Credentials}
		r.opts.Credentials = f
		r.start()
		count(t, noInputCall(r, "a"))
		f.fail.Store(&failure)
		res := noInputCall(r, "a")
		if res.Error == nil || r.closed.Load() != 1 {
			t.Fatal(failure, res.Error, r.closed.Load())
		}
	}
}

// Headless refuses desktop-app profiles before the resolver runs, also
// under --no-input; the guard delegates everything else.
func TestHeadlessGuardRefusesDesktopProfiles(t *testing.T) {
	for _, mode := range []string{config.ProfileModeDesktop, config.ProfileModeDesktopServiceAccount} {
		r := headlessRig(t)
		r.stdio("a", true)
		if mode == config.ProfileModeDesktop {
			r.local.CredentialProfiles["shared"] = config.Profile{Mode: mode, Account: "fixture"}
		}
		r.local.Runtime = nil // a config.json changed since the daemon started headless
		r.start()
		res := noInputCall(r, "a")
		responseCode(t, res, "config_required", false)
		if res.Error.Message != "This connection's 1Password profile uses the desktop app, which headless mode does not use." {
			t.Fatal(res.Error.Message)
		}
	}
	inner := &spyResolver{}
	g := guardCredentials(inner, headlessProfiles)
	if _, err := g.Resolve(context.Background(), "p", config.Profile{Mode: config.ProfileModeServiceAccount}, nil, true); err != nil {
		t.Fatal("service-account profile refused:", err)
	}
	if _, err := g.Resolve(context.Background(), "p", config.Profile{Mode: config.ProfileModeDesktop}, nil, false); err == nil {
		t.Fatal("desktop profile served")
	}
	g.Invalidate("p", nil)
	g.Lock()
	_ = g.Sessions()
	_ = g.Close()
	if inner.calls != [5]int{1, 1, 1, 1, 1} {
		t.Fatal("guard did not delegate", inner.calls)
	}
}

// spyResolver counts Resolve, Invalidate, Lock, Sessions and Close.
type spyResolver struct{ calls [5]int }

func (s *spyResolver) Resolve(context.Context, string, config.Profile, []string, bool) (auth.Lease, error) {
	s.calls[0]++
	return auth.Lease{}, nil
}
func (s *spyResolver) Invalidate(string, []string)  { s.calls[1]++ }
func (s *spyResolver) Lock()                        { s.calls[2]++ }
func (s *spyResolver) Sessions() []auth.SessionInfo { s.calls[3]++; return nil }
func (s *spyResolver) Close() error                 { s.calls[4]++; return nil }

// A quarantined or closed resolver never sent the token, so its busy error is
// not reported as a rejected service-account token.
func TestServiceAccountBusyIsNotRejected(t *testing.T) {
	p := &pool{}
	profile := config.Profile{Mode: config.ProfileModeServiceAccount, TokenEnv: "OP_SERVICE_ACCOUNT_TOKEN"}
	if e := p.serviceAccountError("shared", profile, auth.ErrProviderBusy); e != nil {
		t.Fatal("busy resolver reported as rejected token:", e)
	}
	if e := p.serviceAccountError("shared", profile, auth.ErrProvider); e == nil {
		t.Fatal("rejected token not named")
	}
}
