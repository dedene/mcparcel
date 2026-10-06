package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

// setStayAlive writes config.json with runtime.keepAlive.
func (r *oauthRig) setStayAlive(on bool) {
	r.t.Helper()
	b, e := json.Marshal(config.Local{SchemaVersion: 1, Runtime: &config.RuntimeDefaults{KeepAlive: on}})
	if e != nil {
		r.t.Fatal(e)
	}
	r.write(r.paths.ConfigFile, string(b), 0o600)
}

func TestRuntimeStatusStayAliveBlackBox(t *testing.T) {
	r := newOAuthRig(t, testutil.AuthServerOptions{Registration: true}, &config.OAuth{Type: "oauth"})
	r.login(0, "")
	if r.status().StayAlive {
		t.Fatal("stay-alive without runtime.keepAlive")
	}
	r.setStayAlive(true)
	if !r.status().StayAlive {
		t.Fatal("no stay-alive with a stored session")
	}
	if v := r.run("runtime", "status"); v.code != 0 || !strings.Contains(v.stdout, "\nStay-alive: on\n") {
		t.Fatalf("%q", v.stdout)
	}
	if v := r.run("auth", "status", "n"); v.code != 0 || !strings.Contains(v.stdout, "\n  Keep-alive: 24h\n") {
		t.Fatalf("%q", v.stdout)
	}
	r.check(r.run("auth", "logout", "n", "--json"), 0, "")
	if r.status().StayAlive {
		t.Fatal("stay-alive after logout")
	}
	if v := r.run("runtime", "status"); v.code != 0 || !strings.Contains(v.stdout, "\nStay-alive: off\n") {
		t.Fatalf("%q", v.stdout)
	}
}
