package ui

import (
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

func TestListGolden120x35(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	golden(t, "list_120x35", h.view())
}

func TestGolden80x24Compact(t *testing.T) {
	h := newHarness(t, seedFixture, 80, 24)
	if !h.m.compact() {
		t.Fatal("80x24 is not compact")
	}
	golden(t, "list_80x24", h.view())
}

func TestGolden120x35Details(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35, fixedAges(7200))
	h.details("Research", "Notes")
	golden(t, "details_120x35", h.view())
}

// checkFits asserts the view fits w x h display cells and holds no control
// character or escape other than SGR styling.
func checkFits(t *testing.T, h *harness, w, ht int, state string) {
	t.Helper()
	view := h.plain()
	lines := strings.Split(view, "\n")
	if len(lines) > ht {
		t.Fatalf("%dx%d %s: %d lines", w, ht, state, len(lines))
	}
	for i, line := range lines {
		if n := textWidth(line); n > w {
			t.Fatalf("%dx%d %s: line %d is %d wide: %q", w, ht, state, i, n, line)
		}
		for _, r := range line {
			if r < 32 || r == 127 || r >= 0x80 && r <= 0x9f {
				t.Fatalf("%dx%d %s: line %d has %q", w, ht, state, i, r)
			}
		}
	}
}

// seedLong adds a personal connection with an overlong label and a
// multi-line, non-ASCII description.
func seedLong(t *testing.T, store *config.Store) {
	seedSecrets(t, store)
	mustUpdate(t, store, func(s *config.State) {
		s.Personal.Connections["long"] = config.Connection{
			Label:       strings.Repeat("Very long label ", 12),
			Description: "Line one " + strings.Repeat("wide ", 40) + "\nline two é世",
			Transport:   stdio("fixture"),
		}
	})
}

func TestEveryLineFitsWidthAndHeight(t *testing.T) {
	for _, w := range []int{100, 120, 160} {
		for _, ht := range []int{30, 35, 50} {
			h := newHarness(t, seedLong, w, ht, func(o *Options) { o.Color, o.Unicode = true, true })
			h.selectTab("Other")
			h.selectRow(strings.Repeat("Very long label ", 12))
			checkFits(t, h, w, ht, "list")
			h.press("/")
			h.typeText(strings.Repeat("x", 200))
			checkFits(t, h, w, ht, "search")
			h.press("esc", "space", "u")
			checkFits(t, h, w, ht, "undone")
			h.press("tab")
			checkFits(t, h, w, ht, "next domain")
		}
	}
}

// tour visits every screen once, calling visit on each.
func tour(h *harness, visit func(name string)) {
	h.t.Helper()
	h.loads.items = append(fixtureTools, `{"name":"`+strings.Repeat("n", 90)+`","description":"`+strings.Repeat("d ", 90)+`é"}`)
	h.selectTab("Research")
	visit("list")
	h.press("/")
	h.typeText("no")
	visit("search")
	h.press("esc", "?")
	visit("help")
	h.press("down", "down", "down")
	visit("help scrolled")
	h.press("esc")
	h.selectRow("Notes")
	h.press("enter")
	visit("details")
	h.press("i")
	visit("details technical")
	h.press("i")
	h.do("Set workspace")
	h.typeText(strings.Repeat("w", 200))
	visit("inline field")
	h.press("esc")
	h.do("Choose tools")
	visit("tools of a server that is off")
	h.press("esc")
	h.do("Turn on")
	visit("enable asks")
	h.press("esc", "esc", "space")
	visit("enable asks from the list")
	h.press("esc")
	h.selectTab("Other")
	h.selectRow(strings.Repeat("Very long label ", 12))
	h.press("enter")
	visit("long details")
	h.press("esc")
	h.selectRow("Excalidraw")
	h.press("enter", "enter")
	visit("tools loaded")
	h.press("esc")
	h.do("Edit")
	visit("edit form")
	h.press("esc", "esc", "n")
	visit("add form")
	h.press("enter")
	visit("add form error")
	h.press("esc")
	h.selectRow("Spare")
	h.press("space", "u")
	visit("undone")
	h.press("esc")
	visit("esc hint")
	finishTour(h, visit)
	joinTour(h, visit)
}

