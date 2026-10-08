package cli_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

const oauthCanary = "OAUTH-CANARY-"

// oauthRig serves the fixture MCP server behind the fake authorization server
// as personal HTTP connection n, and checks every output for token leaks.
type oauthRig struct {
	*rig
	as      *testutil.AuthServer
	outputs []string
}

func newOAuthRig(t *testing.T, o testutil.AuthServerOptions, auth *config.OAuth) *oauthRig {
	t.Helper()
	o.TokenPrefix = oauthCanary
	r := &oauthRig{rig: newRig(t), as: testutil.NewAuthServer(t, o)}
	server := testutil.NewFixtureServer()
	h := httptest.NewServer(r.as.Protect(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil), "/mcp"))
	r.closers = append(r.closers, h.Close)
	r.personal.Connections["n"] = config.Connection{Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal(h.URL + "/mcp"), AllowInsecureHTTP: "loopback"}}, Auth: auth}
	r.save()
	// Registered after newRig, so it runs before the rig removes its root.
	t.Cleanup(r.checkLeaks)
	return r
}

func (r *oauthRig) run(args ...string) result {
	v := r.rig.run(args...)
	r.outputs = append(r.outputs, v.stdout, v.stderr)
	return v
}

func (r *oauthRig) call(target string, args ...string) result {
	return r.check(r.run(append([]string{"call", target, "--json"}, args...)...), 0, "")
}

func (r *oauthRig) checkLeaks() {
	texts := slices.Clone(r.outputs)
	for _, file := range []string{r.paths.LogFile, filepath.Join(r.paths.StateDir, "oauth-health.json")} {
		if b, e := os.ReadFile(file); e == nil {
			texts = append(texts, string(b))
		}
	}
	for _, dir := range []string{r.paths.ConfigDir, r.paths.DataDir, r.paths.CacheDir} {
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				if b, e := os.ReadFile(path); e == nil {
					texts = append(texts, string(b))
				}
			}
			return nil
		})
	}
	for _, text := range texts {
		for _, canary := range []string{oauthCanary, "code_verifier"} {
			if strings.Contains(text, canary) {
				r.t.Errorf("%s leaked: %q", canary, text)
			}
		}
	}
}

// login runs auth n --json; the URL goes to stderr.
func (r *oauthRig) login(exit int, code string, extra ...string) result {
	r.t.Helper()
	v := r.run(append([]string{"auth", "n", "--json"}, extra...)...)
	stderr := v.stderr
	v.stderr = ""
	v = r.check(v, exit, code)
	v.stderr = stderr
	return v
}

func (r *oauthRig) item() map[string]any {
	r.t.Helper()
	sum := sha256.Sum256([]byte("mcparcel-oauth\x00local:n"))
	b, e := os.ReadFile(filepath.Join(r.paths.StateDir, "fixture-keyring-"+hex.EncodeToString(sum[:])))
	if e != nil {
		r.t.Fatal(e)
	}
	var m map[string]any
	if e = json.Unmarshal(b, &m); e != nil {
		r.t.Fatal(e)
	}
	return m
}

func (r *oauthRig) page() string {
	b, _ := os.ReadFile(filepath.Join(r.paths.StateDir, "fixture-browser-page"))
	return string(b)
}

type authStatusItem struct {
	Connection           string
	SignedIn             bool
	RefreshToken         bool
	AccessTokenExpiresAt string
	LastRefreshFailure   *struct{ At, Code string }
}

func (r *oauthRig) authStatus(args ...string) []authStatusItem {
	r.t.Helper()
	v := r.check(r.run(append([]string{"auth", "status", "--json"}, args...)...), 0, "")
	var d struct{ Items []authStatusItem }
	if e := json.Unmarshal(v.envelope.Data, &d); e != nil {
		r.t.Fatal(e)
	}
	return d.Items
}

func (r *oauthRig) shell(exports string) {
	r.write(r.root+"/shell", "#!/bin/sh\n"+exports+"exec /bin/sh -c \"$3\"\n", 0o700)
}

