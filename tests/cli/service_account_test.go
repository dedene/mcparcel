package cli_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

const (
	saToken    = "FIXTURE-SA-TOKEN" // the only token the fixture provider accepts
	saWrong    = "FIXTURE-SA-WRONG-TOKEN"
	saTokenEnv = "OP_SERVICE_ACCOUNT_TOKEN"
)

// saProfile makes profile shared a service-account profile. Source "env"
// reads OP_SERVICE_ACCOUNT_TOKEN from the daemon's login environment, which
// the fake login shell exports with the fixture token; any other source is
// the profile's tokenFile.
func (r *rig) saProfile(source string) {
	r.t.Helper()
	r.profile = config.Profile{Mode: config.ProfileModeServiceAccount, TokenFile: source}
	if source == "env" {
		r.profile = config.Profile{Mode: config.ProfileModeServiceAccount, TokenEnv: saTokenEnv}
		r.write(r.root+"/shell", "#!/bin/sh\nexport "+saTokenEnv+"="+saToken+"\nexec /bin/sh -c \"$3\"\n", 0o700)
	}
	r.save()
}

// saHeadless switches the rig to headless mode with a world-writable state
// root, as newFrontRig does, and puts the fixture token in the caller's
// environment: the daemon it auto-starts keeps the profile's tokenEnv.
func (r *rig) saHeadless() {
	r.t.Helper()
	root := filepath.Join(r.root, "sr")
	if err := os.Mkdir(root, 0o700); err != nil {
		r.t.Fatal(err)
	}
	if err := os.Chmod(root, 0o777); err != nil {
		r.t.Fatal(err)
	}
	r.runtime = &config.RuntimeDefaults{Mode: "headless", StateRoot: root}
	r.save()
	var err error
	if r.paths, err = config.ApplyStateRoot(r.paths, root); err != nil {
		r.t.Fatal(err)
	}
	r.env = append(r.env, saTokenEnv+"="+saToken)
}

// noTokenLeaks fails when a fixture token is in an output, the daemon log or
// any regular file below the rig root other than the fake login shell and
// the token files the test wrote (below r.root/secrets).
func noTokenLeaks(t *testing.T, r *rig, outputs ...result) {
	t.Helper()
	texts := []string{}
	for _, v := range outputs {
		texts = append(texts, v.stdout, v.stderr)
	}
	scanned := map[string]bool{}
	err := filepath.WalkDir(r.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == r.root+"/secrets" {
			return filepath.SkipDir
		}
		if !d.Type().IsRegular() || path == r.root+"/shell" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned[path] = true
		texts = append(texts, string(b))
		return nil
	})
	if err != nil || !scanned[r.paths.LogFile] {
		t.Fatalf("daemon log not scanned (%v): %v", err, scanned)
	}
	for _, text := range texts {
		for _, canary := range []string{saToken, saWrong} {
			if strings.Contains(text, canary) {
				t.Fatalf("%s leaked: %q", canary, text)
			}
		}
	}
}

// childHasToken asks connection id's child whether OP_SERVICE_ACCOUNT_TOKEN
// is in its environment.
func (r *rig) childHasToken(id string) bool {
	r.t.Helper()
	set, _ := structured(r.t, r.call(id+".env_set", "name="+saTokenEnv))["set"].(bool)
	return set
}

func (r *rig) statusOutput() result {
	r.t.Helper()
	return r.check(r.run("runtime", "status", "--json"), 0, "")
}

