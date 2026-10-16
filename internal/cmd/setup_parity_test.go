package cmd

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
	"github.com/dedene/mcparcel/internal/testutil"
	"github.com/dedene/mcparcel/internal/ui"
)

func paritySeed(t *testing.T, store *config.Store) {
	t.Helper()
	_, err := store.Update(context.Background(), 0, func(s *config.State) error {
		s.Local.CredentialProfiles["work"] = config.Profile{Mode: "desktop", Account: "Fixture"}
		s.Local.CredentialProfiles["team"] = config.Profile{Mode: "desktop", Account: "Team fixture"}
		s.Personal.CredentialProfiles["team"] = config.ProfileRequirement{}
		for _, id := range []string{"alpha", "beta", "gamma", "omega", "delta", "epsilon"} {
			s.Personal.Connections[id] = config.Connection{Label: id, Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal("nonexistent-fixture")}}}
		}
		delta := s.Personal.Connections["delta"]
		delta.CredentialProfile = "team"
		delta.Inputs = map[string]config.Input{"root": {Kind: "string", Description: "Root"}}
		delta.Transport.Stdio.Args = []config.Value{{Input: &config.InputRef{Input: "root"}}}
		s.Personal.Connections["delta"] = delta
		// epsilon is enabled without an explicit bind; enable links the
		// local profile named like its requirement on both paths.
		epsilon := s.Personal.Connections["epsilon"]
		epsilon.CredentialProfile = "team"
		s.Personal.Connections["epsilon"] = epsilon
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

// TestSetupParityWithCommands makes the same changes once through the
// documented commands and once by pressing keys in setup, which writes each
// change as it is made; the resulting selections and personal definitions
// must be equal, automatic profile links included.
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
	selectionRun(t, 0, "", "enable", "epsilon")
	selectionRun(t, 0, "", "local", "add", "--file", localFile(t, p, parityAdd))
	selectionRun(t, 0, "", "local", "update", "alpha", "--file", localFile(t, p, parityUpdate))
	selectionRun(t, 0, "", "local", "remove", "omega")
	want := selectionRead(t, cliStore)

	paths, _ := testutil.IsolatedPaths(t)
	uiStore := config.NewStore(paths)
	paritySeed(t, uiStore)
	opts := ui.Options{
		Describe: safeFailure,
		Now:      time.Now,
		LoadTools: func(_ context.Context, id string) (output.ToolList, error) {
			return output.ToolList{Connection: id, Items: []json.RawMessage{json.RawMessage(`{"name":"t1"}`), json.RawMessage(`{"name":"t2"}`)}}, nil
		},
	}
	// Each row is found by search: "/", its name, Enter; Esc clears it.
	_, err := driveSetup(t, context.Background(), uiStore, selectionRead(t, uiStore), opts,
		"/", "alpha", "enter", "space", "esc",
		"/", "beta", "enter", "space", "esc",
		"/", "gamma", "enter", "space", "esc",
		// omega's tools page: t1 off, t2 off, t1 on again.
		"/", "omega", "enter", "enter", "enter", "space", "down", "space", "up", "space", "esc", "esc", "esc",
		// delta's page (Turn on, Set root, Choose tools, Link 1Password
		// profile): its input, set on the page, then the profile picker
		// (team, work), then on.
		"/", "delta", "enter", "enter", "down", "enter", "/srv", "enter",
		"down", "down", "down", "enter", "down", "enter", "space", "esc", "esc",
		"/", "epsilon", "enter", "space", "esc",
		"n", "added", "tab", "Added", "tab", "tab", "ctrl+u", "tools", "tab", "tab", "added-fixture", "tab", "--flag", "enter",
		// alpha's page: Choose tools, Start, Turn off, Edit.
		"/", "alpha", "enter", "enter", "down", "down", "down", "enter", "ctrl+u", "Alpha 2", "tab", "tab", "tab", "tab", "ctrl+u", "alpha-v2", "enter", "esc", "esc",
		// omega's page: Choose tools, Test again, Turn off, Edit, Remove.
		"/", "omega", "enter", "enter", "down", "down", "down", "down", "enter", "esc",
		"q")
	if err != nil {
		t.Fatal(err)
	}
	got := selectionRead(t, uiStore)
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
	if sel := got.Selections.Connections["local:delta"]; sel.CredentialProfile != "work" {
		t.Fatal("explicit binding replaced", sel)
	}
	if sel := got.Selections.Connections["local:epsilon"]; !sel.Enabled || sel.CredentialProfile != "team" || sel.ReviewRequired {
		t.Fatal("enable did not link the matching profile", sel)
	}
	if _, ok := got.Personal.Connections["omega"]; ok {
		t.Fatal("remove kept the definition")
	}
}
