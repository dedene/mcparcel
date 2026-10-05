package cli_test

import (
	"os"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

func TestNoTokenInChildEnvironmentBlackBox(t *testing.T) {
	r := newRig(t)
	r.stdio("a", "op://Fixture/api/key")
	r.stdio("b", "")
	r.env = append(r.env, "OP_SERVICE_ACCOUNT_TOKEN=BOOTSTRAP-CANARY", "GITHUB_TOKEN=GITHUB-CANARY")
	for _, id := range []string{"a", "b"} {
		for _, key := range []string{"OP_SERVICE_ACCOUNT_TOKEN", "GITHUB_TOKEN"} {
			v := r.call(id+".env", "name="+key)
			if structured(t, v)["value"] != "" {
				t.Fatal(v.stdout)
			}
		}
		v := r.call(id+".env", "name=API_KEY")
		want := ""
		if id == "a" {
			want = "FIXTURE-API-KEY"
		}
		if structured(t, v)["value"] != want {
			t.Fatal(v.stdout)
		}
	}
	b, _ := os.ReadFile(r.paths.LogFile)
	for _, canary := range []string{"BOOTSTRAP-CANARY", "GITHUB-CANARY", "FIXTURE-API-KEY", "FIXTURE-BOOTSTRAP-TOKEN"} {
		if strings.Contains(string(b), canary) {
			t.Fatalf("log leaked %s", canary)
		}
	}
}

func TestFakeCredentialsAcrossProcesses(t *testing.T) {
	r := newRig(t)
	r.stdio("a", "op://Fixture/api/key")
	r.stdio("b", "op://Fixture/other/key")
	r.write(r.paths.StateDir+"/fixture-bootstrap-block", "block", 0o600)
	a := r.start(binaryA, "", "call", "a.counter", "--json")
	r.waitFile(r.paths.StateDir+"/fixture-auth-events", "bootstrap")
	b := r.start(binaryA, "", "call", "b.counter", "--json")
	r.waitActive(2)
	r.write(r.paths.StateDir+"/fixture-bootstrap-release", "release", 0o600)
	r.check(r.finish(a), 0, "")
	r.check(r.finish(b), 0, "")
	r.call("a.counter")
	r.call("b.counter")
	info, err := os.Stat(r.paths.StateDir + "/fixture-auth-events")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("auth event mode: %o", info.Mode().Perm())
	}
	events, err := os.ReadFile(r.paths.StateDir + "/fixture-auth-events")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(events), "\n"), "\n") {
		if line != "bootstrap" && line != "resolve-api" && line != "resolve-other" {
			t.Fatalf("nonfixed auth event %q", line)
		}
	}
	for _, event := range []string{"bootstrap", "resolve-api", "resolve-other"} {
		if n := r.countEvents(event); n != 1 {
			t.Fatalf("%s count=%d", event, n)
		}
	}
}

func TestNoInputBlackBox(t *testing.T) {
	r := newRig(t)
	r.stdio("a", "op://Fixture/api/key")
	r.check(r.run("call", "a.counter", "--no-input", "--json"), 3, "auth_required")
	if r.countEvents("bootstrap") != 0 {
		t.Fatal("no-input bootstrapped")
	}
	r.call("a.counter")
	r.check(r.run("call", "a.counter", "--no-input", "--json"), 0, "")
	if r.countEvents("bootstrap") != 1 {
		t.Fatal("lease was not reused")
	}
}

func TestAuthFailureRedactionBlackBox(t *testing.T) {
	r := newRig(t)
	r.stdio("bad", "op://Fixture/CANARY-AUTH-ERROR/key")
	v := r.check(r.run("call", "bad.counter", "--json"), 3, "auth_failed")
	b, _ := os.ReadFile(r.paths.LogFile)
	for _, canary := range []string{"CANARY-AUTH-ERROR", "FIXTURE-BOOTSTRAP-TOKEN"} {
		if strings.Contains(v.stdout+v.stderr+string(b), canary) {
			t.Fatalf("provider error leaked %s", canary)
		}
	}
	r.call("fixture.counter")
}

func TestEnvironmentBridgeBlackBox(t *testing.T) {
	r := newRig(t)
	shellWith := func(export string) {
		r.write(r.root+"/shell", "#!/bin/sh\n"+export+"exec /bin/sh -c \"$3\"\n", 0o700)
	}
	shellWith("export MCPARCEL_BRIDGE_TOKEN=bridge-canary-1\n")
	r.personal.Connections["fixture"].Transport.Stdio.Env["BRIDGE"] = config.Value{Secret: &config.SecretRef{Secret: "env:MCPARCEL_BRIDGE_TOKEN", Prefix: "Bearer "}}
	r.save()
	bridge := func() any { return structured(t, r.call("fixture.env", "name=BRIDGE"))["value"] }
	if v := bridge(); v != "Bearer bridge-canary-1" {
		t.Fatal(v)
	}
	inspect := r.run("inspect", "fixture", "--json")
	if !strings.Contains(inspect.stdout, "env:MCPARCEL_BRIDGE_TOKEN") || strings.Contains(inspect.stdout+inspect.stderr, "bridge-canary") {
		t.Fatal(inspect.stdout)
	}
	shellWith("export MCPARCEL_BRIDGE_TOKEN=bridge-canary-2\n")
	if v := bridge(); v != "Bearer bridge-canary-1" {
		t.Fatal("pooled session should keep its value", v)
	}
	r.check(r.run("runtime", "restart", "--json"), 0, "")
	if v := bridge(); v != "Bearer bridge-canary-2" {
		t.Fatal("restart should re-capture the environment", v)
	}
	shellWith("")
	r.check(r.run("runtime", "restart", "--json"), 0, "")
	missing := r.check(r.run("call", "fixture.env", "name=BRIDGE", "--json"), 2, "config_required")
	if !strings.Contains(missing.stdout, "MCPARCEL_BRIDGE_TOKEN") || !strings.Contains(missing.stdout, "runtime restart") || strings.Contains(missing.stdout, "bridge-canary") {
		t.Fatal(missing.stdout)
	}
	b, _ := os.ReadFile(r.paths.LogFile)
	if strings.Contains(string(b), "bridge-canary") {
		t.Fatal("log leaked environment value")
	}
}

func TestEnvironmentBridgeKeychainFallbackBlackBox(t *testing.T) {
	r := newRig(t)
	r.write(r.root+"/shell", "#!/bin/sh\nexec /bin/sh -c \"$3\"\n", 0o700)
	r.personal.Connections["fixture"].Transport.Stdio.Env["BRIDGE"] = config.Value{Secret: &config.SecretRef{Secret: "env:MCPARCEL_KC_TOKEN", Prefix: "Bearer "}}
	r.save()
	missing := r.check(r.run("call", "fixture.env", "name=BRIDGE", "--json"), 2, "config_required")
	if !strings.Contains(missing.stdout, "Keychain") || !strings.Contains(missing.stdout, "security add-generic-password") || !strings.Contains(missing.stdout, "MCPARCEL_KC_TOKEN") {
		t.Fatal(missing.stdout)
	}
	r.write(r.paths.StateDir+"/fixture-keychain-MCPARCEL_KC_TOKEN", "kc-canary\n", 0o600)
	if v := structured(t, r.call("fixture.env", "name=BRIDGE"))["value"]; v != "Bearer kc-canary" {
		t.Fatal(v)
	}
	b, _ := os.ReadFile(r.paths.LogFile)
	if strings.Contains(string(b), "kc-canary") {
		t.Fatal("log leaked keychain value")
	}
}
