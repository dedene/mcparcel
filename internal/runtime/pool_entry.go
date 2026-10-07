package runtime

import (
	"context"
	"sync"
	"time"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/mcpclient"
	"github.com/dedene/mcparcel/internal/output"
)

type poolEntry struct {
	hash, identity string
	session        mcpclient.Session
	ctx            context.Context
	cancel         context.CancelCauseFunc
	oauth          *auth.OAuthHandler
	cc             *auth.ClientCredentials // client_credentials token, dropped with the entry
	closeOnce      sync.Once
	sweeping       bool // a sweep is waiting to retire it; guarded by pool.mu
}

// session returns the pooled entry for id, connecting when needed. login
// non-nil always replaces the entry with a new sign-in. epoch is the work's
// lock epoch: a protected entry is never installed after an auth lock that
// ran while it was connecting.
func (p *pool) session(ctx context.Context, id, hash string, c config.Connection, lease auth.Lease, gate chan struct{}, name string, login *auth.LoginOptions, epoch uint64) (*poolEntry, *auth.OAuthHandler, error) {
	p.mu.Lock()
	old := p.entries[id]
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return nil, nil, output.NewError("canceled", nil)
	}
	if login == nil && old != nil && old.hash == hash && old.identity == lease.Identity && old.ctx.Err() == nil {
		return old, old.oauth, nil
	}
	if old != nil {
		if old.hash == hash && old.identity != lease.Identity && lease.Identity != "public" {
			p.opts.Log("credential_rotated")
		}
		p.retire(id, old)
	}
	values, e := envRefValues(ctx, p.opts.LoginEnv, p.opts.Keychain, p.opts.Headless, c, lease.Secrets())
	if e != nil {
		return nil, nil, e
	}
	env, e := BuildChildEnv(p.opts.LoginEnv, c, values)
	if e != nil {
		return nil, nil, e
	}
	headers := map[string]string{}
	if c.Transport.HTTP != nil {
		for k, v := range c.Transport.HTTP.Headers {
			if v.Literal != nil {
				headers[k] = *v.Literal
			} else if v.Secret != nil {
				value, ok := values[v.Secret.Secret]
				if !ok {
					return nil, nil, auth.ErrProvider
				}
				headers[k] = v.Secret.Prefix + value + v.Secret.Suffix
			} else {
				return nil, nil, config.ErrConfig
			}
		}
	}
	var handler *auth.OAuthHandler
	var cc *auth.ClientCredentials
	if clientCredentials(c) {
		cc, e = p.clientCredentialsHandler(c, values)
	} else {
		handler, e = p.oauthHandler(ctx, id, name, c, values, login)
	}
	if e != nil {
		return nil, nil, e
	}
	timeout := startupTimeout(p.opts.ConnectTimeout, c)
	if login != nil {
		timeout = loginTimeout
	}
	connectCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	opts := mcpclient.ConnectOptions{Connection: c, Env: env, Headers: headers, Home: p.opts.Paths.Home, Version: p.opts.Version, ConnectTimeout: timeout, ShutdownTimeout: p.opts.ShutdownTimeout}
	if handler != nil {
		opts.OAuth = handler
	} else if cc != nil {
		opts.OAuth = cc
	}
	session, e := p.opts.Connect(connectCtx, opts)
	if e != nil || session == nil {
		if handler != nil {
			handler.Close()
		}
		if cc != nil {
			cc.Close()
		}
		if e == nil {
			e = output.NewError("internal_error", nil)
		}
		return nil, nil, e
	}
	entryCtx, stop := context.WithCancelCause(context.Background())
	entry := &poolEntry{hash: hash, identity: lease.Identity, session: session, ctx: entryCtx, cancel: stop, oauth: handler, cc: cc}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		entry.close(p, context.Background())
		return nil, nil, output.NewError("canceled", nil)
	}
	if (lease.Identity != "public" || handler != nil) && epoch != p.lockEpoch {
		p.mu.Unlock()
		entry.close(p, context.Background())
		return nil, nil, auth.ErrLocked
	}
	p.entries[id] = entry
	if lease.Identity != "public" {
		p.expiry.Add(1)
		go p.watchExpiry(id, entry, lease.SessionExpiresAt, gate)
	}
	p.mu.Unlock()
	p.opts.Log("connection_opened")
	return entry, handler, nil
}

