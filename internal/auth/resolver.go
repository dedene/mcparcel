package auth

import (
	"context"
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
	id                         string
	client                     SecretClient
	started, deadline, lastNow time.Time
	expired                    bool
	identity                   string
	generation                 uint64
	values                     map[string]cachedValue
	history                    map[string]string
	op                         *operation
}
type operation struct {
	ctx                    context.Context
	cancel                 context.CancelFunc
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
	ctx, cancel := context.WithCancel(context.Background())
	return &resolver{opts: opts, ctx: ctx, cancel: cancel, states: map[string]*profileState{}}
}

func sessionDuration(profile config.Profile) time.Duration {
	d, err := time.ParseDuration(profile.SessionDuration)
	if err != nil || d <= 0 || d > 24*time.Hour {
		return 24 * time.Hour
	}
	return d
}

func newIdentity() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", ErrProvider
	}
	return hex.EncodeToString(b[:]), nil
}

func (s *profileState) clear() {
	s.client = nil
	s.values = nil
	s.history = nil
	s.identity = ""
	s.generation = 0
}

func (s *profileState) checkExpiry(now time.Time) {
	if s.client != nil && (now.Round(0).Before(s.lastNow.Round(0)) || !now.Round(0).Before(s.deadline.Round(0)) || now.Sub(s.started) >= s.deadline.Sub(s.started)) {
		s.clear()
		s.expired = true
	}
	s.lastNow = now
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
		state := r.states[key]
		if state == nil {
			state = &profileState{id: id}
			r.states[key] = state
		}
		now := r.opts.Now()
		state.checkExpiry(now)
		if noInput && state.client == nil {
			failure := ErrRequired
			if state.expired {
				failure = ErrExpired
			}
			r.mu.Unlock()
			return Lease{}, failure
		}
		if state.client != nil {
			values := make(map[string]string, len(refs))
			expiry := now.Add(r.opts.LeaseDuration)
			if state.deadline.Before(expiry) {
				expiry = state.deadline
			}
			complete := true
			for _, ref := range refs {
				v, ok := state.values[ref]
				if !ok || !now.Before(v.expires) {
					complete = false
					break
				}
				values[ref] = v.value
				if v.expires.Before(expiry) {
					expiry = v.expires
				}
			}
			if complete {
				lease := Lease{Identity: state.identity + ":" + strconv.FormatUint(state.generation, 10), ExpiresAt: expiry, SessionExpiresAt: state.deadline, Values: values}
				r.mu.Unlock()
				return lease, nil
			}
		}
		op := state.op
		if op == nil {
			opCtx, cancel := context.WithTimeout(r.ctx, r.opts.AuthTimeout)
			op = &operation{ctx: opCtx, cancel: cancel, done: make(chan struct{})}
			state.op = op
			missing := []string{}
			for _, ref := range refs {
				v, ok := state.values[ref]
				if !ok || !now.Before(v.expires) {
					missing = append(missing, ref)
				}
			}
			existing, started := state.client, state.started
			go r.run(state, op, profile, missing, existing, started)
		}
		if op.abandoned {
			r.mu.Unlock()
			return Lease{}, ErrProvider
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
		op.err = op.ctx.Err()
		op.quarantined = true
		state.clear()
		close(op.done)
		r.mu.Unlock()
		// The record remains until the underlying SDK worker actually returns.
		<-result
		r.mu.Lock()
		if state.op == op {
			state.op = nil
		}
		r.mu.Unlock()
	}
	op.cancel()
}

func (r *resolver) finish(state *profileState, op *operation, profile config.Profile, out operationResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || op.abandoned || op.waiters == 0 || op.ctx.Err() != nil {
		out.err = op.ctx.Err()
		if out.err == nil {
			out.err = context.Canceled
		}
		state.clear()
	}
	now := r.opts.Now()
	deadline := out.started.Add(sessionDuration(profile))
	if out.err == nil && (now.Round(0).Before(out.started.Round(0)) || now.Round(0).Before(state.lastNow.Round(0)) || !now.Round(0).Before(deadline.Round(0)) || now.Sub(out.started) >= sessionDuration(profile) || state.expired && !out.bootstrapped) {
		out.err = ErrExpired
		state.expired = true
	}
	if out.err == nil {
		if state.client == nil {
			identity, err := newIdentity()
			if err != nil {
				out.err = err
			} else {
				state.identity = identity
				state.generation = 1
				state.values = map[string]cachedValue{}
				state.history = map[string]string{}
				state.started = out.started
				state.deadline = deadline
				state.expired = false
			}
		}
		if out.err == nil {
			changed := false
			expires := now.Add(r.opts.LeaseDuration)
			if deadline.Before(expires) {
				expires = deadline
			}
			for ref, value := range out.values {
				if old, ok := state.history[ref]; ok && old != value {
					changed = true
				}
				state.history[ref] = value
				state.values[ref] = cachedValue{value, expires}
			}
			if changed {
				state.generation++
			}
			state.client = out.client
			state.lastNow = now
		}
	}
	if out.err != nil {
		state.clear()
	}
	op.err = out.err
	state.op = nil
	close(op.done)
}

func (r *resolver) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
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
	return nil
}
