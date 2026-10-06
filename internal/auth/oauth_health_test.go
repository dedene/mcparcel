package auth_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/testutil"
)

// healthRig is a health file in a private state directory.
type healthRig struct {
	*auth.Health
	dir string
}

func newHealthRig(t *testing.T, now func() time.Time) *healthRig {
	t.Helper()
	p, _ := testutil.IsolatedPaths(t)
	return &healthRig{auth.NewHealth(p.StateDir, now), p.StateDir}
}

func (r *healthRig) conn(t *testing.T) auth.ConnectionHealth {
	t.Helper()
	m, err := auth.ReadHealth(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	return m[account]
}

func (r *healthRig) last(t *testing.T) auth.HealthEvent {
	t.Helper()
	events := r.conn(t).Events
	if len(events) == 0 {
		t.Fatal("no health events")
	}
	return events[len(events)-1]
}

// open builds a session-mode handler from the stored item with health.
func (f *fixture) open(t *testing.T, health *auth.Health, now func() time.Time) *auth.OAuthHandler {
	t.Helper()
	s := f.stored(t)
	h := auth.NewOAuthHandler(auth.OAuthOptions{Account: account, Name: "demo", URL: f.mcpURL, State: &s, Keyring: f.kr, Health: health, Now: now})
	t.Cleanup(h.Close)
	return h
}

func (f *fixture) explain(t *testing.T, health *healthRig) auth.SessionReport {
	t.Helper()
	s, err := auth.LoadOAuth(context.Background(), f.kr, account)
	return auth.ExplainSession(auth.SessionInput{Connection: account, Name: "demo", URL: f.mcpURL, State: s, Found: err == nil, Health: health.conn(t), Now: time.Now()})
}

func TestRefreshRecordsHealth(t *testing.T) {
	for name, tc := range map[string]struct {
		o          testutil.AuthServerOptions
		rotated    bool
		refreshTTL int64
	}{
		"rotating":     {testutil.AuthServerOptions{Registration: true, RotateRefresh: true, RefreshExpiresIn: 14 * 24 * time.Hour}, true, 14 * 24 * 3600},
		"non-rotating": {testutil.AuthServerOptions{Registration: true}, false, 0},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, tc.o)
			signIn(t, f, auth.OAuthClient{})
			health := newHealthRig(t, nil)
			h := f.open(t, health.Health, nil)
			if _, err := h.Token(); err != nil {
				t.Fatal(err)
			}
			e := health.last(t)
			if e.Kind != auth.HealthRefreshed || e.Trigger != auth.TriggerCall || e.AccessTTL != 3600 || e.RefreshTTL != tc.refreshTTL || e.Rotated != tc.rotated || e.At == 0 {
				t.Fatalf("%+v", e)
			}
			if err := h.Refresh(auth.TriggerKeepAlive); err != nil {
				t.Fatal(err)
			}
			if e := health.last(t); e.Kind != auth.HealthRefreshed || e.Trigger != auth.TriggerKeepAlive {
				t.Fatalf("%+v", e)
			}
			if c := health.conn(t); c.Pending != 0 || len(c.Events) != 2 {
				t.Fatalf("%+v", c)
			}
		})
	}
}

func TestRefreshFailureRecordsCodeAndStatus(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true})
	signIn(t, f, auth.OAuthClient{})
	health := newHealthRig(t, nil)

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":"temporarily_unavailable"}`)
	}))
	t.Cleanup(broken.Close)
	s := f.stored(t)
	s.TokenURL = broken.URL + "/token"
	h := auth.NewOAuthHandler(auth.OAuthOptions{Account: account, Name: "demo", URL: f.mcpURL, State: &s, Keyring: f.kr, Health: health.Health})
	t.Cleanup(h.Close)
	_, err := h.Token()
	code(t, err, "connection_failed")
	if e := health.last(t); e.Kind != auth.HealthRefreshFailed || e.Code != "temporarily_unavailable" || e.HTTPStatus != 503 || e.Terminal || e.Trigger != auth.TriggerCall {
		t.Fatalf("%+v", e)
	}
	if r := f.explain(t, health); r.State != auth.StateExpiring || r.Cause == nil || r.Cause.Code != "refresh_failing" {
		t.Fatalf("%+v", r)
	}

	f.as.Revoke()
	_, err = f.open(t, health.Health, nil).Token()
	code(t, err, "auth_required")
	if e := health.last(t); e.Kind != auth.HealthRefreshFailed || e.Code != "invalid_grant" || e.HTTPStatus != 400 || !e.Terminal {
		t.Fatalf("%+v", e)
	}
}

