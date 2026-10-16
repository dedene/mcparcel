package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

// seedDomains adds twelve long-named domains, more than fit one line at 160
// columns, each with one personal connection.
func seedDomains(t *testing.T, store *config.Store) {
	seedFixture(t, store)
	mustUpdate(t, store, func(s *config.State) {
		s.Personal.Domains = map[string]config.Domain{}
		for i := range 12 {
			id := fmt.Sprintf("area%02d", i)
			s.Personal.Domains[id] = config.Domain{Label: fmt.Sprintf("Area %02d Collaboration", i)}
			s.Personal.Connections[id] = config.Connection{Label: fmt.Sprintf("Tool %02d", i), Domains: []string{id}, Transport: stdio("fixture")}
		}
	})
}

// The tab bar scrolls so the current tab is always whole on screen, with a
// mark on each side that hides tabs.
func TestTabScrollKeepsActiveVisible(t *testing.T) {
	for _, unicode := range []bool{false, true} {
		for _, w := range []int{40, 80, 120, 160} {
			h := newHarness(t, seedDomains, w, 35, func(o *Options) { o.Color, o.Unicode = true, unicode })
			all := tabs(h.m.live.Effective())
			prev, next := h.m.theme.glyph.prev, h.m.theme.glyph.next
			for range len(all) + 1 { // once around, back to the first
				current := h.m.currentTab()
				line := strings.Split(h.plain(), "\n")[1]
				if !strings.Contains(line, "["+current.label+" ") || !strings.Contains(line, "]") || textWidth(line) > w {
					t.Fatalf("%d cols: current %q not whole in %q", w, current.label, line)
				}
				if first := strings.Contains(line, all[0].label+" "); first == strings.HasPrefix(line, prev) {
					t.Fatalf("%d cols: first tab shown=%v but line %q", w, first, line)
				}
				if last := strings.Contains(line, "Other "); last == strings.HasSuffix(line, next) {
					t.Fatalf("%d cols: last tab shown=%v but line %q", w, last, line)
				}
				// Each mark sits one space from the tab beside it, pill or not.
				if strings.Contains(line, prev+"  ") || strings.Contains(line, "  "+next) {
					t.Fatalf("%d cols: uneven mark spacing in %q", w, line)
				}
				h.press("tab")
			}
		}
	}
}

func TestTabsArePills(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35, func(o *Options) { o.Color = true })
	if !painted(h.view(), "[Design 0/2]", "7") || !painted(h.view(), "1/2", "90") {
		t.Fatalf("%q", h.view())
	}
}

// Typing a query with no match in the current tab moves to the first tab,
// in tab order, that has one; otherwise the tab stays.
func TestSearchJumpsToFirstTabWithMatches(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.press("/")
	h.typeText("notes")
	if h.m.currentTab().label != "Research" {
		t.Fatal(h.m.currentTab())
	}
	h.contains("[Research (1)]")
	h.focused("Notes")
	h.press("enter")
	if h.m.currentTab().label != "Research" || h.m.screen != screenList {
		t.Fatal(h.m.currentTab(), h.m.screen)
	}

	// Paper is in Design and Development: the first of them wins, from any tab.
	h.press("esc")
	h.selectTab("Research")
	h.press("/")
	h.paste("paper")
	if h.m.currentTab().label != "Design" {
		t.Fatal(h.m.currentTab())
	}
	h.press("esc")
	h.selectTab("Development")
	h.press("/")
	h.typeText("paper")
	if h.m.currentTab().label != "Development" {
		t.Fatal("left a tab that has matches:", h.m.currentTab())
	}

	// Nothing matches anywhere: the tab stays and says so.
	h.press("ctrl+u")
	h.typeText("zzz")
	if h.m.currentTab().label != "Development" {
		t.Fatal(h.m.currentTab())
	}
	h.contains(`No MCPs match "zzz" in Development.`)
}

