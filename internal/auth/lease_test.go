package auth_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/testutil"
)

func vaultResolver(t *testing.T, clock *testutil.Clock, values map[string]string) (*testutil.FakeVault, auth.Resolver) {
	t.Helper()
	vault := &testutil.FakeVault{}
	for ref, value := range values {
		vault.Set(ref, value)
	}
	return vault, resolver(t, auth.ResolverOptions{Now: clock.Now, Provider: vault})
}

func TestLeaseHasNoDiagnosticForm(t *testing.T) {
	_, r := vaultResolver(t, testutil.NewClock(), map[string]string{"a": "VALUE-CANARY"})
	lease := resolve(t, r, []string{"a"}, false)
	if lease.Secrets()["a"] != "VALUE-CANARY" {
		t.Fatal("lease lost its value")
	}
	forms := []string{}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		forms = append(forms, fmt.Sprintf(verb, lease), fmt.Sprintf(verb, &lease))
	}
	data, err := json.Marshal(lease)
	if err != nil {
		t.Fatal(err)
	}
	forms = append(forms, string(data))
	for _, form := range forms {
		if strings.Contains(form, "VALUE-CANARY") {
			t.Fatalf("lease printed its value: %s", form)
		}
	}
}

func TestUnrelatedSecretChangeKeepsIdentity(t *testing.T) {
	clock := testutil.NewClock()
	vault, r := vaultResolver(t, clock, map[string]string{"a": "a1", "b": "b1"})
	first := resolve(t, r, []string{"a"}, false)
	resolve(t, r, []string{"b"}, false)
	vault.Set("b", "b2")
	clock.Advance(5 * time.Minute)
	if b := resolve(t, r, []string{"b"}, true); b.Secrets()["b"] != "b2" {
		t.Fatal("changed value not read")
	}
	if a := resolve(t, r, []string{"a"}, true); a.Identity != first.Identity {
		t.Fatal("an unrelated secret change rotated the identity")
	}
	both := resolve(t, r, []string{"a", "b"}, true)
	if both.Identity == first.Identity {
		t.Fatal("a requested secret change kept the identity")
	}
}

func TestTwentyFourHourSession(t *testing.T) {
	clock := testutil.NewClock()
	_, r := vaultResolver(t, clock, map[string]string{"a": "v"})
	a := resolve(t, r, []string{"a"}, false)
	if !a.SessionExpiresAt.Equal(clock.Now().Add(24 * time.Hour)) {
		t.Fatal("default session is not 24 hours")
	}
	clock.Advance(23*time.Hour + 59*time.Minute)
	b := resolve(t, r, []string{"a"}, true)
	if !b.SessionExpiresAt.Equal(a.SessionExpiresAt) {
		t.Fatal("a re-read extended the session")
	}
	if !b.ExpiresAt.Equal(a.SessionExpiresAt) {
		t.Fatal("lease outlives the session")
	}
	for duration, want := range map[string]time.Duration{"1h": time.Hour, "bogus": 24 * time.Hour, "25h": 24 * time.Hour, "": 24 * time.Hour} {
		t.Run(duration, func(t *testing.T) {
			clock := testutil.NewClock()
			_, r := vaultResolver(t, clock, map[string]string{"a": "v"})
			p := profile
			p.SessionDuration = duration
			l, e := r.Resolve(testContext(t), "p", p, []string{"a"}, false)
			if e != nil || !l.SessionExpiresAt.Equal(clock.Now().Add(want)) {
				t.Fatal("session duration", l.SessionExpiresAt, e)
			}
		})
	}
}

func TestSleepAndClockRollback(t *testing.T) {
	for _, d := range []time.Duration{25 * time.Hour, -time.Hour} {
		t.Run(d.String(), func(t *testing.T) {
			clock := testutil.NewClock()
			_, r := vaultResolver(t, clock, map[string]string{"a": "v"})
			resolve(t, r, []string{"a"}, false)
			clock.AdvanceWall(d)
			_, e := r.Resolve(testContext(t), "p", profile, []string{"a"}, true)
			if !errors.Is(e, auth.ErrExpired) {
				t.Fatal("session not expired", e)
			}
		})
	}
	// Regression: the 5-minute cache was checked on the monotonic clock only,
	// which stops while a Mac sleeps.
	t.Run("sleep re-reads cache", func(t *testing.T) {
		clock := testutil.NewClock()
		vault, r := vaultResolver(t, clock, map[string]string{"a": "v"})
		resolve(t, r, []string{"a"}, false)
		clock.AdvanceWall(10 * time.Minute)
		resolve(t, r, []string{"a"}, true)
		if vault.Reads("a") != 2 || vault.Boots() != 1 {
			t.Fatalf("reads/boots %d/%d", vault.Reads("a"), vault.Boots())
		}
	})
}

