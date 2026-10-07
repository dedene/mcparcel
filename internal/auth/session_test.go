package auth_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

func waitBoots(t *testing.T, vault *testutil.FakeVault, n int) {
	t.Helper()
	c := testContext(t)
	for vault.Boots() < n {
		select {
		case <-c.Done():
			t.Fatal("bootstrap did not start")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestLockEndsSessions(t *testing.T) {
	t.Run("idle session", func(t *testing.T) {
		vault, r := vaultResolver(t, testutil.NewClock(), map[string]string{"a": "v"})
		first := resolve(t, r, []string{"a"}, false)
		r.Lock()
		if _, e := r.Resolve(testContext(t), "p", profile, []string{"a"}, true); e != auth.ErrRequired {
			t.Fatal("--no-input after lock", e)
		}
		next := resolve(t, r, []string{"a"}, false)
		if next.Identity == first.Identity || vault.Boots() != 2 {
			t.Fatal("lock kept the session")
		}
	})
	t.Run("in-flight bootstrap", func(t *testing.T) {
		// Resolve reads the clock under the resolver lock just before it joins
		// the bootstrap, so two clock reads mean both callers are waiting.
		var reads atomic.Int64
		vault := &testutil.FakeVault{Block: make(chan struct{})}
		vault.Set("a", "v")
		r := resolver(t, auth.ResolverOptions{Provider: vault, Now: func() time.Time { reads.Add(1); return time.Now() }})
		out := make(chan answer, 2)
		for range 2 {
			go func() { l, e := r.Resolve(testContext(t), "p", profile, []string{"a"}, false); out <- answer{l, e} }()
		}
		waitBoots(t, vault, 1)
		for c := testContext(t); reads.Load() < 2; {
			select {
			case <-c.Done():
				t.Fatal("second caller did not join")
			case <-time.After(time.Millisecond):
			}
		}
		r.Lock()
		for range 2 {
			if a := receive(t, out); a.err != auth.ErrLocked || a.lease.Secrets() != nil {
				t.Fatal("waiter not locked", a.err)
			}
		}
		close(vault.Block)
		waitNoSession(t, r)
		if vault.Reads("a") != 0 {
			t.Fatal("late success was used")
		}
		if l := resolve(t, r, []string{"a"}, false); l.Secrets()["a"] != "v" || vault.Boots() != 2 {
			t.Fatal("no new bootstrap after lock")
		}
	})
}

func TestLockKeepsQuarantine(t *testing.T) {
	vault, r := vaultResolver(t, testutil.NewClock(), map[string]string{"a": "v"})
	vault.Block = make(chan struct{})
	out := make(chan answer, 1)
	go func() { l, e := r.Resolve(testContext(t), "p", profile, []string{"a"}, false); out <- answer{l, e} }()
	waitBoots(t, vault, 1)
	r.Lock()
	if e := receive(t, out).err; e != auth.ErrLocked {
		t.Fatal("waiter not locked", e)
	}
	for range 3 {
		if _, e := r.Resolve(testContext(t), "p", profile, []string{"a"}, false); !errors.Is(e, auth.ErrProvider) {
			t.Fatal("hung provider call did not block a second bootstrap", e)
		}
	}
	if vault.Boots() != 1 {
		t.Fatal("second bootstrap while the first is hung")
	}
	close(vault.Block)
	waitNoSession(t, r)
	resolve(t, r, []string{"a"}, false)
	if vault.Boots() != 2 {
		t.Fatal("quarantine did not clear")
	}
}

func TestInvalidateRereadsWithoutBootstrap(t *testing.T) {
	vault, r := vaultResolver(t, testutil.NewClock(), map[string]string{"a": "v1", "b": "b"})
	first := resolve(t, r, []string{"a", "b"}, false)
	r.Invalidate("p", []string{"a"})
	same := resolve(t, r, []string{"a", "b"}, true)
	if same.Identity != first.Identity || !same.SessionExpiresAt.Equal(first.SessionExpiresAt) {
		t.Fatal("an unchanged re-read changed the lease")
	}
	if vault.Reads("a") != 2 || vault.Reads("b") != 1 || vault.Boots() != 1 {
		t.Fatalf("reads a/b, boots %d/%d, %d", vault.Reads("a"), vault.Reads("b"), vault.Boots())
	}
	vault.Set("a", "v2")
	r.Invalidate("p", []string{"a"})
	changed := resolve(t, r, []string{"a", "b"}, true)
	if changed.Identity == first.Identity || changed.Secrets()["a"] != "v2" || vault.Boots() != 1 {
		t.Fatal("changed value kept the identity")
	}
	r.Invalidate("other", []string{"a"})
	resolve(t, r, []string{"a"}, true)
	if vault.Reads("a") != 3 {
		t.Fatal("another profile's invalidation dropped this cache")
	}
}

func TestChangedProfileEndsOldSession(t *testing.T) {
	vault, r := vaultResolver(t, testutil.NewClock(), map[string]string{"a": "v"})
	resolve(t, r, []string{"a"}, false)
	changed := profile
	changed.SessionDuration = "1h"
	if _, e := r.Resolve(testContext(t), "p", changed, []string{"a"}, true); e != auth.ErrRequired {
		t.Fatal("changed profile reused the old session", e)
	}
	if sessions := r.Sessions(); len(sessions) != 0 {
		t.Fatal("old session kept", sessions)
	}
	if _, e := r.Resolve(testContext(t), "p", profile, []string{"a"}, true); e != auth.ErrRequired {
		t.Fatal("old session revived", e)
	}
	if _, e := r.Resolve(testContext(t), "p", changed, []string{"a"}, false); e != nil || vault.Boots() != 2 {
		t.Fatal("changed profile did not bootstrap", e)
	}
	if sessions := r.Sessions(); len(sessions) != 1 {
		t.Fatal("sessions", sessions)
	}
}

func TestSessionsReportNoSecrets(t *testing.T) {
	clock := testutil.NewClock()
	vault := &testutil.FakeVault{}
	vault.Set("op://REF-CANARY/item/field", "VALUE-CANARY")
	r := resolver(t, auth.ResolverOptions{Now: clock.Now, Provider: vault})
	p := config.Profile{Mode: "desktop-service-account", Account: "ACCOUNT-CANARY", BootstrapRef: "op://BOOT-CANARY/item/field"}
	if _, e := r.Resolve(testContext(t), "work", p, []string{"op://REF-CANARY/item/field"}, false); e != nil {
		t.Fatal(e)
	}
	start := clock.Now()
	sessions := r.Sessions()
	if len(sessions) != 1 || sessions[0].Profile != "work" || sessions[0].Mode != "desktop-service-account" || sessions[0].State != "active" || !sessions[0].ExpiresAt.Equal(start.Add(24*time.Hour)) {
		t.Fatalf("sessions %+v", sessions)
	}
	clock.Advance(25 * time.Hour)
	sessions = r.Sessions()
	if len(sessions) != 1 || sessions[0].State != "expired" {
		t.Fatalf("sessions %+v", sessions)
	}
	data, err := json.Marshal(sessions)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{string(data), fmt.Sprintf("%#v", sessions)} {
		for _, canary := range []string{"REF-CANARY", "VALUE-CANARY", "ACCOUNT-CANARY", "BOOT-CANARY"} {
			if strings.Contains(text, canary) {
				t.Fatalf("sessions expose %s", canary)
			}
		}
	}
}
