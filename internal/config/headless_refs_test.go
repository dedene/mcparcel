package config_test

import (
	"errors"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

// Headless mode never resolves a 1Password reference: the connection that
// needs one is config_required; one without stays usable.
func TestRuntimeConnectionHeadlessRefusesOnePassword(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	writeFile(t, p.PersonalFile, protectedPersonal, 0o644)
	writeFile(t, p.ConfigFile, `{"schemaVersion":1,"credentialProfiles":{"team":{"mode":"desktop-service-account","account":"Fixture","bootstrapRef":"op://v/i/token"}},"runtime":{"mode":"headless","stateRoot":"/var/lib/mcparcel"}}`, 0o644)
	snap, e := config.Load(p)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = snap.RuntimeConnection("protected"); !errors.Is(e, config.ErrHeadlessOnePassword) || !errors.Is(e, config.ErrConfigRequired) {
		t.Fatal(e)
	}
	if _, _, e = snap.RuntimeConnection("paper"); e != nil {
		t.Fatal(e)
	}
	writeFile(t, p.ConfigFile, localTeam, 0o644)
	if snap, e = config.Load(p); e != nil {
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
