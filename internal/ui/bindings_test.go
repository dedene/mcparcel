package ui

import (
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

// One Space asks for each missing item, then turns the row on: the input,
// then the only local profile, linked without asking.
func TestSpaceOnConfigRequiredFillsThenEnables(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.selectTab("Research")
	h.selectRow("Notes")
	h.press("space")
	if h.m.screen != screenField || h.writes() != 0 {
		t.Fatal(h.m.screen, h.writes())
	}
	h.contains("Input workspace", "Notes needs workspace and a 1Password profile. Esc keeps it off.")
	h.typeText("docs")
	h.press("enter")
	if h.m.screen != screenList || h.m.pendingEnable != "" {
		t.Fatal(h.m.screen, h.m.pendingEnable)
	}
	h.focused("[x] Notes")
	h.contains("Linked 1Password profile work. Notes is on.")
	h.lacks("Fixture account")
	if h.writes() != 1 {
		t.Fatal("the enable was not one change:", h.writes())
	}
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
	h.paneContains("[*] Set workspace", "[+] 1Password: links team when on")
	h.lacks("credential profile", "Link a 1Password profile")
	h.press("space")
	h.contains("Notes needs workspace. Esc keeps it off.")
	h.typeText("docs")
	h.press("enter")
	h.focused("[x] Notes")
	h.contains("Linked 1Password profile team. Notes is on.")
	if sel := h.disk().Selections.Connections[idNotes]; !sel.Enabled || sel.CredentialProfile != "team" {
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
	direct.paneContains("[+] 1Password: links team when on", "[+] workspace is set")
	direct.press("space")
	if direct.m.screen != screenList {
		t.Fatal(direct.m.screen)
	}
	direct.focused("[x] Notes")
	direct.contains("Linked 1Password profile team.")
	if direct.writes() != 1 {
		t.Fatal(direct.writes())
	}
}

// A row that is off and misses only the profile Space links by itself
// reads ready (off, nothing missing) in the wide list, the compact list
// and on its page.
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
	if line := wide.focused("[ ] Notes"); strings.Contains(line, "needs") || strings.Contains(line, "link") {
		t.Fatalf("%q", line)
	}
	wide.paneContains("[+] 1Password: links team when on")
	wide.press("enter")
	wide.contains("[ ] off", "> Turn on", "[+] 1Password: links team when on", "Link 1Password profile", "team links when on")
	wide.lacks("asks for what it misses", "not linked")

	compact := newHarness(t, seed, 80, 24)
	compact.selectTab("Research")
	compact.selectRow("Notes")
	compact.focused("[ ] Notes")
	compact.contains("Space turns it on and links 1Password profile team.")
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
	h.contains("Choose a 1Password profile for Notes.", "Notes needs a 1Password profile. Esc keeps it off.")
	h.press("down", "enter")
	if h.m.screen != screenList {
		t.Fatal(h.m.screen)
	}
	h.focused("[x] Notes")
	h.contains("Linked 1Password profile work. Notes is on.")
	if sel := h.disk().Selections.Connections[idNotes]; !sel.Enabled || sel.CredentialProfile != "work" || h.writes() != 1 {
		t.Fatal(sel, h.writes())
	}
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
	h.contains("No profiles yet. Create one: mcparcel config profile set <name> --file <profile.json>.", "Notes needs a 1Password profile.")
	h.press("enter", "esc")
	if h.m.screen != screenList || h.m.pendingEnable != "" {
		t.Fatal(h.m.screen, h.m.pendingEnable)
	}
	h.focused("[ ] Notes")
	h.contains("Notes stays off.")
	if h.writes() != 0 {
		t.Fatal("a canceled enable wrote what it filled in")
	}
}

// Esc ends a pending enable; a later edit of the same row never turns it on.
func TestEscClearsPendingEnable(t *testing.T) {
	h := newHarness(t, withProfiles("other"), 120, 35)
	h.selectTab("Research")
	h.selectRow("Notes")
	h.press("space", "esc")
	if h.m.screen != screenList || h.m.pendingEnable != "" || h.writes() != 0 {
		t.Fatal(h.m.screen, h.m.pendingEnable, h.writes())
	}
	h.focused("[ ] Notes")
	h.contains("Notes stays off.")

	h.press("space")
	h.typeText("docs")
	h.press("enter", "esc")
	h.contains("Notes stays off.")
	if h.writes() != 0 {
		t.Fatal("a canceled enable wrote what it filled in")
	}

	h.press("enter")
	h.do("Set workspace")
	h.typeText("wiki")
	h.press("enter")
	h.do("Link 1Password profile")
	h.press("enter")
	if h.m.screen != screenDetails {
		t.Fatal(h.m.screen)
	}
	h.contains("[+] 1Password: other", "[ ] off")
	if sel := h.disk().Selections.Connections[idNotes]; sel.Enabled || sel.Inputs["workspace"] != "wiki" {
		t.Fatal(sel)
	}
}

// Esc on a pending accept of a row under review leaves it on and under review.
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
	h.contains("Notes needs workspace and a 1Password profile. Esc leaves it unchanged.")
	h.lacks("Esc keeps it off.")
	h.press("esc")
	if h.m.screen != screenList || h.m.pendingEnable != "" || h.writes() != 0 {
		t.Fatal(h.m.screen, h.m.pendingEnable, h.writes())
	}
	h.focused("[~] Notes", "review")
	h.contains("Notes is unchanged.")
	if sel := h.m.live.State().Selections.Connections[idNotes]; !sel.Enabled || !sel.ReviewRequired {
		t.Fatal(sel)
	}
}
