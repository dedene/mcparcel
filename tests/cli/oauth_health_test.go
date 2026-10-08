package cli_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

type sessionStatus struct {
	Connection, State, NextAction        string
	SignedIn                             bool
	LastRefreshAt, RefreshTokenExpiresAt string
	Cause, PreviousCause                 *struct{ Code, Message string }
	Events                               []struct {
		Kind, Code, Trigger string
		Terminal            bool
	}
}

func (r *oauthRig) sessionStatus(args ...string) []sessionStatus {
	r.t.Helper()
	v := r.check(r.run(append([]string{"auth", "status", "--json"}, args...)...), 0, "")
	var d struct{ Items []sessionStatus }
	if e := json.Unmarshal(v.envelope.Data, &d); e != nil {
		r.t.Fatal(e)
	}
	return d.Items
}

func (r *oauthRig) healthFile() string {
	b, _ := os.ReadFile(filepath.Join(r.paths.StateDir, "oauth-health.json"))
	return string(b)
}

func TestAuthStatusExplainsRevokedRefreshBlackBox(t *testing.T) {
	r := newOAuthRig(t, testutil.AuthServerOptions{Registration: true}, &config.OAuth{Type: "oauth"})
	r.login(0, "")
	r.as.Revoke()
	r.check(r.run("call", "n.echo", "text=hi", "--json"), 3, "auth_required")
	items := r.sessionStatus("n")
	if len(items) != 1 || items[0].State != "sign-in required" || items[0].SignedIn || items[0].Cause == nil ||
		items[0].Cause.Code != "refresh_expired_or_revoked" || items[0].NextAction != "mcparcel auth n" || items[0].LastRefreshAt == "" {
		t.Fatalf("%+v", items)
	}
	events := items[0].Events
	if len(events) != 2 || events[0].Kind != "authorized" || events[1].Kind != "refresh_failed" || events[1].Code != "invalid_grant" || !events[1].Terminal {
		t.Fatalf("%+v", events)
	}
	if all := r.sessionStatus(); len(all) != 1 || all[0].Events != nil || all[0].Cause == nil {
		t.Fatalf("list carries events: %+v", all)
	}
	human := r.run("auth", "status")
	if human.code != 0 || human.stdout != "local:n  sign-in required  (refresh_expired_or_revoked)\n" {
		t.Fatalf("%q", human.stdout)
	}
	// The keys are the documented camelCase names; json.Unmarshal above
	// matches them without regard to case.
	raw := r.run("auth", "status", "n", "--json").stdout
	for _, key := range []string{`"state":`, `"signedIn":`, `"lastRefreshAt":`, `"lastRefreshFailure":`, `"keepAlive":`, `"cause":{"code":`, `"nextAction":`, `"events":[{"at":`, `"kind":`, `"terminal":true`} {
		if !strings.Contains(raw, key) {
			t.Fatalf("%s missing: %s", key, raw)
		}
	}
	// Signed in again, status still says why that sign-in was needed.
	r.login(0, "")
	items = r.sessionStatus("n")
	if len(items) != 1 || items[0].State != "ok" || items[0].Cause != nil || items[0].PreviousCause == nil || items[0].PreviousCause.Code != "refresh_expired_or_revoked" {
		t.Fatalf("%+v", items)
	}
	if raw := r.run("auth", "status", "n", "--json").stdout; !strings.Contains(raw, `"previousCause":{"code":"refresh_expired_or_revoked"`) {
		t.Fatal(raw)
	}
	if human := r.run("auth", "status", "n"); human.code != 0 || !strings.Contains(human.stdout, "local:n  ok\n  Last sign-in needed: The provider rejected the refresh token (invalid_grant)") {
		t.Fatalf("%q", human.stdout)
	}
}

