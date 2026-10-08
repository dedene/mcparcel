package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"sync"
	"time"
)

const (
	// A token 1Password refused is not sent again for tokenBackoff, doubling
	// on each further refusal up to tokenBackoffMax.
	tokenBackoff    = 30 * time.Second
	tokenBackoffMax = 10 * time.Minute
	// tokenFailuresMax bounds the cache; the oldest entry is dropped first.
	tokenFailuresMax = 64
)

type tokenFailure struct {
	at, until time.Time
	backoff   time.Duration
}

// bootstrapFailures is the negative cache for service-account bootstraps. It
// is keyed by an HMAC of the token under a random per-process key, so it never
// holds the token, and lives in memory only.
type bootstrapFailures struct {
	mu      sync.Mutex
	key     []byte
	entries map[[32]byte]tokenFailure
}

func (f *bootstrapFailures) fingerprint(token string) [32]byte {
	var out [32]byte
	mac := hmac.New(sha256.New, f.key)
	mac.Write([]byte(token))
	copy(out[:], mac.Sum(nil))
	return out
}

// blocked reports whether fp failed recently enough to be refused without a
// call.
func (f *bootstrapFailures) blocked(fp [32]byte, now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.entries[fp]
	return ok && !Expired(now, e.until)
}

// record notes a refusal of fp and doubles its backoff.
func (f *bootstrapFailures) record(fp [32]byte, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.entries == nil {
		f.entries = map[[32]byte]tokenFailure{}
	}
	e, ok := f.entries[fp]
	switch {
	case !ok:
		e.backoff = tokenBackoff
		for len(f.entries) >= tokenFailuresMax {
			f.dropOldest()
		}
	case e.backoff < tokenBackoffMax/2:
		e.backoff *= 2
	default:
		e.backoff = tokenBackoffMax
	}
	e.at, e.until = now, now.Add(e.backoff)
	f.entries[fp] = e
}

// forget clears fp after a successful bootstrap.
func (f *bootstrapFailures) forget(fp [32]byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.entries, fp)
}

// dropOldest removes the least recently recorded entry; f.mu is held.
func (f *bootstrapFailures) dropOldest() {
	var oldest [32]byte
	first := true
	for fp, e := range f.entries {
		if first || e.at.Before(f.entries[oldest].at) {
			oldest, first = fp, false
		}
	}
	delete(f.entries, oldest)
}
