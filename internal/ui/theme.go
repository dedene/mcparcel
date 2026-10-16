package ui

import (
	"image/color"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/lipgloss/v2"
)

// role names what a piece of text means; the theme decides how it looks.
type role int

const (
	roleOK        role = iota // green: on and ready, done
	roleWarn                  // amber: on with one step left
	roleBad                   // red: unavailable or failed
	roleInfo                  // blue: 1Password
	roleAccent                // violet: sign-in
	roleDim                   // secondary text
	roleFaint                 // borders and rules
	roleTitle                 // header and page titles
	roleSelected              // the focused row or action
	roleActiveTab             // the current domain tab
	roleKey                   // a key chip in a help line
	roleFlashOn               // a row just turned on, briefly
	roleFlashOff              // a row just turned off, briefly
	roleCount
)

// glyphs are the state marks. Each one says its state without color, so a
// monochrome screen reads the same.
type glyphs struct {
	on          string // on with nothing to do
	off         string // off
	pending     string // on, needs a step
	unavailable string // on, but it cannot run
	done        string // a finished step, in a To do
	todo        string // a step left for you, in a To do
	later       string // a step that happens later, in a To do
	warn        string // join: a profile that differs from the suggestion

	sep         string // between items of one line
	prev, next  string // tabs scrolled off the left or right
	left, right string // the Left and Right keys in a key hint
}

var (
	unicodeGlyphs = glyphs{
		on: "●", off: "○", pending: "◐", unavailable: "✕", done: "✓", todo: "◆", later: "○", warn: "!",
		sep: " · ", prev: "‹", next: "›", left: "←", right: "→",
	}
	asciiGlyphs = glyphs{
		on: "[x]", off: "[ ]", pending: "[~]", unavailable: "[-]", done: "[+]", todo: "[*]", later: "[.]", warn: "[!]",
		sep: " - ", prev: "<", next: ">", left: "Left", right: "Right",
	}
)

// frameRate is the spinner's frame interval (see the motion rules).
const frameRate = 80 * time.Millisecond

// theme is built from Options, and again when Bubble Tea reports the
// terminal's color profile (tea.ColorProfileMsg). Colors come from the
// terminal's own 16-color palette, so they follow its theme; 256-color and
// truecolor terminals get finer greys for dimmed text, borders and key chips.
// Without color every role renders its text unchanged.
type theme struct {
	color   bool
	motion  bool // animate: color is on and reduced motion is off
	styles  [roleCount]lipgloss.Style
	glyph   glyphs
	border  lipgloss.Border
	spinner spinner.Spinner
}

// basePalette picks the 16-color grey: the palette until the profile is
// known, downsampled by the renderer where needed.
func basePalette(ansi, _, _ color.Color) color.Color { return ansi }

// newTheme builds the theme; grey picks a grey for the terminal's profile
// (lipgloss.Complete), nil the 16-color one.
func newTheme(opts Options, grey lipgloss.CompleteFunc) theme {
	t := theme{
		color:   opts.Color,
		motion:  opts.Color && !opts.ReduceMotion,
		glyph:   asciiGlyphs,
		border:  lipgloss.ASCIIBorder(),
		spinner: spinner.Spinner{Frames: []string{".", "o", "O", "o"}, FPS: frameRate}, // no "-": "[-]" marks a row that cannot run
	}
	if opts.Unicode {
		t.glyph = unicodeGlyphs
		t.border = lipgloss.RoundedBorder()
		t.spinner = spinner.Spinner{Frames: spinner.MiniDot.Frames, FPS: frameRate}
	}
	if !opts.Color {
		return t
	}
	if grey == nil {
		grey = basePalette
	}
	dim := grey(lipgloss.BrightBlack, lipgloss.Color("245"), lipgloss.Color("#8a8a8a"))
	faint := grey(lipgloss.BrightBlack, lipgloss.Color("240"), lipgloss.Color("#585858"))
	chip := grey(lipgloss.BrightBlack, lipgloss.Color("238"), lipgloss.Color("#444444"))
	chipText := grey(lipgloss.BrightWhite, lipgloss.Color("255"), lipgloss.Color("#eeeeee"))
	s := lipgloss.NewStyle()
	t.styles = [roleCount]lipgloss.Style{
		roleOK:        s.Foreground(lipgloss.Green),
		roleWarn:      s.Foreground(lipgloss.Yellow),
		roleBad:       s.Foreground(lipgloss.Red),
		roleInfo:      s.Foreground(lipgloss.Blue),
		roleAccent:    s.Foreground(lipgloss.Magenta),
		roleDim:       s.Foreground(dim),
		roleFaint:     s.Foreground(faint),
		roleTitle:     s.Bold(true),
		roleSelected:  s.Reverse(true),
		roleActiveTab: s.Bold(true).Reverse(true),
		roleKey:       s.Foreground(chipText).Background(chip),
		roleFlashOn:   s.Reverse(true).Foreground(lipgloss.Green),
		roleFlashOff:  s.Reverse(true).Foreground(dim),
	}
	return t
}

// paint styles text for its role. Without color it returns text unchanged.
func (t theme) paint(r role, text string) string {
	if !t.color || text == "" {
		return text
	}
	return t.styles[r].Render(text)
}

// glyphSet lists every non-ASCII character the theme may draw.
func (t theme) glyphSet() string {
	g := t.glyph
	set := g.on + g.off + g.pending + g.unavailable + g.done + g.todo + g.later + g.warn + g.sep + g.prev + g.next + g.left + g.right
	for _, frame := range t.spinner.Frames {
		set += frame
	}
	b := t.border
	set += b.Top + b.Bottom + b.Left + b.Right + b.TopLeft + b.TopRight + b.BottomLeft + b.BottomRight +
		b.MiddleLeft + b.MiddleRight + b.Middle + b.MiddleTop + b.MiddleBottom
	out := ""
	for _, r := range set {
		if r > 126 {
			out += string(r)
		}
	}
	return out
}