// watchExpiry retires a protected entry once its credential session ends. It
// checks every ExpiryCheck rather than sleeping until the deadline: a timer
// stops while a Mac sleeps, so only a check on the wall clock notices a
// deadline passed during sleep. A wall clock stepped back ends the session too,
// as it does in the resolver.
func (p *pool) watchExpiry(id string, entry *poolEntry, deadline time.Time, gate chan struct{}) {
	defer p.expiry.Done()
	last := p.opts.Now()
	for {
		now := p.opts.Now()
		if auth.Expired(now, deadline) || now.Round(0).Before(last.Round(0)) {
			break
		}
		last = now
		wait := min(p.opts.ExpiryCheck, max(deadline.Sub(now), time.Millisecond))
		timer := time.NewTimer(wait)
		select {
		case <-entry.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
	entry.cancel(auth.ErrExpired)
	<-gate
	defer func() { gate <- struct{}{} }()
	p.retire(id, entry)
}

// startupTimeout bounds connect, initialize and tool listing: the
// connection's startupTimeout when set, else the pool default.
func startupTimeout(base time.Duration, c config.Connection) time.Duration {
	if d, e := time.ParseDuration(c.StartupTimeout); e == nil && d > 0 {
		return d
	}
	return base
}

// sweep retires pooled sessions the snapshot no longer admits (disabled,
// removed, under review, unsupported) or whose config hash changed. Each
// retire waits for the connection's gate, so an in-flight call drains first,
// and rechecks the then-current config so a newer entry is never retired.
func (p *pool) sweep(snapshot config.Snapshot) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	entries := make(map[string]*poolEntry, len(p.entries))
	for id, entry := range p.entries {
		entries[id] = entry
	}
	p.mu.Unlock()
	var stale []string
	for id, entry := range entries {
		if staleEntry(snapshot, id, entry) {
			stale = append(stale, id)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, id := range stale {
		entry := entries[id]
		if p.closed || p.entries[id] != entry || entry.sweeping {
			continue
		}
		entry.sweeping = true
		p.sweeps.Add(1)
		go func() {
			defer p.sweeps.Done()
			gate := p.gate(id)
			<-gate
			defer func() { gate <- struct{}{} }()
			current, e := p.opts.Load(p.opts.Paths)
			if e != nil || !staleEntry(current, id, entry) {
				p.mu.Lock()
				entry.sweeping = false
				p.mu.Unlock()
				return
			}
			p.retire(id, entry)
		}()
	}
}

func staleEntry(snapshot config.Snapshot, id string, entry *poolEntry) bool {
	if _, _, e := snapshot.RuntimeConnection(id); e != nil {
		return true
	}
	hash, e := snapshot.ConnectionHash(id)
	return e != nil || hash != entry.hash
}

func (p *pool) retire(id string, entry *poolEntry) {
	p.mu.Lock()
	if p.entries[id] == entry {
		delete(p.entries, id)
	}
	p.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), p.opts.ShutdownTimeout)
	defer cancel()
	entry.close(p, ctx)
}

func (e *poolEntry) close(p *pool, ctx context.Context) {
	e.closeOnce.Do(func() {
		e.cancel(context.Canceled)
		if e.oauth != nil {
			e.oauth.Close()
		}
		if e.cc != nil {
			e.cc.Close()
		}
		bounded, cancel := context.WithTimeout(ctx, p.opts.ShutdownTimeout)
		defer cancel()
		_ = e.session.Close(bounded)
		p.opts.Log("connection_closed")
	})
}
