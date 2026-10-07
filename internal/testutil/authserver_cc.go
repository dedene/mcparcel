package testutil

import (
	"crypto/rand"
	"net/http"
	"time"
)

// GrantCounts reports client_credentials tokens issued and 401 answers sent
// by Protect (expired, revoked or unknown tokens).
type GrantCounts struct {
	ClientCredentials int
	Unauthorized      int
}

// GrantCounts reports the client_credentials grants and the 401s so far.
func (a *AuthServer) GrantCounts() GrantCounts {
	a.mu.Lock()
	defer a.mu.Unlock()
	return GrantCounts{ClientCredentials: a.ccGrants, Unauthorized: a.unauthorized}
}

// clientCredentialsLocked issues a fresh opaque access token for the
// preconfigured client, already authenticated by token; a.mu is held. The
// token is valid for CCExpiresIn seconds, or AccessTTL when that is 0, and
// Revoke invalidates it like any other access token.
func (a *AuthServer) clientCredentialsLocked(w http.ResponseWriter, r *http.Request) {
	a.ccGrants++
	ttl := a.o.AccessTTL
	if a.o.CCExpiresIn > 0 {
		ttl = time.Duration(a.o.CCExpiresIn) * time.Second
	}
	access := a.o.TokenPrefix + rand.Text()
	a.access[access] = a.o.Now().Add(ttl)
	tokenType := a.o.CCTokenType
	if tokenType == "" {
		tokenType = "bearer"
	}
	resp := map[string]any{"access_token": access, "token_type": tokenType}
	if a.o.CCExpiresIn > 0 {
		resp["expires_in"] = a.o.CCExpiresIn
	}
	if scope := r.PostForm.Get("scope"); scope != "" {
		resp["scope"] = scope
	}
	writeJSON(w, 200, resp)
}
