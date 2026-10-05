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

// KeyringService names the one Keychain service that holds every OAuth item.
const KeyringService = "mcparcel-oauth"

const keyringTimeout = 10 * time.Second

// ErrNoSession means no usable OAuth item exists for the connection.
var ErrNoSession = errors.New("no OAuth session")

// Keyring stores one secret per service and account. Get and Delete return
// ErrNoSession when the item is absent.
type Keyring interface {
	Get(service, account string) (string, error)
	Set(service, account, secret string) error
	Delete(service, account string) error
}

// SystemKeyring is the macOS Keychain through go-keyring.
type SystemKeyring struct{}

func (SystemKeyring) Get(service, account string) (string, error) {
	v, err := keyring.Get(service, account)
	return v, notFound(err)
}

func (SystemKeyring) Set(service, account, secret string) error {
	return keyring.Set(service, account, secret)
}

func (SystemKeyring) Delete(service, account string) error {
	return notFound(keyring.Delete(service, account))
}

func notFound(err error) error {
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrNoSession
	}
	return err
}

// OAuthState is the Keychain item of one connection. Access tokens never
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
	raw, err := keyringCall(ctx, k, func() (string, error) { return k.Get(KeyringService, account) })
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
		e.Message = "The sign-in is too large for one Keychain item."
		return e
	}
	_, err = keyringCall(ctx, k, func() (struct{}, error) { return struct{}{}, k.Set(KeyringService, account, string(data)) })
	return err
}

// DeleteOAuth removes the item; a missing item is (false, nil).
func DeleteOAuth(ctx context.Context, k Keyring, account string) (bool, error) {
	_, err := keyringCall(ctx, k, func() (struct{}, error) { return struct{}{}, k.Delete(KeyringService, account) })
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

// keyringCall runs one keyring call bounded by ctx and keyringTimeout. Any
// failure other than ErrNoSession becomes keychain_unavailable; the
// underlying error text is never passed on.
func keyringCall[T any](ctx context.Context, k Keyring, f func() (T, error)) (T, error) {
	var zero T
	if k == nil {
		return zero, output.NewError("keychain_unavailable", nil)
	}
	ctx, cancel := context.WithTimeout(ctx, keyringTimeout)
	defer cancel()
	type result struct {
		v   T
		err error
	}
	done := make(chan result, 1)
	go func() {
		v, err := f()
		done <- result{v, err}
	}()
	select {
	case r := <-done:
		if r.err == nil {
			return r.v, nil
		}
		if errors.Is(r.err, ErrNoSession) {
			return zero, ErrNoSession
		}
	case <-ctx.Done():
	}
	return zero, output.NewError("keychain_unavailable", nil)
}
