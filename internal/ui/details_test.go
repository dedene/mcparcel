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
	h.contains("MCParcel / Setup / Notes", "ID: "+idNotes, "State: [ ] Off",
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

// withProfiles adds local profiles to the fixture, which has only "work".
func withProfiles(names ...string) func(*testing.T, *config.Store) {
	return func(t *testing.T, store *config.Store) {
		seedFixture(t, store)
		mustUpdate(t, store, func(s *config.State) {
			for _, name := range names {
				s.Local.CredentialProfiles[name] = config.Profile{Mode: "desktop", Account: "Fixture account"}
			}
		})
	}
}

func TestBindProfileFromExistingProfiles(t *testing.T) {
	h := newHarness(t, withProfiles("other"), 120, 35)
	h.details("Research", "Notes")
	h.contains("Credential profile: (not bound)")
	h.press("down", "enter")
	h.contains("Choose a credential profile for Notes.", "> other", "  work")
	h.lacks("Fixture account")
	h.press("down", "enter")
	h.contains("Credential profile: work", "1 unsaved change")
	if h.m.draft.State().Selections.Connections[idNotes].CredentialProfile != "work" {
		t.Fatal(h.m.draft.State().Selections.Connections[idNotes])
	}
	h.press("enter")
	h.contains("> other", "  work (bound)")
}

func TestSingleProfileLinksWithoutPicker(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.details("Research", "Notes")
	h.press("down", "enter")
	if h.m.screen != screenDetails {
		t.Fatal(h.m.screen)
	}
	h.contains("Credential profile: work", "Linked credential profile work.", "1 unsaved change")
	h.lacks("Choose a credential profile", "Fixture account")
	if sel := h.m.draft.State().Selections.Connections[idNotes]; sel.CredentialProfile != "work" || sel.Enabled {
		t.Fatal(sel)
	}
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

// One Space asks for each missing item, then turns the row on: the input,
// then the only local profile, linked without asking.
func TestSpaceOnConfigRequiredFillsThenEnables(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.selectTab("Research")
	h.selectRow("Notes")
	h.press("space")
	if h.m.screen != screenField || h.m.draft.Changes() != 0 {
		t.Fatal(h.m.screen, h.m.draft.Changes())
	}
	h.contains("Input workspace (string)", "Notes needs: input workspace, credential profile. Esc keeps it off.")
	h.typeText("docs")
	h.press("enter")
	if h.m.screen != screenList || h.m.pendingEnable != "" {
		t.Fatal(h.m.screen, h.m.pendingEnable)
	}
	h.focused("[x] Notes")
	h.contains("Linked credential profile work. Notes is on.", "3 unsaved changes")
	h.lacks("Fixture account")
	h.press("ctrl+s")
	if sel := h.disk().Selections.Connections[idNotes]; !sel.Enabled || sel.CredentialProfile != "work" || sel.Inputs["workspace"] != "docs" {
		t.Fatal(sel)
	}
}

// A local profile with the required ID is linked by the enable itself
// (edit.StageEnabled), so setup neither asks for it nor lists it as missing.
func TestSpaceLinksProfileMatchingRequirement(t *testing.T) {
	h := newHarness(t, withProfiles("team"), 120, 35)
	h.selectTab("Research")
	h.selectRow("Notes")
	h.paneContains("[*] input workspace - not set", "[+] 1Password team - links automatically")
	h.lacks("credential profile")
	h.press("space")
	h.contains("Notes needs: input workspace. Esc keeps it off.")
	h.typeText("docs")
	h.press("enter")
	h.focused("[x] Notes")
	h.contains("Linked credential profile team. Notes is on.", "3 unsaved changes")
	if sel := h.m.draft.State().Selections.Connections[idNotes]; !sel.Enabled || sel.CredentialProfile != "team" {
		t.Fatal(sel)
	}

	ready := func(t *testing.T, store *config.Store) {
		withProfiles("team")(t, store)
		mustUpdate(t, store, func(s *config.State) {
			s.Selections.Connections[idNotes] = config.Selection{Inputs: map[string]string{"workspace": "docs"}}
		})
	}
	direct := newHarness(t, ready, 120, 35)
	direct.selectTab("Research")
	direct.selectRow("Notes")
	direct.paneContains("Space turns it on and links credential profile team.", "1Password team - links automatically")
	direct.press("space")
	if direct.m.screen != screenList {
		t.Fatal(direct.m.screen)
	}
	direct.focused("[x] Notes")
	direct.contains("Linked credential profile team.", "2 unsaved changes")
}

// A row that misses only the profile Space links by itself reads ready (off,
// nothing missing) in the wide list, the compact list and details, not
// configuration required.
func TestAutoLinkedProfileRowReadsReady(t *testing.T) {
	seed := func(t *testing.T, store *config.Store) {
		withProfiles("team")(t, store)
		mustUpdate(t, store, func(s *config.State) {
			s.Selections.Connections[idNotes] = config.Selection{Inputs: map[string]string{"workspace": "docs"}}
		})
	}
	wide := newHarness(t, seed, 120, 35)
	wide.selectTab("Research")
	wide.selectRow("Notes")
	if line := wide.focused("[ ] Notes", "1Password"); strings.Contains(line, "input") {
		t.Fatalf("%q", line)
	}
	wide.lacks("Configuration required", "[?]")
	wide.press("enter")
	wide.contains("State: [ ] Off", "Space turns it on and links credential profile team.")

	compact := newHarness(t, seed, 80, 24)
	compact.selectTab("Research")
	compact.selectRow("Notes")
	compact.focused("[ ] Notes")
	compact.lacks("[?]")
	compact.contains("Space turns it on and links credential profile team.")
}

func TestSpaceOpensPickerWithSeveralProfiles(t *testing.T) {
	h := newHarness(t, withProfiles("other"), 120, 35)
	h.selectTab("Research")
	h.selectRow("Notes")
	h.press("space")
	h.typeText("docs")
	h.press("enter")
	if h.m.screen != screenProfile {
		t.Fatal(h.m.screen)
	}
	h.contains("Choose a credential profile for Notes.", "Notes needs: credential profile. Esc keeps it off.")
	h.press("down", "enter")
	if h.m.screen != screenList {
		t.Fatal(h.m.screen)
	}
	h.focused("[x] Notes")
	h.contains("Linked credential profile work. Notes is on.", "3 unsaved changes")
}

func TestSpaceWithoutProfilesShowsHint(t *testing.T) {
	seed := func(t *testing.T, store *config.Store) {
		seedFixture(t, store)
		mustUpdate(t, store, func(s *config.State) { delete(s.Local.CredentialProfiles, "work") })
	}
	h := newHarness(t, seed, 120, 35)
	h.selectTab("Research")
	h.selectRow("Notes")
	h.press("space")
	h.typeText("docs")
	h.press("enter")
	h.contains("No profiles yet. Create one: mcparcel config profile set <name> --file <profile.json>.", "Notes needs: credential profile.")
	h.press("enter", "esc")
	if h.m.screen != screenList || h.m.pendingEnable != "" {
		t.Fatal(h.m.screen, h.m.pendingEnable)
	}
	h.focused("[?] Notes")
	h.contains("Notes stays off.", "1 unsaved change")
	if h.m.draft.State().Selections.Connections[idNotes].Enabled {
		t.Fatal("enabled without a profile")
	}
}

// Esc ends a pending enable; a later edit of the same row never turns it on.
func TestEscClearsPendingEnable(t *testing.T) {
	h := newHarness(t, withProfiles("other"), 120, 35)
	h.selectTab("Research")
	h.selectRow("Notes")
	h.press("space", "esc")
	if h.m.screen != screenList || h.m.pendingEnable != "" || h.m.draft.Changes() != 0 {
		t.Fatal(h.m.screen, h.m.pendingEnable, h.m.draft.Changes())
	}
	h.focused("[?] Notes")
	h.contains("Notes stays off.")

	h.press("space")
	h.typeText("docs")
	h.press("enter", "esc")
	h.contains("Notes stays off.", "1 unsaved change")

	h.press("enter", "enter")
	h.press("ctrl+u")
	h.typeText("wiki")
	h.press("enter")
	h.press("down", "enter", "enter")
	if h.m.screen != screenDetails {
		t.Fatal(h.m.screen)
	}
	h.contains("Credential profile: other", "State: [ ] Off")
	if sel := h.m.draft.State().Selections.Connections[idNotes]; sel.Enabled || sel.Inputs["workspace"] != "wiki" {
		t.Fatal(sel)
	}
}

// Esc on a pending accept of a [!] row leaves it on and under review.
func TestEscKeepsReviewOnPendingAccept(t *testing.T) {
	seed := func(t *testing.T, store *config.Store) {
		withProfiles("other")(t, store)
		mustUpdate(t, store, func(s *config.State) {
			s.Selections.Connections[idNotes] = config.Selection{Enabled: true, ReviewRequired: true}
		})
	}
	h := newHarness(t, seed, 120, 35)
	h.selectTab("Research")
	h.selectRow("Notes")
	h.press("space")
	if h.m.screen != screenField {
		t.Fatal(h.m.screen)
	}
	h.contains("Notes needs: input workspace, credential profile. Esc leaves it unchanged.")
	h.lacks("Esc keeps it off.")
	h.press("esc")
	if h.m.screen != screenList || h.m.pendingEnable != "" || h.m.draft.Changes() != 0 {
		t.Fatal(h.m.screen, h.m.pendingEnable, h.m.draft.Changes())
	}
	h.focused("[!] Notes", "review")
	h.contains("Notes is unchanged.")
	if sel := h.m.draft.State().Selections.Connections[idNotes]; !sel.Enabled || !sel.ReviewRequired {
		t.Fatal(sel)
	}
}

func TestSharedDefinitionReadOnly(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.details("Design", "Paper")
	h.contains("Shared definition: read-only.")
	h.lacks("Edit personal connection", "e edit")
	h.press("e")
	if h.m.screen != screenDetails || h.m.form != nil {
		t.Fatal(h.m.screen)
	}
	h.press("esc")
	h.details("Other", "Excalidraw")
	h.contains("Edit personal connection", "e edit")
	h.lacks("Shared definition")
	h.press("e")
	if h.m.screen != screenForm {
		t.Fatal(h.m.screen)
	}
}
