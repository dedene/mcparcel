package auth

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type mapKeyring struct {
	mu    sync.Mutex
	items map[string]string
}

func (k *mapKeyring) Get(_, account string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.items[account]
	if !ok {
		return "", ErrNoSession
	}
	return v, nil
}

func (k *mapKeyring) Set(_, account, secret string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.items == nil {
		k.items = map[string]string{}
	}
	k.items[account] = secret
	return nil
}

func (k *mapKeyring) Delete(_, account string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.items, account)
	return nil
}

// On darwin the monotonic clock stops while the machine sleeps, so a token
// that expired during sleep still looks fresh to it. Moving only the wall
// clock past the refresh point must refresh.
func TestTokenExpiryUsesWallClock(t *testing.T) {
	var refreshes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		refreshes.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"a","token_type":"Bearer","expires_in":3600,"refresh_token":"r"}`)
	}))
	t.Cleanup(srv.Close)
	start := time.Now()
	var mu sync.Mutex
	now := start
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	state := OAuthState{Version: 1, URL: "http://127.0.0.1:1/mcp", TokenURL: srv.URL + "/token", ClientID: "c", RefreshToken: "r"}
	h := NewOAuthHandler(OAuthOptions{Account: "local:a", Name: "a", URL: state.URL, State: &state, Keyring: &mapKeyring{}, Now: clock})
	t.Cleanup(h.Close)
	if _, err := h.Token(); err != nil || refreshes.Load() != 1 {
		t.Fatal(err, refreshes.Load())
	}
	// Two hours of sleep: the wall clock moved, the monotonic one did not.
	mu.Lock()
	now = shiftWall(start, 7200)
	mu.Unlock()
	if clock().Sub(start) != 0 {
		t.Fatal("test moved the monotonic clock")
	}
	if _, err := h.Token(); err != nil || refreshes.Load() != 2 {
		t.Fatal("expired token not refreshed after sleep", err, refreshes.Load())
	}
	// A wall clock moved back is caught by the monotonic reading.
	mu.Lock()
	now = shiftWall(start.Add(50*time.Minute), -7200)
	mu.Unlock()
	h.mu.Lock()
	h.issued, h.refreshAt = start, start.Unix()+48*60
	h.mu.Unlock()
	if _, err := h.Token(); err != nil || refreshes.Load() != 3 {
		t.Fatal("clock moved back hid an expiring token", err, refreshes.Load())
	}
}
