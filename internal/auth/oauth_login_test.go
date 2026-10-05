package auth_test

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

func TestLoginWithoutRegistrationOrClient(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{})
	b := newBrowser()
	h := f.handler(t, nil, auth.OAuthClient{}, b.login(), nil)
	_, err := send(ctx(t), h, f.mcpURL)
	e := code(t, err, "auth_failed")
	if !strings.Contains(e.Message, "auth.clientId") || len(b.shown()) != 0 {
		t.Fatal(e.Message, b.shown())
	}
}

func TestLoginDenied(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true, DenyWith: "access_denied"})
	b := newBrowser()
	h := f.handler(t, nil, auth.OAuthClient{}, b.login(), nil)
	_, err := send(ctx(t), h, f.mcpURL)
	if e := code(t, err, "auth_failed"); e.Message != "Sign-in to Demo failed: access_denied." {
		t.Fatal(e.Message)
	}
	if p := b.page(t); !strings.Contains(p.body, "access_denied") || !strings.Contains(p.body, "The fixture denied the request.") {
		t.Fatal(p.body)
	}
	_, err = send(ctx(t), h, f.mcpURL)
	code(t, err, "auth_failed")
	if len(b.shown()) != 1 || h.SignedIn() {
		t.Fatal("second browser flow", b.shown())
	}
}

func TestCallbackStateMismatchKeepsWaiting(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true})
	b := newBrowser()
	var mismatch page
	b.visit = func(u string) page {
		parsed, _ := url.Parse(u)
		mismatch = get(parsed.Query().Get("redirect_uri") + "?state=wrong&code=x")
		return get(u)
	}
	h := f.handler(t, nil, auth.OAuthClient{}, b.login(), nil)
	if status, err := send(ctx(t), h, f.mcpURL); err != nil || status != 200 {
		t.Fatal(status, err)
	}
	if p := b.page(t); p.status != 200 || mismatch.status != 400 || !strings.Contains(mismatch.body, "Wrong browser session") {
		t.Fatal(p.status, mismatch)
	}
}

func TestCallbackSecondUseExpired(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true})
	saving, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.kr.BeforeSet = func() { once.Do(func() { close(saving); <-release }) }
	b := newBrowser()
	var second page
	b.visit = func(u string) page {
		noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := noFollow.Get(u)
		if err != nil {
			return page{body: err.Error()}
		}
		resp.Body.Close()
		callback := resp.Header.Get("Location")
		first := make(chan page, 1)
		go func() { first <- get(callback) }()
		<-saving
		second = get(callback)
		close(release)
		return <-first
	}
	h := f.handler(t, nil, auth.OAuthClient{}, b.login(), nil)
	if status, err := send(ctx(t), h, f.mcpURL); err != nil || status != 200 {
		t.Fatal(status, err)
	}
	if p := b.page(t); p.status != 200 || second.status != 410 || !strings.Contains(second.body, "Link expired") {
		t.Fatal(p.status, second)
	}
}

func TestCallbackWrongIssuerFails(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true})
	b := newBrowser()
	b.visit = func(u string) page {
		noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := noFollow.Get(u)
		if err != nil {
			return page{body: err.Error()}
		}
		resp.Body.Close()
		return get(strings.Replace(resp.Header.Get("Location"), "iss=", "iss=https%3A%2F%2Fevil.invalid&was=", 1))
	}
	h := f.handler(t, nil, auth.OAuthClient{}, b.login(), nil)
	_, err := send(ctx(t), h, f.mcpURL)
	code(t, err, "auth_failed")
	if p := b.page(t); !strings.Contains(p.body, "Sign-in failed") {
		t.Fatal(p.body)
	}
	if _, err := f.kr.Get(auth.KeyringService, account); err != auth.ErrNoSession {
		t.Fatal("stored", err)
	}
}

// Some servers send iss in the callback without advertising RFC 9207 support.
// A matching iss is accepted; a wrong one still fails.
func TestCallbackUnadvertisedIssuer(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true, UnadvertisedIss: true})
	h := f.handler(t, nil, auth.OAuthClient{}, newBrowser().login(), nil)
	if _, err := send(ctx(t), h, f.mcpURL); err != nil {
		t.Fatal(err)
	}

	f = newFixture(t, testutil.AuthServerOptions{Registration: true, UnadvertisedIss: true})
	b := newBrowser()
	b.visit = func(u string) page {
		noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := noFollow.Get(u)
		if err != nil {
			return page{body: err.Error()}
		}
		resp.Body.Close()
		return get(strings.Replace(resp.Header.Get("Location"), "iss=", "iss=https%3A%2F%2Fevil.invalid&was=", 1))
	}
	h = f.handler(t, nil, auth.OAuthClient{}, b.login(), nil)
	_, err := send(ctx(t), h, f.mcpURL)
	code(t, err, "auth_failed")
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func TestCallbackPortBusy(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()
	b := newBrowser()
	h := f.handler(t, &config.OAuth{Type: "oauth", RedirectURL: "http://" + addr + "/cb"}, auth.OAuthClient{}, b.login(), nil)
	_, err = send(ctx(t), h, f.mcpURL)
	e := code(t, err, "auth_callback_unavailable")
	if !strings.Contains(e.Message, addr) || e.NextAction != "Close the program using that port, then run mcparcel auth login demo again." || len(b.shown()) != 0 {
		t.Fatal(e)
	}
}

func TestFixedRedirectVerbatim(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true})
	redirect := fmt.Sprintf("http://127.0.0.1:%d/cb", freePort(t))
	b := newBrowser()
	h := f.handler(t, &config.OAuth{Type: "oauth", RedirectURL: redirect, Scopes: []string{"read", "offline_access"}}, auth.OAuthClient{}, b.login(), nil)
	if status, err := send(ctx(t), h, f.mcpURL); err != nil || status != 200 {
		t.Fatal(status, err)
	}
	b.page(t)
	u, _ := url.Parse(b.shown()[0])
	if got := u.Query().Get("redirect_uri"); got != redirect {
		t.Fatal(got)
	}
	if got := u.Query().Get("scope"); got != "read offline_access" {
		t.Fatal("configured scopes not used:", got)
	}
	for _, bad := range []string{"https://127.0.0.1:1/cb", "http://example.com/cb", "::"} {
		h := f.handler(t, &config.OAuth{Type: "oauth", RedirectURL: bad}, auth.OAuthClient{}, newBrowser().login(), nil)
		_, err := send(ctx(t), h, f.mcpURL)
		code(t, err, "invalid_arguments")
	}
}

func TestIssuerOverrideMismatch(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true})
	b := newBrowser()
	h := f.handler(t, &config.OAuth{Type: "oauth", IssuerURL: "https://issuer.invalid"}, auth.OAuthClient{}, b.login(), nil)
	_, err := send(ctx(t), h, f.mcpURL)
	code(t, err, "auth_failed")
	if len(b.shown()) != 0 {
		t.Fatal("browser opened")
	}
	h = f.handler(t, &config.OAuth{Type: "oauth", IssuerURL: f.as.URL + "/"}, auth.OAuthClient{}, b.login(), nil)
	if status, err := send(ctx(t), h, f.mcpURL); err != nil || status != 200 {
		t.Fatal("trailing slash", status, err)
	}
}
