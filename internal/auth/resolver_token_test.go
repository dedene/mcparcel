package auth_test

import (
	"errors"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

var tokenProfile = config.Profile{Mode: config.ProfileModeServiceAccount, TokenFile: "/run/secrets/op-token", SessionDuration: "1h"}

func TestNoInputBootstrapsServiceAccount(t *testing.T) {
	clock := testutil.NewClock()
	vault, r := vaultResolver(t, clock, map[string]string{"a": "va", "b": "vb"})
	l, e := r.Resolve(testContext(t), "sa", tokenProfile, []string{"a"}, true)
	if e != nil || l.Secrets()["a"] != "va" || vault.Boots() != 1 {
		t.Fatal("service-account --no-input did not bootstrap", e)
	}
	// Unlike desktop mode, an uncached value is read through the session.
	if l, e := r.Resolve(testContext(t), "sa", tokenProfile, []string{"b"}, true); e != nil || l.Secrets()["b"] != "vb" || vault.Boots() != 1 {
		t.Fatal("service-account --no-input read", e)
	}
	// An ended session bootstraps again under --no-input.
	clock.Advance(time.Hour)
	if _, e := r.Resolve(testContext(t), "sa", tokenProfile, []string{"a"}, true); e != nil || vault.Boots() != 2 {
		t.Fatal("expired service-account session", e, vault.Boots())
	}
	t.Run("desktop modes", func(t *testing.T) {
		vault, r := vaultResolver(t, testutil.NewClock(), map[string]string{"a": "v"})
		for _, p := range []config.Profile{
			profile,
			{Mode: config.ProfileModeDesktop, Account: "fixture"},
		} {
			if _, e := r.Resolve(testContext(t), p.Mode, p, []string{"a"}, true); !errors.Is(e, auth.ErrRequired) {
				t.Fatal(p.Mode, e)
			}
		}
		if vault.Boots() != 0 {
			t.Fatal("desktop --no-input reached the provider")
		}
	})
}

func TestTokenErrorsAreNotCached(t *testing.T) {
	for _, sentinel := range []error{auth.ErrTokenUnavailable, auth.ErrTokenUnsafe} {
		t.Run(sentinel.Error(), func(t *testing.T) {
			vault, r := vaultResolver(t, testutil.NewClock(), map[string]string{"a": "v"})
			vault.FailBootstrap(sentinel)
			for call := 1; call <= 2; call++ {
				if _, e := r.Resolve(testContext(t), "sa", tokenProfile, []string{"a"}, true); e != sentinel {
					t.Fatal("sentinel collapsed", e)
				}
				if vault.Boots() != call {
					t.Fatal("token failure was cached", vault.Boots())
				}
			}
			vault.FailBootstrap(nil)
			if l, e := r.Resolve(testContext(t), "sa", tokenProfile, []string{"a"}, true); e != nil || l.Secrets()["a"] != "v" || vault.Boots() != 3 {
				t.Fatal("fixed token not read again", e)
			}
		})
	}
}

// auth lock ends a service-account session but does not bar it: the next
// --no-input call bootstraps again from the token.
func TestLockEndsServiceAccountSession(t *testing.T) {
	vault, r := vaultResolver(t, testutil.NewClock(), map[string]string{"a": "v"})
	first, e := r.Resolve(testContext(t), "sa", tokenProfile, []string{"a"}, true)
	if e != nil {
		t.Fatal(e)
	}
	r.Lock()
	if len(r.Sessions()) != 0 {
		t.Fatal("lock kept the service-account session")
	}
	next, e := r.Resolve(testContext(t), "sa", tokenProfile, []string{"a"}, true)
	if e != nil || next.Secrets()["a"] != "v" {
		t.Fatal("--no-input after lock", e)
	}
	if next.Identity == first.Identity || vault.Boots() != 2 {
		t.Fatal("lock did not end the session", vault.Boots())
	}
}