func TestAuthStatusOneWordStatesBlackBox(t *testing.T) {
	r := newOAuthRig(t, testutil.AuthServerOptions{Registration: true, RefreshExpiresIn: 48 * time.Hour}, &config.OAuth{Type: "oauth"})
	other := testutil.NewAuthServer(t, testutil.AuthServerOptions{Registration: true, TokenPrefix: oauthCanary})
	server := testutil.NewFixtureServer()
	h := httptest.NewServer(other.Protect(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil), "/mcp"))
	r.closers = append(r.closers, h.Close)
	r.personal.Connections["o"] = config.Connection{Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal(h.URL + "/mcp"), AllowInsecureHTTP: "loopback"}}, Auth: &config.OAuth{Type: "oauth"}}
	r.save()

	if human := r.run("auth", "status"); human.code != 0 || human.stdout != "local:n  sign-in required  (never_signed_in)\nlocal:o  sign-in required  (never_signed_in)\n" {
		t.Fatalf("%q", human.stdout)
	}
	r.login(0, "")
	v := r.run("auth", "o", "--json")
	v.stderr = "" // the authorization URL
	r.check(v, 0, "")
	items := r.sessionStatus()
	if len(items) != 2 || items[0].State != "expiring" || items[0].RefreshTokenExpiresAt == "" || items[1].State != "ok" || items[1].Cause != nil {
		t.Fatalf("%+v", items)
	}
	if human := r.run("auth", "status"); human.code != 0 || human.stdout != "local:n  expiring\nlocal:o  ok\n" {
		t.Fatalf("%q", human.stdout)
	}
	if raw := r.run("auth", "status", "--json").stdout; !strings.Contains(raw, `"refreshTokenExpiresAt":`) || !strings.Contains(raw, `"accessTokenExpiresAt":`) {
		t.Fatal(raw)
	}
}

func TestHealthLogNoTokenMaterialBlackBox(t *testing.T) {
	// Access tokens within the refresh lead refresh on every use.
	r := newOAuthRig(t, testutil.AuthServerOptions{Registration: true, RotateRefresh: true, AccessTTL: 30 * time.Second}, &config.OAuth{Type: "oauth"})
	r.login(0, "")
	r.call("n.echo", "text=hi")
	r.check(r.run("runtime", "stop", "--json"), 0, "")
	health := r.healthFile()
	var doc struct {
		V           int
		Connections map[string]struct {
			Client, Redirect string
			Events           []struct{ Kind string }
		}
	}
	if e := json.Unmarshal([]byte(health), &doc); e != nil || doc.V != 1 {
		t.Fatal(health, e)
	}
	c := doc.Connections["local:n"]
	if len(c.Events) < 2 || c.Events[0].Kind != "authorized" || c.Events[len(c.Events)-1].Kind != "refreshed" || c.Client == "" || c.Redirect == "" {
		t.Fatalf("%s", health)
	}
	// The documented keys, matched exactly (json.Unmarshal ignores case).
	for _, key := range []string{`{"v":1,"connections":{"local:n":{`, `"client":`, `"redirect":`, `"events":[{"at":`, `"kind":"authorized"`, `"trigger":"login"`, `"accessTtl":30`, `"refreshToken":true`, `"rotated":true`} {
		if !strings.Contains(health, key) {
			t.Fatalf("%s missing: %s", key, health)
		}
	}
	if st, e := os.Stat(filepath.Join(r.paths.StateDir, "oauth-health.json")); e != nil || st.Mode().Perm() != 0o600 {
		t.Fatal(st, e)
	}
	for _, args := range [][]string{{"auth", "status", "n", "--json"}, {"auth", "status", "n"}, {"auth", "status"}} {
		v := r.run(args...)
		if v.code != 0 || strings.Contains(v.stdout, c.Client) || strings.Contains(v.stdout, c.Redirect) || strings.Contains(v.stdout, "127.0.0.1") {
			t.Fatalf("%v: %q", args, v.stdout)
		}
	}
	// checkLeaks also scans the health file, daemon.log and every output for
	// the token canary.
}
