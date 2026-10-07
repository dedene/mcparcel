package cmd

import (
	"context"
	"reflect"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
	"github.com/dedene/mcparcel/internal/ui"
)

func paritySeed(t *testing.T, store *config.Store) {
	t.Helper()
	_, err := store.Update(context.Background(), 0, func(s *config.State) error {
		s.Local.CredentialProfiles["work"] = config.Profile{Mode: "desktop", Account: "Fixture"}
		s.Personal.CredentialProfiles["team"] = config.ProfileRequirement{}
		for _, id := range []string{"alpha", "beta", "gamma", "omega", "delta"} {
			s.Personal.Connections[id] = config.Connection{Label: id, Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal("nonexistent-fixture")}}}
		}
		delta := s.Personal.Connections["delta"]
		delta.CredentialProfile = "team"
		delta.Inputs = map[string]config.Input{"root": {Kind: "string", Description: "Root"}}
		delta.Transport.Stdio.Args = []config.Value{{Input: &config.InputRef{Input: "root"}}}
		s.Personal.Connections["delta"] = delta
		s.Selections.Connections["local:beta"] = config.Selection{Enabled: true, ReviewRequired: true}
		s.Selections.Connections["local:gamma"] = config.Selection{Enabled: true}
		s.Selections.Connections["local:omega"] = config.Selection{Enabled: true}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

const (
	parityAdd    = `{"id":"added","label":"Added","domains":["tools"],"transport":{"type":"stdio","command":"added-fixture","args":["--flag"]}}`
	parityUpdate = `{"id":"alpha","label":"Alpha 2","transport":{"type":"stdio","command":"alpha-v2"}}`
)

// TestSetupParityWithCommands applies the same changes once through the
// setup draft and once through the documented commands; the resulting
// selections and personal definitions must be equal.
func TestSetupParityWithCommands(t *testing.T) {
	p := metadataEnv(t)
	cliStore := config.NewStore(p)
	paritySeed(t, cliStore)
	selectionRun(t, 0, "", "enable", "alpha")
	selectionRun(t, 0, "", "enable", "beta")
	selectionRun(t, 0, "", "disable", "gamma")
	selectionRun(t, 0, "", "tools", "disable", "omega", "t1", "t2")
	selectionRun(t, 0, "", "tools", "enable", "omega", "t1")
	selectionRun(t, 0, "", "config", "input", "set", "delta", "root", "/srv")
	selectionRun(t, 0, "", "config", "profile", "bind", "delta", "work")
	selectionRun(t, 0, "", "enable", "delta")
	selectionRun(t, 0, "", "local", "add", "--file", localFile(t, p, parityAdd))
	selectionRun(t, 0, "", "local", "update", "alpha", "--file", localFile(t, p, parityUpdate))
	want := selectionRead(t, cliStore)

	paths, _ := testutil.IsolatedPaths(t)
	uiStore := config.NewStore(paths)
	paritySeed(t, uiStore)
	d, err := ui.NewDraft(selectionRead(t, uiStore))
	if err != nil {
		t.Fatal(err)
	}
	steps := []func() error{
		func() error { return d.SetEnabled("local:alpha", true) },
		func() error { return d.SetEnabled("local:beta", true) },
		func() error { return d.SetEnabled("local:gamma", false) },
		func() error { return d.SetTool("local:omega", "t1", false) },
		func() error { return d.SetTool("local:omega", "t2", false) },
		func() error { return d.SetTool("local:omega", "t1", true) },
		func() error { return d.SetInput("local:delta", "root", "/srv") },
		func() error { return d.BindProfile("local:delta", "work") },
		func() error { return d.SetEnabled("local:delta", true) },
		func() error { _, err := d.AddLocal([]byte(parityAdd)); return err },
		func() error { return d.UpdateLocal("local:alpha", []byte(parityUpdate)) },
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatal(i, err)
		}
	}
	before := d.Base().Selections.Revision
	got, err := d.Save(context.Background(), uiStore)
	if err != nil {
		t.Fatal(err)
	}
	if got.Selections.Revision != before+1 {
		t.Fatal("setup saved in more than one update", before, got.Selections.Revision)
	}
	if !reflect.DeepEqual(want.Selections.Connections, got.Selections.Connections) {
		t.Fatalf("selections differ:\ncli   %+v\nsetup %+v", want.Selections.Connections, got.Selections.Connections)
	}
	if !reflect.DeepEqual(want.Personal, got.Personal) {
		t.Fatalf("personal differs:\ncli   %+v\nsetup %+v", want.Personal, got.Personal)
	}
	if sel := got.Selections.Connections["local:alpha"]; !sel.Enabled || !sel.ReviewRequired {
		t.Fatal("local update after enable did not require review", sel)
	}
	if sel := got.Selections.Connections["local:beta"]; !sel.Enabled || sel.ReviewRequired {
		t.Fatal("review not accepted", sel)
	}
}
