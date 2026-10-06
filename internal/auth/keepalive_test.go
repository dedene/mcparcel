package auth_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/output"
	"github.com/dedene/mcparcel/internal/testutil"
)

// keepRig drives a KeepAlive with a fake clock and a scripted refresher. A
// successful refresh is recorded in the health file, as the handler does.
type keepRig struct {
	clock  *testutil.Clock
	health *healthRig
	keep   *auth.KeepAlive

	mu       sync.Mutex
	targets  []auth.KeepAliveTarget
	calls    []string // account/trigger per refresh
	answer   func(account string) error
	onCalled chan struct{}
}

func newKeepRig(t *testing.T, targets ...auth.KeepAliveTarget) *keepRig {
	t.Helper()
	r := &keepRig{clock: testutil.NewClock(), targets: targets}
	r.health = newHealthRig(t, r.clock.Now)
	r.keep = auth.NewKeepAlive(auth.KeepAliveOptions{
		Health: r.health.Health,
		Now:    r.clock.Now,
		Every:  time.Hour,
		Targets: func(context.Context) ([]auth.KeepAliveTarget, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			return append([]auth.KeepAliveTarget(nil), r.targets...), nil
		},
		Refresh: func(_ context.Context, account, trigger string) error {
			r.mu.Lock()
			r.calls = append(r.calls, account+"/"+trigger)
			answer, called := r.answer, r.onCalled
			r.mu.Unlock()
			var err error
			if answer != nil {
				err = answer(account)
			}
			if err == nil {
				_ = r.health.Record(account, auth.HealthEvent{Kind: auth.HealthRefreshed, Trigger: trigger})
			}
			if called != nil {
				called <- struct{}{}
			}
			return err
		},
	})
	return r
}

func (r *keepRig) answerWith(f func(string) error) { r.mu.Lock(); r.answer = f; r.mu.Unlock() }

func (r *keepRig) sweep() { r.keep.Sweep(context.Background(), auth.TriggerKeepAlive) }

func (r *keepRig) count() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.calls) }

func (r *keepRig) want(t *testing.T, n int) {
	t.Helper()
	if got := r.count(); got != n {
		r.mu.Lock()
		defer r.mu.Unlock()
		t.Fatalf("refreshes = %d, want %d: %v", got, n, r.calls)
	}
}

func (r *keepRig) signedIn(t *testing.T, account string) {
	t.Helper()
	if err := r.health.Record(account, auth.HealthEvent{Kind: auth.HealthAuthorized, Trigger: auth.TriggerLogin}); err != nil {
		t.Fatal(err)
	}
}

func daily(account string) auth.KeepAliveTarget {
	return auth.KeepAliveTarget{Account: account, Interval: auth.DefaultKeepAlive}
}

func TestIdleKeepAlive(t *testing.T) {
	r := newKeepRig(t, daily(account))
	r.signedIn(t, account)
	r.clock.Advance(time.Hour)
	r.sweep()
	r.want(t, 0)
	r.clock.Advance(23 * time.Hour)
	r.sweep()
	r.want(t, 1)
	r.clock.Advance(time.Hour)
	r.sweep()
	r.want(t, 1)
	r.clock.Advance(23 * time.Hour)
	r.sweep()
	r.want(t, 2)
	if r.calls[1] != account+"/"+auth.TriggerKeepAlive {
		t.Fatal(r.calls)
	}
	// A shorter configured interval is honoured.
	r.targets = []auth.KeepAliveTarget{{Account: account, Interval: 2 * time.Hour}}
	r.clock.Advance(2 * time.Hour)
	r.sweep()
	r.want(t, 3)
}

