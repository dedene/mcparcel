package ui

import (
	"errors"
	"regexp"
	"slices"
	"strings"
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
	h.press("esc", "esc", "n")
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
	h.focused("[-] local:gone", "unavailable")
	h.press("space")
	h.focused("[ ] local:gone", "unavailable")
	if sel := h.disk().Selections.Connections[idGone]; sel.Enabled {
		t.Fatal(sel)
	}
	h.lacks("The connection is not defined")
	h.press("space")
	h.focused("[ ] local:gone", "unavailable")
	h.contains("Personal no longer has this server.")

	details := newHarness(t, seed, 120, 35)
	details.selectTab("Other")
	details.selectRow("local:gone")
	details.press("enter")
	details.contains("[-] unavailable", "> Turn off")
	details.press("space")
	details.contains("[ ] off - unavailable")
	details.lacks("Turn off", "Turn on")
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
	h.press("n")
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

// A terminal too small ignores every key but q, and keeps the screen for
// when it grows again.
func TestTooSmallKeepsScreen(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.press("n")
	h.typeText("half")
	h.send(resize(30, 8))
	h.press("x", "esc", "enter")
	h.send(resize(120, 35))
	if h.m.screen != screenForm || h.m.form.get("id").value.String() != "half" {
		t.Fatalf("form lost:\n%s", h.plain())
	}
	h.send(resize(30, 8))
	h.press("q")
	if !h.quit || h.writes() != 0 {
		t.Fatal(h.quit, h.writes())
	}
}

func TestSearchShowsSearchKeys(t *testing.T) {
	for _, size := range [][2]int{{120, 35}, {80, 24}} {
		h := newHarness(t, seedFixture, size[0], size[1])
		h.press("/")
		h.contains("Enter/Down list   Backspace/Ctrl+U edit   Esc clear")
		h.lacks("Space on/off")
	}
}

func TestHelpListsEveryScreen(t *testing.T) {
	for _, section := range []string{"List", "Server page", "Marks", "Status"} {
		if !slices.ContainsFunc(helpText(asciiGlyphs), func(line string) bool { return line == section || strings.HasPrefix(line, section+" (") }) {
			t.Fatalf("help lacks a %s section", section)
		}
	}
}

// Each mark in the help legend means one thing: off is not also "happens
// later".
func TestHelpMarksAreDistinct(t *testing.T) {
	help := strings.Join(helpText(unicodeGlyphs), "\n")
	if !strings.Contains(help, "○ off") || !strings.Contains(help, "⋯ happens later") || strings.Count(help, "○") != 1 {
		t.Fatalf("%s", help)
	}
	if !strings.Contains(strings.Join(helpText(asciiGlyphs), "\n"), "[.] happens later") {
		t.Fatal("the ASCII legend lacks [.]")
	}
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
	h.contains("Change endpoint", "https://relay.example/...")
	h.do("Change endpoint")
	h.contains("Now https://relay.example/...; an empty Enter keeps it.")
	h.lacks("QUERY-MARKER")
}

// An enable whose input was cleared elsewhere asks for it on the fresh
// state instead of failing.
func TestEnableAsksForWhatWasClearedElsewhere(t *testing.T) {
	seed := func(t *testing.T, store *config.Store) {
		seedFixture(t, store)
		mustUpdate(t, store, func(s *config.State) {
			s.Selections.Connections[idNotes] = config.Selection{Inputs: map[string]string{"workspace": "docs"}, CredentialProfile: "work"}
		})
	}
	h := newHarness(t, seed, 120, 35)
	h.selectTab("Research")
	h.selectRow("Notes")
	mustUpdate(t, h.store, func(s *config.State) {
		sel := s.Selections.Connections[idNotes]
		sel.Inputs = nil
		s.Selections.Connections[idNotes] = sel
	})
	revision := h.disk().Selections.Revision
	h.press("space")
	if h.m.screen != screenField || h.disk().Selections.Revision != revision {
		t.Fatalf("%s", h.plain())
	}
	h.contains("Notes needs workspace. Esc keeps it off.")
	h.typeText("wiki")
	h.press("enter")
	if sel := h.disk().Selections.Connections[idNotes]; !sel.Enabled || sel.Inputs["workspace"] != "wiki" {
		t.Fatal(sel)
	}
}

func TestCtrlCDuringUnconfirmedWriteReportsIt(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.hold = true
	h.press("space", "ctrl+c")
	h.held, h.hold = nil, false
	h.send(writtenMsg{writeResult{err: errors.Join(config.ErrConfigWrite, errors.New("fsync"))}})
	if r := h.m.Result(); !h.quit || !r.Interrupted || !r.Unconfirmed {
		t.Fatal(h.quit, r)
	}
}

// A value as wide as its column keeps a gap before the next column instead of
// running into it ("zenjoy/prompt-engineering-clubRemote"): every cut ("...")
// in the list is followed by the gutter or ends the list.
func TestListColumnsKeepAGutter(t *testing.T) {
	seed := func(t *testing.T, store *config.Store) {
		seedFixture(t, store)
		mustUpdate(t, store, func(s *config.State) {
			s.Local.Sources[0].Owner, s.Local.Sources[0].Repo = "zenjoy", "prompt-engineering-club"
			s.Personal.Connections["excalidraw"] = config.Connection{Label: strings.Repeat("Long label ", 6), Transport: stdio("excalidraw-fixture")}
		})
	}
	runInto := regexp.MustCompile(`\.\.\.(\S| \S)`)
	check := func(h *harness, width int) {
		t.Helper()
		for _, tab := range []string{"Design", "Research", "Other"} {
			h.selectTab(tab)
			lines := strings.Split(h.plain(), "\n")
			for _, line := range lines[4 : 4+h.m.listHeight()] {
				if list := truncate(line, width, ""); runInto.MatchString(list) {
					t.Fatalf("%s: a cut runs into the next column: %q", tab, list)
				}
			}
		}
	}
	h := newHarness(t, seed, 120, 35)
	h.selectTab("Other")
	h.contains("Long label Long label", "...  ")
	check(h, listWidth(120))
	h.selectTab("Research")
	h.focused("needs workspace", "zenjoy/")

	compact := newHarness(t, seed, 80, 24)
	compact.selectTab("Other")
	compact.contains("Long label Long label", "...  ")
	check(compact, 80)
}

// With a single source the Source column is hidden and MCP and Status share
// its width.
func TestSourceColumnHiddenWithSingleSource(t *testing.T) {
	seed := func(t *testing.T, store *config.Store) {
		seedFixture(t, store)
		mustUpdate(t, store, func(s *config.State) {
			delete(s.Personal.Connections, "excalidraw")
			delete(s.Selections.Connections, idExcali)
			delete(s.Selections.Connections, idGone)
		})
	}
	columns := func(h *harness) string { return strings.Split(h.plain(), "\n")[3] }
	for _, size := range [][2]int{{120, 35}, {80, 24}} {
		h := newHarness(t, seed, size[0], size[1])
		h.selectTab("Design")
		if line := h.focused("Figma"); strings.Contains(line, "acmeco/mcp-catalog") || strings.Contains(columns(h), "Source") {
			t.Fatalf("%q\n%s", line, h.plain())
		}
	}
	h := newHarness(t, seedFixture, 120, 35)
	if line := h.focused("Figma", "acmeco/mcp-catalog"); !strings.Contains(columns(h), "Source") {
		t.Fatalf("%q\n%s", line, h.plain())
	}
	single := newHarness(t, seed, 120, 35)
	with, without := h.m.wideColumns(listWidth(120)), single.m.wideColumns(listWidth(120))
	if without.source != 0 || without.name < with.name || without.status <= with.status || without.name+without.status != with.name+with.status+with.source {
		t.Fatalf("with Source %+v, without %+v", with, without)
	}
	// Near the wide threshold MCP still shows ordinary labels whole.
	narrow := newHarness(t, seedFixture, 100, 30)
	narrow.selectTab("Other")
	narrow.focused("Excalidraw  ")
}
