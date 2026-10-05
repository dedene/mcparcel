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
	closeOnce      sync.Once
}

func (p *pool) session(ctx context.Context, id, hash string, c config.Connection, lease auth.Lease, gate chan struct{}) (*poolEntry, error) {
	p.mu.Lock()
	old := p.entries[id]
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return nil, output.NewError("canceled", nil)
	}
	if old != nil && old.hash == hash && old.identity == lease.Identity && old.ctx.Err() == nil {
		return old, nil
	}
	if old != nil {
		p.retire(id, old)
	}
	values, e := envRefValues(ctx, p.opts.LoginEnv, p.opts.Keychain, c, lease.Values)
	if e != nil {
		return nil, e
	}
	env, e := BuildChildEnv(p.opts.LoginEnv, c, values)
	if e != nil {
		return nil, e
	}
	headers := map[string]string{}
	if c.Transport.HTTP != nil {
		for k, v := range c.Transport.HTTP.Headers {
			if v.Literal != nil {
				headers[k] = *v.Literal
			} else if v.Secret != nil {
				value, ok := values[v.Secret.Secret]
				if !ok {
					return nil, auth.ErrProvider
				}
				headers[k] = v.Secret.Prefix + value + v.Secret.Suffix
			} else {
				return nil, config.ErrConfig
			}
		}
	}
	connectCtx, cancel := context.WithTimeout(ctx, p.opts.ConnectTimeout)
	defer cancel()
	session, e := p.opts.Connect(connectCtx, mcpclient.ConnectOptions{Connection: c, Env: env, Headers: headers, Home: p.opts.Paths.Home, Version: p.opts.Version, ConnectTimeout: p.opts.ConnectTimeout, ShutdownTimeout: p.opts.ShutdownTimeout})
	if e != nil {
		return nil, e
	}
	if session == nil {
		return nil, output.NewError("internal_error", nil)
	}
	entryCtx, stop := context.WithCancelCause(context.Background())
	entry := &poolEntry{hash: hash, identity: lease.Identity, session: session, ctx: entryCtx, cancel: stop}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		entry.close(p, context.Background())
		return nil, output.NewError("canceled", nil)
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
	return entry, nil
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
		bounded, cancel := context.WithTimeout(ctx, p.opts.ShutdownTimeout)
		defer cancel()
		_ = e.session.Close(bounded)
		p.opts.Log("connection_closed")
	})
}
