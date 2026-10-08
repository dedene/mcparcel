package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"golang.org/x/oauth2"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

const refreshSkew = 30 * time.Second

// OAuthClient is a preconfigured client from the connection definition.
type OAuthClient struct{ ID, Secret string }

// LoginOptions turns a handler into login mode; ShowURL receives the
// authorization URL.
type LoginOptions struct{ ShowURL func(string) error }

// OAuthOptions configures one connection's handler. Account is the Keychain
// account, Name the connection name shown in next actions, Label the display
// name. Auth nil means defaults. Login nil means session mode from State.
// LogSignInFailure receives the stage and error class of a failed sign-in.
// Health records the session's history (nil records nothing); Now is the
// clock (nil means time.Now). Previous is the stored item a login may reuse
// the client registration of.
type OAuthOptions struct {
	Account, Name, Label, URL string
	Auth                      *config.OAuth
	Client                    OAuthClient
	State                     *OAuthState
	Keyring                   Keyring
	Log                       func(string)
	LogSignInFailure          func(stage, code string)
	Login                     *LoginOptions
	Health                    *Health
	Now                       func() time.Time
	Previous                  *OAuthState
}

// OAuthHandler implements the SDK's OAuthHandler for one connection. It is the
// only writer of the connection's Keychain item while it runs.
type OAuthHandler struct {
	opts   OAuthOptions
	auth   config.OAuth
	client *http.Client
	closed atomic.Bool
	ready  atomic.Bool // login mode: signed in

	mu          sync.Mutex
	terminal    error
	state       OAuthState
	token       *oauth2.Token
	minted      string // access token minted by an Authorize refresh
	pendingSave bool
	shut        bool // set by Close under mu: nothing is saved or refreshed after it
	// The access token's lifetime and when it was issued; refreshAt is the
	// wall-clock second (Unix) from which it is refreshed, 0 for never.
	issued       time.Time
	lifetime     time.Duration
	lead         time.Duration
	refreshAt    int64
	pendingEvent *HealthEvent // refreshed event recorded once a failed save succeeds
}

var _ sdkauth.OAuthHandler = (*OAuthHandler)(nil)

func NewOAuthHandler(o OAuthOptions) *OAuthHandler {
	h := &OAuthHandler{opts: o, client: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil, ForceAttemptHTTP2: true}, CheckRedirect: checkRedirect}}
	if o.Auth != nil {
		h.auth = *o.Auth
	}
	if o.State != nil {
		h.state = *o.State
	}
	if h.opts.Label == "" {
		h.opts.Label = o.Name
	}
	if h.opts.Now == nil {
		h.opts.Now = time.Now
	}
	return h
}

// NotSignedIn is auth_required pointing at auth <mcp>. name is what the
// user typed, canonical the connection's ID.
func NotSignedIn(name, canonical string) *output.Error {
	e := output.NewError("auth_required", nil)
	e.Message = "Sign-in required for " + name + "."
	e.NextAction = output.AuthAction(name, canonical)
	return e
}

// SignInNeedsInput is auth_required for a sign-in under --no-input, which
// never opens a browser.
func SignInNeedsInput(name, canonical string) *output.Error {
	e := output.NewError("auth_required", nil)
	e.Message = "Signing in to " + name + " opens a browser, which --no-input does not allow."
	e.NextAction = "Run " + output.AuthAction(name, canonical) + " without --no-input."
	return e
}

type toolCallKey struct{}

// WithToolCall marks ctx as carrying a tools/call request, which is never resent.
func WithToolCall(ctx context.Context) context.Context {
	return context.WithValue(ctx, toolCallKey{}, true)
}

func IsToolCall(ctx context.Context) bool { v, _ := ctx.Value(toolCallKey{}).(bool); return v }

func (h *OAuthHandler) SignedIn() bool { return h.ready.Load() }

// Close stops all refreshes; later calls fail with auth_required. It waits for
// a refresh or sign-in in progress, so a rotated refresh token is saved before
// the item is deleted or another handler loads it, and tries once more to save
// a session whose save failed, so the only copy of a rotated refresh token is
// not dropped with the handler. Never call it with h.mu held.
func (h *OAuthHandler) Close() {
	h.closed.Store(true)
	h.mu.Lock()
	_ = h.retrySaveLocked()
	h.shut = true
	h.mu.Unlock()
	h.client.CloseIdleConnections()
}

func (h *OAuthHandler) TokenSource(context.Context) (oauth2.TokenSource, error) {
	if h.opts.Login != nil && !h.ready.Load() {
		return nil, nil
	}
	return h, nil
}

// Token returns the in-memory access token, refreshing it when a fifth of its
// lifetime (at least refreshSkew) remains.
func (h *OAuthHandler) Token() (*oauth2.Token, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.terminal != nil {
		return nil, h.terminal
	}
	if h.shut {
		return nil, NotSignedIn(h.opts.Name, h.opts.Account)
	}
	if err := h.retrySaveLocked(); err != nil {
		return nil, err
	}
	if h.fresh(h.opts.Now()) {
		return h.token, nil
	}
	return h.refreshLocked(TriggerCall)
}