func TestServiceAccountEnvBlackBox(t *testing.T) {
	r := newRig(t)
	r.stdio("a", "op://Fixture/api/key")
	r.saProfile("env")
	out := []result{r.call("a.counter")}
	if r.countEvents("bootstrap-token") != 1 || r.countEvents("resolve-api") != 1 || r.countEvents("bootstrap-desktop") != 0 || r.countEvents("bootstrap") != 0 {
		t.Fatal("service-account profile did not bootstrap from its token once")
	}
	b, _ := os.ReadFile(r.paths.StateDir + "/fixture-auth-events")
	if string(b) != "bootstrap-token\nresolve-api\n" {
		t.Fatalf("events %q", b)
	}
	if r.childHasToken("a") || r.childHasToken("fixture") {
		t.Fatal("a child inherited the service-account token")
	}
	// A token bootstrap never prompts, so --no-input bootstraps a new
	// runtime's session.
	r.check(r.run("runtime", "restart", "--json"), 0, "")
	out = append(out, r.check(r.run("call", "a.counter", "--no-input", "--json"), 0, ""))
	if r.countEvents("bootstrap-token") != 2 || r.countEvents("resolve-api") != 2 {
		t.Fatal("--no-input did not bootstrap the service-account profile")
	}
	rows, v := r.profileRows()
	out = append(out, v, r.statusOutput())
	if len(rows) != 1 || rows[0].Mode != "service-account" || rows[0].Session != "active" || !strings.Contains(v.stdout, `"tokenEnv":"`+saTokenEnv+`"`) {
		t.Fatalf("%+v %s", rows, v.stdout)
	}
	noTokenLeaks(t, r, out...)
	noCredentialLeaks(t, r, out...)
}

func TestServiceAccountEnvUnsetBlackBox(t *testing.T) {
	r := newRig(t)
	r.stdio("a", "op://Fixture/api/key")
	r.saProfile("env")
	r.write(r.root+"/shell", "#!/bin/sh\nexec /bin/sh -c \"$3\"\n", 0o700)
	v := r.check(r.run("call", "a.counter", "--json"), 2, "config_required")
	vars, _ := json.Marshal(v.envelope.Error.Details["variables"])
	if string(vars) != `["`+saTokenEnv+`"]` || !strings.Contains(v.stdout, "is not set in the runtime's environment.") || !strings.Contains(v.stdout, "runtime restart") {
		t.Fatal(v.stdout)
	}
	if r.countEvents("bootstrap-token") != 0 || r.countEvents("resolve-api") != 0 {
		t.Fatal("bootstrapped without a token")
	}
	noTokenLeaks(t, r, v)
}

func TestServiceAccountTokenFileBlackBox(t *testing.T) {
	r := newRig(t)
	token := r.root + "/secrets/op-token"
	r.write(token, saToken+"\n", 0o600)
	r.stdio("a", "op://Fixture/api/key")
	r.saProfile(token)
	out := []result{r.call("a.counter")}
	if r.countEvents("bootstrap-token") != 1 || r.countEvents("resolve-api") != 1 {
		t.Fatal("token file profile did not bootstrap once")
	}
	if r.childHasToken("a") {
		t.Fatal("a child inherited the service-account token")
	}
	// A new runtime reads the file again: readable by others is unsafe.
	r.check(r.run("runtime", "restart", "--json"), 0, "")
	if err := os.Chmod(token, 0o644); err != nil {
		t.Fatal(err)
	}
	unsafe := r.check(r.run("call", "a.counter", "--json"), 2, "unsafe_local_path")
	if !strings.Contains(unsafe.stdout, "is unsafe") || !strings.Contains(unsafe.stdout, "chmod 600") {
		t.Fatal(unsafe.stdout)
	}
	// A wrong token is rejected, and an immediate retry is answered from the
	// rejected-token backoff without asking 1Password again (D22).
	if err := os.Chmod(token, 0o600); err != nil {
		t.Fatal(err)
	}
	r.write(token, saWrong+"\n", 0o600)
	rejected := r.check(r.run("call", "a.counter", "--json"), 3, "auth_failed")
	if !strings.Contains(rejected.stdout, "did not accept the service-account token") || !strings.Contains(rejected.stdout, "token from file") {
		t.Fatal(rejected.stdout)
	}
	if r.countEvents("bootstrap-token-rejected") != 1 {
		t.Fatal("the wrong token was not sent once", r.countEvents("bootstrap-token-rejected"))
	}
	again := r.check(r.run("call", "a.counter", "--json"), 3, "auth_failed")
	if r.countEvents("bootstrap-token") != 1 || r.countEvents("bootstrap-token-rejected") != 1 {
		t.Fatal("a rejected token was sent again", r.countEvents("bootstrap-token"), r.countEvents("bootstrap-token-rejected"))
	}
	// The file is read again on the next bootstrap: no restart needed.
	r.write(token, saToken+"\n", 0o600)
	if v := r.call("a.counter"); counterOf(t, v) != 1 || r.countEvents("bootstrap-token") != 2 {
		t.Fatal("replaced token file not read again", v.stdout)
	}
	noTokenLeaks(t, r, append(out, unsafe, rejected, again, r.statusOutput())...)
}