// A refresh whose rotated token could not be saved, followed by a crash,
// leaves the stored refresh token stale. The next refresh's invalid_grant is
// recorded as an interrupted rotation, not as an ordinary revocation.
func TestRotatedTokenPersistedBeforeUse(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true, RotateRefresh: true})
	signIn(t, f, auth.OAuthClient{})
	health := newHealthRig(t, nil)
	h := f.open(t, health.Health, nil)
	f.kr.FailSets = 1
	_, err := h.Token()
	code(t, err, "keychain_unavailable")
	if e := health.last(t); e.Kind != auth.HealthRefreshFailed || e.Code != "keychain_save_failed" || health.conn(t).Pending == 0 {
		t.Fatalf("%+v", health.conn(t))
	}
	// The daemon crashes here: nothing closes h, so the new token is lost.

	restarted := auth.NewHealth(health.dir, nil)
	_, err = f.open(t, restarted, nil).Token()
	code(t, err, "auth_required")
	if e := health.last(t); e.Code != "invalid_grant" || !e.Interrupted || !e.Terminal {
		t.Fatalf("%+v", e)
	}
	if r := f.explain(t, health); r.State != auth.StateSignInRequired || r.Cause == nil || r.Cause.Code != "interrupted_refresh" {
		t.Fatalf("%+v", r)
	}
}

func TestProactiveRefreshBeforeExpiry(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true})
	signIn(t, f, auth.OAuthClient{})
	start := time.Now()
	var mu sync.Mutex
	now := start
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	set := func(d time.Duration) { mu.Lock(); now = start.Add(d); mu.Unlock() }
	h := f.open(t, nil, clock)
	refreshes := func() int { _, _, n := f.as.Counts(); return n }
	if _, err := h.Token(); err != nil || refreshes() != 1 {
		t.Fatal(err, refreshes())
	}
	set(47 * time.Minute)
	if _, err := h.Token(); err != nil || refreshes() != 1 {
		t.Fatal("refreshed with 13 minutes left", err, refreshes())
	}
	set(49 * time.Minute)
	if _, err := h.Token(); err != nil || refreshes() != 2 {
		t.Fatal("not refreshed with 11 minutes left", err, refreshes())
	}
	if exp := f.stored(t).AccessExpiry; exp != start.Add(49*time.Minute+time.Hour).Unix() {
		t.Fatal("access expiry", exp)
	}
}

func TestIssuerChangeRecordsReauthorization(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true})
	other := testutil.NewAuthServer(t, testutil.AuthServerOptions{Registration: true})
	signIn(t, f, auth.OAuthClient{})
	health := newHealthRig(t, nil)
	h := f.open(t, health.Health, nil)
	if status, err := send(ctx(t), h, f.mcpURL); err != nil || status != 200 {
		t.Fatal(status, err)
	}
	f.as.SetIssuerForPRM(other.URL)
	f.as.Revoke()
	_, err := send(ctx(t), h, f.mcpURL)
	code(t, err, "auth_required")
	if e := health.last(t); e.Kind != auth.HealthReauthorizationRequired || e.Code != "issuer_changed" || !e.Terminal {
		t.Fatalf("%+v", e)
	}
	if r := f.explain(t, health); r.Cause == nil || r.Cause.Code != "issuer_changed" || r.NextAction != "mcparcel auth login demo" {
		t.Fatalf("%+v", r)
	}
}

// A token the server rejects right after a refresh is now a persisted
// terminal failure: the refresh token is cleared and the cause recorded.
func TestTokenRejectedPersistsFailure(t *testing.T) {
	as := testutil.NewAuthServer(t, testutil.AuthServerOptions{ClientID: "pre-id"})
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	t.Cleanup(hs.Close)
	f := &fixture{as: as, mcpURL: hs.URL + "/mcp", kr: &testutil.MemKeyring{}}
	s := auth.OAuthState{Version: 1, URL: f.mcpURL, Issuer: as.URL, Resource: f.mcpURL, TokenURL: as.URL + "/token", RefreshToken: as.Grant()}
	if err := auth.SaveOAuth(context.Background(), f.kr, account, s); err != nil {
		t.Fatal(err)
	}
	health := newHealthRig(t, nil)
	h := auth.NewOAuthHandler(auth.OAuthOptions{Account: account, Name: "demo", URL: f.mcpURL, Client: auth.OAuthClient{ID: "pre-id"}, State: &s, Keyring: f.kr, Health: health.Health})
	t.Cleanup(h.Close)
	if status, err := send(ctx(t), h, f.mcpURL); err != nil || status != 401 {
		t.Fatal(status, err)
	}
	_, err := send(ctx(t), h, f.mcpURL)
	code(t, err, "auth_required")
	if stored := f.stored(t); stored.Failure == nil || stored.Failure.Code != "token_rejected" || stored.RefreshToken != "" {
		t.Fatalf("stored %+v", stored.Failure)
	}
	if e := health.last(t); e.Kind != auth.HealthReauthorizationRequired || e.Code != "token_rejected" || !e.Terminal {
		t.Fatalf("%+v", e)
	}
	if r := f.explain(t, health); r.Cause == nil || r.Cause.Code != "token_rejected" {
		t.Fatalf("%+v", r)
	}
}

