package auth

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/dedene/mcparcel/internal/output"
)

// DefaultKeepAlive is how often an idle session is refreshed when its
// connection sets no lifecycle.keepAlive.
const DefaultKeepAlive = 24 * time.Hour

const (
	keepAliveEvery    = 5 * time.Minute
	keepAliveBackoff  = 5 * time.Minute
	keepAliveMaxDelay = 6 * time.Hour
)

var (
	// ErrKeepAliveBusy: the connection is in use; retried at the next sweep.
	ErrKeepAliveBusy = errors.New("connection busy")
	// ErrKeepAliveDormant: no session the background may refresh; skipped
	// until a newer sign-in or refresh is recorded.
	ErrKeepAliveDormant = errors.New("no usable session")
)

// KeepAliveInterval reads a connection's lifecycle.keepAlive: empty is the
// default, "off" (ok false) excludes the connection.
func KeepAliveInterval(setting string) (time.Duration, bool) {
	if setting == "off" {
		return 0, false
	}
	if d, err := time.ParseDuration(setting); err == nil && d >= time.Hour {
		return d, true
	}
	return DefaultKeepAlive, true
}

// KeepAliveTarget is one connection the keep-alive looks after.
type KeepAliveTarget struct {
	Account  string
	Interval time.Duration
}

// KeepAliveOptions: Targets lists the connections to look after; Refresh
// refreshes one without ever signing in. Health supplies the last success.
type KeepAliveOptions struct {
	Health  *Health
	Now     func() time.Time
	Every   time.Duration // default 5m
	Targets func(ctx context.Context) ([]KeepAliveTarget, error)
	Refresh func(ctx context.Context, account, trigger string) error
}

// KeepAlive refreshes idle OAuth sessions often enough to stay inside the
// provider's idle window. All deadlines are wall-clock seconds, so a Mac that
// slept is caught up at the first sweep after it wakes.
type KeepAlive struct {
	o KeepAliveOptions

	mu      sync.Mutex
	swept   bool
	targets []string
	state   map[string]keepAliveState
}

type keepAliveState struct {
	dormant  bool
	mark     uint64 // Health.successCount when the refresh that found nothing began
	failures int
	nextAt   int64 // backoff: not before this second
}

func NewKeepAlive(o KeepAliveOptions) *KeepAlive {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Every <= 0 {
		o.Every = keepAliveEvery
	}
	return &KeepAlive{o: o, state: map[string]keepAliveState{}}
}

// Run sweeps once at start, then every Every, until ctx ends.
func (k *KeepAlive) Run(ctx context.Context) error {
	k.Sweep(ctx, TriggerStart)
	ticker := time.NewTicker(k.o.Every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			k.Sweep(ctx, TriggerKeepAlive)
		}
	}
}

// Sweep refreshes each due target in turn, checking ctx between them.
func (k *KeepAlive) Sweep(ctx context.Context, trigger string) {
	if ctx.Err() != nil {
		return
	}
	targets, err := k.o.Targets(ctx)
	if err != nil {
		return
	}
	accounts := make([]string, 0, len(targets))
	for _, t := range targets {
		accounts = append(accounts, t.Account)
	}
	k.mu.Lock()
	k.swept, k.targets = true, accounts
	for account := range k.state {
		if !slices.Contains(accounts, account) {
			delete(k.state, account)
		}
	}
	k.mu.Unlock()
	for _, t := range targets {
		if ctx.Err() != nil {
			return
		}
		if k.due(t) {
			mark := k.o.Health.successCount(t.Account)
			k.settle(t.Account, mark, k.o.Refresh(ctx, t.Account, trigger))
		}
	}
}

// HasSessions reports whether any target may hold a session worth keeping
// alive: true before the first sweep, then any target that is not dormant and
// whose history does not end in a logout or terminal failure.
func (k *KeepAlive) HasSessions() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.swept {
		return true
	}
	for _, account := range k.targets {
		if !k.dormantLocked(account) && !k.o.Health.ended(account) {
			return true
		}
	}
	return false
}

func (k *KeepAlive) due(t KeepAliveTarget) bool {
	now := k.o.Now().Unix()
	k.mu.Lock()
	dormant, s := k.dormantLocked(t.Account), k.state[t.Account]
	k.mu.Unlock()
	if dormant {
		return false
	}
	// A backoff more than its cap away means the clock went back.
	if s.nextAt > now && s.nextAt-now <= int64(keepAliveMaxDelay/time.Second) {
		return false
	}
	last := k.o.Health.LastSuccess(t.Account)
	if last.IsZero() || k.o.Health.ended(t.Account) {
		return true
	}
	elapsed := now - last.Unix()
	return elapsed < 0 || elapsed >= int64(t.Interval/time.Second)
}

// dormantLocked reports whether account is dormant; any sign-in or refresh
// recorded since the refresh that found nothing began wakes it. It counts
// events rather than comparing times, so neither a sign-in in the same second
// nor a wall clock moved back keeps a new session dormant.
func (k *KeepAlive) dormantLocked(account string) bool {
	s := k.state[account]
	if !s.dormant {
		return false
	}
	if k.o.Health.successCount(account) != s.mark {
		delete(k.state, account)
		return false
	}
	return true
}

func (k *KeepAlive) settle(account string, mark uint64, err error) {
	now := k.o.Now().Unix()
	k.mu.Lock()
	defer k.mu.Unlock()
	var oe *output.Error
	switch {
	case err == nil:
		delete(k.state, account)
	case errors.Is(err, ErrKeepAliveBusy):
	case errors.Is(err, ErrKeepAliveDormant), errors.As(err, &oe) && oe.Code == "auth_required":
		k.state[account] = keepAliveState{dormant: true, mark: mark}
	default:
		s := k.state[account]
		s.failures++
		delay := keepAliveBackoff
		for i := 1; i < s.failures && delay < keepAliveMaxDelay; i++ {
			delay *= 2
		}
		s.nextAt = now + int64(min(delay, keepAliveMaxDelay)/time.Second)
		k.state[account] = s
	}
}

// ended reports whether the account's history ends in a logout or a terminal
// failure, so no session is left to refresh.
func (h *Health) ended(account string) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	events := h.connLocked(account).Events
	if len(events) == 0 {
		return false
	}
	last := events[len(events)-1]
	return last.Kind == HealthLogout || last.Terminal
}
