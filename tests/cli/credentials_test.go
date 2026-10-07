package cli_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

const rotatedCanary = "FIXTURE-ROTATED-CANARY"

// noCredentialLeaks fails when a fixture secret, the fixture bootstrap token,
// the account or a reference shows up in any output or the daemon log.
func noCredentialLeaks(t *testing.T, r *rig, outputs ...result) {
	t.Helper()
	texts := []string{}
	for _, v := range outputs {
		texts = append(texts, v.stdout, v.stderr)
	}
	if b, e := os.ReadFile(r.paths.LogFile); e == nil {
		texts = append(texts, string(b))
	}
	for _, text := range texts {
		for _, canary := range []string{"FIXTURE-API-KEY", "FIXTURE-OTHER-KEY", "FIXTURE-BOOTSTRAP-TOKEN", rotatedCanary, "Fixture account", "op://"} {
			if strings.Contains(text, canary) {
				t.Fatalf("%s leaked: %q", canary, text)
			}
		}
	}
}

// logged reports whether the daemon log holds event.
func logged(r *rig, event string) bool {
	b, _ := os.ReadFile(r.paths.LogFile)
	return strings.Contains(string(b), `{"event":"`+event+`"}`)
}

func counterOf(t *testing.T, v result) float64 {
	t.Helper()
	n, _ := structured(t, v)["count"].(float64)
	return n
}

func TestAuthLockBlackBox(t *testing.T) {
	r := newRig(t)
	r.stdio("a", "op://Fixture/api/key")
	out := []result{r.call("a.counter"), r.call("fixture.counter")}
	lock := r.check(r.run("auth", "lock", "--json"), 0, "")
	if string(lock.envelope.Data) != `{"locked":true}` {
		t.Fatal(lock.stdout)
	}
	out = append(out, lock, r.check(r.run("call", "a.counter", "--no-input", "--json"), 3, "auth_required"))
	if r.countEvents("bootstrap") != 1 {
		t.Fatal("--no-input bootstrapped after the lock")
	}
	// The secret-free connection kept its process.
	if v := r.call("fixture.counter"); counterOf(t, v) != 2 {
		t.Fatal(v.stdout)
	}
	if v := r.call("a.counter"); counterOf(t, v) != 1 || r.countEvents("bootstrap") != 2 {
		t.Fatal("no single new bootstrap", v.stdout, r.countEvents("bootstrap"))
	}
	human := r.run("auth", "lock")
	if human.code != 0 || human.stdout != "Locked. 1Password sessions ended; signed-in connections need mcparcel auth login.\n" {
		t.Fatalf("%q", human.stdout)
	}
	if !logged(r, "auth_locked") {
		t.Fatal("auth_locked not logged")
	}
	noCredentialLeaks(t, r, append(out, human)...)
}

func TestAuthRefreshBlackBox(t *testing.T) {
	r := newRig(t)
	r.stdio("a", "op://Fixture/api/key")
	// Not running: nothing cached, and no daemon is started.
	v := r.check(r.run("auth", "refresh", "a", "--json"), 0, "")
	if string(v.envelope.Data) != `{"connection":"local:a","invalidated":false}` || len(r.daemonPIDs()) != 0 {
		t.Fatal(v.stdout, r.daemonPIDs())
	}
	// Checked without a daemon too: a connection without op:// references,
	// or one that does not exist.
	free := r.check(r.run("auth", "refresh", "fixture", "--json"), 2, "invalid_arguments")
	if !strings.Contains(free.stdout, "local:fixture uses no 1Password secrets.") {
		t.Fatal(free.stdout)
	}
	r.check(r.run("auth", "refresh", "local:missing", "--json"), 4, "connection_unavailable")
	if len(r.daemonPIDs()) != 0 {
		t.Fatal("started a daemon", r.daemonPIDs())
	}
	out := []result{v, free, r.call("a.counter")}
	v = r.check(r.run("auth", "refresh", "a", "--json"), 0, "")
	if string(v.envelope.Data) != `{"connection":"local:a","invalidated":true}` {
		t.Fatal(v.stdout)
	}
	if v := r.check(r.run("call", "a.counter", "--no-input", "--json"), 0, ""); counterOf(t, v) != 2 || r.countEvents("resolve-api") != 2 || r.countEvents("bootstrap") != 1 {
		t.Fatal("refresh did not re-read through the session", v.stdout, r.countEvents("resolve-api"))
	}
	r.write(r.paths.StateDir+"/fixture-api-value", rotatedCanary, 0o600)
	human := r.run("auth", "refresh", "a")
	if human.code != 0 || human.stdout != "a: the next call reads its 1Password secrets again.\n" {
		t.Fatalf("%q", human.stdout)
	}
	if v := r.check(r.run("call", "a.counter", "--no-input", "--json"), 0, ""); counterOf(t, v) != 1 || r.countEvents("bootstrap") != 1 {
		t.Fatal("changed value did not replace the process", v.stdout)
	}
	if !logged(r, "credential_invalidated") || !logged(r, "credential_rotated") {
		t.Fatal("refresh or rotation not logged")
	}
	missing := r.check(r.run("auth", "refresh", "fixture", "--json"), 2, "invalid_arguments")
	if !strings.Contains(missing.stdout, "local:fixture uses no 1Password secrets.") {
		t.Fatal(missing.stdout)
	}
	noCredentialLeaks(t, r, append(out, human, missing)...)
}

