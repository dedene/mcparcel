package auth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/config"
)

var saProfile = config.Profile{Mode: config.ProfileModeServiceAccount, TokenEnv: "OP_SERVICE_ACCOUNT_TOKEN"}

// tokenRig is a provider whose service-account constructor counts the tokens
// it receives and answers with fail.
type tokenRig struct {
	mu     sync.Mutex
	token  string
	tokens []string
	fail   error
	now    time.Time
	p      *onePasswordProvider
}

func newTokenRig(t *testing.T) *tokenRig {
	t.Helper()
	r := &tokenRig{token: "ops_first", now: time.Unix(1_700_000_000, 0)}
	desktop := func(context.Context, string, string) (SecretClient, error) {
		return localSecretClient{resolve: func(context.Context, string) (string, error) { return "desktop", nil }}, nil
	}
	service := func(_ context.Context, token, version string) (SecretClient, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if version != "test" {
			t.Error("service version", version)
		}
		r.tokens = append(r.tokens, token)
		if r.fail != nil {
			return nil, r.fail
		}
		return localSecretClient{resolve: func(context.Context, string) (string, error) { return "value", nil }}, nil
	}
	r.p = newOnePasswordProvider("test", desktop, service, func(p config.Profile) (string, error) {
		if !p.PromptFree() {
			t.Error("token read for a desktop profile")
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.token, nil
	})
	r.p.now = func() time.Time { r.mu.Lock(); defer r.mu.Unlock(); return r.now }
	return r
}

func (r *tokenRig) set(token string, fail error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.token, r.fail = token, fail
}

func (r *tokenRig) advance(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = r.now.Add(d)
}

func (r *tokenRig) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.tokens)
}

func TestServiceAccountSkipsDesktop(t *testing.T) {
	var got []string
	desktop := func(context.Context, string, string) (SecretClient, error) {
		t.Fatal("desktop constructor ran for a service-account profile")
		return nil, nil
	}
	service := func(_ context.Context, token, _ string) (SecretClient, error) {
		got = append(got, token)
		return localSecretClient{resolve: func(context.Context, string) (string, error) { return "value", nil }}, nil
	}
	p := newOnePasswordProvider("test", desktop, service, func(p config.Profile) (string, error) {
		return ServiceAccountToken(p, map[string]string{"OP_SERVICE_ACCOUNT_TOKEN": "ops_exact+/="})
	})
	c, err := p.Bootstrap(context.Background(), saProfile)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := c.Resolve(context.Background(), "op://v/i/f"); v != "value" || err != nil {
		t.Fatal("service client", v, err)
	}
	if len(got) != 1 || got[0] != "ops_exact+/=" {
		t.Fatal("token did not reach the service constructor exactly", len(got))
	}
}

func TestServiceAccountTokenErrors(t *testing.T) {
	for _, sentinel := range []error{ErrTokenUnavailable, ErrTokenUnsafe} {
		p := newOnePasswordProvider("test", nil, func(context.Context, string, string) (SecretClient, error) {
			t.Fatal("service constructor ran without a token")
			return nil, nil
		}, func(config.Profile) (string, error) { return "", sentinel })
		if _, err := p.Bootstrap(context.Background(), saProfile); err != sentinel {
			t.Fatal("token error", sentinel, err)
		}
	}
}

func TestServiceAccountHasNoAccountPin(t *testing.T) {
	r := newTokenRig(t)
	for _, c := range []struct {
		profile config.Profile
		want    error
	}{
		{config.Profile{Mode: config.ProfileModeDesktop, Account: "A"}, nil},
		{saProfile, nil},
		{config.Profile{Mode: config.ProfileModeDesktop, Account: "A"}, nil},
		{config.Profile{Mode: config.ProfileModeDesktop, Account: "B"}, ErrAccountConflict},
		{saProfile, nil},
	} {
		if _, err := r.p.Bootstrap(context.Background(), c.profile); err != c.want {
			t.Fatalf("%s %q: %v", c.profile.Mode, c.profile.Account, err)
		}
	}
}

func TestWithoutDesktopAppDesktopProfilesFail(t *testing.T) {
	p := NewOnePasswordProvider("test", OnePasswordOptions{Env: map[string]string{"OP_SERVICE_ACCOUNT_TOKEN": "ops_env"}}).(*onePasswordProvider)
	if p.desktop != nil {
		t.Fatal("desktop constructor wired without DesktopApp")
	}
	for _, mode := range []string{config.ProfileModeDesktop, config.ProfileModeDesktopServiceAccount} {
		if _, err := p.Bootstrap(context.Background(), config.Profile{Mode: mode, Account: "A", BootstrapRef: "op://v/i/f"}); err != ErrProvider {
			t.Fatal(mode, err)
		}
	}
	if token, err := p.token(saProfile); token != "ops_env" || err != nil {
		t.Fatal("token not read from Env", err)
	}
	if NewOnePasswordProvider("test", OnePasswordOptions{DesktopApp: true}).(*onePasswordProvider).desktop == nil {
		t.Fatal("desktop constructor missing with DesktopApp")
	}
}