// Logout closes the handler and then deletes the item. A refresh in flight
// at Close must finish saving first, or it would bring the item back.
func TestCloseWaitsForInflightRefresh(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true, RotateRefresh: true})
	signIn(t, f, auth.OAuthClient{})
	h := f.session(t, auth.OAuthClient{})
	saving, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.kr.BeforeSet = func() { once.Do(func() { close(saving); <-release }) }
	refreshed := make(chan error, 1)
	go func() { _, err := h.Token(); refreshed <- err }()
	<-saving
	closed := make(chan struct{})
	go func() { h.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("Close returned during the refresh's save")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	<-closed
	if f.stored(t).RefreshToken != f.as.RefreshToken() {
		t.Fatal("rotated refresh token not saved before Close returned")
	}
	if removed, err := auth.DeleteOAuth(context.Background(), f.kr, account); !removed || err != nil {
		t.Fatal(removed, err)
	}
	if err := <-refreshed; err != nil {
		t.Fatal(err)
	}
	_, err := h.Token()
	code(t, err, "auth_required")
	if _, err := f.kr.Get(auth.KeyringService, account); err != auth.ErrNoSession {
		t.Fatal("deleted item came back", err)
	}
}

// Close saves a rotated refresh token whose save failed, so a handler retired
// or shut down right after a failed save does not lose it.
func TestCloseSavesUnsavedRotation(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true, RotateRefresh: true})
	signIn(t, f, auth.OAuthClient{})
	health := newHealthRig(t, nil)
	h := f.open(t, health.Health, nil)
	f.kr.FailSets = 1
	_, err := h.Token()
	code(t, err, "keychain_unavailable")
	if !h.Unsaved() {
		t.Fatal("failed save not pending")
	}
	h.Close()
	if f.stored(t).RefreshToken != f.as.RefreshToken() {
		t.Fatal("rotated refresh token lost at Close")
	}
	if e := health.last(t); e.Kind != auth.HealthRefreshed || health.conn(t).Pending != 0 {
		t.Fatalf("%+v", health.conn(t))
	}
}

// A refresh that never reached the provider cannot have rotated anything, so
// it clears the pending marker: a later invalid_grant is not an interruption.
func TestUnreachableRefreshClearsPending(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true})
	signIn(t, f, auth.OAuthClient{})
	health := newHealthRig(t, nil)
	s := f.stored(t)
	s.TokenURL = fmt.Sprintf("http://127.0.0.1:%d/token", freePort(t))
	h := auth.NewOAuthHandler(auth.OAuthOptions{Account: account, Name: "demo", URL: f.mcpURL, State: &s, Keyring: f.kr, Health: health.Health})
	t.Cleanup(h.Close)
	_, err := h.Token()
	code(t, err, "connection_failed")
	if e := health.last(t); e.Code != "unreachable" || e.Terminal || health.conn(t).Pending != 0 {
		t.Fatalf("%+v", health.conn(t))
	}
	f.as.Revoke()
	_, err = f.open(t, auth.NewHealth(health.dir, nil), nil).Token()
	code(t, err, "auth_required")
	if e := health.last(t); e.Code != "invalid_grant" || e.Interrupted {
		t.Fatalf("%+v", e)
	}
	if r := f.explain(t, health); r.Cause == nil || r.Cause.Code != "refresh_expired_or_revoked" {
		t.Fatalf("%+v", r.Cause)
	}
}

// A late 401 for an access token that a later refresh replaced is retried
// with the current token; it is not "rejected right after a refresh".
func TestLateRejectionOfReplacedTokenKeepsSession(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true})
	signIn(t, f, auth.OAuthClient{})
	health := newHealthRig(t, nil)
	h := f.open(t, health.Health, nil)
	unauthorized := func(bearer string) error {
		req := httptest.NewRequest(http.MethodPost, f.mcpURL, nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		return h.Authorize(ctx(t), req, &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))})
	}
	if err := unauthorized(""); err != nil { // Authorize refreshes and mints A
		t.Fatal(err)
	}
	minted, err := h.Token()
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Refresh(auth.TriggerKeepAlive); err != nil { // mints B
		t.Fatal(err)
	}
	if err := unauthorized(minted.AccessToken); err != nil {
		t.Fatal("late 401 for the replaced token ended the session:", err)
	}
	if s := f.stored(t); s.Failure != nil || s.RefreshToken == "" {
		t.Fatalf("%+v", s.Failure)
	}
	for _, e := range health.conn(t).Events {
		if e.Terminal {
			t.Fatalf("%+v", e)
		}
	}
}
