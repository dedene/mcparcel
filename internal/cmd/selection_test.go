package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/output"

	"github.com/dedene/mcparcel/internal/config"
)

func selectionRead(t *testing.T, store *config.Store) config.State {
	t.Helper()
	s, e := store.Read(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	return s
}

func selectionRun(t *testing.T, want int, code string, argv ...string) {
	t.Helper()
	got, out, err := run(t, append(argv, "--json")...)
	if got != want || err != "" || code != "" && !strings.Contains(out, `"code":"`+code+`"`) {
		t.Fatal(got, out, err)
	}
}

func TestEnableBulkAtomic(t *testing.T) {
	_, store, state := metadataSeed(t)
	selectionRun(t, 2, "config_required", "enable", "disabled", "missing")
	if !reflect.DeepEqual(state, selectionRead(t, store)) {
		t.Fatal("partial enable")
	}
	selectionRun(t, 4, "connection_unavailable", "enable", "disabled", "absent")
	if !reflect.DeepEqual(state, selectionRead(t, store)) {
		t.Fatal("partial enable")
	}
}

func TestEnableClearsReview(t *testing.T) {
	p, store, state := metadataSeed(t)
	state, e := store.Update(context.Background(), state.Selections.Revision, func(s *config.State) error {
		sel := s.Selections.Connections["local:paper"]
		sel.ReviewRequired = true
		s.Selections.Connections["local:paper"] = sel
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	selectionRun(t, 0, "", "enable", "review")
	next := selectionRead(t, store)
	if next.Selections.Revision != state.Selections.Revision+1 || next.Selections.Connections["local:review"].ReviewRequired || !next.Selections.Connections["local:review"].Enabled || !reflect.DeepEqual(next.Selections.Connections["local:paper"], state.Selections.Connections["local:paper"]) {
		t.Fatal(next)
	}
	for _, path := range []string{p.SocketFile, p.LogFile, filepath.Join(p.StateDir, "fixture-auth-events")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal(path, err)
		}
	}
}

func TestDisableRetainsSettings(t *testing.T) {
	_, store, state := metadataSeed(t)
	state, e := store.Update(context.Background(), state.Selections.Revision, func(s *config.State) error {
		s.Selections.Connections["local:removed"] = config.Selection{Enabled: true, ReviewRequired: true, Inputs: map[string]string{"x": "retained"}, CredentialProfile: "work", DisabledTools: []string{"read"}}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	selectionRun(t, 0, "", "disable", "removed")
	next := selectionRead(t, store)
	want := state.Selections.Connections["local:removed"]
	want.Enabled = false
	if !reflect.DeepEqual(want, next.Selections.Connections["local:removed"]) {
		t.Fatal(next)
	}
	selectionRun(t, 4, "connection_unavailable", "enable", "removed")
	selectionRun(t, 0, "", "inspect", "removed")
	code, out, _ := run(t, "list", "--json")
	var v struct{ Data output.MetadataData }
	if err := json.Unmarshal([]byte(out), &v); err != nil || code != 0 {
		t.Fatal(out, err)
	}
	for _, row := range v.Data.Items {
		if row.ID == "local:removed" {
			t.Fatal("disabled retained row listed")
		}
	}
}

func TestEnableIdentity(t *testing.T) {
	p := metadataEnv(t)
	store := config.NewStore(p)
	report, e := config.ImportMcporter([]byte(`{"mcpServers":{"paper":{"command":"fixture"}}}`), nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = config.ApplyImport(context.Background(), store, 0, report, nil); e != nil {
		t.Fatal(e)
	}
	state := selectionRead(t, store)
	source := config.Source{ID: "github-42", RepositoryID: 42, Owner: "fixture-owner", Repo: "fixture.repo", Path: "mcparcel.json", Ref: "main", Commit: strings.Repeat("a", 40)}
	_, e = store.Update(context.Background(), state.Selections.Revision, func(s *config.State) error {
		s.Local.Sources = []config.Source{source}
		s.Catalogs[source.ID] = config.Catalog{SchemaVersion: 1, Domains: s.Personal.Domains, Connections: map[string]config.Connection{"paper": s.Personal.Connections["paper"]}}
		sel := s.Selections.Connections["local:paper"]
		sel.Enabled = false
		s.Selections.Connections["local:paper"] = sel
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	selectionRun(t, 0, "", "enable", "paper")
	next := selectionRead(t, store)
	team := "github:fixture-owner/fixture.repo#paper"
	if !next.Selections.Connections["local:paper"].Enabled || next.Selections.Connections[team].Enabled || next.Local.Aliases["paper"] != "local:paper" {
		t.Fatal(next)
	}
	selectionRun(t, 0, "", "enable", team)
	if !selectionRead(t, store).Selections.Connections[team].Enabled {
		t.Fatal("team not enabled")
	}
}

func TestSelectionDuplicateIDs(t *testing.T) {
	_, store, state := metadataSeed(t)
	selectionRun(t, 0, "", "enable", "disabled", "local:disabled", "disabled")
	next := selectionRead(t, store)
	if next.Selections.Revision != state.Selections.Revision+1 || !next.Selections.Connections["local:disabled"].Enabled {
		t.Fatal(next)
	}
}

func TestToolSelectionOffline(t *testing.T) {
	_, store, state := metadataSeed(t)
	state, e := store.Update(context.Background(), state.Selections.Revision, func(s *config.State) error {
		c := s.Personal.Connections["review"]
		allow := []string{"read"}
		c.ToolPolicy = &config.ToolPolicy{Allow: &allow}
		s.Personal.Connections["review"] = c
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	selectionRun(t, 0, "", "tools", "disable", "review", "read", "read")
	next := selectionRead(t, store)
	if !reflect.DeepEqual(next.Selections.Connections["local:review"].DisabledTools, []string{"read"}) {
		t.Fatal(next)
	}
	selectionRun(t, 0, "", "tools", "enable", "review", "write", "read")
	next = selectionRead(t, store)
	effective, e := config.Resolve(next)
	if e != nil || len(next.Selections.Connections["local:review"].DisabledTools) != 0 || config.ToolAllowed(effective.Connections["local:review"].Policy, "write") || !next.Selections.Connections["local:review"].ReviewRequired {
		t.Fatal(next, e)
	}
	for _, tool := range []string{"", "bad\nname", "bad\rname", "bad\x00name"} {
		_, e := saveTools(context.Background(), store, next, "review", []string{tool}, false)
		if e == nil {
			t.Fatal(tool)
		}
	}
	selectionRun(t, 0, "", "tools", "disable", "review", "a.b", "unknown")
	next = selectionRead(t, store)
	if !reflect.DeepEqual(next.Selections.Connections["local:review"].DisabledTools, []string{"a.b", "unknown"}) {
		t.Fatal(next)
	}
}

func TestSelectionCAS(t *testing.T) {
	_, store, state := metadataSeed(t)
	selectionRun(t, 0, "", "disable", "paper")
	current := selectionRead(t, store)
	_, e := saveEnabled(context.Background(), store, state, []string{"paper"}, true)
	if !errors.Is(e, config.ErrConfigConflict) {
		t.Fatal(e)
	}
	_, e = saveTools(context.Background(), store, state, "paper", []string{"read"}, false)
	if !errors.Is(e, config.ErrConfigConflict) || !reflect.DeepEqual(current, selectionRead(t, store)) {
		t.Fatal(e)
	}
}

func TestEnableMetadataPrerequisites(t *testing.T) {
	_, store, state := metadataSeed(t)
	_, err := store.Update(context.Background(), state.Selections.Revision, func(s *config.State) error {
		s.Personal.CredentialProfiles["team"] = config.ProfileRequirement{}
		s.Personal.Connections["protected"] = config.Connection{CredentialProfile: "team", Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal("nonexistent-fixture")}}}
		s.Personal.Connections["oauth"] = config.Connection{Auth: &config.OAuth{Type: "oauth"}, Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal("https://fixture.invalid")}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	selectionRun(t, 2, "config_required", "enable", "protected")
	selectionRun(t, 0, "", "enable", "oauth")
	if !selectionRead(t, store).Selections.Connections["local:oauth"].Enabled {
		t.Fatal("OAuth metadata not selectable")
	}
}
