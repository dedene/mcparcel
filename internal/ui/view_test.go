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
			h.press("esc", "space", "tab")
			checkFits(t, h, w, ht, "focus")
			h.press("q")
			checkFits(t, h, w, ht, "confirm")
			h.press("n")
			mustUpdate(t, h.store, func(s *config.State) { s.Local.Aliases = map[string]string{"x": idPaper} })
			h.press("ctrl+s")
			checkFits(t, h, w, ht, "conflict")
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
	h.press("enter")
	visit("field")
	h.press("esc", "down", "enter")
	visit("profile")
	h.press("esc", "t")
	visit("tools before load")
	h.press("esc", "space")
	visit("details needs config")
	h.press("esc")
	h.selectTab("Other")
	h.selectRow(strings.Repeat("Very long label ", 12))
	h.press("enter")
	visit("long details")
	h.press("esc")
	h.selectRow("Excalidraw")
	h.press("enter", "l")
	visit("tools loaded")
	h.press("esc", "e")
	visit("edit form")
	h.press("esc", "esc", "a")
	visit("add form")
	h.press("enter")
	visit("add form error")
	h.press("esc", "space", "q")
	visit("confirm")
	h.press("n", "tab")
	visit("save focus")
	h.press("shift+tab")
	mustUpdate(h.t, h.store, func(s *config.State) { s.Local.Aliases = map[string]string{"x": idPaper} })
	h.press("ctrl+s")
	visit("conflict")
	h.press("esc")
	finishTour(h, visit)
	joinTour(h, visit)
}

// finishTour saves past the conflict and walks the finish step: the offer,
// connections starting, a sign-in asked for, its URL, and the settled
// screen with a failure.
func finishTour(h *harness, visit func(name string)) {
	h.t.Helper()
	long := strings.Repeat("Very long label ", 12)
	h.fin.needs = map[string]SignInState{"local:long": {Possible: true, Needed: true}}
	h.fin.url = "https://auth.example/authorize?" + strings.Repeat("state=abcdef&", 20)
	h.loads.errs = map[string]error{"local:spare": output.NewError("connection_failed", nil)}
	h.press("ctrl+s", "r") // the conflict again; reload reapplies the draft
	h.selectTab("Other")
	h.selectRow("Spare")
	h.press("space")
	h.selectRow(long)
	h.press("space", "ctrl+s")
	if h.m.screen != screenFinish {
		h.t.Fatalf("no finish offer:\n%s", h.plain())
	}
	visit("finish offer")
	h.hold = true
	h.press("enter")
	visit("finish starting")
	h.flush()
	visit("finish asks")
	h.fin.block = make(chan struct{})
	h.hold = true
	h.press("enter")
	msg := firstMsg(h.held[0])
	h.held = nil
	h.send(msg)
	visit("finish sign-in URL")
	close(h.fin.block)
	h.flush()
	if !h.m.finish.settled() || h.m.finish.count(finFailed) != 1 || h.m.finish.count(finStarted) != 1 {
		h.t.Fatalf("finish did not settle as expected:\n%s", h.plain())
	}
	visit("finish settled")
	h.press("enter")
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
		h.press("space", "down", "enter", "a", "?")
		if h.m.draft.Changes() != 0 || h.m.screen != screenList || h.quit {
			t.Fatal(h.m.draft.Changes(), h.m.screen)
		}
		h.press("q")
		if !h.quit {
			t.Fatal("q did not quit")
		}
	}
	h := newHarness(t, seedFixture, 120, 35)
	h.press("space")
	h.send(resize(60, 8))
	h.press("q")
	h.contains("Discard 1 unsaved change? y/N")
	h.press("y")
	if !h.quit {
		t.Fatal("y did not discard")
	}
}

func TestHelpOverlayEnglish(t *testing.T) {
	h := newHarness(t, seedFixture, 80, 24)
	h.contains("? help")
	h.press("?")
	h.contains("MCParcel / Setup / Help", "Space           turn on or off; on [!] it accepts the review",
		"Ctrl+C          quit now and discard unsaved changes", "Up/Down scroll   Esc or ? close")
	for range 60 { // past the end: the offset stops there
		h.press("down")
	}
	h.contains("run mcparcel --help.")
	h.press("?")
	if h.m.screen != screenList {
		t.Fatal(h.m.screen)
	}
	wide := newHarness(t, seedFixture, 120, 35)
	wide.contains("A add personal connection   q cancel   ? help")
}
