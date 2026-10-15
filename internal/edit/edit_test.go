package edit

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/dedene/mcparcel/internal/args"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

func editState(t *testing.T) config.State {
	t.Helper()
	personal := config.Catalog{
		SchemaVersion:      1,
		Domains:            map[string]config.Domain{},
		CredentialProfiles: map[string]config.ProfileRequirement{"team": {}},
		Connections: map[string]config.Connection{
			"paper": {
				Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal("fixture")}},
				Inputs:    map[string]config.Input{"workspace": {Kind: "string", Description: "Workspace"}},
			},
			"plain": {Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal("fixture")}}},
			"team":  {CredentialProfile: "team", Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal("fixture")}}},
			"both": {
				CredentialProfile: "team",
				Transport:         config.Transport{Stdio: &config.Stdio{Command: config.Literal("fixture")}},
				Inputs:            map[string]config.Input{"workspace": {Kind: "string", Description: "Workspace"}},
			},
		},
	}
	return config.State{
		Local:      config.Local{SchemaVersion: 1, CredentialProfiles: map[string]config.Profile{"work": {Mode: "desktop", Account: "Fixture account"}}},
		Personal:   personal,
		Selections: config.Selections{SchemaVersion: 1, Connections: map[string]config.Selection{}},
		Catalogs:   map[string]config.Catalog{},
	}
}

func TestStageInputRequiresDeclaredInput(t *testing.T) {
	state := editState(t)
	if err := StageInput(&state, "paper", "other", "x"); !errors.Is(err, config.ErrConfig) {
		t.Fatal(err)
	}
	if _, ok := state.Selections.Connections["local:paper"]; ok {
		t.Fatal("undeclared input staged")
	}
	if err := StageInput(&state, "absent", "workspace", "x"); !errors.Is(err, config.ErrNotFound) {
		t.Fatal(err)
	}
	if err := StageInput(&state, "paper", "workspace", "docs"); err != nil {
		t.Fatal(err)
	}
	if got := state.Selections.Connections["local:paper"].Inputs["workspace"]; got != "docs" {
		t.Fatal(got)
	}
}

func TestStageProfileRequiresRequirement(t *testing.T) {
	state := editState(t)
	if err := StageProfile(&state, "plain", "work"); !errors.Is(err, config.ErrConfig) {
		t.Fatal(err)
	}
	if err := StageProfile(&state, "team", "absent"); !errors.Is(err, config.ErrConfig) {
		t.Fatal(err)
	}
	if err := StageProfile(&state, "absent", "work"); !errors.Is(err, config.ErrNotFound) {
		t.Fatal(err)
	}
	if err := StageProfile(&state, "team", "work"); err != nil {
		t.Fatal(err)
	}
	if got := state.Selections.Connections["local:team"].CredentialProfile; got != "work" {
		t.Fatal(got)
	}
}

func TestStageToolsRejectsControlCharacters(t *testing.T) {
	state := editState(t)
	for _, tools := range [][]string{nil, {""}, {"a\x00b"}, {"a\rb"}, {"a\nb"}} {
		if err := StageTools(&state, "plain", []string{"local:plain"}, tools, false); !errors.Is(err, args.ErrInvalidArgs) {
			t.Fatal(tools, err)
		}
	}
	if len(state.Selections.Connections) != 0 {
		t.Fatal("rejected tools staged")
	}
	if err := StageTools(&state, "plain", []string{"local:plain"}, []string{"write", "read", "write"}, false); err != nil {
		t.Fatal(err)
	}
	if got := state.Selections.Connections["local:plain"].DisabledTools; len(got) != 2 || got[0] != "read" || got[1] != "write" {
		t.Fatal(got)
	}
	if err := StageTools(&state, "plain", []string{"local:other"}, []string{"read"}, true); !errors.Is(err, config.ErrConfigConflict) {
		t.Fatal(err)
	}
	if err := StageTools(&state, "plain", []string{"local:plain"}, []string{"read"}, true); err != nil {
		t.Fatal(err)
	}
	if got := state.Selections.Connections["local:plain"].DisabledTools; len(got) != 1 || got[0] != "write" {
		t.Fatal(got)
	}
}

