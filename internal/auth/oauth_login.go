package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"

	"github.com/dedene/mcparcel/internal/output"
)

const callbackCSP = "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// login runs the authorization-code flow once; h.mu is held throughout so a
// concurrent Authorize waits for its result. Any failure is terminal.
func (h *OAuthHandler) login(ctx context.Context, req *http.Request, resp *http.Response) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.terminal != nil {
		return h.terminal
	}
	if h.ready.Load() {
		return nil
	}
	err := h.loginLocked(ctx, req, resp)
	if err != nil {
		h.terminal = err
		return err
	}
	h.ready.Store(true)
	h.log("oauth_signed_in")
	return nil
}

func (h *OAuthHandler) loginLocked(ctx context.Context, req *http.Request, resp *http.Response) error {
	issuer, resource, asm, _, err := discoverIssuer(ctx, h.client, h.opts.URL, resp.Header.Values("WWW-Authenticate"))
	if err != nil {
		return h.authFailed(fmt.Sprintf("Could not discover the authorization server for %s.", h.opts.Label))
	}
	if h.auth.IssuerURL != "" && !issuersEqual(h.auth.IssuerURL, issuer) {
		return h.authFailed("The authorization server does not match auth.issuerUrl.")
	}
	if h.opts.Client.ID == "" && asm != nil && asm.RegistrationEndpoint == "" {
		return h.authFailed("The authorization server offers no dynamic client registration; add auth.clientId to this connection's definition.")
	}
	cb, err := h.listenCallback()
	if err != nil {
		return err
	}
	defer cb.shutdown()
	cfg := &sdkauth.AuthorizationCodeHandlerConfig{
		RedirectURL:              cb.redirect,
		AuthorizationCodeFetcher: cb.fetch,
		RequestRefreshToken:      true,
		AcceptUnadvertisedIss:    true,
		Client:                   h.client,
		ScopeFilter: func(discovered []string) []string {
			if len(h.auth.Scopes) > 0 {
				return h.auth.Scopes
			}
			return discovered
		},
		NewTokenSource: func(_ context.Context, c *oauth2.Config, tok *oauth2.Token) (oauth2.TokenSource, error) {
			if asm != nil && c.Endpoint.TokenURL != asm.TokenEndpoint {
				return nil, h.authFailed("The authorization server metadata changed during sign-in.")
			}
			s := OAuthState{Version: 1, URL: h.opts.URL, Issuer: issuer, Resource: resource, TokenURL: c.Endpoint.TokenURL, AuthStyle: int(c.Endpoint.AuthStyle), RefreshToken: tok.RefreshToken, AccessExpiry: unixOrZero(tok.Expiry)}
			if h.opts.Client.ID == "" {
				s.ClientID, s.ClientSecret = c.ClientID, c.ClientSecret
			}
			if err := SaveOAuth(context.Background(), h.opts.Keyring, h.opts.Account, s); err != nil {
				return nil, err
			}
			h.state, h.token = s, tok
			return oauth2.StaticTokenSource(tok), nil
		},
	}
	if h.opts.Client.ID != "" {
		pre := &oauthex.ClientCredentials{ClientID: h.opts.Client.ID, Issuer: h.auth.IssuerURL}
		if h.opts.Client.Secret != "" {
			pre.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: h.opts.Client.Secret}
		}
		cfg.PreregisteredClient = pre
	} else {
		name := h.auth.ClientName
		if name == "" {
			name = "MCParcel"
		}
		cfg.DynamicClientRegistrationConfig = &sdkauth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
			RedirectURIs: []string{cb.redirect}, ClientName: name, GrantTypes: []string{"authorization_code", "refresh_token"},
			ResponseTypes: []string{"code"}, TokenEndpointAuthMethod: h.auth.TokenEndpointAuthMethod,
		}}
	}
	sdk, err := sdkauth.NewAuthorizationCodeHandler(cfg)
	if err == nil {
		err = sdk.Authorize(ctx, req, resp)
	}
	err = h.loginError(err)
	cb.finish(err)
	return err
}

// loginError keeps MCParcel errors from the chain and replaces every other
// SDK, provider or network error with fixed text.
func (h *OAuthHandler) loginError(err error) error {
	if err == nil {
		return nil
	}
	var safe *output.Error
	if errors.As(err, &safe) && safe != nil {
		return safe
	}
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		return h.authFailed(fmt.Sprintf("Sign-in to %s failed: %s.", h.opts.Label, sanitizeCode(re.ErrorCode, "token_error")))
	}
	return h.authFailed(fmt.Sprintf("Sign-in to %s failed.", h.opts.Label))
}

func (h *OAuthHandler) authFailed(message string) *output.Error {
	e := output.NewError("auth_failed", nil)
	e.Message = message
	e.NextAction = "mcparcel auth login " + h.opts.Name
	return e
}

