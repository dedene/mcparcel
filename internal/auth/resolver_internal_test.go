package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/config"
)

type providerFunc func(context.Context, config.Profile) (SecretClient, error)

func (f providerFunc) Bootstrap(ctx context.Context, p config.Profile) (SecretClient, error) {
	return f(ctx, p)
}

type offsetClock struct {
	base   time.Time
	offset atomic.Int64
}

func (c *offsetClock) now() time.Time          { return c.base.Add(time.Duration(c.offset.Load())) }
func (c *offsetClock) advance(d time.Duration) { c.offset.Add(int64(d)) }

func canaryProvider(value string) providerFunc {
	return func(context.Context, config.Profile) (SecretClient, error) {
		return localSecretClient{resolve: func(context.Context, string) (string, error) { return value, nil }}, nil
	}
}

var internalProfile = config.Profile{Mode: "desktop-service-account", Account: "fixture", BootstrapRef: "op://fixture/bootstrap/token"}

func internalResolver(t *testing.T, o ResolverOptions) *resolver {
	t.Helper()
	r := NewResolver(o).(*resolver)
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func onlyState(t *testing.T, r *resolver) *profileState {
	t.Helper()
	if len(r.states) != 1 {
		t.Fatalf("%d states", len(r.states))
	}
	for _, s := range r.states {
		return s
	}
	return nil
}

func TestHistoryHoldsNoPlaintext(t *testing.T) {
	r := internalResolver(t, ResolverOptions{Provider: canaryProvider("PLAINTEXT-CANARY")})
	ctx := context.Background()
	if _, err := r.Resolve(ctx, "p", internalProfile, []string{"a"}, false); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	s := onlyState(t, r)
	digest, ok := s.history["a"]
	text := fmt.Sprintf("%v %x", s.history, digest)
	first := digest
	r.mu.Unlock()
	if !ok || strings.Contains(text, "PLAINTEXT-CANARY") || strings.Contains(text, "PLAINTEXT") || digest == sha256.Sum256([]byte("PLAINTEXT-CANARY")) {
		t.Fatal("history holds the value or an unkeyed digest")
	}
	r.Lock()
	if _, err := r.Resolve(ctx, "p", internalProfile, []string{"a"}, false); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	second := onlyState(t, r).history["a"]
	r.mu.Unlock()
	if second == first {
		t.Fatal("digest key reused across sessions")
	}
}

func TestSweepDropsExpiredValuesAndSessions(t *testing.T) {
	clock := &offsetClock{base: time.Now()}
	r := internalResolver(t, ResolverOptions{Now: clock.now, SweepEvery: 5 * time.Millisecond, Provider: canaryProvider("PLAINTEXT-CANARY")})
	if _, err := r.Resolve(context.Background(), "p", internalProfile, []string{"a"}, false); err != nil {
		t.Fatal(err)
	}
	waitState := func(what string, done func(*profileState) bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			r.mu.Lock()
			s := onlyState(t, r)
			ok := done(s)
			r.mu.Unlock()
			if ok {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal(what)
			}
			time.Sleep(time.Millisecond)
		}
	}
	clock.advance(5 * time.Minute)
	waitState("value survived the lease", func(s *profileState) bool {
		return len(s.values) == 0 && !strings.Contains(fmt.Sprintf("%#v", *s), "PLAINTEXT-CANARY")
	})
	r.mu.Lock()
	if onlyState(t, r).client == nil {
		t.Fatal("sweep ended a live session")
	}
	r.mu.Unlock()
	clock.advance(24 * time.Hour)
	waitState("session survived its deadline", func(s *profileState) bool { return s.client == nil && s.expired })
}

// Regression: a profile changed while its bootstrap was in flight kept the old
// session, and its token, alive beside the new one until the old deadline.
func TestProfileChangeEndsInFlightSession(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var boots atomic.Int32
	r := internalResolver(t, ResolverOptions{Provider: providerFunc(func(context.Context, config.Profile) (SecretClient, error) {
		if boots.Add(1) == 1 {
			close(entered)
			<-release
		}
		return localSecretClient{resolve: func(context.Context, string) (string, error) { return "v", nil }}, nil
	})})
	old, changed := internalProfile, internalProfile
	old.SessionDuration, changed.SessionDuration = "24h", "1h"
	first := make(chan error, 1)
	go func() { _, err := r.Resolve(context.Background(), "p", old, []string{"a"}, false); first <- err }()
	<-entered
	if _, err := r.Resolve(context.Background(), "p", changed, []string{"a"}, false); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-first; !errors.Is(err, ErrExpired) {
		t.Fatal("old profile's session installed", err)
	}
	s := r.Sessions()
	if len(s) != 1 || s[0].ExpiresAt.After(time.Now().Add(2*time.Hour)) {
		t.Fatalf("%+v", s)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.states) != 1 {
		t.Fatal("old state kept", len(r.states))
	}
}