// Refresh forces one refresh under h.mu. It never signs in; a terminal or
// closed handler, or one without a refresh token, answers NotSignedIn.
func (h *OAuthHandler) Refresh(trigger string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.terminal != nil || h.shut || h.state.RefreshToken == "" {
		return NotSignedIn(h.opts.Name, h.opts.Account)
	}
	if err := h.retrySaveLocked(); err != nil {
		return err
	}
	_, err := h.refreshLocked(trigger)
	return err
}

// Unsaved reports whether the session is held only in memory because its last
// save failed.
func (h *OAuthHandler) Unsaved() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pendingSave && !h.shut
}

// SavePending retries a failed save; nil means nothing is left unsaved.
func (h *OAuthHandler) SavePending() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.shut {
		return nil
	}
	return h.retrySaveLocked()
}

// retrySaveLocked saves an item whose earlier save failed, then records the
// refresh that produced it.
func (h *OAuthHandler) retrySaveLocked() error {
	if !h.pendingSave {
		return nil
	}
	if err := h.save(); err != nil {
		return err
	}
	if h.pendingEvent != nil {
		h.log("oauth_refreshed")
		h.record(*h.pendingEvent)
		h.pendingEvent = nil
	}
	return nil
}

// fresh reports whether the access token is used as is. The wall clock
// catches sleep (the monotonic clock stops while a Mac sleeps) and a clock
// moved forward; the monotonic clock catches one moved back.
func (h *OAuthHandler) fresh(now time.Time) bool {
	if h.token == nil {
		return false
	}
	return h.refreshAt == 0 || now.Unix() < h.refreshAt && now.Sub(h.issued) < h.lifetime-h.lead
}

// setToken keeps tok as the access token and schedules its refresh.
func (h *OAuthHandler) setToken(tok *oauth2.Token) {
	now := h.opts.Now()
	lifetime := time.Duration(tok.ExpiresIn) * time.Second
	if lifetime <= 0 && !tok.Expiry.IsZero() {
		// Only form-encoded token responses leave ExpiresIn unset.
		lifetime = time.Until(tok.Expiry)
	}
	h.token, h.issued, h.lifetime, h.lead, h.refreshAt = tok, now, 0, 0, 0
	h.minted = "" // Authorize sets it again for the token it mints
	h.state.AccessExpiry = 0
	if lifetime > 0 {
		h.lifetime = lifetime.Truncate(time.Second)
		h.lead = max(h.lifetime/5, refreshSkew)
		h.refreshAt = now.Unix() + int64((h.lifetime-h.lead)/time.Second)
		h.state.AccessExpiry = now.Unix() + int64(h.lifetime/time.Second)
	}
}

// Authorize handles 401 and 403 answers from the MCP server.
func (h *OAuthHandler) Authorize(ctx context.Context, req *http.Request, resp *http.Response) error {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	_ = resp.Body.Close()
	h.mu.Lock()
	terminal := h.terminal
	h.mu.Unlock()
	if terminal != nil {
		return terminal
	}
	if resp.StatusCode == http.StatusForbidden {
		e := output.NewError("auth_failed", nil)
		e.Message = "The server refused this account access (HTTP 403)."
		return e
	}
	if h.opts.Login != nil && !h.ready.Load() {
		return h.login(ctx, req, resp)
	}
	// Only fetched metadata proves a change; without it, refresh as usual.
	issuer, resource, _, found, err := discoverIssuer(ctx, h.client, h.opts.URL, resp.Header.Values("WWW-Authenticate"))
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.terminal != nil {
		return h.terminal
	}
	if h.shut {
		return NotSignedIn(h.opts.Name, h.opts.Account)
	}
	if err == nil && found && (!issuersEqual(issuer, h.state.Issuer) || resource != h.state.Resource) {
		return h.failLocked(HealthReauthorizationRequired, "issuer_changed", TriggerCall, 0)
	}
	rejected, _ := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
	if IsToolCall(ctx) {
		if h.token != nil && h.token.AccessToken == rejected {
			h.token = nil
		}
		e := output.NewError("auth_expired", nil)
		e.Message = "The server rejected the access token; the tool call was not sent again."
		e.NextAction = "Run the call again."
		return e
	}
	// A token a later refresh replaced is retried with the current one; only
	// the token this Authorize just minted proves a rejection.
	if h.token != nil && h.token.AccessToken != rejected {
		return nil
	}
	if rejected != "" && rejected == h.minted {
		return h.failLocked(HealthReauthorizationRequired, "token_rejected", TriggerCall, 0)
	}
	tok, err := h.refreshLocked(TriggerCall)
	if err != nil {
		return err
	}
	h.minted = tok.AccessToken
	return nil
}