func TestCredentialErrorRedaction(t *testing.T) {
	cases := map[string]struct {
		boot, read error
		want       error
	}{
		"bootstrap":    {boot: errors.New("CANARY-TEXT secret https://private.invalid"), want: auth.ErrProvider},
		"resolve":      {read: errors.New("CANARY-TEXT secret https://private.invalid"), want: auth.ErrProvider},
		"rate limited": {read: fmt.Errorf("CANARY-TEXT https://private.invalid: %w", auth.ErrRateLimited), want: auth.ErrRateLimited},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			vault, r := vaultResolver(t, testutil.NewClock(), map[string]string{"a": "partial"})
			vault.FailBootstrap(c.boot)
			vault.Fail("a", c.read)
			l, e := r.Resolve(testContext(t), "p", profile, []string{"a"}, false)
			if e != c.want || l.Secrets() != nil {
				t.Fatal("unsafe error or leaked lease", e)
			}
			for _, s := range []string{"CANARY-TEXT", "secret", "https://", "partial"} {
				if strings.Contains(e.Error(), s) {
					t.Fatal("unsafe error")
				}
			}
		})
	}
	t.Run("lock in flight", func(t *testing.T) {
		vault, r := vaultResolver(t, testutil.NewClock(), nil)
		vault.Block = make(chan struct{})
		defer close(vault.Block)
		vault.FailBootstrap(errors.New("CANARY-TEXT"))
		out := make(chan answer, 1)
		go func() { l, e := r.Resolve(testContext(t), "p", profile, nil, false); out <- answer{l, e} }()
		waitBoots(t, vault, 1)
		r.Lock()
		if e := receive(t, out).err; e != auth.ErrLocked {
			t.Fatal("lock cause", e)
		}
	})
}

func TestRevokedServiceAccountEndsSession(t *testing.T) {
	clock := testutil.NewClock()
	vault, r := vaultResolver(t, clock, map[string]string{"a": "v"})
	resolve(t, r, []string{"a"}, false)
	vault.Fail("a", errors.New("service account revoked"))
	clock.Advance(5 * time.Minute)
	if _, e := r.Resolve(testContext(t), "p", profile, []string{"a"}, false); !errors.Is(e, auth.ErrProvider) {
		t.Fatal("revocation not reported", e)
	}
	if vault.Boots() != 1 || vault.Reads("a") != 2 {
		t.Fatal("retried inside the call")
	}
	if _, e := r.Resolve(testContext(t), "p", profile, []string{"a"}, true); !errors.Is(e, auth.ErrRequired) || vault.Boots() != 1 {
		t.Fatal("--no-input after revocation", e)
	}
	for call := 2; call <= 3; call++ {
		if _, e := r.Resolve(testContext(t), "p", profile, []string{"a"}, false); !errors.Is(e, auth.ErrProvider) {
			t.Fatal("revoked session revived", e)
		}
		if vault.Boots() != call || vault.Reads("a") != call+1 {
			t.Fatalf("call %d: boots/reads %d/%d", call, vault.Boots(), vault.Reads("a"))
		}
	}
}

func TestRateLimitKeepsSession(t *testing.T) {
	clock := testutil.NewClock()
	vault, r := vaultResolver(t, clock, map[string]string{"a": "va", "b": "vb"})
	first := resolve(t, r, []string{"a"}, false)
	vault.Fail("b", auth.ErrRateLimited)
	if _, e := r.Resolve(testContext(t), "p", profile, []string{"a", "b"}, false); e != auth.ErrRateLimited {
		t.Fatal("rate limit not classified", e)
	}
	if vault.Reads("b") != 1 || vault.Boots() != 1 {
		t.Fatal("rate limit retried or bootstrapped")
	}
	if a := resolve(t, r, []string{"a"}, true); a.Identity != first.Identity || a.Secrets()["a"] != "va" {
		t.Fatal("rate limit ended the session")
	}
	vault.Fail("b", nil)
	if b := resolve(t, r, []string{"b"}, true); b.Secrets()["b"] != "vb" || vault.Boots() != 1 {
		t.Fatal("client not kept after a rate limit")
	}
}

func TestFailedRefreshDoesNotExtendSession(t *testing.T) {
	clock := testutil.NewClock()
	vault, r := vaultResolver(t, clock, map[string]string{"a": "stale"})
	first := resolve(t, r, []string{"a"}, false)
	vault.Fail("a", errors.New("refresh failed"))
	clock.Advance(6 * time.Minute)
	l, e := r.Resolve(testContext(t), "p", profile, []string{"a"}, true)
	if !errors.Is(e, auth.ErrProvider) || l.Secrets() != nil {
		t.Fatal("failed refresh served stale values", e)
	}
	if _, e := r.Resolve(testContext(t), "p", profile, []string{"a"}, true); !errors.Is(e, auth.ErrRequired) {
		t.Fatal("session survived a failed refresh", e)
	}
	vault.Fail("a", nil)
	next := resolve(t, r, []string{"a"}, false)
	if next.Identity == first.Identity || !next.SessionExpiresAt.Equal(clock.Now().Add(24*time.Hour)) || vault.Boots() != 2 {
		t.Fatal("old session reused after a failed refresh")
	}
}
