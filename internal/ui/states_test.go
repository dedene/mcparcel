package ui

import (
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

func TestNoCatalogShowsAddHint(t *testing.T) {
	for _, size := range [][2]int{{120, 35}, {80, 24}} {
		h := newHarness(t, nil, size[0], size[1])
		h.contains("personal", "No catalogs yet. Add one from a terminal: mcparcel add <owner/repo>.", "Press A to add a personal connection.")
		h.press("A")
		if h.m.screen != screenForm {
			t.Fatal(h.m.screen)
		}
	}
}

func TestUnavailableRowKeepsSelection(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.selectTab("Other")
	h.selectRow("local:gone")
	h.contains("> local:gone", "[x]      Unavailable", "Personal no longer defines this connection. Your selection is kept for a future restoration.")
	h.press("enter")
	h.contains("Enabled: [x]   State: Unavailable", "Source: Personal")
	h.lacks("Connect and load tools", "Tools:")
	h.press("t", "l", "e")
	if h.m.screen != screenDetails || h.loads.calls != 0 {
		t.Fatal(h.m.screen, h.loads.calls)
	}
	h.press("space")
	h.contains("Enabled: [ ]   State: Unavailable")
	h.press("space")
	h.contains("Enabled: [ ]", "Personal no longer defines this connection. Your selection is kept for a future restoration.")
	if h.m.draft.Changes() != 1 {
		t.Fatal(h.m.draft.Changes())
	}
	h.press("esc", "ctrl+s")
	if sel, ok := h.disk().Selections.Connections[idGone]; !ok || sel.Enabled {
		t.Fatal("selection not kept", sel, ok)
	}
}

func TestReviewRequiredAcceptWithSpace(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.selectTab("Development")
	h.selectRow("Reviewer")
	h.contains("> Reviewer", "[!]      Review required")
	h.press("enter")
	h.contains("Transport: stdio: review-fixture (0 arguments)")
	h.containsWrapped("Review required. Space accepts it (same as mcparcel enable " + idReview + "). Run mcparcel sync to see what changed.")
	h.press("space")
	h.contains("Enabled: [x]   State: Ready", "1 unsaved change")
	h.press("space")
	h.contains("Enabled: [ ]   State: Ready")
	h.press("space", "esc", "ctrl+s")
	if sel := h.disk().Selections.Connections[idReview]; !sel.Enabled || sel.ReviewRequired {
		t.Fatal(sel)
	}
}

func TestOfflineShowsSnapshotAge(t *testing.T) {
	calls := 0
	h := newHarness(t, seedFixture, 120, 35, func(o *Options) {
		o.SourceAges = func(state config.State) ([]output.SourceMetadata, error) {
			calls++
			return []output.SourceMetadata{{Source: state.Local.Sources[0], CacheAgeSeconds: float64(3 * 86400)}}, nil
		}
	})
	h.contains("MCParcel / Setup", "1 catalog + personal")
	h.details("Design", "Figma")
	h.contains("snapshot 3d old", "Transport: HTTP: mcp.figma.example")
	h.press("space", "esc", "ctrl+s")
	if calls != 2 {
		t.Fatal("snapshot ages not recomputed after save", calls)
	}
	if h.loads.calls != 0 {
		t.Fatal(h.loads.calls)
	}
}

func TestConfigRequiredSeparateFromEnabled(t *testing.T) {
	seed := func(t *testing.T, store *config.Store) {
		seedFixture(t, store)
		mustUpdate(t, store, func(s *config.State) {
			s.Selections.Connections[idNotes] = config.Selection{Enabled: true}
		})
	}
	h := newHarness(t, seed, 120, 35)
	h.selectTab("Research")
	h.contains("> Notes", "[x]      Configuration required", "Configuration required: input workspace, credential profile.")
	h.press("space")
	h.contains("[ ]      Configuration required")
	compact := newHarness(t, seedFixture, 80, 24)
	compact.selectTab("Research")
	compact.contains("> Notes (config)", "[ ]")
}
