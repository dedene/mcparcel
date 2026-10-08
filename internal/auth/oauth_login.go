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
	if h.shut {
		return NotSignedIn(h.opts.Name, h.opts.Account)
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

func (h *OAuthHandler) loginLocked(ctx context.Context, req *http.Request, resp *http.Response) (err error) {
	issuer, resource, asm, found, err := discoverIssuer(ctx, h.client, h.opts.URL, resp.Header.Values("WWW-Authenticate"))
	if err != nil {
		return h.signInFailed("discovery", failureCode(err, "discovery_failed"), h.authFailed(fmt.Sprintf("Could not discover the authorization server for %s.", h.opts.Label)))
	}
	client := h.client
	// Without PRM only the MCP URL fallback gives an issuer with a path.
	if u, err := url.Parse(issuer); !found && err == nil && u.Path != "" {
		client = noPRMClient(h.client, h.opts.URL)
		defer client.CloseIdleConnections()
	}
	if h.auth.IssuerURL != "" && !issuersEqual(h.auth.IssuerURL, issuer) {
		return h.signInFailed("discovery", "issuer_mismatch", h.authFailed("The authorization server does not match auth.issuerUrl."))
	}
	reused, redirect := h.reusableClient(issuer, resource, asm)
	unsupported := h.opts.Client.ID == "" && asm != nil && asm.RegistrationEndpoint == ""
	if reused == nil && unsupported {
		return h.signInFailed("registration", "registration_unsupported", h.authFailed("The authorization server offers no dynamic client registration; add auth.clientId to this connection's definition."))
	}
	cb, err := h.listenCallback(redirect)
	if err != nil && reused != nil && h.auth.RedirectURL == "" && !unsupported {
		// The stored loopback port is taken: register a new client instead.
		reused = nil
		cb, err = h.listenCallback("")
	}
	if err != nil {
		return h.signInFailed("callback", failureCode(err, "callback_failed"), err)
	}
	defer cb.shutdown()
	if reused != nil {
		// Forget a reused client the provider no longer honours (it may never
		// redirect back), so the next sign-in registers a new one.
		defer func() {
			if err != nil {
				_ = h.opts.Health.RememberClient(h.opts.Account, "", "")
			}
		}()
	}
	var issued *oauth2.Token
	// Where a failing Authorize stopped: the SDK calls the hooks below on
	// this goroutine, in order (registration, scopes, callback, token).
	stage, failCode := "registration", ""
	cfg := &sdkauth.AuthorizationCodeHandlerConfig{
		RedirectURL: cb.redirect,
		AuthorizationCodeFetcher: func(ctx context.Context, args *sdkauth.AuthorizationArgs) (*sdkauth.AuthorizationResult, error) {
			res, err := cb.fetch(ctx, args)
			switch {
			case !cb.answered:
				failCode = failureCode(ctx.Err(), "")
			case err != nil:
				stage, failCode = "callback", cb.code
			case asm != nil && issuerMismatch(res.Iss, asm):
				stage, failCode = "callback", "issuer_mismatch"
			default:
				stage = "token_exchange"
			}
			return res, err
		},
		RequestRefreshToken:   true,
		AcceptUnadvertisedIss: true,
		Client:                client,
		ScopeFilter: func(discovered []string) []string {
			stage = "authorization"
			if len(h.auth.Scopes) > 0 {
				return h.auth.Scopes
			}
			return discovered
		},
		NewTokenSource: func(_ context.Context, c *oauth2.Config, tok *oauth2.Token) (oauth2.TokenSource, error) {
			if asm != nil && c.Endpoint.TokenURL != asm.TokenEndpoint {
				failCode = "metadata_changed"
				return nil, h.authFailed("The authorization server metadata changed during sign-in.")
			}
			stage = "token_save"
			h.state = OAuthState{Version: 1, URL: h.opts.URL, Issuer: issuer, Resource: resource, TokenURL: c.Endpoint.TokenURL, AuthStyle: int(c.Endpoint.AuthStyle), RefreshToken: tok.RefreshToken}
			if h.opts.Client.ID == "" {
				h.state.ClientID, h.state.ClientSecret = c.ClientID, c.ClientSecret
			}
			h.setToken(tok)
			if err := SaveOAuth(context.Background(), h.opts.Keyring, h.opts.Account, h.state); err != nil {
				h.token = nil
				return nil, err
			}
			issued = tok
			return oauth2.StaticTokenSource(tok), nil
		},
	}
	switch {
	case reused != nil:
		cfg.PreregisteredClient = reused
	case h.opts.Client.ID != "":
		pre := &oauthex.ClientCredentials{ClientID: h.opts.Client.ID, Issuer: h.auth.IssuerURL}
		if h.opts.Client.Secret != "" {
			pre.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: h.opts.Client.Secret}
		}
		cfg.PreregisteredClient = pre
	default:
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
	if err != nil {
		if failCode == "" {
			failCode = failureCode(err, stage+"_failed")
		}
		_ = h.signInFailed(stage, failCode, err)
	} else if issued != nil {
		h.record(HealthEvent{Kind: HealthAuthorized, Trigger: TriggerLogin, AccessTTL: int64(h.lifetime / time.Second), RefreshTTL: refreshTTL(issued), RefreshToken: issued.RefreshToken != "", ReusedClient: reused != nil})
		if h.state.ClientID != "" {
			_ = h.opts.Health.RememberClient(h.opts.Account, h.state.ClientID, cb.redirect)
		}
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

// signInFailed logs where and why a sign-in failed and returns err. Only the
// stage and a sanitized code are logged: never a token, code, state or URL.
func (h *OAuthHandler) signInFailed(stage, code string, err error) error {
	if h.opts.LogSignInFailure != nil {
		h.opts.LogSignInFailure(stage, sanitizeCode(code, "error"))
	}
	return err
}

// failureCode classifies a sign-in error: an OAuth error code, an HTTP
// status, a timeout or network failure, an MCParcel error code, else fallback.
func failureCode(err error, fallback string) string {
	var re *oauth2.RetrieveError
	var reg *oauthex.ClientRegistrationError
	var ne net.Error
	var safe *output.Error
	switch {
	case err == nil:
		return fallback
	case errors.As(err, &re):
		if re.ErrorCode == "" && re.Response != nil {
			return fmt.Sprintf("http_%d", re.Response.StatusCode)
		}
		return sanitizeCode(re.ErrorCode, fallback)
	case errors.As(err, &reg):
		return sanitizeCode(reg.ErrorCode, fallback)
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.As(err, &safe) && safe != nil:
		return safe.Code
	case errors.As(err, &ne):
		if ne.Timeout() {
			return "timeout"
		}
		return "network_error"
	}
	return fallback
}

// issuerMismatch mirrors the SDK's RFC 9207 check (unadvertised iss accepted)
// so a rejected callback is logged as such.
func issuerMismatch(iss string, asm *oauthex.AuthServerMeta) bool {
	if iss == "" {
		return asm.AuthorizationResponseIssParameterSupported
	}
	return iss != asm.Issuer
}

func (h *OAuthHandler) authFailed(message string) *output.Error {
	e := output.NewError("auth_failed", nil)
	e.Message = message
	e.NextAction = output.AuthAction(h.opts.Name, h.opts.Account)
	return e
}

// callbackResult is what the browser brought back; code is the sanitized
// provider error code of a failed callback.
type callbackResult struct {
	res  *sdkauth.AuthorizationResult
	err  error
	code string
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

	// Set by fetch on the Authorize goroutine: the browser answered, and the
	// failure code it brought.
	answered bool
	code     string

	mu    sync.Mutex
	state string
	used  bool
}

// listenCallback binds the redirect address: redirect, else the configured
// URL verbatim (http on a loopback host), else 127.0.0.1 on a random port.
func (h *OAuthHandler) listenCallback(redirect string) (*callback, error) {
	if redirect == "" {
		redirect = h.auth.RedirectURL
	}
	cb := &callback{h: h, redirect: redirect, path: "/callback", result: make(chan callbackResult, 1), outcome: make(chan error, 1)}
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
			e.NextAction = "Fix the connection definition, then run " + output.AuthAction(h.opts.Name, h.opts.Account) + " again."
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
		e.NextAction = "Close the program using that port, then run " + output.AuthAction(h.opts.Name, h.opts.Account) + " again."
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
		cb.answered, cb.code = true, r.code
		return r.res, r.err
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			e := NotSignedIn(cb.h.opts.Name, cb.h.opts.Account)
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
	p := callbackPage{Label: cb.h.opts.Label, Name: output.AuthTarget(cb.h.opts.Name, cb.h.opts.Account)}
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
		cb.result <- callbackResult{err: cb.h.authFailed(fmt.Sprintf("Sign-in to %s failed: %s.", cb.h.opts.Label, p.Code)), code: p.Code}
	case q.Get("code") == "":
		p.State, p.Code = "failed", "invalid_request"
		cb.result <- callbackResult{err: cb.h.authFailed(fmt.Sprintf("Sign-in to %s failed: invalid_request.", cb.h.opts.Label)), code: "invalid_request"}
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
