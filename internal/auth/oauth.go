package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
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
type OAuthOptions struct {
	Account, Name, Label, URL string
	Auth                      *config.OAuth
	Client                    OAuthClient
	State                     *OAuthState
	Keyring                   Keyring
	Log                       func(string)
	LogSignInFailure          func(stage, code string)
	Login                     *LoginOptions
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
	return h
}

// NotSignedIn is auth_required pointing at auth login.
func NotSignedIn(name string) *output.Error {
	e := output.NewError("auth_required", nil)
	e.Message = "Sign-in required for " + name + "."
	e.NextAction = "mcparcel auth login " + name
	return e
}

type toolCallKey struct{}

// WithToolCall marks ctx as carrying a tools/call request, which is never resent.
func WithToolCall(ctx context.Context) context.Context {
	return context.WithValue(ctx, toolCallKey{}, true)
}

func IsToolCall(ctx context.Context) bool { v, _ := ctx.Value(toolCallKey{}).(bool); return v }

func (h *OAuthHandler) SignedIn() bool { return h.ready.Load() }

// Close stops all refreshes; later calls fail with auth_required.
func (h *OAuthHandler) Close() {
	h.closed.Store(true)
	h.client.CloseIdleConnections()
}

func (h *OAuthHandler) TokenSource(context.Context) (oauth2.TokenSource, error) {
	if h.opts.Login != nil && !h.ready.Load() {
		return nil, nil
	}
	return h, nil
}

// Token returns the in-memory access token, refreshing it when it expires
// within refreshSkew.
func (h *OAuthHandler) Token() (*oauth2.Token, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.terminal != nil {
		return nil, h.terminal
	}
	if h.pendingSave {
		if err := h.save(); err != nil {
			return nil, err
		}
	}
	if h.token != nil && (h.token.Expiry.IsZero() || time.Now().Add(refreshSkew).Before(h.token.Expiry)) {
		return h.token, nil
	}
	return h.refreshLocked()
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
	if err == nil && found && (!issuersEqual(issuer, h.state.Issuer) || resource != h.state.Resource) {
		return h.failLocked("issuer_changed")
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
	if rejected != "" && rejected == h.minted {
		h.terminal = NotSignedIn(h.opts.Name)
		return h.terminal
	}
	if h.token != nil && h.token.AccessToken != rejected {
		return nil
	}
	tok, err := h.refreshLocked()
	if err != nil {
		return err
	}
	h.minted = tok.AccessToken
	return nil
}

// refreshLocked exchanges the refresh token; h.mu is held. A rotated refresh
// token is saved before the new access token is returned.
func (h *OAuthHandler) refreshLocked() (*oauth2.Token, error) {
	if h.closed.Load() || h.state.RefreshToken == "" {
		return nil, NotSignedIn(h.opts.Name)
	}
	cfg := oauth2.Config{ClientID: h.state.ClientID, ClientSecret: h.state.ClientSecret, Endpoint: oauth2.Endpoint{TokenURL: h.state.TokenURL, AuthStyle: oauth2.AuthStyle(h.state.AuthStyle)}}
	if cfg.ClientID == "" {
		cfg.ClientID, cfg.ClientSecret = h.opts.Client.ID, h.opts.Client.Secret
	}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, h.client)
	tok, err := cfg.TokenSource(ctx, &oauth2.Token{RefreshToken: h.state.RefreshToken}).Token()
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) {
			status := 0
			if re.Response != nil {
				status = re.Response.StatusCode
			}
			// 5xx and 429 are a provider outage or rate limit, not a rejected grant.
			if status < 500 && status != http.StatusTooManyRequests && (re.ErrorCode != "" || status == 400 || status == 401) {
				return nil, h.failLocked(sanitizeCode(re.ErrorCode, "refresh_rejected"))
			}
		}
		return nil, output.NewError("connection_failed", nil)
	}
	h.token = tok
	h.state.RefreshToken = tok.RefreshToken
	h.state.AccessExpiry = unixOrZero(tok.Expiry)
	h.state.Failure = nil
	h.log("oauth_refreshed")
	if err := h.save(); err != nil {
		return nil, err
	}
	return tok, nil
}

// failLocked records a terminal failure, clears the refresh token and makes
// the handler answer auth_required from now on.
func (h *OAuthHandler) failLocked(code string) error {
	h.state.Failure = &OAuthFailure{At: time.Now().Unix(), Code: code}
	h.state.RefreshToken = ""
	h.token = nil
	_ = h.save()
	h.log("oauth_refresh_failed")
	h.terminal = NotSignedIn(h.opts.Name)
	return h.terminal
}

func (h *OAuthHandler) save() error {
	err := SaveOAuth(context.Background(), h.opts.Keyring, h.opts.Account, h.state)
	h.pendingSave = err != nil
	return err
}

func (h *OAuthHandler) log(event string) {
	if h.opts.Log != nil {
		h.opts.Log(event)
	}
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

var oauthCode = regexp.MustCompile(`^[a-z0-9_.-]{1,64}$`)

// sanitizeCode keeps an OAuth error code only when it is a plain token.
func sanitizeCode(code, fallback string) string {
	if oauthCode.MatchString(code) {
		return code
	}
	return fallback
}
