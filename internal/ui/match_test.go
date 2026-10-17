package ui

import (
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

func TestHumanize(t *testing.T) {
	for id, want := range map[string]string{
		"design": "Design", "dev-tools": "Dev tools", "data_science": "Data science", "x": "X", "": "", "Ops": "Ops",
	} {
		if got := humanize(id); got != want {
			t.Errorf("humanize(%q) = %q, want %q", id, got, want)
		}
	}
}

// seedBareDomains adds personal domains labeled with just their IDs, as
// `local add` declares them from a definition's domains.
func seedBareDomains(t *testing.T, store *config.Store) {
	seedFixture(t, store)
	mustUpdate(t, store, func(s *config.State) {
		s.Personal.Domains = map[string]config.Domain{"dev-tools": {Label: "dev-tools"}, "zines": {Label: "zines"}}
		s.Personal.Connections["linter"] = config.Connection{Label: "Linter", Domains: []string{"dev-tools", "zines"}, Transport: stdio("linter-fixture")}
	})
}

// A domain without a label reads as its ID humanized in the tabs, the
// server page's title and the pane's From line, never as a raw ID beside
// the labeled ones.
func TestUnlabeledDomainsAreHumanized(t *testing.T) {
	h := newHarness(t, seedBareDomains, 120, 35)
	h.contains(" Dev tools 0/1 ", " Zines 0/1 ")
	if line := strings.Split(h.plain(), "\n")[1]; strings.Contains(line, "dev-tools") || strings.Contains(line, "zines") {
		t.Fatalf("raw domain ID in the tabs: %q", line)
	}
	h.selectTab("Dev tools")
	h.selectRow("Linter")
	h.contains("From  Personal - Dev tools, Zines")
	h.press("enter")
	h.contains("Dev tools / Linter")
}

func TestSnippet(t *testing.T) {
	text := "Up-to-date code examples and library documentation for any package, straight from the source."
	for _, c := range []struct {
		name, text, query string
		n                 int
		want              [3]string
	}{
		{"fits whole", "Up-to-date library documentation.", "doc", 40, [3]string{"Up-to-date library ", "doc", "umentation."}},
		{"cut at the end", text, "code", 30, [3]string{"Up-to-date ", "code", " examples an..."}},
		{"cut both sides, from a word", text, "doc", 30, [3]string{"...library ", "doc", "umentation fo..."}},
		{"case kept", text, "DOC", 30, [3]string{"...library ", "doc", "umentation fo..."}},
		{"match at the very end", text, "source.", 30, [3]string{"...straight from the ", "source.", ""}},
		{"query lowered to ASCII", "Kelvin scale", "\u212a", 20, [3]string{"", "K", "elvin scale"}},
		{"not found", "\"Caf\\u00e9 notes\"", "\u00e9", 10, [3]string{"\"Caf\\u0...", "", ""}},
	} {
		before, match, after := snippet(c.text, c.query, c.n)
		if got := [3]string{before, match, after}; got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
		if w := textWidth(before + match + after); w > c.n {
			t.Errorf("%s: %d cells, more than %d", c.name, w, c.n)
		}
	}
}

// A search that matched a row on its description or ID alone says so on
// the compact summary line and in the wide pane, the matched text
// emphasized with color and plain without; a label match says nothing.
func TestSearchSaysWhyARowMatched(t *testing.T) {
	compact := newHarness(t, seedFixture, 80, 24)
	compact.press("/")
	compact.typeText("device")
	compact.contains("Matches description: Connect to the Paper app on this device.")
	compact.press("enter")
	compact.contains("Matches description: Connect to the Paper app on this device.")
	compact.press("esc")
	if strings.Contains(compact.plain(), "Matches ") {
		t.Fatalf("a cleared search still says why:\n%s", compact.plain())
	}
	compact.press("/")
	compact.typeText("pap")
	if strings.Contains(compact.plain(), "Matches ") {
		t.Fatalf("a label match says why:\n%s", compact.plain())
	}

	narrow := newHarness(t, seedFixture, 40, 12)
	narrow.press("/")
	narrow.typeText("device")
	narrow.contains("\nMatches description: ...on this device.\n")

	// The pane says why only when its description does not show the match.
	pane := newHarness(t, seedFixture, 120, 35)
	pane.press("/")
	pane.typeText("device")
	if strings.Contains(pane.plain(), "Matches ") {
		t.Fatalf("the pane repeats the description it shows:\n%s", pane.plain())
	}

	// A match past the description's first line, which the pane leaves out.
	later := newHarness(t, seedLong, 120, 35)
	later.press("/")
	later.typeText("two")
	later.contains("Matches description: ...line two ")

	wide := newHarness(t, seedFixture, 120, 35, func(o *Options) { o.Color = true })
	wide.press("/")
	wide.typeText("local:")
	wide.contains("Matches ID: local:excalidraw")
	if !strings.Contains(wide.view(), wide.m.theme.paint(roleMatch, "local:")+"excalidraw") {
		t.Fatalf("the match is not emphasized:\n%q", wide.view())
	}
}
