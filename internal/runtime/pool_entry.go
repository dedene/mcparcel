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
	timer          *time.Timer
	oauth          *auth.OAuthHandler
	closeOnce      sync.Once
	sweeping       bool // a sweep is waiting to retire it; guarded by pool.mu
}

// session returns the pooled entry for id, connecting when needed. login
// non-nil always replaces the entry with a new sign-in.
func (p *pool) session(ctx context.Context, id, hash string, c config.Connection, lease auth.Lease, gate chan struct{}, name string, login *auth.LoginOptions) (*poolEntry, *auth.OAuthHandler, error) {
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
		p.retire(id, old)
	}
	values, e := envRefValues(ctx, p.opts.LoginEnv, p.opts.Keychain, c, lease.Values)
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
	handler, e := p.oauthHandler(ctx, id, name, c, values, login)
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
	}
	session, e := p.opts.Connect(connectCtx, opts)
	if e != nil {
		if handler != nil {
			handler.Close()
		}
		return nil, nil, e
	}
	if session == nil {
		return nil, nil, output.NewError("internal_error", nil)
	}
	entryCtx, stop := context.WithCancelCause(context.Background())
	entry := &poolEntry{hash: hash, identity: lease.Identity, session: session, ctx: entryCtx, cancel: stop, oauth: handler}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		entry.close(p, context.Background())
		return nil, nil, output.NewError("canceled", nil)
	}
	p.entries[id] = entry
	if lease.Identity != "public" {
		p.expiry.Add(1)
		entry.timer = time.AfterFunc(max(0, lease.SessionExpiresAt.Sub(p.opts.Now())), func() {
			defer p.expiry.Done()
			entry.cancel(auth.ErrExpired)
			<-gate
			defer func() { gate <- struct{}{} }()
			p.retire(id, entry)
		})
	}
	p.mu.Unlock()
	p.opts.Log("connection_opened")
	return entry, handler, nil
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
		if e.timer != nil && e.timer.Stop() {
			p.expiry.Done()
		}
		if e.oauth != nil {
			e.oauth.Close()
		}
		bounded, cancel := context.WithTimeout(ctx, p.opts.ShutdownTimeout)
		defer cancel()
		_ = e.session.Close(bounded)
		p.opts.Log("connection_closed")
	})
}