func TestServiceAccountRateLimitNotCached(t *testing.T) {
	r := newTokenRig(t)
	r.set("ops_first", ErrRateLimited)
	for i := range 3 {
		if _, err := r.p.Bootstrap(context.Background(), saProfile); err != ErrRateLimited {
			t.Fatal(i, err)
		}
	}
	if r.calls() != 3 {
		t.Fatal("rate limit was negatively cached", r.calls())
	}
}

func TestServiceAccountNegativeCache(t *testing.T) {
	rejected := errors.New("rejected token ops_first")
	r := newTokenRig(t)
	r.set("ops_first", rejected)
	boot := func() error {
		t.Helper()
		_, err := r.p.Bootstrap(context.Background(), saProfile)
		if err != nil && strings.Contains(err.Error(), "ops_") {
			t.Fatal("token text in error")
		}
		return err
	}
	for range 5 {
		if err := boot(); err != ErrProvider {
			t.Fatal(err)
		}
		r.advance(5 * time.Second)
	}
	if r.calls() != 1 {
		t.Fatal("rejected token sent again inside the window", r.calls())
	}
	// 25 s have passed; the window ends at 30 s.
	r.advance(5 * time.Second)
	if err := boot(); err != ErrProvider || r.calls() != 2 {
		t.Fatal("no retry after the window", r.calls())
	}
	// The second refusal doubles the window to 60 s.
	r.advance(45 * time.Second)
	if err := boot(); err != ErrProvider || r.calls() != 2 {
		t.Fatal("backoff did not double", r.calls())
	}
	r.advance(15 * time.Second)
	if err := boot(); err != ErrProvider || r.calls() != 3 {
		t.Fatal("no retry after the doubled window", r.calls())
	}
	// A changed token is tried at once, and its success clears its entry.
	r.set("ops_second", nil)
	if err := boot(); err != nil || r.calls() != 4 {
		t.Fatal("changed token", err, r.calls())
	}
	r.set("ops_second", rejected)
	if err := boot(); err != ErrProvider || r.calls() != 5 {
		t.Fatal("success did not clear the entry", r.calls())
	}
	if err := boot(); err != ErrProvider || r.calls() != 5 {
		t.Fatal("fresh refusal not cached", r.calls())
	}
	// The window is capped at ten minutes.
	r.set("ops_first", rejected)
	for range 10 {
		r.advance(tokenBackoffMax)
		_ = boot()
	}
	fp := r.p.failures.fingerprint("ops_first")
	if e := r.p.failures.entries[fp]; e.backoff != tokenBackoffMax {
		t.Fatal("backoff not capped", e.backoff)
	}
}

func TestServiceAccountSuccessClearsFailure(t *testing.T) {
	r := newTokenRig(t)
	r.set("ops_first", errors.New("unreachable"))
	_, _ = r.p.Bootstrap(context.Background(), saProfile)
	r.advance(tokenBackoff)
	r.set("ops_first", nil)
	if _, err := r.p.Bootstrap(context.Background(), saProfile); err != nil {
		t.Fatal(err)
	}
	if len(r.p.failures.entries) != 0 {
		t.Fatal("success kept the failure entry")
	}
	r.set("ops_first", errors.New("unreachable"))
	_, _ = r.p.Bootstrap(context.Background(), saProfile)
	if e := r.p.failures.entries[r.p.failures.fingerprint("ops_first")]; e.backoff != tokenBackoff {
		t.Fatal("backoff survived a success", e.backoff)
	}
}

func TestNegativeCacheBounded(t *testing.T) {
	var f bootstrapFailures
	f.key = []byte("fixture key")
	start := time.Unix(1_700_000_000, 0)
	for i := range tokenFailuresMax + 10 {
		f.record(f.fingerprint("ops_"+strings.Repeat("x", i)), start.Add(time.Duration(i)*time.Millisecond))
	}
	if len(f.entries) != tokenFailuresMax {
		t.Fatal("cache not capped", len(f.entries))
	}
	now := start.Add(time.Second)
	for i := range 10 {
		if f.blocked(f.fingerprint("ops_"+strings.Repeat("x", i)), now) {
			t.Fatal("oldest entry kept", i)
		}
	}
	if !f.blocked(f.fingerprint("ops_"+strings.Repeat("x", tokenFailuresMax+9)), now) {
		t.Fatal("newest entry dropped")
	}
}

func TestNegativeCacheKeyIsPerProvider(t *testing.T) {
	a := newOnePasswordProvider("test", nil, nil, nil)
	b := newOnePasswordProvider("test", nil, nil, nil)
	if a.failures.fingerprint("ops_x") == b.failures.fingerprint("ops_x") {
		t.Fatal("fingerprint key is not random")
	}
	var plain [32]byte
	copy(plain[:], "ops_x")
	if a.failures.fingerprint("ops_x") == plain {
		t.Fatal("fingerprint holds the token")
	}
}
