package auth

import (
	"maps"
	"time"
)

// Lease is a short-lived view of resolved secret values. It lives in process
// memory only. The values sit behind an unexported closure: encoding/json skips
// it and fmt prints only a func address with any verb (a pointer would be
// dereferenced by fmt's bad-verb output for %s). Lease deliberately has no
// String, GoString, Format, MarshalJSON or MarshalText method.
type Lease struct {
	// Identity is the session ID plus the summed versions of the requested
	// references, so it changes only when one of those values changed.
	Identity         string
	ExpiresAt        time.Time
	SessionExpiresAt time.Time
	values           func() map[string]string
}

func leaseValues(m map[string]string) func() map[string]string {
	return func() map[string]string { return m }
}

// Secrets returns a fresh copy of the leased values keyed by reference, or nil
// when the lease carries none.
func (l Lease) Secrets() map[string]string {
	if l.values == nil {
		return nil
	}
	m := l.values()
	if len(m) == 0 {
		return nil
	}
	return maps.Clone(m)
}

// Expired reports whether deadline has passed on either the wall or the
// monotonic clock. The monotonic clock stops while a Mac sleeps, so a check on
// it alone would stretch caches and sessions across a sleep.
func Expired(now, deadline time.Time) bool {
	return !now.Round(0).Before(deadline.Round(0)) || !now.Before(deadline)
}
