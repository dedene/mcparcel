package edit

import (
	"errors"
	"testing"

	"github.com/dedene/mcparcel/internal/args"
	"github.com/dedene/mcparcel/internal/config"
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