type profileRow struct {
	Profile, Mode, Session, SessionExpiresAt string
	Connections                              []string
}

func (r *rig) profileRows(args ...string) ([]profileRow, result) {
	r.t.Helper()
	v := r.check(r.run(append([]string{"auth", "status", "--json"}, args...)...), 0, "")
	var d struct{ Profiles []profileRow }
	if e := json.Unmarshal(v.envelope.Data, &d); e != nil {
		r.t.Fatal(e)
	}
	return d.Profiles, v
}

func TestAuthStatusProfilesBlackBox(t *testing.T) {
	r := newRig(t)
	r.stdio("a", "op://Fixture/api/key")
	r.stdio("b", "op://Fixture/other/key")
	rows, offline := r.profileRows()
	if len(rows) != 1 || rows[0].Profile != "shared" || rows[0].Mode != "desktop-service-account" || rows[0].Session != "none" || rows[0].SessionExpiresAt != "" || strings.Join(rows[0].Connections, ",") != "local:a,local:b" {
		t.Fatalf("%+v", rows)
	}
	if len(r.daemonPIDs()) != 0 {
		t.Fatal("auth status started the runtime")
	}
	out := []result{offline, r.call("a.counter")}
	rows, v := r.profileRows("a")
	if len(rows) != 1 || rows[0].Session != "active" {
		t.Fatalf("%+v", rows)
	}
	if at, e := time.Parse(time.RFC3339, rows[0].SessionExpiresAt); e != nil || time.Until(at) < 23*time.Hour {
		t.Fatal(rows[0].SessionExpiresAt, e)
	}
	human := r.run("auth", "status")
	if human.code != 0 || !strings.HasPrefix(human.stdout, "profile shared  desktop-service-account  active  until ") {
		t.Fatalf("%q", human.stdout)
	}
	if s := r.status(); len(s.CredentialSessions) != 1 || s.CredentialSessions[0].Profile != "shared" || s.CredentialSessions[0].State != "active" {
		t.Fatalf("%+v", s.CredentialSessions)
	}
	neither := r.check(r.run("auth", "status", "fixture", "--json"), 2, "invalid_arguments")
	if !strings.Contains(neither.stdout, "fixture uses neither sign-in nor 1Password.") {
		t.Fatal(neither.stdout)
	}
	noCredentialLeaks(t, r, append(out, v, human, neither)...)
}

func TestDesktopProfileBlackBox(t *testing.T) {
	r := newRig(t)
	r.stdio("a", "op://Fixture/api/key")
	b, e := json.Marshal(config.Local{SchemaVersion: 1, CredentialProfiles: map[string]config.Profile{"shared": {Mode: "desktop", Account: "Fixture account"}}})
	if e != nil {
		t.Fatal(e)
	}
	r.write(r.paths.ConfigFile, string(b), 0o600)
	out := []result{r.call("a.counter"), r.check(r.run("call", "a.counter", "--no-input", "--json"), 0, "")}
	if r.countEvents("bootstrap-desktop") != 1 || r.countEvents("bootstrap") != 0 || r.countEvents("resolve-api") != 1 {
		t.Fatal("desktop profile did not use the desktop client once")
	}
	// Past the 5-minute cache (dropped here by refresh), --no-input never
	// reaches the desktop app, which could prompt.
	r.check(r.run("auth", "refresh", "a", "--json"), 0, "")
	out = append(out, r.check(r.run("call", "a.counter", "--no-input", "--json"), 3, "auth_required"))
	if r.countEvents("resolve-api") != 1 || r.countEvents("bootstrap-desktop") != 1 {
		t.Fatal("--no-input reached the provider")
	}
	if v := r.call("a.counter"); counterOf(t, v) != 3 || r.countEvents("resolve-api") != 2 {
		t.Fatal("interactive call did not re-read", v.stdout)
	}
	noCredentialLeaks(t, r, out...)
}

