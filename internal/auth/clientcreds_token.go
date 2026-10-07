package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/dedene/mcparcel/internal/output"
)

const (
	ccBodyLimit   = 64 * 1024
	ccMaxLifetime = 366 * 24 * time.Hour
)

// ccErrorCodes are the RFC 6749 §5.2 token error codes an error names; any
// other "error" value is replaced by the HTTP status class, so a server that
// echoes the secret as its error code cannot leak it.
var ccErrorCodes = []string{"invalid_request", "invalid_client", "invalid_grant", "unauthorized_client", "unsupported_grant_type", "invalid_scope"}

// mint posts one client_credentials grant. lifetime is 0 when the answer has
// no positive expires_in. status is the token endpoint's HTTP status, 0
// without an answer; reason is the sanitized failure code for the log.
// Response bodies never enter an error.
func (h *ClientCredentials) mint(ctx context.Context) (tok *oauth2.Token, lifetime time.Duration, status int, reason string, err error) {
	form := url.Values{"grant_type": {"client_credentials"}}
	if len(h.cfg.Scopes) > 0 {
		form.Set("scope", strings.Join(h.cfg.Scopes, " "))
	}
	basic := h.cfg.AuthStyle == oauth2.AuthStyleInHeader
	if !basic {
		form.Set("client_id", h.cfg.ClientID)
		form.Set("client_secret", h.cfg.Secret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, 0, "invalid_token_url", ccConnectionFailed("invalid_token_url")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if basic {
		// RFC 6749 §2.3.1: both parts are form-encoded before Basic encoding.
		req.SetBasicAuth(url.QueryEscape(h.cfg.ClientID), url.QueryEscape(h.cfg.Secret))
	}
	resp, err := h.client.Do(req)
	if err != nil {
		reason = "network_error"
		var ne net.Error
		if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout() {
			reason = "timeout"
		}
		return nil, 0, 0, reason, ccConnectionFailed(reason)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, ccBodyLimit+1))
	status = resp.StatusCode
	if err != nil {
		return nil, 0, status, "network_error", ccConnectionFailed("network_error")
	}
	switch {
	case status == http.StatusOK:
		tok, lifetime, ok := parseCCToken(body)
		if !ok {
			return nil, 0, status, "invalid_token_response", ccAuthFailed("invalid_token_response", "The token endpoint returned no usable bearer token")
		}
		return tok, lifetime, status, "", nil
	case status == http.StatusTooManyRequests || status >= 500:
		reason = statusClass(status)
		return nil, 0, status, reason, ccConnectionFailed(reason)
	case status >= 300 && status < 400:
		return nil, 0, status, "redirect", ccConnectionFailed("redirect")
	case status >= 400:
		reason = statusClass(status)
		var answer struct {
			Error string `json:"error"`
		}
		if (status == 400 || status == 401) && json.Unmarshal(body, &answer) == nil && slices.Contains(ccErrorCodes, answer.Error) {
			reason = answer.Error
		}
		return nil, 0, status, reason, ccAuthFailed(reason, "The token endpoint rejected the client credentials")
	}
	return nil, 0, status, "invalid_token_response", ccAuthFailed("invalid_token_response", "The token endpoint returned no usable bearer token")
}

// parseCCToken accepts a JSON answer with a nonempty access_token and a
// bearer token_type (any case).
func parseCCToken(body []byte) (*oauth2.Token, time.Duration, bool) {
	if len(body) > ccBodyLimit {
		return nil, 0, false
	}
	var answer struct {
		AccessToken string          `json:"access_token"`
		TokenType   string          `json:"token_type"`
		ExpiresIn   json.RawMessage `json:"expires_in"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if dec.Decode(&answer) != nil || answer.AccessToken == "" || !strings.EqualFold(answer.TokenType, "bearer") {
		return nil, 0, false
	}
	var lifetime time.Duration
	text := strings.Trim(string(answer.ExpiresIn), `"`)
	if seconds, err := strconv.ParseFloat(text, 64); err == nil && seconds >= 1 {
		lifetime = min(time.Duration(seconds)*time.Second, ccMaxLifetime)
	}
	tok := &oauth2.Token{AccessToken: answer.AccessToken, TokenType: "Bearer"}
	if lifetime > 0 {
		tok.ExpiresIn = int64(lifetime / time.Second)
	}
	return tok, lifetime, true
}

// statusClass names a status without echoing anything from the body:
// http_429 for rate limiting, else http_4xx or http_5xx.
func statusClass(status int) string {
	if status == http.StatusTooManyRequests {
		return "http_429"
	}
	return fmt.Sprintf("http_%dxx", status/100)
}

func ccAuthFailed(reason, text string) error {
	e := output.NewError("auth_failed", nil)
	e.Message = text + " (" + reason + ")."
	e.NextAction = "Check auth.clientId, auth.clientSecret, auth.scopes and auth.tokenUrl for this connection."
	return e
}

func ccConnectionFailed(reason string) error {
	e := output.NewError("connection_failed", nil)
	e.Message = "Could not get an access token from the token endpoint (" + reason + ")."
	e.NextAction = "Check auth.tokenUrl and the network, then retry."
	return e
}
