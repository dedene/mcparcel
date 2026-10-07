package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

func fixedAges(seconds float64) func(*Options) {
	return func(o *Options) {
		o.SourceAges = func(state config.State) ([]output.SourceMetadata, error) {
			out := []output.SourceMetadata{}
			for _, source := range state.Local.Sources {
				out = append(out, output.SourceMetadata{Source: source, CacheAgeSeconds: seconds})
			}
			return out, nil
		}
	}
}

func TestDetailsShowsBindingsTransportPolicyRevision(t *testing.T) {
	seed := func(t *testing.T, store *config.Store) {
		seedFixture(t, store)
		mustUpdate(t, store, func(s *config.State) {
			catalog := s.Catalogs[fixSource]
			notes := catalog.Connections["notes"]
			notes.ToolPolicy = &config.ToolPolicy{Allow: &[]string{"a", "b", "c"}, Deny: []string{"d"}}
			catalog.Connections["notes"] = notes
			s.Catalogs[fixSource] = catalog
			s.Local.Sources[0].Commit = strings.Repeat("b", 40)
			s.Selections.Connections[idNotes] = config.Selection{Inputs: map[string]string{"workspace": "docs"}, CredentialProfile: "work", DisabledTools: []string{"b"}}
		})
	}
	h := newHarness(t, seed, 120, 35, fixedAges(7200))
	h.details("Research", "Notes")
	revision := h.disk().Selections.Revision
	h.contains("MCParcel / Setup / Notes", "ID: "+idNotes, "Enabled: [ ]   State: Ready",
		"Source: acmeco/mcp-catalog, commit bbbbbbb, snapshot 2h old", "Domains: Research",
		"Transport: stdio: notes-fixture (2 arguments)", "Shared definition: read-only.",
		"> Input workspace (string): docs", "Credential profile: work",
		"Tools: 3 allowed by source, 1 denied by source, 1 disabled by you", "Connect and load tools",
		"Schema: none cached", fmt.Sprintf("Config revision %d, no unsaved changes", revision))
	h.press("esc")
	if h.m.screen != screenList {
		t.Fatal(h.m.screen)
	}
}

// visitAll opens every screen for every row and asserts no marker shows.
func visitAll(t *testing.T, h *harness, markers []string) {
	t.Helper()
	check := func(where string) {
		t.Helper()
		for _, marker := range markers {
			if strings.Contains(h.view(), marker) {
				t.Fatalf("%s shows %q:\n%s", where, marker, h.plain())
			}
		}
	}
	for _, tb := range tabs(h.m.draft.Effective()) {
		h.selectTab(tb.label)
		check("list " + tb.label)
		for i := range h.m.rows() {
			h.press("home")
			for range i {
				h.press("down")
			}
			check("row")
			h.press("enter")
			check("details")
			row := h.detailRow()
			for j, item := range h.m.detailItems(row) {
				h.m.detailCursor = j
				switch item.kind {
				case itemInput, itemProfile, itemEdit:
					h.press("enter")
					check(fmt.Sprintf("%s item %d", row.ID, j))
					h.press("esc")
				case itemTools:
					h.press("t")
					check("tools")
					h.press("esc")
				}
			}
			h.press("esc")
		}
	}
	h.press("?")
	check("help")
	h.press("esc")
}

func (h *harness) detailRow() config.EffectiveConnection { return h.m.detailRow() }

func TestDetailsNeverShowsSecretsOrURLPath(t *testing.T) {
	h := newHarness(t, seedSecrets, 120, 35, fixedAges(60))
	visitAll(t, h, secretMarkers)
	if h.m.screen != screenList {
		t.Fatalf("visit ended on screen %d:\n%s", h.m.screen, h.plain())
	}
	h.details("Other", "Hook")
	h.contains("Transport: HTTP: hook.example")
	h.press("esc")
	h.details("Other", "Vault")
	h.contains("Transport: stdio: vault-fixture (2 arguments)", "Input endpoint (url): https://in.example/...")
	small := newHarness(t, seedSecrets, 60, 20)
	visitAll(t, small, secretMarkers)
}

func TestSetInputStagesConfigInputSet(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	before := h.disk().Selections.Revision
	h.details("Research", "Notes")
	h.press("enter")
	h.contains("Input workspace (string)", "Workspace", "Value: _")
	h.typeText("docs")
	h.press("enter")
	h.contains("> Input workspace (string): docs", "1 unsaved change")
	h.press("enter")
	h.contains("Value: docs_")
	h.press("esc", "esc", "ctrl+s")
	disk := h.disk()
	if disk.Selections.Revision != before+1 || disk.Selections.Connections[idNotes].Inputs["workspace"] != "docs" {
		t.Fatal(disk.Selections.Revision, disk.Selections.Connections[idNotes])
	}
}

