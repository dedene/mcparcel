package config_test

import (
	"errors"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

// Headless mode resolves a 1Password reference only through a
// service-account profile: a connection bound to a desktop-app profile is
// config_required; one without references stays usable.
func TestRuntimeConnectionHeadlessRefusesOnePassword(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	writeFile(t, p.PersonalFile, protectedPersonal, 0o644)
	const headless = `,"runtime":{"mode":"headless","stateRoot":"/var/lib/mcparcel"}}`
	for _, profile := range []string{
		`{"mode":"desktop-service-account","account":"Fixture","bootstrapRef":"op://v/i/token"}`,
		`{"mode":"desktop","account":"Fixture"}`,
	} {
		writeFile(t, p.ConfigFile, `{"schemaVersion":1,"credentialProfiles":{"team":`+profile+`}`+headless, 0o644)
		snap, e := config.Load(p)
		if e != nil {
			t.Fatal(e)
		}
		if _, _, e = snap.RuntimeConnection("protected"); !errors.Is(e, config.ErrHeadlessOnePassword) || !errors.Is(e, config.ErrConfigRequired) {
			t.Fatal(profile, e)
		}
		if _, _, e = snap.RuntimeConnection("paper"); e != nil {
			t.Fatal(e)
		}
	}
	for _, profile := range []string{
		`{"mode":"service-account","tokenEnv":"OP_SERVICE_ACCOUNT_TOKEN"}`,
		`{"mode":"service-account","tokenFile":"/var/run/secrets/mcparcel/op-token"}`,
	} {
		writeFile(t, p.ConfigFile, `{"schemaVersion":1,"credentialProfiles":{"team":`+profile+`}`+headless, 0o644)
		snap, e := config.Load(p)
		if e != nil {
			t.Fatal(e)
		}
		if _, _, e = snap.RuntimeConnection("protected"); e != nil {
			t.Fatal("headless mode refused a service-account profile:", profile, e)
		}
	}
	writeFile(t, p.ConfigFile, localTeam, 0o644)
	snap, e := config.Load(p)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = snap.RuntimeConnection("protected"); e != nil {
		t.Fatal("desktop mode refused a 1Password reference:", e)
	}
}

func TestPathsHeadless(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	if p.Headless() {
		t.Fatal("desktop paths headless")
	}
	p.StateRoot = "/var/lib/mcparcel"
	if !p.Headless() {
		t.Fatal("state root paths not headless")
	}
}
