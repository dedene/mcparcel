package elicit_test

import (
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/elicit"
)

func TestCleanLinesKeepsNewlines(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"empty", "", ""},
		{"lines and tabs", "a\n\tb\n\nc", "a\n\tb\n\nc"},
		{"csi", "a\x1b[2J\x1b[31mred\x1b[0m\nb", "ared\nb"},
		{"osc 52 clipboard", "x\x1b]52;c;SGVsbG8=\x07y", "xy"},
		{"osc st", "\x1b]0;pwned\x1b\\title", "title"},
		{"dcs", "a\x1bPq#0\x1b\\b", "ab"},
		{"c1", "a\u009b31mb\u009d0;t\u0007c\u0085d", "abcd"},
		{"esc pair", "a\x1b7b", "ab"},
		{"carriage return and c0", "a\rb\x00c\x08d\x7fe\x07f", "abcdef"},
		{"bidi", "safe\u202eexe\u2066x\u2069\u200e\u061c", "safeexex"},
		{"invalid utf8", "a" + string([]byte{0xff}) + "b", "ab"},
		{"keeps text", "héllo 👍🏽 ❤️ — 日本", "héllo 👍🏽 ❤️ — 日本"},
	} {
		if got := elicit.CleanLines(tc.in); got != tc.want {
			t.Errorf("%s: %q want %q", tc.name, got, tc.want)
		}
	}
	long := strings.Repeat("x\n", 5000)
	if elicit.CleanLines(long) != long {
		t.Fatal("CleanLines must not cap length")
	}
}
