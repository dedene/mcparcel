package ui

import (
	"strings"

	"github.com/dedene/mcparcel/internal/output"
)

// All rendered text is ASCII (metadata goes through output.DisplayMetadata),
// so a string's width is its byte length.

// fit truncates s to at most n bytes, marking a cut with "...".
func fit(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}

// pad fits s to exactly n bytes.
func pad(s string, n int) string {
	s = fit(s, n)
	return s + strings.Repeat(" ", n-len(s))
}

// spread places left and right on one line of width n, keeping right whole.
func spread(left, right string, n int) string {
	if len(right) >= n {
		return fit(right, n)
	}
	return pad(left, n-len(right)-1) + " " + right
}

type style struct{ color bool }

func (s style) sgr(code, text string) string {
	if !s.color || text == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func (s style) reverse(text string) string { return s.sgr("7", text) }
func (s style) bold(text string) string    { return s.sgr("1", text) }

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
	if len(text) > n && n > 0 {
		text = text[len(text)-n:]
	}
	return text
}