func TestOAuthLoginCallRefreshBlackBox(t *testing.T) {
	r := newOAuthRig(t, testutil.AuthServerOptions{Registration: true, AccessTTL: 35 * time.Second, RotateRefresh: true}, &config.OAuth{Type: "oauth"})
	v := r.login(0, "")
	if string(v.envelope.Data) != `{"connection":"local:n","signedIn":true}` {
		t.Fatal(v.stdout)
	}
	if !strings.Contains(v.stderr, r.as.URL+"/authorize?") || !strings.Contains(v.stderr, "Opening your browser to sign in to n.") {
		t.Fatalf("stderr: %q", v.stderr)
	}
	if !strings.Contains(r.page(), "n is connected") {
		t.Fatal(r.page())
	}
	if got := structured(t, r.call("n.echo", "text=hi")); got["text"] != "hi" {
		t.Fatal(got)
	}
	items := r.authStatus("n")
	if len(items) != 1 || items[0].Connection != "local:n" || !items[0].SignedIn || !items[0].RefreshToken || items[0].AccessTokenExpiresAt == "" || items[0].LastRefreshFailure != nil {
		t.Fatalf("%+v", items)
	}
	if all := r.authStatus(); len(all) != 1 || all[0].Connection != "local:n" {
		t.Fatalf("%+v", all)
	}
	human := r.run("auth", "status", "n")
	if human.code != 0 || !strings.HasPrefix(human.stdout, "local:n  ok\n") || !strings.Contains(human.stdout, "  Last refresh: ") {
		t.Fatalf("%q %q", human.stdout, human.stderr)
	}
	time.Sleep(6 * time.Second)
	r.call("n.echo", "text=again")
	if _, _, refreshes := r.as.Counts(); refreshes != 1 {
		t.Fatalf("refreshes=%d", refreshes)
	}
	if rt := r.item()["rt"]; rt != r.as.RefreshToken() {
		t.Fatal("rotated refresh token was not stored")
	}
}

func TestOAuthNoInputBlackBox(t *testing.T) {
	r := newOAuthRig(t, testutil.AuthServerOptions{Registration: true}, &config.OAuth{Type: "oauth"})
	v := r.check(r.run("call", "n.echo", "text=hi", "--no-input", "--json"), 3, "auth_required")
	if !strings.Contains(v.stdout, `"nextAction":"mcparcel auth n"`) {
		t.Fatal(v.stdout)
	}
	// Refused offline: no runtime is needed to know a browser cannot open.
	r.check(r.run("runtime", "stop", "--json"), 0, "")
	refused := r.login(3, "auth_required", "--no-input")
	if _, err := os.Lstat(r.paths.SocketFile); !strings.Contains(refused.stdout, "Signing in to n opens a browser, which --no-input does not allow.") || !os.IsNotExist(err) {
		t.Fatal(refused.stdout, err)
	}
	if n := r.as.Requests(""); n != 0 {
		t.Fatalf("authorization server requests: %d", n)
	}
	if r.page() != "" {
		t.Fatal("browser opened")
	}
	// Under --no-input an unusable connection still gets its own error, not
	// a next action that can never succeed.
	r.check(r.run("auth", "typo", "--no-input", "--json"), 4, "connection_unavailable")
	r.stdio("s", "")
	s := r.check(r.run("auth", "s", "--no-input", "--json"), 2, "invalid_arguments")
	if !strings.Contains(s.stdout, "s has no sign-in, client credentials or 1Password secrets to refresh.") {
		t.Fatal(s.stdout)
	}
}

func TestOAuthRefreshFailureBlackBox(t *testing.T) {
	r := newOAuthRig(t, testutil.AuthServerOptions{Registration: true}, &config.OAuth{Type: "oauth"})
	r.login(0, "")
	r.as.Revoke()
	v := r.check(r.run("call", "n.echo", "text=hi", "--json"), 3, "auth_required")
	if !strings.Contains(v.stdout, `"nextAction":"mcparcel auth n"`) {
		t.Fatal(v.stdout)
	}
	items := r.authStatus("n")
	if len(items) != 1 || items[0].SignedIn || items[0].RefreshToken || items[0].LastRefreshFailure == nil || items[0].LastRefreshFailure.Code != "invalid_grant" {
		t.Fatalf("%+v", items)
	}
	human := r.run("auth", "status", "n")
	if human.code != 0 || !strings.HasPrefix(human.stdout, "local:n  sign-in required  (refresh_expired_or_revoked)\n") || !strings.Contains(human.stdout, "  Next: mcparcel auth n\n") {
		t.Fatalf("%q", human.stdout)
	}
	_, _, before := r.as.Counts()
	r.check(r.run("call", "n.echo", "text=hi", "--json"), 3, "auth_required")
	if _, _, after := r.as.Counts(); after != before {
		t.Fatalf("refresh after a recorded failure: %d -> %d", before, after)
	}
}

