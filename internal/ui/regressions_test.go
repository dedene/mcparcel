package ui

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

func TestFieldRenderKeepsQuotes(t *testing.T) {
	for _, tc := range []struct{ value, want string }{
		{`"`, `"_`},
		{`"abc`, `"abc_`},
		{`"abc"`, `"abc"_`},
		{"caf\u00e9", `caf\u00e9_`},
	} {
		var f field
		f.insert(tc.value)
		if got := f.render(80); got != tc.want {
			t.Fatalf("render(%q) = %q, want %q", tc.value, got, tc.want)
		}
	}
}

func TestTypingAQuoteNeverCrashes(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.press("/")
	h.typeText(`"`)
	h.contains(`Search: "_`, `No MCPs match "\"" in Design.`)
	h.press("esc")
	h.details("Research", "Notes")
	h.press("enter")
	h.typeText(`"`)
	h.contains(`Value: "_`)
	h.press("esc", "esc", "a")
	h.typeText(`"`)
	h.contains(`> ID:          "_`)
}

func TestUnavailableReviewRowTurnsOff(t *testing.T) {
	seed := func(t *testing.T, store *config.Store) {
		seedFixture(t, store)
		mustUpdate(t, store, func(s *config.State) {
			s.Selections.Connections[idGone] = config.Selection{Enabled: true, ReviewRequired: true}
		})
	}
	h := newHarness(t, seed, 120, 35)
	h.selectTab("Other")
	h.selectRow("local:gone")
	h.contains("> local:gone", "[!]      Unavailable")
	h.press("space")
	h.contains("[ ]      Unavailable", "1 unsaved change")
	h.lacks("The connection is not defined")
	h.press("space")
	h.contains("[ ]      Unavailable", "Personal no longer defines this connection.")

	details := newHarness(t, seed, 120, 35)
	details.selectTab("Other")
	details.selectRow("local:gone")
	details.press("enter")
	details.contains("Enabled: [!]   State: Unavailable")
	details.press("space")
	details.contains("Enabled: [ ]   State: Unavailable", "1 unsaved change")
}

func TestAddWithNewDomainKeepsTab(t *testing.T) {
	seed := func(t *testing.T, store *config.Store) {
		seedFixture(t, store)
		mustUpdate(t, store, func(s *config.State) {
			s.Personal.Domains = map[string]config.Domain{"work": {Label: "work"}}
			s.Personal.Connections["desk"] = config.Connection{Label: "Desk", Domains: []string{"work"}, Transport: stdio("desk-fixture")}
		})
	}
	h := newHarness(t, seed, 120, 35)
	h.selectTab("Other")
	h.selectRow("local:gone")
	h.press("a")
	h.typeText("dev-tool")
	h.formTo("domains")
	h.typeText("dev")
	h.formTo("command")
	h.typeText("dev-fixture")
	h.press("enter")
	if h.m.currentTab().id != "other" {
		t.Fatalf("tab moved to %q", h.m.currentTab().id)
	}
	h.contains("[Other 2/2]", "Added ")
}

func TestTooSmallDiscardNoKeepsScreen(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.press("space", "a")
	h.typeText("half")
	h.send(resize(30, 8))
	h.press("q")
	h.contains("Discard 1 unsaved change? y/N")
	h.press("n")
	h.send(resize(120, 35))
	if h.m.screen != screenForm || h.m.form.get("id").value.String() != "half" {
		t.Fatalf("form lost:\n%s", h.plain())
	}
	h.press("esc", "down", "space")
	mustUpdate(t, h.store, func(s *config.State) { s.Local.Aliases = map[string]string{"p": idPaper} })
	h.press("ctrl+s")
	h.send(resize(30, 8))
	h.press("q", "esc")
	h.send(resize(120, 35))
	if h.m.screen != screenConflict {
		t.Fatalf("conflict lost:\n%s", h.plain())
	}
}

