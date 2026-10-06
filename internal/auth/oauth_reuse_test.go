package auth_test

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

// relogin signs in like the pool does: with the stored item as Previous and
// the health file. It returns the handler and the sign-in error.
func (f *fixture) relogin(t *testing.T, health *auth.Health, a *config.OAuth, c context.Context) (*auth.OAuthHandler, error) {
	t.Helper()
	var prev *auth.OAuthState
	if s, err := auth.LoadOAuth(context.Background(), f.kr, account); err == nil {
		prev = &s
	}
	b := newBrowser()
	h := auth.NewOAuthHandler(auth.OAuthOptions{Account: account, Name: "demo", Label: "Demo", URL: f.mcpURL, Auth: a, Keyring: f.kr, Login: b.login(), Health: health, Previous: prev})
	t.Cleanup(h.Close)
	status, err := send(c, h, f.mcpURL)
	if err == nil && status != 200 {
		t.Fatal("status", status)
	}
	if len(b.shown()) > 0 {
		b.page(t)
	}
	return h, err
}

var postMethod = &config.OAuth{Type: "oauth", TokenEndpointAuthMethod: "client_secret_post"}

func registrations(f *fixture) int { r, _, _ := f.as.Counts(); return r }

func TestReloginReusesClientRegistration(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true})
	health := newHealthRig(t, nil)
	if _, err := f.relogin(t, health.Health, postMethod, ctx(t)); err != nil {
		t.Fatal(err)
	}
	first := f.stored(t).ClientID
	if _, err := f.relogin(t, health.Health, postMethod, ctx(t)); err != nil {
		t.Fatal(err)
	}
	if n := registrations(f); n != 1 || f.stored(t).ClientID != first {
		t.Fatal("registrations", n)
	}
	if e := health.last(t); e.Kind != auth.HealthAuthorized || !e.ReusedClient {
		t.Fatalf("%+v", e)
	}
	if _, err := f.open(t, nil, nil).Token(); err != nil {
		t.Fatal("refresh with the reused client", err)
	}
	if c := health.conn(t); c.Client != auth.ClientHash(first) || c.Redirect == "" {
		t.Fatalf("%+v", c)
	}
}

// Without client_secret_post the client was registered for basic auth, but
// the SDK would present it with post: register a new one.
func TestReloginRegistersWhenAuthStyleDiffers(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true})
	health := newHealthRig(t, nil)
	for range 2 {
		if _, err := f.relogin(t, health.Health, nil, ctx(t)); err != nil {
			t.Fatal(err)
		}
	}
	if n := registrations(f); n != 2 {
		t.Fatal("registrations", n)
	}
}

func TestReloginRegistersWhenPortBusy(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true})
	health := newHealthRig(t, nil)
	if _, err := f.relogin(t, health.Health, postMethod, ctx(t)); err != nil {
		t.Fatal(err)
	}
	_, redirect := health.Client(account)
	u, err := url.Parse(redirect)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if _, err := f.relogin(t, health.Health, postMethod, ctx(t)); err != nil {
		t.Fatal(err)
	}
	if n := registrations(f); n != 2 {
		t.Fatal("registrations", n)
	}
	if e := health.last(t); e.ReusedClient {
		t.Fatalf("%+v", e)
	}
}

// A provider that dropped the client never redirects back, so that sign-in
// times out; it forgets the client and the next sign-in registers a new one.
func TestReloginAfterDroppedClientRegistersNextTime(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true})
	health := newHealthRig(t, nil)
	if _, err := f.relogin(t, health.Health, postMethod, ctx(t)); err != nil {
		t.Fatal(err)
	}
	f.as.DropClients()
	short, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := f.relogin(t, health.Health, postMethod, short)
	code(t, err, "auth_required")
	if hash, redirect := health.Client(account); hash != "" || redirect != "" {
		t.Fatal("dropped client remembered")
	}
	if _, err := f.relogin(t, health.Health, postMethod, ctx(t)); err != nil {
		t.Fatal(err)
	}
	if n := registrations(f); n != 2 {
		t.Fatal("registrations", n)
	}
}

func TestReloginSkipsRejectedClient(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true})
	health := newHealthRig(t, nil)
	if _, err := f.relogin(t, health.Health, postMethod, ctx(t)); err != nil {
		t.Fatal(err)
	}
	s := f.stored(t)
	s.RefreshToken, s.Failure = "", &auth.OAuthFailure{At: 1, Code: "invalid_client"}
	if err := auth.SaveOAuth(context.Background(), f.kr, account, s); err != nil {
		t.Fatal(err)
	}
	if _, err := f.relogin(t, health.Health, postMethod, ctx(t)); err != nil {
		t.Fatal(err)
	}
	if n := registrations(f); n != 2 {
		t.Fatal("registrations", n)
	}
}

// A configured auth.redirectUrl is fixed, so it is the safest case to reuse
// (section 0 problem 6); a changed one registers a new client.
func TestReloginReusesWithConfiguredRedirect(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true})
	health := newHealthRig(t, nil)
	fixed := &config.OAuth{Type: "oauth", TokenEndpointAuthMethod: "client_secret_post", RedirectURL: fmt.Sprintf("http://127.0.0.1:%d/cb", freePort(t))}
	for range 2 {
		if _, err := f.relogin(t, health.Health, fixed, ctx(t)); err != nil {
			t.Fatal(err)
		}
	}
	if n := registrations(f); n != 1 {
		t.Fatal("registrations", n)
	}
	if e := health.last(t); !e.ReusedClient {
		t.Fatalf("%+v", e)
	}
	if _, redirect := health.Client(account); redirect != fixed.RedirectURL {
		t.Fatal("redirect", redirect)
	}
	moved := *fixed
	moved.RedirectURL = fmt.Sprintf("http://127.0.0.1:%d/cb", freePort(t))
	if _, err := f.relogin(t, health.Health, &moved, ctx(t)); err != nil {
		t.Fatal(err)
	}
	if n := registrations(f); n != 2 {
		t.Fatal("client reused for a changed redirect", n)
	}
}

// A stored client still signs in after the provider removed its registration
// endpoint (section 0 problem 12).
func TestReloginReusesWhenRegistrationRemoved(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true})
	health := newHealthRig(t, nil)
	if _, err := f.relogin(t, health.Health, postMethod, ctx(t)); err != nil {
		t.Fatal(err)
	}
	f.as.StopRegistration()
	if _, err := f.relogin(t, health.Health, postMethod, ctx(t)); err != nil {
		t.Fatal(err)
	}
	if e := health.last(t); e.Kind != auth.HealthAuthorized || !e.ReusedClient || registrations(f) != 1 {
		t.Fatalf("%+v", e)
	}
	// Without a reusable client the sign-in fails as before.
	if err := health.RememberClient(account, "", ""); err != nil {
		t.Fatal(err)
	}
	_, err := f.relogin(t, health.Health, postMethod, ctx(t))
	if e := code(t, err, "auth_failed"); !strings.Contains(e.Message, "no dynamic client registration") {
		t.Fatal(e)
	}
}
