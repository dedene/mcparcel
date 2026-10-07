package runtime

import (
	"context"
	"slices"
	"time"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
)

// keepAliveSaveRetries are the waits before each retry of a temporary
// handler's failed save; a variable so tests can shorten them.
var keepAliveSaveRetries = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

// RunKeepAlive refreshes stored OAuth sessions in the background: once at
// daemon start, then every five minutes for the ones that are due. It never
// signs in. Without a health log there is no history to schedule from, so it
// does nothing. Headless mode stores no session, so neither does it.
func (p *pool) RunKeepAlive(ctx context.Context) error {
	if p.opts.Health == nil || p.opts.Headless {
		return nil
	}
	return p.keep.Run(ctx)
}

// StayAlive reports whether the daemon should skip its idle exit:
// runtime.keepAlive is set and an OAuth session may still need refreshing.
func (p *pool) StayAlive() bool {
	if p.opts.Health == nil || p.opts.Headless {
		return false
	}
	snapshot, err := p.opts.Load(p.opts.Paths)
	if err != nil || snapshot.Local.Runtime == nil || !snapshot.Local.Runtime.KeepAlive {
		return false
	}
	return p.keep.HasSessions()
}

// keepAliveTargets lists the runnable OAuth-capable connections whose
// lifecycle.keepAlive is not "off". A client_credentials connection has no
// stored session to keep alive: its token is minted when a call needs it.
func (p *pool) keepAliveTargets(context.Context) ([]auth.KeepAliveTarget, error) {
	snapshot, err := p.opts.Load(p.opts.Paths)
	if err != nil {
		return nil, err
	}
	if snapshot.Effective == nil {
		return nil, nil
	}
	ids := make([]string, 0, len(snapshot.Effective.Connections))
	for id := range snapshot.Effective.Connections {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var targets []auth.KeepAliveTarget
	for _, id := range ids {
		canonical, c, err := snapshot.RuntimeConnection(id)
		if err != nil || !oauthCapable(c) || clientCredentials(c) {
			continue
		}
		if interval, ok := auth.KeepAliveInterval(keepAliveSetting(c)); ok {
			targets = append(targets, auth.KeepAliveTarget{Account: canonical, Interval: interval})
		}
	}
	return targets, nil
}

func keepAliveSetting(c config.Connection) string {
	if c.Lifecycle == nil {
		return ""
	}
	return c.Lifecycle.KeepAlive
}

// refreshStored refreshes one connection's session without waiting for a busy
// connection or prompting for anything; it waits only to retry a failed save
// of a rotated refresh token. It holds the connection's gate, so logout and
// sign-in never run at the same time, and counts as pool work, so shutdown
// waits for a rotated refresh token to be saved.
func (p *pool) refreshStored(ctx context.Context, canonical, trigger string) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return context.Canceled
	}
	p.workers.Add(1)
	p.mu.Unlock()
	defer p.workers.Done()
	gate := p.gate(canonical)
	select {
	case <-gate:
	default:
		return auth.ErrKeepAliveBusy
	}
	defer func() { gate <- struct{}{} }()
	snapshot, err := p.opts.Load(p.opts.Paths)
	if err != nil {
		return err
	}
	_, c, err := snapshot.RuntimeConnection(canonical)
	if err != nil || !oauthCapable(c) {
		return auth.ErrKeepAliveDormant
	}
	p.mu.Lock()
	entry, closed := p.entries[canonical], p.closed
	p.mu.Unlock()
	if closed {
		// Shutdown empties the pool without taking gates; its handlers may
		// still be saving a rotated token the Keychain does not hold yet.
		return context.Canceled
	}
	if entry != nil && entry.oauth != nil && entry.ctx.Err() == nil {
		// The pooled handler holds the current refresh token in memory.
		return entry.oauth.Refresh(trigger)
	}
	if len(config.SecretRefs(c)) > 0 {
		// Resolving a 1Password reference could prompt.
		return auth.ErrKeepAliveDormant
	}
	// env: values come from the login environment only, never the Keychain.
	values, err := envRefValues(ctx, p.opts.LoginEnv, nil, p.opts.Headless, c, nil)
	if err != nil {
		return auth.ErrKeepAliveDormant
	}
	h, err := p.oauthHandler(ctx, canonical, canonical, c, values, nil)
	if err != nil {
		return err
	}
	if h == nil {
		return auth.ErrKeepAliveDormant
	}
	defer h.Close() // which tries the save once more
	err = h.Refresh(trigger)
	// This handler holds the only copy of a rotated refresh token whose save
	// failed: retry before it is dropped.
	for _, wait := range keepAliveSaveRetries {
		if err == nil || !h.Unsaved() {
			break
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
		err = h.SavePending()
	}
	return err
}