func TestOAuthLogoutBlackBox(t *testing.T) {
	r := newOAuthRig(t, testutil.AuthServerOptions{Registration: true}, &config.OAuth{Type: "oauth"})
	r.login(0, "")
	logout := func(removed bool) {
		t.Helper()
		v := r.check(r.run("auth", "logout", "n", "--json"), 0, "")
		var d struct {
			Connection      string
			Removed         bool
			ProviderRevoked bool
		}
		if e := json.Unmarshal(v.envelope.Data, &d); e != nil || d.Connection != "local:n" || d.Removed != removed || d.ProviderRevoked {
			t.Fatal(v.stdout)
		}
	}
	logout(true)
	before := r.as.Requests("")
	r.check(r.run("call", "n.echo", "text=hi", "--json"), 3, "auth_required")
	if after := r.as.Requests(""); after != before {
		t.Fatalf("authorization server contacted after logout: %d -> %d", before, after)
	}
	logout(false)
	human := r.run("auth", "logout", "n")
	if human.code != 0 || human.stdout != "No stored sign-in for n.\n" {
		t.Fatalf("%q", human.stdout)
	}
	r.check(r.run("runtime", "stop", "--json"), 0, "")
	logout(false)
}

func TestOAuthDeniedBlackBox(t *testing.T) {
	r := newOAuthRig(t, testutil.AuthServerOptions{Registration: true, DenyWith: "access_denied"}, &config.OAuth{Type: "oauth"})
	v := r.login(3, "auth_failed")
	if !strings.Contains(v.stdout, "access_denied") {
		t.Fatal(v.stdout)
	}
	if page := r.page(); !strings.Contains(page, "did not sign you in") || !strings.Contains(page, "access_denied") {
		t.Fatal(page)
	}
	if n := r.as.Requests("/authorize"); n != 1 {
		t.Fatalf("authorize requests: %d", n)
	}
	r.check(r.run("runtime", "stop", "--json"), 0, "")
	if b, e := os.ReadFile(r.paths.LogFile); e != nil || !strings.Contains(string(b), `{"event":"oauth_sign_in_failed","stage":"callback","code":"access_denied"}`) {
		t.Fatalf("daemon log %q %v", b, e)
	}
}

func TestOAuthPreconfiguredClientBlackBox(t *testing.T) {
	secret := oauthCanary + "client-secret"
	r := newOAuthRig(t, testutil.AuthServerOptions{ClientID: "fixture-client", ClientSecret: secret}, &config.OAuth{
		Type:         "oauth",
		ClientID:     &config.Value{Secret: &config.SecretRef{Secret: "env:FIXTURE_CLIENT_ID"}},
		ClientSecret: &config.Value{Secret: &config.SecretRef{Secret: "env:FIXTURE_CLIENT_SECRET"}},
	})
	r.shell("export FIXTURE_CLIENT_ID=fixture-client FIXTURE_CLIENT_SECRET=" + secret + "\n")
	r.login(0, "")
	item := r.item()
	if _, ok := item["s"]; ok {
		t.Fatal("preconfigured client secret stored")
	}
	if _, ok := item["c"]; ok {
		t.Fatal("preconfigured client ID stored")
	}
	r.call("n.echo", "text=hi")
}

func TestUnmarkedHTTP401BlackBox(t *testing.T) {
	r := newOAuthRig(t, testutil.AuthServerOptions{Registration: true}, nil)
	if all := r.authStatus(); len(all) != 0 {
		t.Fatalf("listed before any call: %+v", all)
	}
	started := time.Now()
	v := r.check(r.run("call", "n.echo", "text=hi", "--json"), 3, "auth_required")
	if time.Since(started) > 5*time.Second || !strings.Contains(v.stdout, `"nextAction":"mcparcel auth n"`) {
		t.Fatal(time.Since(started), v.stdout)
	}
	// The server asked for sign-in, so the list now shows the connection.
	if all := r.authStatus(); len(all) != 1 || all[0].Connection != "local:n" || all[0].SignedIn || all[0].RefreshToken || all[0].LastRefreshFailure != nil {
		t.Fatalf("%+v", all)
	}
	if human := r.run("auth", "status"); human.code != 0 || human.stdout != "local:n  sign-in required  (server_requested)\n" {
		t.Fatalf("%q", human.stdout)
	}
	r.login(0, "")
	r.call("n.echo", "text=hi")
	if all := r.authStatus(); len(all) != 1 || !all[0].SignedIn {
		t.Fatalf("%+v", all)
	}
}

