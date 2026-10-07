package auth_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/testutil"
)

func TestConcurrentTokenSingleMint(t *testing.T) {
	as := ccServer(t, testutil.AuthServerOptions{CCExpiresIn: 900})
	h := newCC(t, auth.ClientCredentialsConfig{TokenURL: as.URL + "/token"})
	start := make(chan struct{})
	tokens := make([]string, 50)
	errs := make([]error, 50)
	var wg sync.WaitGroup
	for i := range tokens {
		wg.Go(func() {
			<-start
			tok, err := ccToken(h)
			if err == nil {
				tokens[i] = tok.AccessToken
			}
			errs[i] = err
		})
	}
	close(start)
	wg.Wait()
	for i := range tokens {
		if errs[i] != nil || tokens[i] != tokens[0] {
			t.Fatal(errs[i], "token differs")
		}
	}
	if grants(as) != 1 {
		t.Fatal("grants", grants(as))
	}
}

// Expired tokens: concurrent first calls after expiry share one mint.
func TestConcurrentExpiredSingleMint(t *testing.T) {
	clock := testutil.NewClock()
	as := ccServer(t, testutil.AuthServerOptions{CCExpiresIn: 900, Now: clock.Now})
	h := newCC(t, auth.ClientCredentialsConfig{TokenURL: as.URL + "/token", Now: clock.Now})
	stale := mustToken(t, h)
	clock.Advance(time.Hour)
	var wg sync.WaitGroup
	got := make([]string, 2)
	for i := range got {
		wg.Go(func() {
			if tok, err := ccToken(h); err == nil {
				got[i] = tok.AccessToken
			}
		})
	}
	wg.Wait()
	if got[0] == "" || got[0] == stale || got[1] != got[0] || grants(as) != 2 {
		t.Fatal("tokens differ or stale; grants", grants(as))
	}
}

func TestConcurrent401SingleRemint(t *testing.T) {
	as := ccServer(t, testutil.AuthServerOptions{CCExpiresIn: 900})
	h := newCC(t, auth.ClientCredentialsConfig{TokenURL: as.URL + "/token"})
	stale := mustToken(t, h)
	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Go(func() {
			<-start
			errs[i] = ccReject(context.Background(), h, http.StatusUnauthorized, stale)
		})
	}
	close(start)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatal(errs)
	}
	if mustToken(t, h) == stale || grants(as) != 2 {
		t.Fatal("grants", grants(as))
	}
}

func TestStale401AfterNewerTokenNoMint(t *testing.T) {
	clock := testutil.NewClock()
	as := ccServer(t, testutil.AuthServerOptions{CCExpiresIn: 900, Now: clock.Now})
	h := newCC(t, auth.ClientCredentialsConfig{TokenURL: as.URL + "/token", Now: clock.Now})
	old := mustToken(t, h)
	clock.Advance(800 * time.Second)
	newer := mustToken(t, h)
	if newer == old || grants(as) != 2 {
		t.Fatal("no refresh", grants(as))
	}
	if err := ccReject(context.Background(), h, http.StatusUnauthorized, old); err != nil {
		t.Fatal(err)
	}
	if mustToken(t, h) != newer || grants(as) != 2 {
		t.Fatal("stale 401 minted", grants(as))
	}
	// The newer token was minted for expiry, not for a 401: its own 401
	// re-mints instead of failing token_rejected.
	if err := ccReject(context.Background(), h, http.StatusUnauthorized, newer); err != nil || grants(as) != 3 {
		t.Fatal(err, grants(as))
	}
}

func TestCanceledWaiterDoesNotCancelMint(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"at-1","token_type":"bearer","expires_in":900}`)
	}))
	defer srv.Close()
	h := newCC(t, auth.ClientCredentialsConfig{TokenURL: srv.URL})
	ctx, cancel := context.WithCancel(context.Background())
	ts, err := h.TokenSource(ctx)
	if err != nil {
		t.Fatal(err)
	}
	canceled := make(chan error, 1)
	go func() { _, err := ts.Token(); canceled <- err }()
	<-entered
	other := make(chan string, 1)
	go func() {
		tok, err := ccToken(h)
		if err != nil {
			other <- err.Error()
			return
		}
		other <- tok.AccessToken
	}()
	cancel()
	select {
	case err := <-canceled:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled waiter still waiting")
	}
	close(release)
	if got := <-other; got != "at-1" || calls.Load() != 1 {
		t.Fatal(got, calls.Load())
	}
}

// A 401 that arrives while a mint is running waits for that mint instead of
// starting another.
func TestAuthorizeJoinsMintInFlight(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		if n == 2 {
			entered <- struct{}{}
			<-release
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"at-`+string(rune('0'+n))+`","token_type":"bearer"}`)
	}))
	defer srv.Close()
	defer close(release)
	h := newCC(t, auth.ClientCredentialsConfig{TokenURL: srv.URL})
	stale := mustToken(t, h)
	done := make(chan error, 2)
	go func() { done <- ccReject(context.Background(), h, http.StatusUnauthorized, stale) }()
	<-entered
	go func() { done <- ccReject(context.Background(), h, http.StatusUnauthorized, stale) }()
	time.Sleep(50 * time.Millisecond)
	release <- struct{}{}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if mustToken(t, h) != "at-2" || calls.Load() != 2 {
		t.Fatal(calls.Load())
	}
}