// finishTour turns servers on and walks setting up (s) in place: servers
// starting, a sign-in waiting with its URL, and the list after it, with a
// failure. A server page sets up its server too.
func finishTour(h *harness, visit func(name string)) {
	h.t.Helper()
	long := strings.Repeat("Very long label ", 12)
	h.fin.needs = map[string]SignInState{"local:long": {Possible: true, Needed: true}}
	h.fin.url = "https://auth.example/authorize?" + strings.Repeat("state=abcdef&", 20)
	h.loads.errs = map[string]error{"local:spare": output.NewError("connection_failed", nil)}
	h.selectTab("Other")
	h.selectRow("Spare")
	h.press("space")
	h.selectRow(long)
	h.press("space")
	visit("steps left")
	h.fin.block = make(chan struct{})
	h.hold = true
	h.press("s")
	visit("setting up")
	h.stepUntil(h.signingIn("local:long"))
	visit("sign-in URL")
	close(h.fin.block)
	h.flush()
	if h.m.finish != nil || len(h.m.loadErr["local:spare"]) == 0 {
		h.t.Fatalf("setting up did not settle as expected:\n%s", h.plain())
	}
	visit("set up")
	h.selectRow("Spare")
	h.press("enter", "s")
	visit("server page set up")
	h.press("esc")
}

func TestLayoutFitsAllSizesAllScreens(t *testing.T) {
	for _, unicode := range []bool{false, true} {
		h := newHarness(t, seedLong, 120, 35, fixedNow(), func(o *Options) { o.Color, o.Unicode = true, unicode })
		sizes := 0
		tour(h, func(name string) {
			for _, w := range []int{40, 41, 59, 80, 99, 100, 120, 160} {
				for _, ht := range []int{10, 11, 17, 24, 29, 30, 35, 50} {
					h.send(resize(w, ht))
					checkFits(t, h, w, ht, name)
					sizes++
					if name == "list" { // the details pane follows the wide size exactly
						rule := h.m.theme.border.Left
						hasPane := strings.Contains(strings.Split(h.plain(), "\n")[3], " "+rule+" Notes")
						if wide := w >= wideWidth && ht >= wideHeight; hasPane != wide {
							t.Fatalf("%dx%d: pane %v:\n%s", w, ht, hasPane, h.plain())
						}
					}
				}
			}
			h.send(resize(120, 35))
		})
		if sizes < 15*64 {
			t.Fatal("tour visited too few screens", sizes)
		}
	}
}

func TestNoColorEmitsNoEscapes(t *testing.T) {
	h := newHarness(t, seedLong, 80, 24, fixedNow())
	tour(h, func(name string) {
		if strings.ContainsRune(h.view(), 0x1b) {
			t.Fatalf("%s: %q", name, h.view())
		}
	})
}

// TestColorOnlyAddsSGR: color may only add SGR sequences. With them
// stripped, the colored screens equal the plain ones, glyphs included.
func TestColorOnlyAddsSGR(t *testing.T) {
	for _, unicode := range []bool{false, true} {
		var plain, colored []string
		for _, color := range []bool{false, true} {
			h := newHarness(t, seedLong, 100, 30, fixedNow(), func(o *Options) { o.Color, o.Unicode = color, unicode })
			tour(h, func(name string) {
				if !color {
					plain = append(plain, h.view())
					return
				}
				stripped := stripSGR(h.view())
				if strings.ContainsRune(stripped, 0x1b) {
					t.Fatalf("%s: escape other than SGR: %q", name, stripped)
				}
				// Reverse video pads the focused line; trailing spaces are not text.
				lines := strings.Split(stripped, "\n")
				for i := range lines {
					lines[i] = strings.TrimRight(lines[i], " ")
				}
				colored = append(colored, strings.Join(lines, "\n"))
			})
		}
		if len(plain) != len(colored) {
			t.Fatal(unicode, len(plain), len(colored))
		}
		for i := range plain {
			if plain[i] != colored[i] {
				t.Fatalf("unicode=%v screen %d differs:\n%s\n---\n%s", unicode, i, plain[i], colored[i])
			}
		}
	}
	h := newHarness(t, seedFixture, 120, 35, func(o *Options) { o.Color = true })
	if !painted(h.view(), "", "1") || !painted(h.view(), "", "7") {
		t.Fatalf("no bold or reverse: %q", h.view())
	}
}