// auth lock ends a service-account session but does not bar the profile:
// the next --no-input call bootstraps again from the token.
func TestServiceAccountAuthLockBlackBox(t *testing.T) {
	r := newRig(t)
	token := r.root + "/secrets/op-token"
	r.write(token, saToken+"\n", 0o600)
	r.stdio("a", "op://Fixture/api/key")
	r.saProfile(token)
	out := []result{r.call("a.counter"), r.check(r.run("auth", "lock", "--json"), 0, "")}
	r.write(token, saToken+"\n", 0o600)
	out = append(out, r.check(r.run("call", "a.counter", "--no-input", "--json"), 0, ""))
	if r.countEvents("bootstrap-token") != 2 {
		t.Fatal("no new bootstrap after auth lock", r.countEvents("bootstrap-token"))
	}
	rows, v := r.profileRows()
	if len(rows) != 1 || rows[0].Session != "active" || !strings.Contains(v.stdout, `"tokenFile":`) {
		t.Fatalf("%+v %s", rows, v.stdout)
	}
	noTokenLeaks(t, r, append(out, v)...)
}

func TestServiceAccountHeadlessBlackBox(t *testing.T) {
	r := newRig(t)
	r.stdio("a", "op://Fixture/api/key")
	r.profile = config.Profile{Mode: config.ProfileModeServiceAccount, TokenEnv: saTokenEnv}
	r.saHeadless()
	// Not running: the row has no session and no runtime starts.
	rows, offline := r.profileRows()
	if len(rows) != 1 || rows[0].Session != "none" || len(r.daemonPIDs()) != 0 {
		t.Fatalf("%+v %v", rows, r.daemonPIDs())
	}
	out := []result{offline, r.call("a.counter")}
	if r.countEvents("bootstrap-token") != 1 || r.countEvents("resolve-api") != 1 {
		t.Fatal("headless service-account profile did not bootstrap once")
	}
	if r.childHasToken("a") || r.childHasToken("fixture") {
		t.Fatal("a child of the headless runtime inherited the service-account token")
	}
	rows, v := r.profileRows()
	if len(rows) != 1 || rows[0].Profile != "shared" || rows[0].Mode != "service-account" || rows[0].Session != "active" || strings.Join(rows[0].Connections, ",") != "local:a" || !strings.Contains(v.stdout, `"items":[]`) || !strings.Contains(v.stdout, `"tokenEnv":"`+saTokenEnv+`"`) {
		t.Fatalf("%+v %s", rows, v.stdout)
	}
	human := r.run("auth", "status")
	if human.code != 0 || !strings.HasPrefix(human.stdout, "No sign-ins in headless mode.\nprofile shared  service-account  active  until ") || !strings.Contains(human.stdout, "  token from "+saTokenEnv+"\n") {
		t.Fatalf("%q", human.stdout)
	}
	// auth reads the secrets again now, without a prompt or a new bootstrap.
	renewed := r.check(r.run("auth", "a", "--json"), 0, "")
	if string(renewed.envelope.Data) != `{"connection":"local:a","secretsRefreshed":true}` || r.countEvents("bootstrap-token") != 1 || r.countEvents("resolve-api") != 2 {
		t.Fatal(renewed.stdout, r.countEvents("bootstrap-token"), r.countEvents("resolve-api"))
	}
	out = append(out, renewed)
	// A desktop-app profile is refused in headless mode.
	r.profile = config.Profile{Mode: "desktop-service-account", Account: "Fixture account", BootstrapRef: "op://Private/fixture/token"}
	r.save()
	desktop := r.check(r.run("call", "a.counter", "--json"), 2, "config_required")
	if !strings.Contains(desktop.stdout, "This connection's 1Password profile uses the desktop app, which headless mode does not use.") {
		t.Fatal(desktop.stdout)
	}
	if r.countEvents("bootstrap") != 0 {
		t.Fatal("headless mode bootstrapped a desktop profile")
	}
	out = append(out, v, human, desktop, r.statusOutput())
	noTokenLeaks(t, r, out...)
	noCredentialLeaks(t, r, out...)
}
