package runtime

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
	"github.com/dedene/mcparcel/internal/testutil"
)

// Desktop mode without the 1Password app (Linux) refuses both desktop-app
// profile modes before any provider call; a service-account profile in the
// same pool still resolves.
func TestNoDesktopAppGuard(t *testing.T) {
	for _, p := range []config.Profile{
		{Mode: config.ProfileModeDesktop, Account: "a"},
		{Mode: config.ProfileModeDesktopServiceAccount, Account: "a", BootstrapRef: "op://v/bootstrap/token"},
	} {
		r, vault := saRig(t, false)
		r.opts.NoDesktopApp = true
		r.local.CredentialProfiles["shared"] = p
		r.start()
		for _, res := range []Response{noInputCall(r, "a"), r.call(testCtx(t), "a", "counter")} {
			responseCode(t, res, "config_required", false)
			if want := output.DesktopAppUnavailableError("shared"); res.Error.Message != want.Message || res.Error.NextAction != want.NextAction {
				t.Fatal(res.Error)
			}
		}
		if vault.Boots() != 0 {
			t.Fatal(p.Mode, "provider called", vault.Boots())
		}
	}
	r, vault := saRig(t, false)
	r.opts.NoDesktopApp = true
	r.start()
	if count(t, noInputCall(r, "a")); vault.Boots() != 1 {
		t.Fatal("service-account not bootstrapped", vault.Boots())
	}
}

// The login preflight: a keyring that fails before the sign-in refuses it
// with keychain_unavailable before any URL is sent or the authorization
// server is asked. An empty keyring (ErrNoSession) signs in as usual
// (TestPoolLoginSendsAuthURLAndPoolsSession).
func TestPoolLoginKeyringPreflight(t *testing.T) {
	r, as, kr := oauthRig(t, testutil.AuthServerOptions{ClientID: "pre-id"}, true)
	kr.Err = errors.New("no session bus")
	r.start()
	b := &loginBrowser{visit: true}
	res := r.login(testCtx(t), b, false)
	responseCode(t, res, "keychain_unavailable", false)
	if b.count() != 0 || as.Requests("") != 0 || r.connects.Load() != 0 {
		t.Fatal("sign-in started", b.count(), as.Requests(""), r.connects.Load())
	}
}

// While the keyring waits on an unanswered prompt, keep-alive fetches no
// token it could not store; once the prompt ends it refreshes again.
func TestKeepAliveSkipsWhileKeyringWedged(t *testing.T) {
	k := newKeepAliveRig(t, testutil.AuthServerOptions{ClientID: "pre-id", RotateRefresh: true}, true)
	k.storeGrant(t)
	k.start()
	saved := keyringWedged
	t.Cleanup(func() { keyringWedged = saved })
	wedged := true
	keyringWedged = func(auth.Keyring) bool { return wedged }
	k.clock.Advance(25 * time.Hour)
	k.sweep(t, auth.TriggerKeepAlive)
	if n := k.refreshes(); n != 0 {
		t.Fatal("refreshed while wedged", n)
	}
	wedged = false
	k.sweep(t, auth.TriggerKeepAlive)
	if n := k.refreshes(); n != 1 {
		t.Fatal("refreshes after the prompt ended", n)
	}
}

// A desktop daemon inherits the caller's session bus address only when the
// bus check accepts it, and never a token variable.
func TestDaemonEnvironmentSessionBus(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	saved := busAddressOK
	t.Cleanup(func() { busAddressOK = saved })
	const good = "unix:path=/run/user/1000/bus"
	busAddressOK = func(v string) bool { return v == good }
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "sa-canary")
	t.Setenv("OP_E2E_TOKEN", "sa-canary")
	for value, forwarded := range map[string]bool{good: true, "tcp:host=x,port=1": false, "unix:abstract=x": false, "unix:path=/tmp/bus": false} {
		t.Setenv(busEnv, value)
		env := DaemonEnvironment(p)
		if got := slices.Contains(env, busEnv+"="+value); got != forwarded {
			t.Fatal(value, env)
		}
		for _, kv := range env {
			if strings.HasPrefix(kv, "OP_") || strings.Contains(kv, "sa-canary") {
				t.Fatal("token forwarded", kv)
			}
		}
	}
}

// An unsafe desktop runtime directory names MCPARCEL_RUNTIME_DIR on Linux,
// where the default under /tmp is shared; elsewhere, and in headless mode,
// the error stays as it is.
func TestUnsafeRuntimeDirAction(t *testing.T) {
	desktop, _ := testutil.IsolatedPaths(t)
	headless := desktop
	headless.StateRoot = "/var/lib/mcparcel"
	if err := unsafeRuntimeDir(headless, config.ErrUnsafePath); err != config.ErrUnsafePath {
		t.Fatal(err)
	}
	err := unsafeRuntimeDir(desktop, config.ErrUnsafePath)
	if runtimeDirAction == "" {
		if err != config.ErrUnsafePath {
			t.Fatal(err)
		}
		return
	}
	var e *output.Error
	if !errors.As(err, &e) || e.Code != "unsafe_local_path" || !strings.Contains(e.NextAction, "MCPARCEL_RUNTIME_DIR") || output.ExitCode(e) != 2 {
		t.Fatal(err)
	}
}