func TestReloadAfterUnconfirmedSaveCountsLandedSave(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.selectRow("Figma")
	h.press("space")
	// The write landed, but its durable save could not be confirmed.
	landed := mustUpdate(t, h.store, func(s *config.State) {
		sel := s.Selections.Connections[idFigma]
		sel.Enabled = true
		s.Selections.Connections[idFigma] = sel
	})
	h.send(savedMsg{err: errors.Join(config.ErrConfigWrite, errors.New("fsync"))})
	h.contains("Press r to reload.")
	if r := h.m.Result(); !r.Unconfirmed || r.Saves != 0 {
		t.Fatal(r)
	}
	h.press("r")
	h.contains(fmt.Sprintf("Reloaded at revision %d. Reapplied 0 changes. Already in the configuration: enable Figma.", landed.Selections.Revision))
	h.lacks("Dropped")
	if r := h.m.Result(); r.Saves != 1 || r.Unconfirmed || r.Revision != landed.Selections.Revision {
		t.Fatal(r)
	}
}

func TestUnconfirmedSaveThatDidNotLandIsReapplied(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.press("space")
	h.send(savedMsg{err: errors.Join(config.ErrConfigWrite, errors.New("fsync"))})
	h.press("r")
	h.contains("Reapplied 1 change.")
	if r := h.m.Result(); r.Saves != 0 || r.Unconfirmed {
		t.Fatal(r)
	}
}

func TestSearchShowsSearchKeys(t *testing.T) {
	for _, size := range [][2]int{{120, 35}, {80, 24}} {
		h := newHarness(t, seedFixture, size[0], size[1])
		h.press("/")
		h.contains("Enter/Down: list   Esc: clear")
		h.lacks("Space: toggle")
	}
}

func TestHelpListsEveryScreen(t *testing.T) {
	for _, section := range []string{"List", "Search", "Details", "Tools", "Forms and fields", "Conflict", "Confirm"} {
		if !slices.Contains(helpText, section) {
			t.Fatalf("help lacks a %s section", section)
		}
	}
}

func TestPersonalDetailsShowLocalRemoveHint(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.details("Other", "Excalidraw")
	h.containsWrapped("Remove it with mcparcel local remove " + idExcali + ".")
	h.press("esc")
	h.details("Design", "Paper")
	h.lacks("local remove")
}

func TestStringInputInURLShowsHostOnly(t *testing.T) {
	seed := func(t *testing.T, store *config.Store) {
		mustUpdate(t, store, func(s *config.State) {
			s.Personal.Connections["relay"] = config.Connection{
				Label:     "Relay",
				Inputs:    map[string]config.Input{"endpoint": {Kind: "string", Description: "Endpoint"}},
				Transport: config.Transport{HTTP: &config.HTTP{URL: config.Value{Input: &config.InputRef{Input: "endpoint"}}}},
			}
			s.Selections.Connections["local:relay"] = config.Selection{Inputs: map[string]string{"endpoint": "https://relay.example/s/QUERY-MARKER"}}
		})
	}
	h := newHarness(t, seed, 120, 35)
	h.details("Other", "Relay")
	h.contains("Input endpoint (string): https://relay.example/...")
	h.press("enter")
	h.contains("Current: https://relay.example/...", "Value: _")
	h.lacks("QUERY-MARKER")
}

func TestReloadReportsChangesThatNoLongerApply(t *testing.T) {
	seed := func(t *testing.T, store *config.Store) {
		seedFixture(t, store)
		mustUpdate(t, store, func(s *config.State) {
			s.Selections.Connections[idNotes] = config.Selection{Inputs: map[string]string{"workspace": "docs"}, CredentialProfile: "work"}
		})
	}
	h := newHarness(t, seed, 120, 35)
	h.selectTab("Research")
	h.selectRow("Notes")
	h.press("space")
	mustUpdate(t, h.store, func(s *config.State) {
		sel := s.Selections.Connections[idNotes]
		sel.Inputs = nil
		s.Selections.Connections[idNotes] = sel
	})
	h.press("ctrl+s", "r")
	h.contains("Reapplied 0 changes. Dropped (no longer applies): enable Notes.")
}

func TestCtrlCDuringUnconfirmedSaveReportsIt(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.press("space")
	h.hold = true
	h.press("ctrl+s", "ctrl+c")
	h.held, h.hold = nil, false
	h.send(savedMsg{err: errors.Join(config.ErrConfigWrite, errors.New("fsync"))})
	if r := h.m.Result(); !h.quit || !r.Interrupted || !r.Unconfirmed || r.Saves != 0 {
		t.Fatal(h.quit, r)
	}
}