// A revoked session is never signed in again from the background: the
// handler's Refresh fails, the connection goes dormant, and nothing reaches
// the browser or the authorization endpoint.
func TestKeepAliveNeverPrompts(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true, RotateRefresh: true})
	signIn(t, f, auth.OAuthClient{})
	f.as.Revoke()
	authorizeBefore := f.as.Requests("/authorize")
	b := newBrowser()
	r := newKeepRig(t, daily(account))
	r.answerWith(func(string) error {
		s := f.stored(t)
		// Even a handler built for sign-in only refreshes here.
		h := auth.NewOAuthHandler(auth.OAuthOptions{Account: account, Name: "demo", URL: f.mcpURL, State: &s, Keyring: f.kr, Login: b.login(), Health: r.health.Health, Now: r.clock.Now})
		defer h.Close()
		return h.Refresh(auth.TriggerKeepAlive)
	})
	r.sweep()
	r.want(t, 1)
	if len(b.shown()) != 0 || f.as.Requests("/authorize") != authorizeBefore {
		t.Fatal("background refresh prompted", b.shown())
	}
	if r.keep.HasSessions() {
		t.Fatal("revoked session still counted")
	}
	r.clock.Advance(48 * time.Hour)
	r.sweep()
	r.want(t, 1)
}

func TestKeepAliveBackoff(t *testing.T) {
	r := newKeepRig(t, daily(account))
	r.answerWith(func(string) error { return output.NewError("connection_failed", nil) })
	r.sweep() // never refreshed: due at once
	r.want(t, 1)
	steps := []struct {
		advance time.Duration
		want    int
	}{
		{4 * time.Minute, 1}, {time.Minute, 2}, // 5m
		{9 * time.Minute, 2}, {time.Minute, 3}, // 10m
		{19 * time.Minute, 3}, {time.Minute, 4}, // 20m
	}
	for i, s := range steps {
		r.clock.Advance(s.advance)
		r.sweep()
		if r.count() != s.want {
			t.Fatalf("step %d: %d refreshes, want %d", i, r.count(), s.want)
		}
	}
	// The delay doubles up to 6 hours and stays there.
	for range 10 {
		r.clock.Advance(6 * time.Hour)
		r.sweep()
	}
	n := r.count()
	r.clock.Advance(6*time.Hour - time.Minute)
	r.sweep()
	r.want(t, n)
	r.clock.Advance(time.Minute)
	r.sweep()
	r.want(t, n+1)
	if !r.keep.HasSessions() {
		t.Fatal("a failing session is still a session")
	}
	// Success clears the backoff; the next refresh is a day later.
	r.answerWith(nil)
	r.clock.Advance(6 * time.Hour)
	r.sweep()
	r.want(t, n+2)
	r.clock.Advance(5 * time.Minute)
	r.sweep()
	r.want(t, n+2)
}