// TestASCIIOnly: without Unicode every screen is ASCII, color included.
func TestASCIIOnly(t *testing.T) {
	h := newHarness(t, seedLong, 120, 35, fixedNow(), func(o *Options) { o.Color, o.Unicode = true, false })
	tour(h, func(name string) {
		for _, r := range h.view() {
			if r > 126 || r < 32 && r != '\n' && r != 0x1b {
				t.Fatalf("%s: %q", name, r)
			}
		}
	})
}

// TestUnicodeOnlyThemeGlyphs: with Unicode on, nothing beyond ASCII appears
// except the theme's glyphs; metadata stays escaped.
func TestUnicodeOnlyThemeGlyphs(t *testing.T) {
	h := newHarness(t, seedLong, 120, 35, fixedNow(), func(o *Options) { o.Color, o.Unicode = true, true })
	allowed := h.m.theme.glyphSet()
	if allowed == "" {
		t.Fatal("unicode theme has no glyphs")
	}
	tour(h, func(name string) {
		for _, r := range h.view() {
			if r > 126 && !strings.ContainsRune(allowed, r) || r < 32 && r != '\n' && r != 0x1b {
				t.Fatalf("%s: %q", name, r)
			}
		}
	})
}

func TestTooSmallTerminal(t *testing.T) {
	for _, size := range [][2]int{{39, 24}, {80, 9}, {12, 3}} {
		h := newHarness(t, seedFixture, size[0], size[1])
		if size[1] > 3 {
			h.containsWrapped("Terminal too small (need 40x10). Resize, or press q.")
		}
		checkFits(t, h, size[0], size[1], "too small")
		h.press("space", "down", "enter", "n", "u", "s", "?")
		if h.writes() != 0 || h.m.screen != screenList || h.quit {
			t.Fatal(h.writes(), h.m.screen)
		}
		h.press("q")
		if !h.quit {
			t.Fatal("q did not quit")
		}
	}
}

func TestHelpOverlayEnglish(t *testing.T) {
	h := newHarness(t, seedFixture, 80, 24)
	h.contains("? help")
	h.press("?")
	h.contains("Help", "Every change applies at once; u undoes it.",
		"Space           turn on or off; asks for what is missing first", "Up/Down scroll   Esc or ? close")
	h.lacks("Save", "Ctrl+S", "discard", "draft")
	for range 60 { // past the end: the offset stops there
		h.press("down")
	}
	h.contains("starts on first use: nothing left to do")
	h.press("?")
	if h.m.screen != screenList {
		t.Fatal(h.m.screen)
	}
	// With a step left the first line says so. s cannot take a review or
	// a profile, so it names the next step; on a server with a step, Right
	// opens it.
	wide := newHarness(t, seedFixture, 120, 35)
	wide.contains("2 servers need a step.  Next: Reviewer, review change.",
		"Space on/off   Tab domain   / search   u undo   ? help   q quit")
	wide.lacks("s set up all")
	wide.selectTab("Development")
	wide.selectRow("Reviewer")
	wide.contains("2 servers need a step.  Right open Reviewer")
	calm := newHarness(t, nil, 120, 35)
	calm.contains("Space on/off   Tab domain   Up/Down server   Right/Enter open   u undo   s set up",
		"/ search   n new personal connection   q quit   ? help")
}
