package auth

import (
	"slices"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

// reusableClient returns the stored DCR client to present as pre-registered and
// the redirect it was registered with, or nil. It is reused only when nothing
// it was bound to changed and the health file still remembers it: a sign-in
// that failed with it forgot it, so the next one registers a new client.
func (h *OAuthHandler) reusableClient(issuer, resource string, asm *oauthex.AuthServerMeta) (*oauthex.ClientCredentials, string) {
	prev := h.opts.Previous
	if h.opts.Client.ID != "" || prev == nil || prev.ClientID == "" || asm == nil {
		return nil, ""
	}
	if prev.URL != h.opts.URL || !issuersEqual(prev.Issuer, issuer) || prev.Resource != resource {
		return nil, ""
	}
	if prev.Failure != nil && (prev.Failure.Code == "invalid_client" || prev.Failure.Code == "unauthorized_client") {
		return nil, ""
	}
	// The SDK picks a pre-registered client's auth style from the metadata and
	// ignores the method it was registered with.
	if oauth2.AuthStyle(prev.AuthStyle) != sdkAuthStyle(asm.TokenEndpointAuthMethodsSupported) {
		return nil, ""
	}
	hash, redirect := h.opts.Health.Client(h.opts.Account)
	if hash == "" || hash != ClientHash(prev.ClientID) || redirect == "" {
		return nil, ""
	}
	if h.auth.RedirectURL != "" && h.auth.RedirectURL != redirect {
		return nil, ""
	}
	pre := &oauthex.ClientCredentials{ClientID: prev.ClientID, Issuer: issuer}
	if prev.ClientSecret != "" {
		pre.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: prev.ClientSecret}
	}
	return pre, redirect
}

// sdkAuthStyle mirrors go-sdk's selectTokenAuthMethod for pre-registered clients.
func sdkAuthStyle(supported []string) oauth2.AuthStyle {
	switch {
	case slices.Contains(supported, "client_secret_post"):
		return oauth2.AuthStyleInParams
	case slices.Contains(supported, "client_secret_basic"):
		return oauth2.AuthStyleInHeader
	}
	return oauth2.AuthStyleAutoDetect
}
