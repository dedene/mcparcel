package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

// manyTools loads 40 tools whose descriptions are wider than any terminal,
// so every tool line wraps.
func manyTools(h *harness) {
	h.t.Helper()
	h.loads.items = nil
	for i := range 40 {
		h.loads.items = append(h.loads.items, fmt.Sprintf(`{"name":"tool_%02d","description":%q}`, i, strings.Repeat("a long tool description ", 10)))
	}
	h.details("Other", "Excalidraw")
	h.press("enter")
	if h.m.screen != screenTools {
		h.t.Fatalf("tools did not load:\n%s", h.plain())
	}
}

// A page keeps its focus line on screen and highlighted however many lines
// wrap before it: a wrapped line never takes the focus over.
func TestPageFocusSurvivesWrappedLines(t *testing.T) {
	for _, size := range [][2]int{{40, 10}, {60, 14}, {120, 24}, {160, 50}} {
		h := newHarness(t, seedFixture, size[0], size[1], fixedNow(), func(o *Options) { o.Color = true })
		manyTools(h)
		for i := range 25 {
			want := fmt.Sprintf("> [+] tool_%02d", i)
			h.focused(want)
			if !painted(h.view(), want, "7") {
				t.Fatalf("%dx%d: %q is not highlighted:\n%s", size[0], size[1], want, h.plain())
			}
			h.press("down")
		}
	}
	// The server page: the description wraps above the focused action on a
	// narrow page.
	for _, size := range [][2]int{{45, 10}, {45, 14}, {50, 11}, {60, 10}} {
		h := newHarness(t, seedFixture, size[0], size[1], func(o *Options) { o.Color = true })
		h.details("Research", "Notes")
		h.press("down")
		line := h.focused("Set workspace")
		if !painted(h.view(), "> Set workspace", "7") || strings.Count(h.view(), "\x1b[7m") != 1 {
			t.Fatalf("%dx%d: focus %q not the one highlighted line:\n%q", size[0], size[1], line, h.view())
		}
	}
}

// The end of the help is reachable at every size, and a wrapped key row
// keeps its description under the description column.
func TestHelpScrollsToTheEndWhenWrapped(t *testing.T) {
	for _, w := range []int{40, 45, 60, 80} {
		for _, ht := range []int{10, 24} {
			h := newHarness(t, seedFixture, w, ht)
			h.press("?")
			seen := map[string]bool{}
			for range 120 {
				for line := range strings.SplitSeq(h.plain(), "\n") {
					seen[line] = true
				}
				h.press("down")
			}
			if !seen["  starts on first use: nothing left to do"] && !seen["starts on first use: nothing left to do"] {
				t.Fatalf("%dx%d: the end of the help is unreachable:\n%s", w, ht, h.plain())
			}
			if w == 45 && (!seen["  sign in         s signs you in, in your"] || !seen[strings.Repeat(" ", helpKeyColumn)+"browser"]) {
				t.Fatalf("%dx%d: wrapped key row lost its column: %v", w, ht, seen)
			}
		}
	}
}

// A long input description never pushes the Value line off the field page.
func TestFieldPageKeepsValueLine(t *testing.T) {
	seed := func(t *testing.T, store *config.Store) {
		seedFixture(t, store)
		mustUpdate(t, store, func(s *config.State) {
			s.Personal.Connections["bulk"] = config.Connection{
				Label:     "Bulk",
				Inputs:    map[string]config.Input{"workspace": {Kind: "string", Description: strings.Repeat("The workspace name goes on. ", 12)}},
				Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal("bulk"), Args: []config.Value{{Input: &config.InputRef{Input: "workspace"}}}}},
			}
		})
	}
	for _, size := range [][2]int{{40, 10}, {60, 10}, {80, 10}} {
		h := newHarness(t, seed, size[0], size[1])
		h.details("Other", "Bulk")
		h.press("enter")
		if h.m.screen != screenField {
			t.Fatal(h.m.screen)
		}
		h.contains("Input workspace", "Value: _", "...")
		h.press("ctrl+u") // a status line from the screen before must not hide it either
		h.m.status = "A status long enough to wrap over two lines at the narrowest width setup draws."
		h.contains("Value: _")
	}
}

// The last key hint (the way out, or help) is kept when hints before it do
// not fit.
func TestKeyHintsKeepTheLastHint(t *testing.T) {
	details := newHarness(t, seedFixture, 80, 24)
	details.details("Other", "Excalidraw")
	details.contains("Left back")
	tools := newHarness(t, seedFixture, 60, 24, fixedNow())
	tools.details("Other", "Excalidraw")
	tools.press("enter")
	tools.contains("Up/Down tool   Space on/off   s set up   u undo   Esc back")
	compact := newHarness(t, seedFixture, 60, 20)
	compact.contains("? help")
	if line := compact.m.keys("a: one   b: two", 3); line != "a o" {
		t.Fatalf("%q", line)
	}
}

// The header counts several catalogs, and a count with nothing ready on is
// not painted green.
func TestHeaderSummarizesSeveralCatalogs(t *testing.T) {
	seed := func(t *testing.T, store *config.Store) {
		seedFixture(t, store)
		mustUpdate(t, store, func(s *config.State) {
			other := fixtureSource()
			other.ID, other.RepositoryID, other.Repo = "github-43", 43, "other-catalog"
			s.Local.Sources = append(s.Local.Sources, other)
			s.Catalogs[other.ID] = config.Catalog{
				SchemaVersion: 1,
				Domains:       map[string]config.Domain{"design": {Label: "Design"}},
				Connections:   map[string]config.Connection{"sketch": {Label: "Sketch", Description: "Sketches.", Domains: []string{"design"}, Transport: stdio("sketch-fixture")}},
			}
		})
	}
	h := newHarness(t, seed, 160, 35, fixedAges(120))
	h.contains("MCParcel  2 catalogs + personal")
	// The header no longer names one catalog: the pane says which, and the
	// server page's technical facts say when it synced.
	h.selectTab("Design")
	h.selectRow("Figma")
	h.paneContains("From acmeco/mcp-catalog - Design")
	h.press("enter", "i")
	h.contains("Source       acmeco/mcp-catalog, commit aaaaaaa, synced 2m ago")
	h.press("esc")
	none := newHarness(t, nil, 80, 24, func(o *Options) { o.Color = true })
	if !painted(none.view(), "0 on", "90") || painted(none.view(), "0 on", "32") {
		t.Fatalf("%q", none.view())
	}
}

// A page that does not fit says how many lines are cut.
func TestCutPageSaysHowMuchIsCut(t *testing.T) {
	h := finishHarness(t, seedFinish, 60, 20)
	h.turnOn("Calendar")
	h.details("Other", "Calendar")
	h.lacks("below.")
	h.press("i")
	h.contains("3 more lines below.")
	h.send(resize(60, 40))
	h.contains("Transport")
	h.lacks("below.")
}

// On a narrow page an action's note gives way before its label does.
func TestNarrowPageKeepsActionLabels(t *testing.T) {
	h := statusHarness(t)
	h.send(resize(40, 12))
	h.details("Other", "Calendar")
	h.focused("> Sign in to Calendar  opens your")
}
