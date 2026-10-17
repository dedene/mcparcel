package ui

import (
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

func TestNoCatalogShowsAddHint(t *testing.T) {
	for _, size := range [][2]int{{120, 35}, {80, 24}} {
		h := newHarness(t, nil, size[0], size[1])
		h.contains("personal", "No catalogs yet. Add one from a terminal: mcparcel add <owner/repo>.", "Press n to add a personal connection.")
		h.press("n")
		if h.m.screen != screenForm {
			t.Fatal(h.m.screen)
		}
	}
}

func TestUnavailableRowKeepsSelection(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.selectTab("Other")
	h.selectRow("local:gone")
	h.focused("[-] local:gone", "unavailable")
	h.paneContains("[-] unavailable", "From Personal", "Personal no longer has this server. Setup keeps your choice for it in case it comes back.")
	h.press("enter")
	h.contains("Other / local:gone", "[-] unavailable", "> Turn off", "From  Personal")
	h.lacks("Choose tools", "Edit", "Remove", "To do")
	h.press("down", "enter")
	if h.m.screen != screenDetails || h.loads.calls != 0 {
		t.Fatal(h.m.screen, h.loads.calls)
	}
	h.contains("[ ] off - unavailable")
	h.press("space")
	h.contains("[ ] off - unavailable", "Personal no longer has this server. Setup keeps your choice for it in case it comes back.")
	if h.writes() != 1 {
		t.Fatal(h.writes())
	}
	if sel, ok := h.disk().Selections.Connections[idGone]; !ok || sel.Enabled {
		t.Fatal("selection not kept", sel, ok)
	}
}

func TestReviewRequiredAcceptWithSpace(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.selectTab("Development")
	h.selectRow("Reviewer")
	h.focused("[~] Reviewer", "review change")
	h.paneContains("[~] needs a review [.] Tools load on first use")
	h.paneLacks("Accept the change", "[+] On") // the state line says it
	h.press("enter")
	h.contains("[~] needs a review", "> Accept the change", "mcparcel sync shows what changed")
	h.press("space")
	h.contains("[x] ready")
	h.press("space")
	h.contains("[ ] off")
	h.press("space")
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
	h.contains("MCParcel  acmeco/mcp-catalog - aaaaaaa - synced 3d ago", "3 on - 2 need a step")
	h.details("Design", "Figma")
	h.press("i")
	h.contains("synced 3d ago", "Transport    HTTP: mcp.figma.example")
	h.press("space")
	if calls != 2 {
		t.Fatal("snapshot ages not recomputed after a change", calls)
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
	h.focused("[~] Notes", "needs workspace")
	h.contains("4 on - 3 need a step")
	h.paneContains("[~] needs workspace [*] Set workspace [*] Link a 1Password profile")
	h.paneLacks("[+] On")
	h.press("space")
	h.focused("[ ] Notes", "needs workspace")
	h.paneContains("[ ] off - needs workspace [*] Set workspace")
	h.paneLacks("Turn it on")
	compact := newHarness(t, seedFixture, 80, 24)
	compact.selectTab("Research")
	compact.focused("[ ] Notes", "needs workspace")
}
