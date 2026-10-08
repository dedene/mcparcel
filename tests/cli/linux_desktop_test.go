package cli_test

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

// The fixture markers below stand in for Linux desktop mode, so these run on
// macOS too: fixture-no-session-bus (no usable D-Bus session bus),
// fixture-no-desktop-app (no 1Password desktop app) and
// fixture-no-desktop-session (no display and no bus).

const keyringUnreachableText = "No Secret Service keyring is reachable from this session (no usable D-Bus session bus), so MCParcel cannot store a sign-in."

// noRuntime fails when a daemon runs or its socket exists.
func (r *rig) noRuntime() {
	r.t.Helper()
	if pids := r.daemonPIDs(); len(pids) != 0 {
		r.t.Fatal("a runtime started:", pids)
	}
	if _, err := os.Lstat(r.paths.SocketFile); !os.IsNotExist(err) {
		r.t.Fatal("runtime socket exists:", err)
	}
}

func TestKeyringUnreachableLoginBlackBox(t *testing.T) {
	r := newOAuthRig(t, testutil.AuthServerOptions{Registration: true}, &config.OAuth{Type: "oauth"})
	r.write(r.paths.StateDir+"/fixture-no-session-bus", "", 0o600)
	before := configListing(t, r.root)
	v := r.login(3, "keychain_unavailable")
	if !strings.Contains(v.stdout, keyringUnreachableText) || !strings.Contains(v.stdout, "from your desktop session") {
		t.Fatal(v.stdout)
	}
	if after := configListing(t, r.root); !reflect.DeepEqual(before, after) {
		t.Fatalf("files changed: %v -> %v", before, after)
	}
	r.noRuntime()
	if r.page() != "" || r.as.Requests("") != 0 {
		t.Fatal("sign-in started without a keyring", r.page(), r.as.Requests(""))
	}
}

// With a runtime already running, auth login skips the offline keyring
// pre-check (D14a): the runtime has its own bus, and the login reaches it.
func TestKeyringUnreachableLoginRunningRuntimeBlackBox(t *testing.T) {
	r := newOAuthRig(t, testutil.AuthServerOptions{Registration: true}, &config.OAuth{Type: "oauth"})
	r.call("fixture.counter")
	pid := r.status().PID
	r.write(r.paths.StateDir+"/fixture-no-session-bus", "", 0o600)
	v := r.login(0, "")
	if strings.Contains(v.stdout, keyringUnreachableText) || !strings.Contains(r.page(), "n is connected") {
		t.Fatal(v.stdout, r.page())
	}
	if r.status().PID != pid {
		t.Fatal("the login did not use the running runtime")
	}
}

func TestNoDesktopAppBlackBox(t *testing.T) {
	r := newRig(t)
	token := r.root + "/secrets/op-token"
	r.write(token, saToken+"\n", 0o600)
	r.stdio("a", "op://Fixture/api/key")
	r.personal.CredentialProfiles["sa"] = config.ProfileRequirement{}
	b := r.personal.Connections["a"]
	b.CredentialProfile = "sa"
	r.personal.Connections["b"] = b
	r.save()
	// The rig writes only profile shared; add sa as a service-account profile.
	local := config.Local{SchemaVersion: 1, CredentialProfiles: map[string]config.Profile{"shared": r.profile, "sa": {Mode: config.ProfileModeServiceAccount, TokenFile: token}}}
	raw, err := json.Marshal(local)
	if err != nil {
		t.Fatal(err)
	}
	r.write(r.paths.ConfigFile, string(raw), 0o600)
	r.write(r.paths.StateDir+"/fixture-no-desktop-app", "", 0o600)
	v := r.check(r.run("call", "a.counter", "--json"), 2, "config_required")
	if !strings.Contains(v.stdout, "Profile shared uses the 1Password desktop app, which MCParcel supports on macOS only.") || !strings.Contains(v.stdout, "config profile bind") {
		t.Fatal(v.stdout)
	}
	if r.countEvents("bootstrap") != 0 || r.countEvents("bootstrap-desktop") != 0 {
		t.Fatal("a desktop profile bootstrapped without the desktop app")
	}
	// The same runtime still serves a service-account profile.
	pids := r.daemonPIDs()
	if got := r.call("b.counter"); counterOf(t, got) != 1 || r.countEvents("bootstrap-token") != 1 || r.countEvents("resolve-api") != 1 {
		t.Fatal("service-account profile did not resolve", got.stdout)
	}
	if after := r.daemonPIDs(); len(pids) != 1 || !reflect.DeepEqual(pids, after) {
		t.Fatal("not the same runtime:", pids, after)
	}
	noTokenLeaks(t, r, v)
}

// Without a desktop session and without config.json, desktop mode is most
// likely a container or service that misses its headless configuration.
func TestNoDesktopSessionNoConfigBlackBox(t *testing.T) {
	r := newRig(t)
	r.write(r.paths.StateDir+"/fixture-no-desktop-session", "", 0o600)
	if err := os.Remove(r.paths.ConfigFile); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"call", "fixture.counter", "--json"}, {"runtime", "status", "--json"}, {"auth", "status", "--json"}} {
		v := r.check(r.run(args...), 2, "runtime_unsupported")
		if !strings.Contains(v.stdout, "No MCParcel configuration was found and there is no desktop session.") || !strings.Contains(v.stdout, "XDG_CONFIG_HOME") {
			t.Fatal(args, v.stdout)
		}
	}
	r.noRuntime()
}

// With config.json, desktop mode runs without a desktop session (a
// devcontainer, SSH without a user bus): a service-account profile works,
// and a sign-in is refused before a runtime starts or a browser opens.
func TestNoDesktopSessionWithConfigBlackBox(t *testing.T) {
	r := newOAuthRig(t, testutil.AuthServerOptions{Registration: true}, &config.OAuth{Type: "oauth"})
	token := r.root + "/secrets/op-token"
	r.write(token, saToken+"\n", 0o600)
	r.stdio("a", "op://Fixture/api/key")
	r.saProfile(token)
	r.write(r.paths.StateDir+"/fixture-no-desktop-session", "", 0o600)
	login := r.login(3, "keychain_unavailable")
	if !strings.Contains(login.stdout, keyringUnreachableText) {
		t.Fatal(login.stdout)
	}
	r.noRuntime()
	if r.page() != "" || r.as.Requests("") != 0 {
		t.Fatal("sign-in started without a keyring", r.page(), r.as.Requests(""))
	}
	call := r.call("a.counter")
	if counterOf(t, call) != 1 || r.countEvents("bootstrap-token") != 1 || r.countEvents("resolve-api") != 1 {
		t.Fatal("service-account call failed without a desktop session", call.stdout)
	}
	noTokenLeaks(t, r.rig, login, call)
}