func TestStageEnabledBindsMatchingProfile(t *testing.T) {
	state := editState(t)
	// Only a local profile with the requirement's exact ID is bound; "work"
	// is the only profile, but it is never picked on its own.
	if err := StageEnabled(&state, []string{"team"}, []string{"local:team"}, true); !errors.Is(err, config.ErrConfigRequired) {
		t.Fatal(err)
	}
	if got := state.Selections.Connections["local:team"].CredentialProfile; got != "" {
		t.Fatal("bound a profile with another ID", got)
	}
	state = editState(t)
	state.Local.CredentialProfiles["team"] = config.Profile{Mode: "desktop", Account: "Team account"}
	if err := StageEnabled(&state, []string{"team"}, []string{"local:team"}, true); err != nil {
		t.Fatal(err)
	}
	if sel := state.Selections.Connections["local:team"]; !sel.Enabled || sel.CredentialProfile != "team" {
		t.Fatal(sel)
	}
}

func TestStageEnabledKeepsValidBinding(t *testing.T) {
	state := editState(t)
	state.Local.CredentialProfiles["team"] = config.Profile{Mode: "desktop", Account: "Team account"}
	state.Selections.Connections["local:team"] = config.Selection{CredentialProfile: "work"}
	if err := StageEnabled(&state, []string{"team"}, []string{"local:team"}, true); err != nil {
		t.Fatal(err)
	}
	if got := state.Selections.Connections["local:team"].CredentialProfile; got != "work" {
		t.Fatal("valid binding replaced", got)
	}
	// Disabling never binds.
	state.Selections.Connections["local:team"] = config.Selection{Enabled: true, CredentialProfile: "gone"}
	if err := StageEnabled(&state, []string{"team"}, []string{"local:team"}, false); err != nil {
		t.Fatal(err)
	}
	if got := state.Selections.Connections["local:team"].CredentialProfile; got != "gone" {
		t.Fatal("disable changed the binding", got)
	}
}

func TestStageEnabledRebindsDanglingProfile(t *testing.T) {
	state := editState(t)
	state.Local.CredentialProfiles["team"] = config.Profile{Mode: "desktop", Account: "Team account"}
	state.Selections.Connections["local:team"] = config.Selection{CredentialProfile: "gone"}
	if got := AutoProfile(state, mustRow(t, state, "local:team")); got != "team" {
		t.Fatal(got)
	}
	if err := StageEnabled(&state, []string{"team"}, []string{"local:team"}, true); err != nil {
		t.Fatal(err)
	}
	if got := state.Selections.Connections["local:team"].CredentialProfile; got != "team" {
		t.Fatal(got)
	}
	if got := AutoProfile(state, mustRow(t, state, "local:team")); got != "" {
		t.Fatal("bound connection still offers a profile", got)
	}
	if got := AutoProfile(state, mustRow(t, state, "local:plain")); got != "" {
		t.Fatal("connection without a requirement offers a profile", got)
	}
}

func TestStageEnabledMissingInputPersistsNoBinding(t *testing.T) {
	paths, _ := testutil.IsolatedPaths(t)
	store := config.NewStore(paths)
	seed := editState(t)
	seed.Local.CredentialProfiles["team"] = config.Profile{Mode: "desktop", Account: "Team account"}
	state, err := store.Update(context.Background(), 0, func(s *config.State) error {
		s.Personal = seed.Personal
		s.Local.CredentialProfiles = seed.Local.CredentialProfiles
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"local:both"}
	_, err = store.UpdateAccepted(context.Background(), state.Selections.Revision, ids, func(draft *config.State) error {
		return StageEnabled(draft, []string{"both"}, ids, true)
	})
	if !errors.Is(err, config.ErrConfigRequired) {
		t.Fatal(err)
	}
	after, err := store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state.Selections, after.Selections) {
		t.Fatal("failed enable persisted a binding", after.Selections)
	}
}

func mustRow(t *testing.T, state config.State, id string) config.EffectiveConnection {
	t.Helper()
	effective, err := config.Resolve(state)
	if err != nil {
		t.Fatal(err)
	}
	return effective.Connections[id]
}