func TestURLInputShowsHostOnlyAndEmptyKeeps(t *testing.T) {
	h := newHarness(t, seedSecrets, 120, 35)
	h.details("Other", "Vault")
	h.contains("> Input endpoint (url): https://in.example/...")
	h.press("enter")
	h.contains("Current: https://in.example/...", "Value: _", "empty keeps the current value")
	h.press("enter")
	h.contains("Kept the current value.")
	if h.m.screen != screenDetails || h.m.draft.Changes() != 0 {
		t.Fatal(h.m.screen, h.m.draft.Changes())
	}
	h.press("enter")
	h.paste("https://new.example/other/path")
	h.press("enter")
	h.contains("Input endpoint (url): https://new.example/...")
	h.lacks("other/path")
	if h.m.draft.State().Selections.Connections["local:vault"].Inputs["endpoint"] != "https://new.example/other/path" {
		t.Fatal(h.m.draft.State().Selections.Connections["local:vault"])
	}
}

func TestInputValidationErrorInline(t *testing.T) {
	seed := func(t *testing.T, store *config.Store) {
		mustUpdate(t, store, func(s *config.State) {
			s.Personal.Connections["files"] = config.Connection{
				Label: "Files", Inputs: map[string]config.Input{"root": {Kind: "path", Description: "Root"}},
				Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal("files-fixture"), Args: []config.Value{{Input: &config.InputRef{Input: "root"}}}}},
			}
		})
	}
	h := newHarness(t, seed, 120, 35)
	h.details("Other", "Files")
	h.press("enter")
	h.typeText("relative/WRONG-VALUE")
	h.press("enter")
	h.contains("Invalid: selections.connections.local:files.inputs.root: absolute path required.")
	if h.m.screen != screenField || h.m.draft.Changes() != 0 {
		t.Fatal(h.m.screen, h.m.draft.Changes())
	}
	if line := strings.Split(h.plain(), "\n"); strings.Contains(strings.Join(line[len(line)-4:], "\n"), "WRONG-VALUE") {
		t.Fatal("error repeats the value")
	}
	h.press("ctrl+u")
	h.typeText("/srv/files")
	h.press("enter")
	if h.m.screen != screenDetails || h.m.draft.Changes() != 1 {
		t.Fatal(h.m.screen, h.m.draft.Changes())
	}
}

func TestBindProfileFromExistingProfiles(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.details("Research", "Notes")
	h.contains("Credential profile: (not bound)")
	h.press("down", "enter")
	h.contains("Choose a credential profile for Notes.", "> work")
	h.lacks("Fixture account")
	h.press("enter")
	h.contains("Credential profile: work", "1 unsaved change")
	if h.m.draft.State().Selections.Connections[idNotes].CredentialProfile != "work" {
		t.Fatal(h.m.draft.State().Selections.Connections[idNotes])
	}
	h.press("enter")
	h.contains("> work (bound)")
}

func TestNoProfilesShowsProfileSetHint(t *testing.T) {
	seed := func(t *testing.T, store *config.Store) {
		seedFixture(t, store)
		mustUpdate(t, store, func(s *config.State) { delete(s.Local.CredentialProfiles, "work") })
	}
	h := newHarness(t, seed, 120, 35)
	h.details("Research", "Notes")
	h.press("down", "enter")
	h.contains("No profiles yet. Create one: mcparcel config profile set <name> --file <profile.json>.")
	h.press("enter", "esc")
	if h.m.screen != screenDetails || h.m.draft.Changes() != 0 {
		t.Fatal(h.m.screen, h.m.draft.Changes())
	}
}

func TestSpaceOnConfigRequiredOpensDetails(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.selectTab("Research")
	h.selectRow("Notes")
	h.press("space")
	if h.m.screen != screenDetails || h.m.draft.Changes() != 0 {
		t.Fatal(h.m.screen, h.m.draft.Changes())
	}
	h.contains("Notes needs: input workspace, credential profile. Fill them in, then press Space.", "> Input workspace")
	h.press("enter")
	h.typeText("docs")
	h.press("enter", "space")
	h.contains("Notes needs: credential profile.", "> Credential profile")
	h.press("enter", "enter", "space")
	h.contains("Enabled: [x]   State: Ready", "3 unsaved changes")
	h.press("esc", "ctrl+s")
	if sel := h.disk().Selections.Connections[idNotes]; !sel.Enabled || sel.CredentialProfile != "work" || sel.Inputs["workspace"] != "docs" {
		t.Fatal(sel)
	}
}

func TestSharedDefinitionReadOnly(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.details("Design", "Paper")
	h.contains("Shared definition: read-only.")
	h.lacks("Edit personal connection", "e: edit")
	h.press("e")
	if h.m.screen != screenDetails || h.m.form != nil {
		t.Fatal(h.m.screen)
	}
	h.press("esc")
	h.details("Other", "Excalidraw")
	h.contains("Edit personal connection", "e: edit")
	h.lacks("Shared definition")
	h.press("e")
	if h.m.screen != screenForm {
		t.Fatal(h.m.screen)
	}
}
