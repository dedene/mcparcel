package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/dedene/mcparcel/internal/config"
)

type cachedValue struct {
	value   string
	expires time.Time
}
type profileState struct {
	id, mode                   string
	client                     SecretClient
	started, deadline, lastNow time.Time
	expired                    bool
	// superseded marks a state whose profile changed while its operation was
	// in flight; the operation's result is dropped and the state removed.
	superseded bool
	identity   string
	// versions counts value changes per reference; history holds a keyed
	// digest of the last value, never the value itself.
	versions  map[string]uint64
	history   map[string][32]byte
	digestKey []byte
	values    map[string]cachedValue
	op        *operation
}
type operation struct {
	ctx                    context.Context
	cancel                 context.CancelFunc
	cancelCause            context.CancelCauseFunc
	done                   chan struct{}
	waiters                int
	abandoned, quarantined bool
	err                    error
}
type operationResult struct {
	bootstrapped bool
	client       SecretClient
	started      time.Time
	values       map[string]string
	err          error
}
type resolver struct {
	mu     sync.Mutex
	opts   ResolverOptions
	ctx    context.Context
	cancel context.CancelFunc
	closed bool
	states map[string]*profileState
	swept  chan struct{}
}

func NewResolver(opts ResolverOptions) Resolver {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.AuthTimeout <= 0 || opts.AuthTimeout > 120*time.Second {
		opts.AuthTimeout = 120 * time.Second
	}
	if opts.LeaseDuration <= 0 || opts.LeaseDuration > 5*time.Minute {
		opts.LeaseDuration = 5 * time.Minute
	}
	if opts.SweepEvery <= 0 || opts.SweepEvery > time.Minute {
		opts.SweepEvery = time.Minute
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &resolver{opts: opts, ctx: ctx, cancel: cancel, states: map[string]*profileState{}, swept: make(chan struct{})}
	go r.sweepLoop()
	return r
}

func sessionDuration(profile config.Profile) time.Duration {
	d, err := time.ParseDuration(profile.SessionDuration)
	if err != nil || d <= 0 || d > 24*time.Hour {
		return 24 * time.Hour
	}
	return d
}

// newSession returns a random session ID and a random key for the value
// digests of that session.
func newSession() (string, []byte, error) {
	var b [16]byte
	key := make([]byte, 32)
	if _, err := rand.Read(b[:]); err != nil {
		return "", nil, ErrProvider
	}
	if _, err := rand.Read(key); err != nil {
		return "", nil, ErrProvider
	}
	return hex.EncodeToString(b[:]), key, nil
}

func (s *profileState) clear() {
	s.client = nil
	s.values = nil
	s.history = nil
	s.versions = nil
	s.digestKey = nil
	s.identity = ""
}

func (s *profileState) checkExpiry(now time.Time) {
	if s.client != nil && (now.Round(0).Before(s.lastNow.Round(0)) || Expired(now, s.deadline)) {
		s.clear()
		s.expired = true
	}
	s.lastNow = now
}

func (s *profileState) digest(value string) [32]byte {
	var out [32]byte
	mac := hmac.New(sha256.New, s.digestKey)
	mac.Write([]byte(value))
	copy(out[:], mac.Sum(nil))
	return out
}

// lease builds a lease from the cache when every ref holds a live value.
func (s *profileState) lease(refs []string, now time.Time, duration time.Duration) (Lease, bool) {
	if s.client == nil {
		return Lease{}, false
	}
	values := make(map[string]string, len(refs))
	expiry := now.Add(duration)
	if s.deadline.Before(expiry) {
		expiry = s.deadline
	}
	var version uint64
	for _, ref := range refs {
		v, ok := s.values[ref]
		if !ok || Expired(now, v.expires) {
			return Lease{}, false
		}
		values[ref] = v.value
		version += s.versions[ref]
		if v.expires.Before(expiry) {
			expiry = v.expires
		}
	}
	return Lease{Identity: s.identity + ":" + strconv.FormatUint(version, 10), ExpiresAt: expiry, SessionExpiresAt: s.deadline, values: leaseValues(values)}, true
}

func (s *profileState) missing(refs []string, now time.Time) []string {
	out := []string{}
	for _, ref := range refs {
		if v, ok := s.values[ref]; !ok || Expired(now, v.expires) {
			out = append(out, ref)
		}
	}
	return out
}

// state returns the state for key. A new key for a known profile ID means the
// profile changed, so the old profile's sessions end: idle states are dropped
// and busy ones are removed once their operation finishes.
func (r *resolver) state(key, id, mode string) *profileState {
	if s := r.states[key]; s != nil {
		return s
	}
	for k, other := range r.states {
		if other.id != id {
			continue
		}
		other.clear()
		if other.op == nil {
			delete(r.states, k)
		} else {
			other.superseded = true
		}
	}
	s := &profileState{id: id, mode: mode}
	r.states[key] = s
	return s
}

func (r *resolver) Resolve(ctx context.Context, id string, profile config.Profile, refs []string, noInput bool) (Lease, error) {
	data, err := json.Marshal(profile)
	if err != nil {
		return Lease{}, ErrProvider
	}
	sum := sha256.Sum256(data)
	key := id + ":" + hex.EncodeToString(sum[:])
	unique := map[string]bool{}
	for _, ref := range refs {
		unique[ref] = true
	}
	refs = make([]string, 0, len(unique))
	for ref := range unique {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for {
		if ctx.Err() != nil {
			return Lease{}, ctx.Err()
		}
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			return Lease{}, ErrProvider
		}
		for _, other := range r.states {
			if other.id == id && other.op != nil && other.op.quarantined {
				r.mu.Unlock()
				return Lease{}, ErrProvider
			}
		}
		state := r.state(key, id, profile.Mode)
		now := r.opts.Now()
		r.sweep(now)
		if lease, ok := state.lease(refs, now, r.opts.LeaseDuration); ok {
			r.mu.Unlock()
			return lease, nil
		}
		// --no-input never reaches the provider without a session. In desktop
		// mode the SDK may re-authorize (and prompt) on any read, so --no-input
		// is served from the cache only.
		if noInput && (state.client == nil || profile.Mode == "desktop") {
			failure := ErrRequired
			if state.expired {
				failure = ErrExpired
			}
			r.mu.Unlock()
			return Lease{}, failure
		}
		op := state.op
		if op != nil && op.ctx.Err() != nil {
			// A locked or abandoned operation is winding down; its result will
			// be dropped. Wait for it, then look again.
			done := op.done
			r.mu.Unlock()
			select {
			case <-ctx.Done():
				return Lease{}, ctx.Err()
			case <-done:
			}
			continue
		}
		if op == nil {
			causeCtx, cancelCause := context.WithCancelCause(r.ctx)
			opCtx, cancel := context.WithTimeout(causeCtx, r.opts.AuthTimeout)
			op = &operation{ctx: opCtx, cancel: cancel, cancelCause: cancelCause, done: make(chan struct{})}
			state.op = op
			go r.run(state, op, profile, state.missing(refs, now), state.client, state.started)
		}
		op.waiters++
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			r.leave(state, op)
			return Lease{}, ctx.Err()
		case <-op.done:
			r.mu.Lock()
			op.waiters--
			failure := op.err
			r.mu.Unlock()
			if ctx.Err() != nil {
				return Lease{}, ctx.Err()
			}
			if failure != nil {
				return Lease{}, failure
			}
		}
	}
}

