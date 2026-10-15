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
	h.press("l")
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
			want := fmt.Sprintf("> [x] tool_%02d", i)
			h.focused(want)
			if !painted(h.view(), want, "7") {
				t.Fatalf("%dx%d: %q is not highlighted:\n%s", size[0], size[1], want, h.plain())
			}
			h.press("down")
		}
	}
	// Details: lines wrap above the focused field on a narrow page.
	for _, size := range [][2]int{{45, 10}, {45, 14}, {50, 11}, {60, 10}} {
		h := newHarness(t, seedFixture, size[0], size[1], func(o *Options) { o.Color = true })
		h.details("Research", "Notes")
		line := h.focused("Input workspace")
		if !painted(h.view(), "> Input workspace", "7") || strings.Count(h.view(), "\x1b[7m") != 1 {
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
			if !seen["Every change has a command: run mcparcel --help."] && !seen["run mcparcel --help."] && !seen["--help."] {
				t.Fatalf("%dx%d: the end of the help is unreachable:\n%s", w, ht, h.plain())
			}
			if w == 45 && (!seen["  1Password       a credential profile to"] || !seen[strings.Repeat(" ", helpKeyColumn)+"link"]) {
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
		h.contains("Input workspace (string)", "Value: _", "...")
		h.press("ctrl+u") // a status line from the screen before must not hide it either
		h.m.status = "A status long enough to wrap over two lines at the narrowest width setup draws."
		h.contains("Value: _")
	}
}

// A need that only partly fits is either cut with "..." to a readable width
// or left out; never a one-to-three letter fragment.
func TestNeedsColumnNeverShowsFragments(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	needs := []need{{text: "input other"}, {text: "input workspace"}, {text: "npx"}}
	for n := 12; n <= 40; n++ {
		got := strings.TrimRight(h.m.needsText(needs, n, false), " ")
		if textWidth(got) > n-gutter {
			t.Fatalf("%d: %q overflows", n, got)
		}
		for _, part := range strings.Split(got, "  ") {
			if part != "" && part != "npx" && textWidth(part) < 6 {
				t.Fatalf("%d: fragment %q in %q", n, part, got)
			}
		}
	}
}

// The last key hint (the way out, or help) is kept when hints before it do
// not fit.
func TestKeyHintsKeepTheLastHint(t *testing.T) {
	details := newHarness(t, seedFixture, 80, 24)
	details.details("Other", "Excalidraw")
	details.contains("Esc back")
	tools := newHarness(t, seedFixture, 60, 24, fixedNow())
	tools.details("Other", "Excalidraw")
	tools.press("t")
	tools.contains("Up/Down tool   Space on/off   Esc back")
	compact := newHarness(t, seedFixture, 60, 20)
	compact.contains("? help")
	if line := compact.m.keys("a: one   b: two", 3); line != "a o" {
		t.Fatalf("%q", line)
	}
}

// On the compact list a save conflict keeps its whole message over the
// summary and status lines, and the help line shows only the keys it takes.
func TestCompactConflictWrapsAndShowsItsKeys(t *testing.T) {
	for _, w := range []int{40, 60, 80} {
		h := newHarness(t, seedFixture, w, 24)
		h.press("space")
		mustUpdate(t, h.store, func(s *config.State) { s.Local.Aliases = map[string]string{"x": idPaper} })
		h.press("ctrl+s")
		if h.m.screen != screenConflict {
			t.Fatalf("%d: no conflict:\n%s", w, h.plain())
		}
		h.containsWrapped("Your 1 change is kept.")
		h.contains("r reload and reapply   Esc back")
		h.lacks("Space on/off", "Enter details")
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
	// The header no longer names one catalog, so the pane says which, and when.
	h.selectTab("Design")
	h.selectRow("Figma")
	h.paneContains("Source acmeco/mcp-catalog - aaaaaaa - synced 2m ago")
	none := newHarness(t, nil, 80, 24, func(o *Options) { o.Color = true })
	if !painted(none.view(), "0 on", "90") || painted(none.view(), "0 on", "32") {
		t.Fatalf("%q", none.view())
	}
}
