package auth_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/testutil"
)

// failureLog collects the sign-in failures a handler reports.
type failureLog struct {
	mu   sync.Mutex
	rows []string
}

func (l *failureLog) add(stage, code string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rows = append(l.rows, stage+" "+code)
}

func (l *failureLog) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.rows)
}

func loggedLogin(t *testing.T, mcpURL string, client auth.OAuthClient, b *browser) (*auth.OAuthHandler, *failureLog) {
	t.Helper()
	l := &failureLog{}
	h := auth.NewOAuthHandler(auth.OAuthOptions{Account: account, Name: "demo", Label: "Demo", URL: mcpURL, Client: client, Keyring: &testutil.MemKeyring{}, Login: b.login(), LogSignInFailure: l.add})
	t.Cleanup(h.Close)
	return h, l
}

func TestSignInFailureLogged(t *testing.T) {
	wrongIssuer := func(b *browser) {
		b.visit = func(u string) page {
			noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			resp, err := noFollow.Get(u)
			if err != nil {
				return page{body: err.Error()}
			}
			resp.Body.Close()
			return get(strings.Replace(resp.Header.Get("Location"), "iss=", "iss=https%3A%2F%2Fevil.invalid&was=", 1))
		}
	}
	noCode := func(b *browser) {
		b.visit = func(u string) page {
			parsed, _ := url.Parse(u)
			q := parsed.Query()
			return get(q.Get("redirect_uri") + "?state=" + url.QueryEscape(q.Get("state")))
		}
	}
	for _, tc := range []struct {
		name   string
		o      testutil.AuthServerOptions
		client auth.OAuthClient
		setup  func(*browser)
		want   string
	}{
		{"no registration", testutil.AuthServerOptions{}, auth.OAuthClient{}, nil, "registration registration_unsupported"},
		{"denied", testutil.AuthServerOptions{Registration: true, DenyWith: "access_denied"}, auth.OAuthClient{}, nil, "callback access_denied"},
		{"no code", testutil.AuthServerOptions{Registration: true}, auth.OAuthClient{}, noCode, "callback invalid_request"},
		{"wrong issuer", testutil.AuthServerOptions{Registration: true}, auth.OAuthClient{}, wrongIssuer, "callback issuer_mismatch"},
		{"bad client secret", testutil.AuthServerOptions{ClientID: "fixture-client", ClientSecret: "right"}, auth.OAuthClient{ID: "fixture-client", Secret: "wrong"}, nil, "token_exchange invalid_client"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, tc.o)
			b := newBrowser()
			if tc.setup != nil {
				tc.setup(b)
			}
			h, l := loggedLogin(t, f.mcpURL, tc.client, b)
			_, err := send(ctx(t), h, f.mcpURL)
			code(t, err, "auth_failed")
			if got := l.get(); len(got) != 1 || got[0] != tc.want {
				t.Fatalf("logged %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSignInDiscoveryFailureLogged(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/.well-known/oauth-protected-resource") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"resource":"` + srv.URL + `/mcp","authorization_servers":[]}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	b := newBrowser()
	h, l := loggedLogin(t, srv.URL+"/mcp", auth.OAuthClient{}, b)
	_, err := send(ctx(t), h, srv.URL+"/mcp")
	code(t, err, "auth_failed")
	if got := l.get(); len(got) != 1 || got[0] != "discovery discovery_failed" {
		t.Fatalf("logged %q", got)
	}
}

func TestSignInSuccessLogsNoFailure(t *testing.T) {
	f := newFixture(t, testutil.AuthServerOptions{Registration: true})
	b := newBrowser()
	h, l := loggedLogin(t, f.mcpURL, auth.OAuthClient{}, b)
	if status, err := send(ctx(t), h, f.mcpURL); err != nil || status != 200 {
		t.Fatal(status, err)
	}
	if got := l.get(); len(got) != 0 {
		t.Fatalf("logged %q", got)
	}
}