// The details pane shows beside the list from 100x30; below that the list
// keeps its summary line.
func TestDetailsPaneThreshold(t *testing.T) {
	for _, tc := range []struct {
		w, h int
		pane bool
	}{{100, 30, true}, {120, 35, true}, {160, 50, true}, {99, 30, false}, {100, 29, false}, {80, 24, false}, {40, 10, false}} {
		h := newHarness(t, seedFixture, tc.w, tc.h, fixedAges(7200))
		checkFits(t, h, tc.w, tc.h, "pane")
		lines := strings.Split(h.plain(), "\n")
		hasPane := strings.Contains(lines[3], "| Figma") && strings.Contains(lines[4], "| Remote design files.")
		if hasPane != tc.pane {
			t.Fatalf("%dx%d: pane %v:\n%s", tc.w, tc.h, hasPane, h.plain())
		}
		if !tc.pane {
			h.contains("Off. Space turns it on.") // the summary line
			continue
		}
		// The header shows the only catalog's commit and age; the pane does not
		// repeat them, nor the summary line its To do already says.
		h.paneContains("[ ] off", "[*] Turn it on", "Runs remote - mcp.figma.example", "From acmeco/mcp-catalog - Design")
		if pane := h.paneText(); strings.Contains(pane, "aaaaaaa") || strings.Contains(pane, "Space turns it on") {
			t.Fatalf("%dx%d: %q", tc.w, tc.h, pane)
		}
		h.lacks("QUERY-MARKER", "private/path")
		h.press("down")
		h.paneContains("Paper", "Runs this device - paper-fixture", "From acmeco/mcp-catalog - Design")
		h.selectTab("Research")
		h.paneContains("[ ] off - needs workspace", "[*] Set workspace [*] Link a 1Password profile")
	}
	// The pane never shows a profile's account or a secret, whatever row it follows.
	h := newHarness(t, seedSecrets, 120, 35, fixedAges(60))
	for _, tb := range tabs(h.m.live.Effective()) {
		h.selectTab(tb.label)
		for range h.m.rows() {
			for _, marker := range secretMarkers {
				if strings.Contains(h.view(), marker) {
					t.Fatalf("%s shows %q", tb.label, marker)
				}
			}
			h.press("down")
		}
	}
}

// The header names the only catalog and counts rows that are on and those
// of them that need a step.
func TestHeaderCountsOnAndAttention(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35, fixedAges(120), func(o *Options) { o.Color = true })
	h.contains("MCParcel  acmeco/mcp-catalog - aaaaaaa - synced 2m ago", "3 on - 2 need a step")
	if !painted(h.view(), "MCParcel", "1") || !painted(h.view(), "3 on", "32") || !painted(h.view(), "2 need a step", "33") {
		t.Fatalf("%q", h.view())
	}
	h.selectTab("Development")
	h.selectRow("Reviewer")
	h.press("space") // accepts the review
	h.contains("3 on - 1 needs a step")

	calm := func(t *testing.T, store *config.Store) {
		seedFixture(t, store)
		mustUpdate(t, store, func(s *config.State) {
			delete(s.Selections.Connections, idGone)
			delete(s.Selections.Connections, idReview)
		})
	}
	ok := newHarness(t, calm, 80, 24)
	ok.contains("1 on - all ready")
	ok.lacks("need")

	none := newHarness(t, nil, 40, 10)
	none.contains("MCParcel  personal", "0 on")
}

// Help lines are key chips with dim labels in color; without color they
// read "key label".
func TestKeyHintsAreChips(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35, func(o *Options) { o.Color = true })
	if !painted(h.view(), "Space", "100") || !painted(h.view(), "on/off", "90") {
		t.Fatalf("%q", h.view())
	}
	plain := newHarness(t, seedFixture, 120, 35)
	plain.contains("Space on/off   Tab domain")
	// The last hint is kept; the ones before it that do not fit are dropped whole.
	narrow := newHarness(t, seedFixture, 40, 12)
	narrow.contains("2 need a step. Next: Reviewer...  ? help")
	narrow.lacks("Tab dom...", "/ sea...")
	calm := newHarness(t, nil, 40, 12)
	calm.contains("Space on/off   Enter open   ? help")
}
