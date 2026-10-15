package ui

import (
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"

	"github.com/dedene/mcparcel/internal/output"
)

// Widths are terminal cells, not bytes. Rendered text may hold the theme's
// glyphs (multibyte, see theme.go) and its SGR styling (zero wide), so every
// helper here measures with textWidth and cuts without splitting a character
// or an escape sequence. Untrusted metadata still goes through
// output.DisplayMetadata, which keeps it ASCII.

// textWidth is the display width of s, ignoring escape sequences.
func textWidth(s string) int { return lipgloss.Width(s) }

// fit truncates s to at most n cells, marking a cut with "...".
func fit(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if textWidth(s) <= n {
		return s
	}
	if n <= 3 {
		return truncate(s, n, "")
	}
	return truncate(s, n, "...")
}

// escapeAt returns the length of the CSI sequence (ESC [ ... final byte)
// starting at s[i], or 0 when none starts there.
func escapeAt(s string, i int) int {
	if s[i] != 0x1b || i+1 >= len(s) || s[i+1] != '[' {
		return 0
	}
	for j := i + 2; j < len(s); j++ {
		if s[j] >= 0x40 && s[j] <= 0x7e {
			return j + 1 - i
		}
	}
	return 0
}

// truncate cuts s to at most n cells, tail included, without splitting a
// character or an escape sequence. Escape sequences after the cut are kept,
// so a style that ends there still resets. Rendered text is ASCII and the
// theme's glyphs, so a rune is a character here.
func truncate(s string, n int, tail string) string {
	if textWidth(s) <= n {
		return s
	}
	n -= textWidth(tail)
	if n < 0 {
		return ""
	}
	var b strings.Builder
	used, cut := 0, false
	for i := 0; i < len(s); {
		if k := escapeAt(s, i); k > 0 {
			b.WriteString(s[i : i+k])
			i += k
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if w := textWidth(string(r)); !cut && used+w <= n {
			b.WriteRune(r)
			used += w
		} else if !cut {
			b.WriteString(tail)
			cut = true
		}
		i += size
	}
	return b.String()
}

// cutLeft drops the first n cells of s, keeping its escape sequences. A
// character that straddles the cut is dropped whole.
func cutLeft(s string, n int) string {
	var b strings.Builder
	dropped := 0
	for i := 0; i < len(s); {
		if k := escapeAt(s, i); k > 0 {
			b.WriteString(s[i : i+k])
			i += k
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if dropped < n {
			dropped += textWidth(string(r))
		} else {
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}

// pad fits s to exactly n cells.
func pad(s string, n int) string {
	s = fit(s, n)
	return s + strings.Repeat(" ", max(n-textWidth(s), 0))
}

// gutter is the space every list column keeps before the next one, so a
// truncated value never runs into it.
const gutter = 2

// cell fits s to a column of exactly n cells that ends in the gutter.
func cell(s string, n int) string { return pad(fit(s, n-gutter), n) }

// spread places left and right on one line of width n, keeping right whole.
func spread(left, right string, n int) string {
	if textWidth(right) >= n {
		return fit(right, n)
	}
	return pad(left, n-textWidth(right)-1) + " " + right
}

// stripSGR removes SGR sequences (CSI ... m), the only escapes the theme
// emits. Any other escape stays, so a test still sees it.
func stripSGR(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] >= 0x30 && s[j] <= 0x3f { // parameter bytes
				j++
			}
			for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f { // intermediate bytes
				j++
			}
			if j < len(s) && s[j] == 'm' {
				i = j
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// field is a single-line text input.
type field struct{ value []rune }

func (f *field) insert(text string) {
	for _, r := range text {
		if r >= 32 && r != 127 && (r < 0x80 || r > 0x9f) {
			f.value = append(f.value, r)
		}
	}
}

func (f *field) backspace() {
	if len(f.value) > 0 {
		f.value = f.value[:len(f.value)-1]
	}
}

func (f *field) clear()         { f.value = nil }
func (f *field) String() string { return string(f.value) }

// render shows the tail of the value so the cursor end stays visible.
func (f *field) render(n int) string {
	raw := f.String()
	text := output.DisplayMetadata(raw)
	if text != raw { // DisplayMetadata quoted it; show the escapes without the quotes
		text = text[1 : len(text)-1]
	}
	text += "_"
	if over := textWidth(text) - n; over > 0 && n > 0 {
		text = cutLeft(text, over)
	}
	return text
}