// refreshLocked exchanges the refresh token; h.mu is held. A rotated refresh
// token is saved before the new access token is returned. Every outcome is
// recorded in the health file; trigger says what asked for the refresh.
func (h *OAuthHandler) refreshLocked(trigger string) (*oauth2.Token, error) {
	if h.closed.Load() || h.state.RefreshToken == "" {
		return nil, NotSignedIn(h.opts.Name, h.opts.Account)
	}
	cfg := oauth2.Config{ClientID: h.state.ClientID, ClientSecret: h.state.ClientSecret, Endpoint: oauth2.Endpoint{TokenURL: h.state.TokenURL, AuthStyle: oauth2.AuthStyle(h.state.AuthStyle)}}
	if cfg.ClientID == "" {
		cfg.ClientID, cfg.ClientSecret = h.opts.Client.ID, h.opts.Client.Secret
	}
	_ = h.opts.Health.BeginRefresh(h.opts.Account)
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, h.client)
	old := h.state.RefreshToken
	tok, err := cfg.TokenSource(ctx, &oauth2.Token{RefreshToken: old}).Token()
	if err != nil {
		status := 0
		var re *oauth2.RetrieveError
		if errors.As(err, &re) {
			if re.Response != nil {
				status = re.Response.StatusCode
			}
			// 5xx and 429 are a provider outage or rate limit, not a rejected grant.
			if status < 500 && status != http.StatusTooManyRequests && (re.ErrorCode != "" || status == 400 || status == 401) {
				return nil, h.failLocked(HealthRefreshFailed, sanitizeCode(re.ErrorCode, "refresh_rejected"), trigger, status)
			}
		}
		code := failureCode(err, "connection_failed")
		if unreachable(err) {
			code = codeUnreachable
		}
		h.record(HealthEvent{Kind: HealthRefreshFailed, Trigger: trigger, Code: code, HTTPStatus: status})
		return nil, output.NewError("connection_failed", nil)
	}
	h.setToken(tok)
	h.state.RefreshToken = tok.RefreshToken
	h.state.Failure = nil
	event := HealthEvent{Kind: HealthRefreshed, Trigger: trigger, AccessTTL: int64(h.lifetime / time.Second), RefreshTTL: refreshTTL(tok), Rotated: tok.RefreshToken != old}
	if err := h.save(); err != nil {
		h.record(HealthEvent{Kind: HealthRefreshFailed, Trigger: trigger, Code: "keychain_save_failed"})
		h.pendingEvent = &event
		return nil, err
	}
	h.log("oauth_refreshed")
	h.record(event)
	return tok, nil
}

// failLocked records a terminal failure, clears the refresh token and makes
// the handler answer auth_required from now on.
func (h *OAuthHandler) failLocked(kind HealthKind, code, trigger string, status int) error {
	h.state.Failure = &OAuthFailure{At: h.opts.Now().Unix(), Code: code}
	h.state.RefreshToken = ""
	h.token = nil
	_ = h.save()
	h.log("oauth_refresh_failed")
	h.record(HealthEvent{Kind: kind, Trigger: trigger, Code: code, HTTPStatus: status, Terminal: true})
	h.terminal = NotSignedIn(h.opts.Name, h.opts.Account)
	return h.terminal
}

func (h *OAuthHandler) record(e HealthEvent) {
	_ = h.opts.Health.Record(h.opts.Account, e)
}

// unreachable reports a token request that never reached the provider: the
// name did not resolve or the connection was not made.
func unreachable(err error) bool {
	var dns *net.DNSError
	var op *net.OpError
	return errors.As(err, &dns) || errors.As(err, &op) && op.Op == "dial"
}

// refreshTTL is the provider's refresh_token_expires_in in seconds, 0 if absent.
func refreshTTL(tok *oauth2.Token) int64 {
	var seconds float64
	switch v := tok.Extra("refresh_token_expires_in").(type) {
	case float64:
		seconds = v
	case json.Number:
		seconds, _ = v.Float64()
	case string:
		seconds, _ = strconv.ParseFloat(v, 64)
	}
	if seconds < 1 || seconds > 1<<40 {
		return 0
	}
	return int64(seconds)
}

// save writes the item. After Close it writes nothing, so an item deleted at
// logout stays deleted.
func (h *OAuthHandler) save() error {
	if h.shut {
		return nil
	}
	err := SaveOAuth(context.Background(), h.opts.Keyring, h.opts.Account, h.state)
	h.pendingSave = err != nil
	return err
}

func (h *OAuthHandler) log(event string) {
	if h.opts.Log != nil {
		h.opts.Log(event)
	}
}

var oauthCode = regexp.MustCompile(`^[a-z0-9_.-]{1,64}$`)

// sanitizeCode keeps an OAuth error code only when it is a plain token.
func sanitizeCode(code, fallback string) string {
	if oauthCode.MatchString(code) {
		return code
	}
	return fallback
}
