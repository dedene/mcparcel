package auth

import (
	"sort"
	"time"
)

// SessionInfo describes one credential profile session. It never carries the
// account, a reference or a value.
type SessionInfo struct {
	Profile   string
	Mode      string
	State     string // "active" or "expired"
	ExpiresAt time.Time
}

// Lock ends every session. In-flight provider work is canceled with cause
// ErrLocked and its late result is dropped. A quarantined operation (a provider
// call that ignored cancellation) stays and keeps blocking a second bootstrap
// for its profile until the call returns.
func (r *resolver) Lock() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, s := range r.states {
		s.clear()
		s.expired = false
		if s.op == nil {
			delete(r.states, key)
			continue
		}
		if !s.op.quarantined {
			s.op.cancelCause(ErrLocked)
		}
	}
}

// Invalidate drops the cached values of refs for profileID, so the next
// Resolve reads them again through the existing session. The digests stay, so
// a changed value still changes the lease identity. A read already in flight
// may still cache what it read.
func (r *resolver) Invalidate(profileID string, refs []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.states {
		if s.id != profileID {
			continue
		}
		for _, ref := range refs {
			delete(s.values, ref)
		}
	}
}

// Sessions reports active and expired sessions, sorted by profile.
func (r *resolver) Sessions() []SessionInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.sweep(r.opts.Now())
	var out []SessionInfo
	for _, s := range r.states {
		switch {
		case s.client != nil:
			out = append(out, SessionInfo{Profile: s.id, Mode: s.mode, State: "active", ExpiresAt: s.deadline})
		case s.expired:
			out = append(out, SessionInfo{Profile: s.id, Mode: s.mode, State: "expired", ExpiresAt: s.deadline})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Profile < out[j].Profile })
	return out
}

// sweep deletes cached values past their lease and ends sessions past their
// deadline. r.mu must be held.
func (r *resolver) sweep(now time.Time) {
	for _, s := range r.states {
		for ref, v := range s.values {
			if Expired(now, v.expires) {
				delete(s.values, ref)
			}
		}
		s.checkExpiry(now)
	}
}

// sweepLoop purges expired values and sessions without waiting for a call, so
// no value outlives its lease by more than SweepEvery of awake time.
func (r *resolver) sweepLoop() {
	defer close(r.swept)
	tick := time.NewTicker(r.opts.SweepEvery)
	defer tick.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-tick.C:
			r.mu.Lock()
			if !r.closed {
				r.sweep(r.opts.Now())
			}
			r.mu.Unlock()
		}
	}
}
