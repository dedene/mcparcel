package auth

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"golang.org/x/oauth2"

	"github.com/dedene/mcparcel/internal/output"
)

const (
	ccMintTimeout = 30 * time.Second
	ccMinLead     = 10 * time.Second
	// ccRejectWindow: a 401 for the token a 401 minted less than this long
	// ago proves the server refuses fresh tokens, so it is not minted again.
	ccRejectWindow = 30 * time.Second
)

// ClientCredentialsConfig configures a client_credentials handler. The token
// URL is used as is: no discovery and no client registration. Secret is
// never logged and never part of an error. AuthStyle is
// oauth2.AuthStyleInParams (client_secret_post, also for the zero value) or
// oauth2.AuthStyleInHeader (client_secret_basic). HTTPClient nil means a
// client without proxy and with a 30 s timeout; redirects are never
// followed. Now is the clock (nil means time.Now). Log receives
// oauth_token_minted (trigger first|expiry|401, ttl in seconds, 0 when
// unknown), oauth_token_mint_failed (a sanitized code and the token
// endpoint's HTTP status, 0 without an answer) and oauth_token_rejected (the
// MCP server's 401 for a fresh token or 403: code and status). No field
// ever holds a URL, the client ID, the secret or a token.
type ClientCredentialsConfig struct {
	TokenURL   string
	ClientID   string
	Secret     string
	Scopes     []string
	AuthStyle  oauth2.AuthStyle
	HTTPClient *http.Client
	Now        func() time.Time
	Log        func(event string, fields map[string]any)
}

// ClientCredentials implements the SDK's OAuthHandler for the OAuth
// client_credentials grant. Its token lives in memory only. Concurrent
// callers share one mint; a 401 re-mints once and lets the SDK resend the
// request once, also for tools/call; a 403 never re-mints.
type ClientCredentials struct {
	cfg    ClientCredentialsConfig
	client *http.Client
	base   context.Context // canceled by Close; bounds every mint
	cancel context.CancelFunc

	mu      sync.Mutex
	closed  bool
	token   *oauth2.Token
	issued  time.Time     // when the request that minted token was sent
	refresh time.Duration // token age from which it is re-minted; 0 means until a 401
	minting *ccMint       // the mint in flight, nil when none
	minted  bool          // a mint succeeded once: a later one is not "first"
	// reminted is the token the last 401-triggered mint produced, at remintedAt.
	reminted   string
	remintedAt time.Time
}

// UnsentError marks a client_credentials failure that kept an MCP request
// from being sent or resent: no token could be had, or the server answered
// 401 and no new token followed. A 401 is given before the request is
// executed (D7), so the request did not run. Err is an *output.Error or a
// context error; a 403 is never marked, its outcome is the server's.
type UnsentError struct{ Err error }

func (e *UnsentError) Error() string { return e.Err.Error() }
func (e *UnsentError) Unwrap() error { return e.Err }

func unsent(err error) error {
	if err == nil {
		return nil
	}
	return &UnsentError{Err: err}
}

// ccMint is one token request; done is closed once tok or err is set.
type ccMint struct {
	done    chan struct{}
	for401  bool
	trigger string // first, expiry or 401: why it was started, for the log
	tok     *oauth2.Token
	err     error
}

var _ sdkauth.OAuthHandler = (*ClientCredentials)(nil)

func NewClientCredentials(cfg ClientCredentialsConfig) (*ClientCredentials, error) {
	u, err := url.Parse(cfg.TokenURL)
	if err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Host == "" || cfg.ClientID == "" || cfg.Secret == "" {
		e := output.NewError("config_required", nil)
		e.Message = "A client_credentials connection needs a token URL, a client ID and a client secret."
		e.NextAction = "Check auth.tokenUrl, auth.clientId and auth.clientSecret and that their values are set."
		return nil, e
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	client := &http.Client{Timeout: ccMintTimeout, Transport: &http.Transport{Proxy: nil, ForceAttemptHTTP2: true}}
	if cfg.HTTPClient != nil {
		copied := *cfg.HTTPClient
		client = &copied
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	cfg.Scopes = append([]string(nil), cfg.Scopes...)
	h := &ClientCredentials{cfg: cfg, client: client}
	h.base, h.cancel = context.WithCancel(context.Background())
	return h, nil
}

// TokenSource returns a source whose Token waits for a mint in flight only
// as long as ctx lives. It is never nil.
func (h *ClientCredentials) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	return ccSource{h: h, ctx: ctx}, nil
}

type ccSource struct {
	h   *ClientCredentials
	ctx context.Context
}

func (s ccSource) Token() (*oauth2.Token, error) { return s.h.Token(s.ctx) }

// Token returns the cached token until its refresh point, else the result
// of the one mint all callers share. A canceled ctx stops waiting; the mint
// goes on for the others.
func (h *ClientCredentials) Token(ctx context.Context) (*oauth2.Token, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, unsent(ccClosed())
	}
	if h.freshLocked(h.cfg.Now()) {
		tok := h.token
		h.mu.Unlock()
		return tok, nil
	}
	m := h.startMintLocked(false)
	h.mu.Unlock()
	tok, err := ccWait(ctx, m)
	return tok, unsent(err)
}