func TestKeepAliveStartRefreshesStale(t *testing.T) {
	r := newKeepRig(t, daily("stale"), daily("fresh"))
	r.signedIn(t, "stale")
	r.clock.Advance(30 * time.Hour)
	r.signedIn(t, "fresh")
	r.clock.Advance(time.Hour)
	r.onCalled = make(chan struct{}, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.keep.Run(ctx) }()
	select {
	case <-r.onCalled:
	case <-time.After(5 * time.Second):
		t.Fatal("no refresh at start")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	r.want(t, 1)
	if r.calls[0] != "stale/"+auth.TriggerStart {
		t.Fatal(r.calls)
	}
	events := r.health.connFor(t, "stale").Events
	if e := events[len(events)-1]; e.Kind != auth.HealthRefreshed || e.Trigger != auth.TriggerStart {
		t.Fatalf("%+v", e)
	}
}

func TestKeepAliveSkipsFresh(t *testing.T) {
	r := newKeepRig(t, daily(account))
	r.signedIn(t, account)
	for range 23 {
		r.clock.Advance(time.Hour)
		r.sweep()
	}
	r.want(t, 0)
	if !r.keep.HasSessions() {
		t.Fatal("fresh session not counted")
	}
}

func TestKeepAliveDormantAfterTerminal(t *testing.T) {
	for name, fail := range map[string]error{
		"auth_required": auth.NotSignedIn("demo"),
		"dormant":       auth.ErrKeepAliveDormant,
	} {
		t.Run(name, func(t *testing.T) {
			r := newKeepRig(t, daily(account))
			if !r.keep.HasSessions() {
				t.Fatal("before the first sweep every target counts")
			}
			r.answerWith(func(string) error { return fail })
			r.sweep()
			r.want(t, 1)
			if r.keep.HasSessions() {
				t.Fatal("dormant session counted")
			}
			for range 5 {
				r.clock.Advance(48 * time.Hour)
				r.sweep()
			}
			r.want(t, 1)
			// A new sign-in wakes it; it is due again a day later.
			r.answerWith(nil)
			r.signedIn(t, account)
			if !r.keep.HasSessions() {
				t.Fatal("signed-in session not counted")
			}
			r.sweep()
			r.want(t, 1)
			r.clock.Advance(24 * time.Hour)
			r.sweep()
			r.want(t, 2)
		})
	}
}

func TestKeepAliveBusyRetriesWithoutBackoff(t *testing.T) {
	r := newKeepRig(t, daily(account))
	busy := true
	r.answerWith(func(string) error {
		if busy {
			return auth.ErrKeepAliveBusy
		}
		return nil
	})
	r.sweep()
	r.want(t, 1)
	busy = false
	r.clock.Advance(time.Minute)
	r.sweep()
	r.want(t, 2)
	r.clock.Advance(time.Minute)
	r.sweep()
	r.want(t, 2)
}

func TestKeepAliveClockBackwards(t *testing.T) {
	r := newKeepRig(t, daily(account))
	r.signedIn(t, account)
	// The wall clock moves back two days: the last success lies in the
	// future, which is treated as due (one extra refresh is harmless).
	r.clock.Advance(-48 * time.Hour)
	r.sweep()
	r.want(t, 1)
	r.sweep()
	r.want(t, 1)
	// A backoff set before the clock moved back more than 6 hours is ignored.
	r.answerWith(func(string) error { return errors.New("network down") })
	r.clock.Advance(48 * time.Hour)
	r.sweep()
	r.want(t, 2)
	r.clock.Advance(-7 * time.Hour)
	r.sweep()
	r.want(t, 3)
}

func TestKeepAliveWakeAfterSleep(t *testing.T) {
	r := newKeepRig(t, daily(account))
	r.signedIn(t, account)
	r.sweep()
	r.want(t, 0)
	r.clock.Advance(72 * time.Hour) // the lid was closed for three days
	r.sweep()
	r.want(t, 1)
	r.sweep()
	r.want(t, 1)
}

// Logout and a terminal failure end a session at once, without waiting for
// the next refresh, and the next sweep confirms it with one lookup.
func TestKeepAliveSettledSessions(t *testing.T) {
	for name, e := range map[string]auth.HealthEvent{
		"logout":   {Kind: auth.HealthLogout},
		"terminal": {Kind: auth.HealthRefreshFailed, Code: "invalid_grant", Terminal: true},
	} {
		t.Run(name, func(t *testing.T) {
			r := newKeepRig(t, daily(account))
			r.signedIn(t, account)
			r.sweep()
			if !r.keep.HasSessions() {
				t.Fatal("session not counted")
			}
			r.clock.Advance(time.Minute)
			if err := r.health.Record(account, e); err != nil {
				t.Fatal(err)
			}
			if r.keep.HasSessions() {
				t.Fatal("ended session counted")
			}
			r.answerWith(func(string) error { return auth.ErrKeepAliveDormant })
			r.sweep()
			r.sweep()
			r.want(t, 1)
		})
	}
}

func (r *healthRig) connFor(t *testing.T, account string) auth.ConnectionHealth {
	t.Helper()
	m, err := auth.ReadHealth(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	return m[account]
}

// A dormant connection wakes on any new sign-in, even one whose wall-clock
// time lies before the moment it went dormant (the clock was set back).
func TestKeepAliveDormantWakesAfterClockBack(t *testing.T) {
	r := newKeepRig(t, daily(account))
	r.answerWith(func(string) error { return auth.ErrKeepAliveDormant })
	r.sweep()
	r.want(t, 1)
	r.clock.Advance(-30 * time.Minute)
	r.answerWith(nil)
	r.signedIn(t, account)
	if !r.keep.HasSessions() {
		t.Fatal("signed-in session not counted")
	}
	r.clock.Advance(24 * time.Hour)
	r.sweep()
	r.want(t, 2)
}