func (r *resolver) leave(state *profileState, op *operation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	op.waiters--
	if op.waiters == 0 && state.op == op {
		op.abandoned = true
		op.cancel()
	}
}

func (r *resolver) run(state *profileState, op *operation, profile config.Profile, refs []string, existing SecretClient, started time.Time) {
	result := make(chan operationResult, 1)
	go func() {
		out := operationResult{client: existing, started: started, values: map[string]string{}}
		if out.client == nil {
			out.bootstrapped = true
			if r.opts.Provider == nil {
				out.err = ErrProvider
			} else {
				out.client, out.err = r.opts.Provider.Bootstrap(op.ctx, profile)
			}
			if out.err == nil {
				out.started = r.opts.Now()
			}
			if out.client == nil && out.err == nil {
				out.err = ErrProvider
			}
		}
		for _, ref := range refs {
			if out.err != nil {
				break
			}
			if op.ctx.Err() != nil {
				out.err = op.ctx.Err()
				break
			}
			out.values[ref], out.err = out.client.Resolve(op.ctx, ref)
		}
		if out.err != nil {
			out.err = safeProviderError(op.ctx, out.err)
			out.values = nil
			out.client = nil
		}
		result <- out
	}()
	select {
	case out := <-result:
		r.finish(state, op, profile, out)
	case <-op.ctx.Done():
		r.mu.Lock()
		op.err = context.Cause(op.ctx)
		op.quarantined = true
		state.clear()
		close(op.done)
		r.mu.Unlock()
		// The record remains until the underlying SDK worker actually returns.
		<-result
		r.mu.Lock()
		if state.op == op {
			state.op = nil
			r.dropSuperseded(state)
		}
		r.mu.Unlock()
	}
	op.cancel()
	op.cancelCause(nil)
}

