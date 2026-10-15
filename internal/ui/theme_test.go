package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

func TestThemeWithoutColorIsPlain(t *testing.T) {
	for _, unicode := range []bool{false, true} {
		th := newTheme(Options{Unicode: unicode}, nil)
		for r := range roleCount {
			if got := th.paint(r, "text"); got != "text" {
				t.Fatalf("role %d: %q", r, got)
			}
		}
		if th.motion {
			t.Fatal("no color still animates")
		}
	}
}

func TestThemeGlyphs(t *testing.T) {
	ascii := newTheme(Options{Color: true}, nil)
	if g := ascii.glyph; g.on != "[x]" || g.off != "[ ]" || g.review != "[!]" || ascii.glyphSet() != "" {
		t.Fatalf("%+v %q", g, ascii.glyphSet())
	}
	uni := newTheme(Options{Color: true, Unicode: true}, nil)
	g := uni.glyph
	if g.on != "●" || g.off != "○" || g.review != "!" || g.unavailable != "✕" || g.needsConfig != "◌" || g.done != "✓" {
		t.Fatalf("%+v", g)
	}
	// Every mark has an ASCII twin, and each set keeps one width so columns line up.
	for _, set := range []glyphs{asciiGlyphs, unicodeGlyphs} {
		marks := []string{set.on, set.off, set.review, set.unavailable, set.needsConfig, set.pending, set.done}
		for _, mark := range marks {
			if textWidth(mark) != textWidth(marks[0]) {
				t.Fatalf("%q is %d wide, %q is %d", mark, textWidth(mark), marks[0], textWidth(marks[0]))
			}
		}
	}
	for _, frame := range append(ascii.spinner.Frames, uni.spinner.Frames...) {
		if textWidth(frame) != 1 {
			t.Fatalf("spinner frame %q", frame)
		}
	}
	if ascii.spinner.FPS != frameRate || uni.spinner.FPS != frameRate {
		t.Fatal(ascii.spinner.FPS, uni.spinner.FPS)
	}
	for _, r := range strings.Join(ascii.spinner.Frames, "") + ascii.border.Top + ascii.border.TopLeft {
		if r > 126 {
			t.Fatalf("ascii theme draws %q", r)
		}
	}
}

func TestThemeColorFollowsTerminalPalette(t *testing.T) {
	ansi := newTheme(Options{Color: true}, nil) // unknown profile: the 16-color base
	for r, want := range map[role]string{roleOK: "32", roleWarn: "33", roleBad: "31", roleInfo: "34", roleAccent: "35", roleDim: "90", roleFaint: "90", roleTitle: "1", roleSelected: "7"} {
		if got := ansi.paint(r, "x"); !painted(got, "x", want) || stripSGR(got) != "x" {
			t.Errorf("role %d: %q lacks %s", r, got, want)
		}
	}
	// Finer greys only where the terminal has them; semantic colors stay in the base palette.
	fine := newTheme(Options{Color: true}, lipgloss.Complete(colorprofile.ANSI256))
	if got := fine.paint(roleDim, "x"); !strings.Contains(got, "38;5;245") {
		t.Fatalf("%q", got)
	}
	if got := fine.paint(roleOK, "x"); !painted(got, "x", "32") {
		t.Fatalf("%q", got)
	}
	if got := newTheme(Options{Color: true}, lipgloss.Complete(colorprofile.TrueColor)).paint(roleFaint, "x"); !strings.Contains(got, "38;2;") {
		t.Fatalf("%q", got)
	}
}

func TestThemeReduceMotion(t *testing.T) {
	if !newTheme(Options{Color: true}, nil).motion || newTheme(Options{Color: true, ReduceMotion: true}, nil).motion {
		t.Fatal("motion")
	}
}

func TestTextHelpersMeasureCells(t *testing.T) {
	styled := newTheme(Options{Color: true}, nil).paint(roleOK, "●●●●●●")
	for _, tc := range []struct{ got, want string }{
		{fit("●○✕◌✓●", 4), "●..."},
		{fit("●○✕", 3), "●○✕"},
		{fit("●○✕◌", 2), "●○"},
		{pad("●", 3), "●  "},
		{pad("世界", 3), "世 "},
		{spread("●", "✓", 5), "●   ✓"},
		{stripSGR(fit(styled, 4)), "●..."},
	} {
		if tc.got != tc.want {
			t.Errorf("got %q, want %q", tc.got, tc.want)
		}
	}
	if got := fit(styled, 4); textWidth(got) != 4 || !painted(got, "●", "32") {
		t.Fatalf("%q", got)
	}
	lines := wrap("●● ○○○ ✕✕✕✕✕", 4)
	if strings.Join(lines, "|") != "●●|○○○|✕✕✕✕|✕" {
		t.Fatalf("%q", lines)
	}
	if lines := wrap("a 世界", 1); textWidth(strings.Join(lines, "")) == 0 {
		t.Fatalf("%q", lines) // a character wider than the line does not loop forever
	}
	var f field
	f.insert("abcdef")
	if got := f.render(4); got != "def_" {
		t.Fatal(got)
	}
}

func TestStripSGRKeepsOtherEscapes(t *testing.T) {
	in := "\x1b[1;38;5;245ma\x1b[m b\x1b[2Jc\x1b]8;;x\x07"
	if got := stripSGR(in); got != "a b\x1b[2Jc\x1b]8;;x\x07" {
		t.Fatalf("%q", got)
	}
}

func TestTicksWaitForTheTest(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35, fixedNow())
	type frame struct{ at time.Time }
	h.run(h.m.tick(frameRate, func(at time.Time) tea.Msg { return frame{at} }))
	if len(h.ticks) != 1 || h.ticks[0].d != frameRate {
		t.Fatal(h.ticks)
	}
	if d := h.fireTick(); d != frameRate || len(h.ticks) != 0 {
		t.Fatal(d, h.ticks)
	}
	// Without an injected Tick the model uses tea.Tick.
	m, err := NewModel(context.Background(), h.store, h.disk(), Options{})
	if err != nil || m.tick(time.Hour, func(time.Time) tea.Msg { return nil }) == nil {
		t.Fatal(err)
	}
}

// The theme follows the profile Bubble Tea detects: finer greys on a
// 256-color terminal, nothing at all without color.
func TestThemeFollowsColorProfileMsg(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35, func(o *Options) { o.Color = true })
	if strings.Contains(h.view(), "38;5;") {
		t.Fatalf("finer greys before the profile is known: %q", h.view())
	}
	h.send(tea.ColorProfileMsg{Profile: colorprofile.ANSI256})
	if !strings.Contains(h.view(), "38;5;245") {
		t.Fatalf("no 256-color grey: %q", h.view())
	}
	plain := newHarness(t, seedFixture, 120, 35)
	plain.send(tea.ColorProfileMsg{Profile: colorprofile.TrueColor})
	if strings.ContainsRune(plain.view(), 0x1b) {
		t.Fatalf("no color still styles: %q", plain.view())
	}
}