func TestHeaderKey401BlackBox(t *testing.T) {
	r := newOAuthRig(t, testutil.AuthServerOptions{Registration: true}, nil)
	r.personal.Connections["n"].Transport.HTTP.Headers = map[string]config.Value{"Authorization": {Secret: &config.SecretRef{Secret: "env:FIXTURE_KEY", Prefix: "Bearer "}}}
	r.save()
	r.shell("export FIXTURE_KEY=wrong-key\n")
	started := time.Now()
	v := r.check(r.run("call", "n.echo", "text=hi", "--json"), 3, "auth_required")
	if time.Since(started) > 5*time.Second || !strings.Contains(v.stdout, "FIXTURE_KEY") || strings.Contains(v.stdout, "mcparcel auth ") {
		t.Fatal(time.Since(started), v.stdout)
	}
	r.login(2, "invalid_arguments")
	if n := r.as.Requests(""); n != 0 {
		t.Fatalf("authorization server requests: %d", n)
	}
}

func TestKeychainUnavailableBlackBox(t *testing.T) {
	r := newOAuthRig(t, testutil.AuthServerOptions{Registration: true}, &config.OAuth{Type: "oauth"})
	files := func() []string {
		var out []string
		for _, dir := range []string{r.paths.ConfigDir, r.paths.DataDir, r.paths.CacheDir} {
			_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					out = append(out, path)
				}
				return nil
			})
		}
		return out
	}
	before := files()
	r.write(r.paths.StateDir+"/fixture-keyring-unavailable", "", 0o600)
	r.login(3, "keychain_unavailable")
	if after := files(); !slices.Equal(before, after) {
		t.Fatalf("files changed: %v -> %v", before, after)
	}
	// The daemon's login preflight refuses before a callback listener or a
	// browser: nothing reached the authorization server.
	if r.page() != "" || r.as.Requests("/authorize") != 0 || r.as.Requests("/token") != 0 {
		t.Fatal("sign-in started without a keyring", r.page(), r.as.Requests(""))
	}
}

// OAuth with a 1Password client secret: auth reads the secret again, then
// signs in, and says both.
func TestOAuthAuthBothBlackBox(t *testing.T) {
	id := literal("fixture-client")
	r := newOAuthRig(t, testutil.AuthServerOptions{ClientID: "fixture-client", ClientSecret: "FIXTURE-API-KEY"}, &config.OAuth{
		Type: "oauth", ClientID: &id, ClientSecret: &config.Value{Secret: &config.SecretRef{Secret: "op://Fixture/api/key"}},
	})
	c := r.personal.Connections["n"]
	c.CredentialProfile = "shared"
	r.personal.Connections["n"] = c
	r.personal.CredentialProfiles["shared"] = config.ProfileRequirement{}
	r.save()
	v := r.login(0, "")
	if string(v.envelope.Data) != `{"connection":"local:n","secretsRefreshed":true,"signedIn":true}` || r.countEvents("resolve-api") != 1 {
		t.Fatal(v.stdout, r.countEvents("resolve-api"))
	}
	r.call("n.echo", "text=hi")
	human := r.run("auth", "n")
	if human.code != 0 || human.stdout != "Read the 1Password secrets for n again.\nSigned in to n.\n" || r.countEvents("resolve-api") != 2 {
		t.Fatalf("%q %q %d", human.stdout, human.stderr, r.countEvents("resolve-api"))
	}
	noCredentialLeaks(t, r.rig, v, human)
}

// A connection named lock is shadowed by auth lock: next actions use its
// canonical ID, which reaches it, and doctor warns.
func TestAuthNameClashBlackBox(t *testing.T) {
	r := newOAuthRig(t, testutil.AuthServerOptions{Registration: true}, &config.OAuth{Type: "oauth"})
	r.personal.Connections["lock"] = r.personal.Connections["n"]
	delete(r.personal.Connections, "n")
	r.save()
	v := r.check(r.run("call", "lock.echo", "text=hi", "--no-input", "--json"), 3, "auth_required")
	if !strings.Contains(v.stdout, `"nextAction":"mcparcel auth local:lock"`) {
		t.Fatal(v.stdout)
	}
	if locked := r.check(r.run("auth", "lock", "--json"), 0, ""); string(locked.envelope.Data) != `{"locked":true}` {
		t.Fatal(locked.stdout)
	}
	signed := r.run("auth", "local:lock", "--json")
	signed.stderr = "" // the sign-in URL
	if signed = r.check(signed, 0, ""); string(signed.envelope.Data) != `{"connection":"local:lock","signedIn":true}` {
		t.Fatal(signed.stdout)
	}
	r.call("lock.echo", "text=hi")
	d := doctorData(t, r.check(r.run("doctor", "lock", "--json"), 0, ""))
	if row := doctorRow(t, d, "credentials.auth-name", "local:lock"); row.Status != "warn" || row.NextAction != "Use mcparcel auth local:lock to make its credentials fresh." {
		t.Fatalf("%+v", row)
	}
}