func (r *resolver) finish(state *profileState, op *operation, profile config.Profile, out operationResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || op.abandoned || op.waiters == 0 || op.ctx.Err() != nil {
		out.err = context.Cause(op.ctx)
		if out.err == nil {
			out.err = context.Canceled
		}
		state.clear()
	}
	now := r.opts.Now()
	deadline := out.started.Add(sessionDuration(profile))
	if out.err == nil && state.superseded {
		out.err = ErrExpired
	}
	if out.err == nil && (now.Round(0).Before(out.started.Round(0)) || now.Round(0).Before(state.lastNow.Round(0)) || !now.Round(0).Before(deadline.Round(0)) || now.Sub(out.started) >= sessionDuration(profile) || state.expired && !out.bootstrapped) {
		out.err = ErrExpired
		state.expired = true
	}
	if out.err == nil {
		if state.client == nil {
			identity, digestKey, err := newSession()
			if err != nil {
				out.err = err
			} else {
				state.identity = identity
				state.digestKey = digestKey
				state.versions = map[string]uint64{}
				state.values = map[string]cachedValue{}
				state.history = map[string][32]byte{}
				state.started = out.started
				state.deadline = deadline
				state.expired = false
			}
		}
		if out.err == nil {
			expires := now.Add(r.opts.LeaseDuration)
			if deadline.Before(expires) {
				expires = deadline
			}
			for ref, value := range out.values {
				digest := state.digest(value)
				if old, ok := state.history[ref]; ok && old != digest {
					state.versions[ref]++
				}
				state.history[ref] = digest
				state.values[ref] = cachedValue{value, expires}
			}
			state.client = out.client
			state.lastNow = now
		}
	}
	// A rate limit refused this request only; the session stays.
	if out.err != nil && out.err != ErrRateLimited {
		state.clear()
	}
	op.err = out.err
	state.op = nil
	r.dropSuperseded(state)
	close(op.done)
}

// dropSuperseded removes an idle superseded state; r.mu is held.
func (r *resolver) dropSuperseded(state *profileState) {
	if !state.superseded {
		return
	}
	for k, s := range r.states {
		if s == state {
			delete(r.states, k)
		}
	}
}

func (r *resolver) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.cancel()
	for _, state := range r.states {
		state.clear()
		if state.op != nil {
			state.op.abandoned = true
			state.op.cancel()
		}
	}
	r.states = nil
	r.mu.Unlock()
	<-r.swept
	return nil
}
