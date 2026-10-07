package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// loginTimeout bounds one sign-in, browser time included.
const loginTimeout = 10 * time.Minute

// oauthCapable reports whether sign-in applies: an HTTP connection marked
// OAuth, or one without a configured credential header.
func oauthCapable(c config.Connection) bool {
	if c.Transport.HTTP == nil {
		return false
	}
	if c.Auth != nil {
		return true
	}
	for _, v := range c.Transport.HTTP.Headers {
		if v.Secret != nil {
			return false
		}
	}
	return true
}

// oauthHandler returns the connection's OAuth handler, or nil when OAuth does
// not apply. A marked connection without a usable stored session fails with
// auth_required before any network request; after auth lock a stored session
// is not usable until the next sign-in.
func (p *pool) oauthHandler(ctx context.Context, id, name string, c config.Connection, values map[string]string, login *auth.LoginOptions) (*auth.OAuthHandler, error) {
	if !oauthCapable(c) {
		return nil, nil
	}
	if p.opts.Headless && (login != nil || c.Auth != nil) {
		// A sign-in needs a browser and a Keychain; headless has neither.
		return nil, headlessSignIn(nil)
	}
	if p.opts.Keyring == nil {
		if c.Auth != nil || login != nil {
			return nil, output.NewError("keychain_unavailable", nil)
		}
		return nil, nil
	}
	u, e := config.LiteralText(c.Transport.HTTP.URL)
	if e != nil {
		return nil, e
	}
	opts := auth.OAuthOptions{Account: id, Name: name, Label: c.Label, URL: u, Auth: c.Auth, Keyring: p.opts.Keyring, Log: p.opts.Log, LogSignInFailure: p.opts.SignInFailure, Login: login, Health: p.opts.Health, Now: p.opts.Now}
	if c.Auth != nil {
		if opts.Client.ID, e = oauthClientValue(c.Auth.ClientID, values); e != nil {
			return nil, e
		}
		if opts.Client.Secret, e = oauthClientValue(c.Auth.ClientSecret, values); e != nil {
			return nil, e
		}
	}
	if login != nil {
		// A re-login may reuse the stored client registration.
		if prev, e := auth.LoadOAuth(ctx, p.opts.Keyring, id); e == nil {
			opts.Previous = &prev
		}
		return auth.NewOAuthHandler(opts), nil
	}
	state, e := auth.LoadOAuth(ctx, p.opts.Keyring, id)
	usable := e == nil && state.URL == u && state.Failure == nil && state.RefreshToken != "" && !p.oauthLocked(id)
	if c.Auth == nil {
		if !usable {
			return nil, nil
		}
	} else if !usable {
		if e != nil && !errors.Is(e, auth.ErrNoSession) {
			return nil, output.NewError("keychain_unavailable", nil)
		}
		return nil, auth.NotSignedIn(name)
	}
	opts.State = &state
	return auth.NewOAuthHandler(opts), nil
}

// rememberSignIn records that an unmarked connection's server asked for
// sign-in: a Keychain item holding only its URL, which auth status lists as
// sign-in required and which no session can use. An existing item is kept.
func (p *pool) rememberSignIn(ctx context.Context, canonical string, c config.Connection) {
	u, e := config.LiteralText(c.Transport.HTTP.URL)
	if e != nil || p.opts.Keyring == nil || p.opts.Headless {
		return
	}
	ctx = context.WithoutCancel(ctx)
	if _, e = auth.LoadOAuth(ctx, p.opts.Keyring, canonical); errors.Is(e, auth.ErrNoSession) {
		if auth.SaveOAuth(ctx, p.opts.Keyring, canonical, auth.OAuthState{Version: 1, URL: u}) == nil {
			_ = p.opts.Health.Record(canonical, auth.HealthEvent{Kind: auth.HealthReauthorizationRequired, Code: "server_requested", Terminal: true})
		}
	}
}

