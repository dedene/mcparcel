package auth_test

import (
	"testing"

	"golang.org/x/oauth2"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/testutil"
)

// Raindrop.io registers a public client and names no token endpoint auth
// method; RFC 7591's client_secret_basic default then sends a Basic header
// with an empty secret, which it refuses with 401.
func TestLoginPublicRegistrationWithoutAuthMethod(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true, OmitAuthMethod: true, PublicRegistration: true})
	signIn(t, f, auth.OAuthClient{})
	s := f.stored(t)
	if s.ClientID == "" || s.ClientSecret != "" || oauth2.AuthStyle(s.AuthStyle) != oauth2.AuthStyleInParams {
		t.Fatalf("stored %+v", s)
	}
	if _, err := f.session(t, auth.OAuthClient{}).Token(); err != nil {
		t.Fatal(err)
	}
	if r, x, n := f.as.Counts(); r != 1 || x != 1 || n != 1 {
		t.Fatal("registrations/exchanges/refreshes", r, x, n)
	}
}

// A confidential client without a named method keeps the RFC 7591 default.
func TestLoginSecretRegistrationWithoutAuthMethod(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true, OmitAuthMethod: true})
	signIn(t, f, auth.OAuthClient{})
	if s := f.stored(t); s.ClientSecret == "" || oauth2.AuthStyle(s.AuthStyle) != oauth2.AuthStyleInHeader {
		t.Fatalf("stored %+v", s)
	}
}