type callbackResult struct {
	res *sdkauth.AuthorizationResult
	err error
}

// callback is the loopback redirect endpoint of one sign-in.
type callback struct {
	h        *OAuthHandler
	ln       net.Listener
	redirect string
	path     string
	srv      *http.Server
	result   chan callbackResult
	outcome  chan error

	mu    sync.Mutex
	state string
	used  bool
}

// listenCallback binds the redirect address: the configured URL verbatim
// (http on a loopback host), else 127.0.0.1 on a random port.
func (h *OAuthHandler) listenCallback() (*callback, error) {
	cb := &callback{h: h, redirect: h.auth.RedirectURL, path: "/callback", result: make(chan callbackResult, 1), outcome: make(chan error, 1)}
	addr := "127.0.0.1:0"
	if cb.redirect != "" {
		u, err := url.Parse(cb.redirect)
		host := ""
		if err == nil {
			host = u.Hostname()
		}
		if err != nil || u.Scheme != "http" || host != "127.0.0.1" && host != "::1" && host != "localhost" || u.User != nil {
			e := output.NewError("invalid_arguments", nil)
			e.Message = "auth.redirectUrl must be an http URL on 127.0.0.1, [::1] or localhost."
			e.NextAction = "Fix the connection definition, then run mcparcel auth login " + h.opts.Name + " again."
			return nil, e
		}
		if host == "localhost" {
			host = "127.0.0.1"
		}
		port := u.Port()
		if port == "" {
			port = "80"
		}
		addr, cb.path = net.JoinHostPort(host, port), u.Path
		if cb.path == "" {
			cb.path = "/"
		}
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		e := output.NewError("auth_callback_unavailable", nil)
		e.Message = "The sign-in callback address " + addr + " is in use."
		e.NextAction = "Close the program using that port, then run mcparcel auth login " + h.opts.Name + " again."
		return nil, e
	}
	cb.ln = ln
	if cb.redirect == "" {
		cb.redirect = fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)
	}
	cb.srv = &http.Server{Handler: cb, ReadHeaderTimeout: 10 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
	return cb, nil
}

// fetch is the SDK's AuthorizationCodeFetcher: serve the callback, show the
// URL, wait for the browser or ctx.
func (cb *callback) fetch(ctx context.Context, args *sdkauth.AuthorizationArgs) (*sdkauth.AuthorizationResult, error) {
	u, err := url.Parse(args.URL)
	if err != nil {
		return nil, cb.h.authFailed("Sign-in to " + cb.h.opts.Label + " failed.")
	}
	cb.mu.Lock()
	cb.state = u.Query().Get("state")
	cb.mu.Unlock()
	go func() { _ = cb.srv.Serve(cb.ln) }()
	if err := cb.h.opts.Login.ShowURL(args.URL); err != nil {
		return nil, output.NewError("canceled", nil)
	}
	select {
	case r := <-cb.result:
		return r.res, r.err
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			e := NotSignedIn(cb.h.opts.Name)
			e.Message = "Sign-in was not completed in time."
			return nil, e
		}
		return nil, output.NewError("canceled", nil)
	}
}

// finish hands the flow's outcome to a callback request waiting to render it.
func (cb *callback) finish(err error) { cb.outcome <- err }

func (cb *callback) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if cb.srv.Shutdown(ctx) != nil {
		_ = cb.srv.Close()
	}
	_ = cb.ln.Close()
}

func (cb *callback) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", callbackCSP)
	if r.URL.Path != cb.path {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	q := r.URL.Query()
	p := callbackPage{Label: cb.h.opts.Label, Name: cb.h.opts.Name}
	cb.mu.Lock()
	match, used := q.Get("state") == cb.state && cb.state != "", cb.used
	if match {
		cb.used = true
	}
	cb.mu.Unlock()
	switch {
	case !match:
		p.State = "mismatch"
		w.WriteHeader(http.StatusBadRequest)
	case used:
		p.State = "expired"
		w.WriteHeader(http.StatusGone)
	case q.Get("error") != "":
		p.State, p.Code, p.Detail = "failed", sanitizeCode(q.Get("error"), "error"), q.Get("error_description")
		cb.result <- callbackResult{err: cb.h.authFailed(fmt.Sprintf("Sign-in to %s failed: %s.", cb.h.opts.Label, p.Code))}
	case q.Get("code") == "":
		p.State, p.Code = "failed", "invalid_request"
		cb.result <- callbackResult{err: cb.h.authFailed(fmt.Sprintf("Sign-in to %s failed: invalid_request.", cb.h.opts.Label))}
	default:
		cb.result <- callbackResult{res: &sdkauth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}}
		p.State = "failed"
		select {
		case err := <-cb.outcome:
			if err == nil {
				p.State = "signed_in"
			}
		case <-time.After(60 * time.Second):
		case <-r.Context().Done():
		}
	}
	_ = renderCallback(w, p)
}