// oauthClientValue resolves a client ID or secret: a literal, or a resolved
// reference with its prefix and suffix.
func oauthClientValue(v *config.Value, values map[string]string) (string, error) {
	switch {
	case v == nil:
		return "", nil
	case v.Literal != nil:
		return *v.Literal, nil
	case v.Secret != nil:
		value, ok := values[v.Secret.Secret]
		if !ok {
			return "", output.NewError("auth_failed", nil)
		}
		return v.Secret.Prefix + value + v.Secret.Suffix, nil
	}
	return "", config.ErrConfig
}

// loginOptions checks a login request before any effect.
func loginOptions(ctx context.Context, req Request, c config.Connection) (*auth.LoginOptions, error) {
	if !oauthCapable(c) {
		e := output.NewError("invalid_arguments", nil)
		e.Message = "Only HTTP connections without a configured credential header use sign-in."
		return nil, e
	}
	if clientCredentials(c) {
		return nil, clientCredentialsSignIn(req.Connection)
	}
	send := authURLSender(ctx)
	if req.NoInput || send == nil {
		return nil, auth.NotSignedIn(req.Connection)
	}
	return &auth.LoginOptions{ShowURL: send}, nil
}

// finishLogin lists tools on the new session so the sign-in completes, and
// keeps a signed-in session pooled. e is the session's connect error. A
// session that was never asked to sign in is retired, so its login-mode
// handler never serves an ordinary call. A sign-in an auth lock overtook
// clears no lock and is retired.
func (p *pool) finishLogin(ctx context.Context, name, canonical string, entry *poolEntry, handler *auth.OAuthHandler, e error, epoch uint64, fail func(error) Response) Response {
	if e == nil {
		if _, e = entry.session.Tools(ctx); e != nil || handler == nil || !handler.SignedIn() {
			p.retire(canonical, entry)
		} else if !p.unlockOAuth(canonical, epoch) {
			// An auth lock ran after this sign-in: it stays locked.
			p.retire(canonical, entry)
			e = auth.ErrLocked
		}
	}
	if e != nil {
		r := fail(e)
		// After a 401 the transport may report auth_required instead.
		if r.Error.Code == "timeout" || r.Error.Code == "auth_required" && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			r.Error = auth.NotSignedIn(name)
			r.Error.Message = "Sign-in was not completed in time."
		}
		return r
	}
	b, e := json.Marshal(LoginData{Connection: canonical, SignedIn: handler != nil && handler.SignedIn()})
	if e != nil {
		return fail(e)
	}
	return Response{Data: b}
}

// gate returns the connection's one-slot gate, which serializes its work.
func (p *pool) gate(id string) chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	gate := p.gates[id]
	if gate == nil {
		gate = make(chan struct{}, 1)
		gate <- struct{}{}
		p.gates[id] = gate
	}
	return gate
}

// logout retires the pooled session, which stops its refreshes, then deletes
// the Keychain item.
func (p *pool) logout(ctx context.Context, canonical string) Response {
	gate := p.gate(canonical)
	select {
	case <-ctx.Done():
		return Response{Error: poolError(ctx.Err(), context.Cause(ctx), "", false)}
	case <-gate:
	}
	defer func() { gate <- struct{}{} }()
	p.mu.Lock()
	entry := p.entries[canonical]
	p.mu.Unlock()
	if entry != nil {
		p.retire(canonical, entry)
	}
	removed, e := false, error(nil)
	if !p.opts.Headless {
		// Headless mode stores no sign-in: there is nothing to delete.
		removed, e = auth.DeleteOAuth(ctx, p.opts.Keyring, canonical)
	}
	if e != nil {
		return Response{Error: poolError(e, nil, "", false)}
	}
	if removed {
		p.opts.Log("oauth_signed_out")
		_ = p.opts.Health.Record(canonical, auth.HealthEvent{Kind: auth.HealthLogout})
	}
	b, e := json.Marshal(LogoutData{Connection: canonical, Removed: removed})
	if e != nil {
		return Response{Error: output.NewError("internal_error", nil)}
	}
	return Response{Data: b}
}
