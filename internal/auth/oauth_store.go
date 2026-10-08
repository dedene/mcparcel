package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/dedene/mcparcel/internal/output"
)

// KeyringService names the one keyring service that holds every OAuth item.
const KeyringService = "mcparcel-oauth"

const (
	keyringTimeout = 10 * time.Second
	// keyringInteractiveTimeout leaves the user time to answer an unlock
	// prompt before a sign-in starts.
	keyringInteractiveTimeout = 2 * time.Minute
)

// ErrNoSession means no usable OAuth item exists for the connection.
var ErrNoSession = errors.New("no OAuth session")

// Keyring stores one secret per service and account. Get and Delete return
// ErrNoSession when the item is absent.
type Keyring interface {
	Get(service, account string) (string, error)
	Set(service, account, secret string) error
	Delete(service, account string) error
}

// keyringGate is implemented by a keyring whose calls run one at a time
// (the Linux Secret Service, whose prompts block a call until answered).
// acquire takes the call slot: release frees it, and wedge marks it as held
// by an abandoned call until release runs. stuck reports that mark.
type keyringGate interface {
	acquire(ctx context.Context) (release, wedge func(), err error)
	stuck() bool
}

// KeyringWedged reports whether k still waits on a call that keyringCall
// abandoned (an unanswered keyring prompt); every call fails fast until then.
func KeyringWedged(k Keyring) bool {
	g, ok := k.(keyringGate)
	return ok && g.stuck()
}

// notFound maps go-keyring's missing item to ErrNoSession.
func notFound(err error) error {
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrNoSession
	}
	return err
}

// OAuthState is the keyring item of one connection. Access tokens never
// appear here; ClientSecret is only a DCR-issued one.
type OAuthState struct {
	Version      int           `json:"v"`
	URL          string        `json:"u"`
	Issuer       string        `json:"i"`
	Resource     string        `json:"r"`
	TokenURL     string        `json:"t"`
	AuthStyle    int           `json:"a,omitempty"`
	ClientID     string        `json:"c,omitempty"`
	ClientSecret string        `json:"s,omitempty"`
	RefreshToken string        `json:"rt,omitempty"`
	AccessExpiry int64         `json:"e,omitempty"`
	Failure      *OAuthFailure `json:"f,omitempty"`
}

// OAuthFailure is the last terminal refresh failure (unix seconds, sanitized code).
type OAuthFailure struct {
	At   int64  `json:"at"`
	Code string `json:"c"`
}

func (OAuthState) String() string   { return "OAuthState{redacted}" }
func (OAuthState) GoString() string { return "OAuthState{redacted}" }

func LoadOAuth(ctx context.Context, k Keyring, account string) (OAuthState, error) {
	return loadOAuth(ctx, k, account, keyringTimeout)
}

// LoadOAuthInteractive is LoadOAuth for a sign-in the user is waiting on: it
// gives a keyring unlock prompt keyringInteractiveTimeout to be answered.
func LoadOAuthInteractive(ctx context.Context, k Keyring, account string) (OAuthState, error) {
	return loadOAuth(ctx, k, account, keyringInteractiveTimeout)
}

func loadOAuth(ctx context.Context, k Keyring, account string, timeout time.Duration) (OAuthState, error) {
	raw, err := keyringCall(ctx, k, timeout, func() (string, error) { return k.Get(KeyringService, account) })
	if err != nil {
		return OAuthState{}, err
	}
	var s OAuthState
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&s) != nil || dec.More() || s.Version != 1 {
		return OAuthState{}, ErrNoSession
	}
	return s, nil
}

func SaveOAuth(ctx context.Context, k Keyring, account string, s OAuthState) error {
	data, err := json.Marshal(s)
	if err != nil {
		return output.NewError("internal_error", nil)
	}
	if keychainCommandLen(account, data) > 4096 {
		e := output.NewError("keychain_unavailable", nil)
		e.Message = "The sign-in is too large for one keyring item."
		return e
	}
	_, err = keyringCall(ctx, k, keyringTimeout, func() (struct{}, error) { return struct{}{}, k.Set(KeyringService, account, string(data)) })
	return err
}

// DeleteOAuth removes the item; a missing item is (false, nil).
func DeleteOAuth(ctx context.Context, k Keyring, account string) (bool, error) {
	_, err := keyringCall(ctx, k, keyringTimeout, func() (struct{}, error) { return struct{}{}, k.Delete(KeyringService, account) })
	if errors.Is(err, ErrNoSession) {
		return false, nil
	}
	return err == nil, err
}

// keychainCommandLen bounds the line go-keyring writes to `security -i`.
// go-keyring starts that process before its own 4096-byte check and leaks it
// on ErrSetDataTooBig, so the size is checked here first.
func keychainCommandLen(account string, data []byte) int {
	quoted := func(s string) int { return len(s) + 2 + 4*strings.Count(s, "'") }
	return len("add-generic-password -U -s ") + quoted(KeyringService) + len(" -a ") + quoted(account) +
		len(" -w ") + 2 + len("go-keyring-base64:") + base64.StdEncoding.EncodedLen(len(data)) + 1
}

// keyringCall runs one keyring call bounded by ctx and timeout. A gated
// keyring (keyringGate) runs one call at a time: a call that cannot take the
// slot, or that times out while the keyring still works on it, gives
// KeyringPromptPendingError, and the abandoned call wedges the keyring until
// it returns. Any other failure except ErrNoSession becomes
// keychain_unavailable; the underlying error text is never passed on.
func keyringCall[T any](ctx context.Context, k Keyring, timeout time.Duration, f func() (T, error)) (T, error) {
	var zero T
	if k == nil {
		return zero, output.NewError("keychain_unavailable", nil)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	release, wedge := func() {}, func() {}
	gate, gated := k.(keyringGate)
	if gated {
		var err error
		if release, wedge, err = gate.acquire(ctx); err != nil {
			return zero, output.KeyringPromptPendingError()
		}
	}
	type result struct {
		v   T
		err error
	}
	done := make(chan result, 1)
	go func() {
		v, err := func() (T, error) {
			defer release()
			return f()
		}()
		done <- result{v, err}
	}()
	var r result
	select {
	case r = <-done:
	case <-ctx.Done():
		select {
		case r = <-done:
		default:
			if gated {
				wedge()
				return zero, output.KeyringPromptPendingError()
			}
			return zero, output.NewError("keychain_unavailable", nil)
		}
	}
	switch {
	case r.err == nil:
		return r.v, nil
	case errors.Is(r.err, ErrNoSession):
		return zero, ErrNoSession
	}
	return zero, output.NewError("keychain_unavailable", nil)
}