func TestRateLimitBlackBox(t *testing.T) {
	r := newRig(t)
	r.stdio("a", "op://Fixture/api/key")
	r.stdio("b", "op://Fixture/other/key")
	out := []result{r.call("a.counter")}
	r.write(r.paths.StateDir+"/fixture-rate-limit", "on", 0o600)
	limited := r.check(r.run("call", "b.counter", "--json"), 6, "auth_rate_limited")
	if !strings.Contains(limited.stdout, "Service-account limits are hourly and daily.") || !logged(r, "auth_rate_limited") {
		t.Fatal(limited.stdout)
	}
	if err := os.Remove(r.paths.StateDir + "/fixture-rate-limit"); err != nil {
		t.Fatal(err)
	}
	if v := r.call("b.counter"); counterOf(t, v) != 1 || r.countEvents("bootstrap") != 1 {
		t.Fatal("rate limit ended the session", v.stdout)
	}
	// A revoked service account ends the session: one bootstrap per
	// interactive call, none with --no-input.
	r.write(r.paths.StateDir+"/fixture-revoked", "on", 0o600)
	r.check(r.run("auth", "refresh", "a", "--json"), 0, "")
	revoked := r.check(r.run("call", "a.counter", "--json"), 3, "auth_failed")
	out = append(out, limited, revoked, r.check(r.run("call", "a.counter", "--no-input", "--json"), 3, "auth_required"))
	if r.countEvents("bootstrap") != 1 {
		t.Fatal("bootstrapped again", r.countEvents("bootstrap"))
	}
	if err := os.Remove(r.paths.StateDir + "/fixture-revoked"); err != nil {
		t.Fatal(err)
	}
	if v := r.call("a.counter"); counterOf(t, v) != 1 || r.countEvents("bootstrap") != 2 {
		t.Fatal("old process kept or no new bootstrap", v.stdout)
	}
	noCredentialLeaks(t, r, out...)
}

func TestAuthLockOAuthBlackBox(t *testing.T) {
	r := newOAuthRig(t, testutil.AuthServerOptions{Registration: true}, &config.OAuth{Type: "oauth"})
	r.login(0, "")
	r.call("n.echo", "text=hi")
	r.check(r.run("auth", "lock", "--json"), 0, "")
	human := r.run("auth", "status", "n")
	if human.code != 0 || !strings.HasPrefix(human.stdout, "local:n  sign-in required  (locked)\n  Locked by mcparcel auth lock.\n") || !strings.Contains(human.stdout, "  Next: mcparcel auth login n\n") {
		t.Fatalf("%q", human.stdout)
	}
	_, _, before := r.as.Counts()
	denied := r.check(r.run("call", "n.echo", "text=hi", "--json"), 3, "auth_required")
	if !strings.Contains(denied.stdout, "mcparcel auth login n") {
		t.Fatal(denied.stdout)
	}
	if _, _, after := r.as.Counts(); after != before {
		t.Fatal("refreshed a locked session")
	}
	r.login(0, "")
	r.call("n.echo", "text=again")
	if items := r.authStatus("n"); len(items) != 1 || !items[0].SignedIn {
		t.Fatalf("%+v", items)
	}
}

// An unreadable auth-lock.json fails closed in auth status, as in the daemon.
func TestAuthStatusUnreadableLockBlackBox(t *testing.T) {
	r := newOAuthRig(t, testutil.AuthServerOptions{Registration: true}, &config.OAuth{Type: "oauth"})
	r.login(0, "")
	r.write(r.paths.StateDir+"/auth-lock.json", "{", 0o600)
	human := r.run("auth", "status", "n")
	if human.code != 0 || !strings.HasPrefix(human.stdout, "local:n  sign-in required  (locked)\n  Locked by mcparcel auth lock.\n") {
		t.Fatalf("%q", human.stdout)
	}
}
