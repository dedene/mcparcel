package cli_test

import (
	"os"
	"strings"
	"testing"
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