// Authorize handles the MCP server's 401 and 403 answers. A 401 for the
// current token drops it and mints a new one (or joins the mint in flight);
// a 401 for a token that was already replaced mints nothing. The SDK then
// resends the request once with the current token.
func (h *ClientCredentials) Authorize(ctx context.Context, req *http.Request, resp *http.Response) error {
	if resp != nil && resp.Body != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
		_ = resp.Body.Close()
	}
	if resp != nil && resp.StatusCode == http.StatusForbidden {
		h.log("oauth_token_rejected", map[string]any{"code": "http_403", "status": http.StatusForbidden})
		e := output.NewError("auth_failed", nil)
		e.Message = "The server refused this client access (HTTP 403)."
		e.NextAction = "Check the client's scopes and permissions on the server."
		return e
	}
	rejected := ""
	if req != nil {
		rejected, _ = strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return unsent(ccClosed())
	}
	m := h.minting
	if m == nil {
		switch {
		case h.token != nil && h.token.AccessToken != rejected:
			h.mu.Unlock()
			return nil // already replaced: resend with the current token
		case h.token != nil && rejected == h.reminted && h.cfg.Now().Sub(h.remintedAt) < ccRejectWindow:
			h.token, h.reminted = nil, ""
			h.mu.Unlock()
			h.log("oauth_token_rejected", map[string]any{"code": "token_rejected", "status": http.StatusUnauthorized})
			e := output.NewError("auth_failed", nil)
			e.Message = "The server rejected a newly issued access token (token_rejected)."
			e.NextAction = "Check that auth.tokenUrl issues tokens for this MCP server."
			return unsent(e)
		}
		h.token = nil
		m = h.startMintLocked(true)
	}
	h.mu.Unlock()
	_, err := ccWait(ctx, m)
	return unsent(err)
}

// Close drops the token and aborts a mint in flight; later calls fail.
func (h *ClientCredentials) Close() {
	h.mu.Lock()
	h.closed, h.token, h.reminted = true, nil, ""
	h.mu.Unlock()
	h.cancel()
	h.client.CloseIdleConnections()
}

// freshLocked reports whether the token is used as is. The wall clock
// catches a suspended host or a clock moved forward; the monotonic clock
// catches one moved back.
func (h *ClientCredentials) freshLocked(now time.Time) bool {
	if h.token == nil {
		return false
	}
	if h.refresh == 0 {
		return true
	}
	return now.Sub(h.issued) < h.refresh && now.Round(0).Before(h.issued.Round(0).Add(h.refresh))
}

// refreshAge is the token age at which a token of this lifetime is
// re-minted: when max(lifetime/5, 10 s), capped at lifetime/2, remains.
// An unknown lifetime (0) means never.
func refreshAge(lifetime time.Duration) time.Duration {
	if lifetime <= 0 {
		return 0
	}
	lead := min(max(lifetime/5, ccMinLead), lifetime/2)
	return lifetime - lead
}

// startMintLocked returns the mint in flight or starts one; h.mu is held.
func (h *ClientCredentials) startMintLocked(for401 bool) *ccMint {
	if h.minting != nil {
		return h.minting
	}
	trigger := "first"
	switch {
	case for401:
		trigger = "401"
	case h.token != nil:
		trigger = "expiry"
	case h.minted:
		trigger = "401" // the token a 401 dropped was not replaced yet
	}
	m := &ccMint{done: make(chan struct{}), for401: for401, trigger: trigger}
	h.minting = m
	go h.run(m)
	return m
}

func (h *ClientCredentials) run(m *ccMint) {
	start := h.cfg.Now()
	ctx, cancel := context.WithTimeout(h.base, ccMintTimeout)
	tok, lifetime, status, reason, err := h.mint(ctx)
	cancel()
	h.mu.Lock()
	h.minting = nil
	switch {
	case h.closed:
		tok, err = nil, ccClosed()
	case err == nil:
		h.token, h.issued, h.refresh = tok, start, refreshAge(lifetime)
		h.reminted, h.minted = "", true
		if m.for401 {
			h.reminted, h.remintedAt = tok.AccessToken, start
		}
	}
	m.tok, m.err = tok, err
	h.mu.Unlock()
	close(m.done)
	if reason != "" {
		h.log("oauth_token_mint_failed", map[string]any{"code": reason, "status": status})
	} else if err == nil {
		h.log("oauth_token_minted", map[string]any{"trigger": m.trigger, "ttl": int64(lifetime / time.Second)})
	}
}

func ccWait(ctx context.Context, m *ccMint) (*oauth2.Token, error) {
	select {
	case <-m.done:
		return m.tok, m.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func ccClosed() error {
	e := output.NewError("connection_failed", nil)
	e.Message = "The connection was closed."
	return e
}

func (h *ClientCredentials) log(event string, fields map[string]any) {
	if h.cfg.Log != nil {
		h.cfg.Log(event, fields)
	}
}
