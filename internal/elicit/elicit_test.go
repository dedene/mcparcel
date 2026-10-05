package elicit_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/elicit"
)

func TestClean(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"empty", "", ""},
		{"plain", "Allow Computer Use?", "Allow Computer Use?"},
		{"csi colour", "a\x1b[31;1mred\x1b[0m b", "ared b"},
		{"osc 8 hyperlink bel", "x\x1b]8;;https://evil\x07link\x1b]8;;\x07y", "xlinky"},
		{"osc title st", "\x1b]0;pwned\x1b\\title", "title"},
		{"dcs", "a\x1bPq#0\x1b\\b", "ab"},
		{"c1 csi", "a\u009b31mb", "ab"},
		{"c1 osc", "a\u009d0;t\u0007b", "ab"},
		{"unterminated osc", "a\x1b]0;rest", "a"},
		{"bare esc", "a\x1bb\x1b", "a"},
		{"esc pair", "a\x1b7b", "ab"},
		{"bidi", "safe\u202eexe.txt\u2066x\u2067y\u2068z\u2069", "safeexe.txtxyz"},
		{"zero width", "a\u200db\u200bc\ufeffd", "abcd"},
		{"whitespace", " a\r\nb\tc\u2028d  ", "a b c d"},
		{"rune error", "a\ufffdb" + string([]byte{0xff}) + "c", "abc"},
		{"c0 controls", "a\x00b\x08c\x7fd", "abcd"},
		{"invisible padding", "a\u2800b\u3164c\u115fd\u1160e\uffa0f\u034fg\ufe0fh\U000E0100i  \u3164 j", "a b c d e f g h i j"},
	} {
		if got := elicit.Clean(tc.in, 100); got != tc.want {
			t.Errorf("%s: Clean(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
	if got := elicit.Clean("abcdefghij", 8); got != "abcde..." {
		t.Fatalf("cap = %q", got)
	}
	if got := elicit.Clean("abcdefgh", 8); got != "abcdefgh" {
		t.Fatalf("exact cap = %q", got)
	}
	if got := elicit.Clean("\u00e9\u00e9\u00e9\u00e9\u00e9\u00e9\u00e9\u00e9\u00e9", 8); got != "\u00e9\u00e9\u00e9\u00e9\u00e9..." {
		t.Fatalf("rune cap = %q", got)
	}
}

func TestCleanIdempotent(t *testing.T) {
	inputs := []string{
		"abcd efghij", "abcd  efgh", "abcd\tefgh ij", "a b c d e f g h i j", "abcdefgh", "abcdefg ",
		"\x1b[1mabcd\x1b[0m efgh", "ab\u202ecd ef gh ij", "    ", "abcd ...efg", "...........", "ab\u3164\u2800cd ef\ufe0f gh",
	}
	for _, in := range inputs {
		for limit := 4; limit <= 12; limit++ {
			once := elicit.Clean(in, limit)
			if twice := elicit.Clean(once, limit); twice != once {
				t.Fatalf("Clean(%q, %d): %q then %q", in, limit, once, twice)
			}
			if n := len([]rune(once)); n > limit {
				t.Fatalf("Clean(%q, %d) = %q has %d runes", in, limit, once, n)
			}
		}
	}
}

func approval() elicit.Prompt {
	return elicit.Prompt{
		Message: `Allow Computer Use to use "Calculator"?`, Subtitle: "Computer use", RiskLevel: "medium",
		Details: `{"app":"Calculator"}`, Persist: []string{"session"},
	}
}

func form() elicit.Prompt {
	return elicit.Prompt{Message: "Pick one", Fields: []elicit.Field{
		{Name: "count", Title: "Count", Type: "integer"},
		{Name: "flag", Type: "boolean", Required: true},
		{Name: "mode", Type: "string", Enum: []string{"fast", "slow"}, Required: true},
		{Name: "note", Type: "string", Description: "Free text"},
		{Name: "ratio", Type: "number"},
	}}
}

func TestPromptValid(t *testing.T) {
	for _, p := range []elicit.Prompt{approval(), form(), {Message: "m"}} {
		if err := p.Valid(); err != nil {
			t.Fatalf("Valid(%+v) = %v", p, err)
		}
	}
	bad := map[string]func(*elicit.Prompt){
		"empty message":      func(p *elicit.Prompt) { p.Message = "" },
		"dirty message":      func(p *elicit.Prompt) { p.Message = "a\x1b[31mb" },
		"untrimmed":          func(p *elicit.Prompt) { p.Message = " a" },
		"long message":       func(p *elicit.Prompt) { p.Message = strings.Repeat("a", 501) },
		"long subtitle":      func(p *elicit.Prompt) { p.Subtitle = strings.Repeat("a", 201) },
		"long risk":          func(p *elicit.Prompt) { p.RiskLevel = strings.Repeat("a", 33) },
		"long details":       func(p *elicit.Prompt) { p.Details = strings.Repeat("a", 501) },
		"dirty details":      func(p *elicit.Prompt) { p.Details = "a\nb" },
		"persist forever":    func(p *elicit.Prompt) { p.Persist = []string{"forever"} },
		"duplicate persist":  func(p *elicit.Prompt) { p.Persist = []string{"session", "session"} },
		"persist always":     func(p *elicit.Prompt) { p.Persist = []string{"always"} },
		"session and always": func(p *elicit.Prompt) { p.Persist = []string{"session", "always"} },
		"persist and fields": func(p *elicit.Prompt) { p.Fields = form().Fields },
	}
	for name, mutate := range bad {
		p := approval()
		mutate(&p)
		if p.Valid() == nil {
			t.Errorf("%s: Valid accepted %+v", name, p)
		}
	}
	badForm := map[string]func(*elicit.Prompt){
		"bad name":           func(p *elicit.Prompt) { p.Fields[0].Name = "a b" },
		"empty name":         func(p *elicit.Prompt) { p.Fields[0].Name = "" },
		"long name":          func(p *elicit.Prompt) { p.Fields[0].Name = strings.Repeat("a", 65) },
		"duplicate field":    func(p *elicit.Prompt) { p.Fields[1].Name = "count" },
		"unsorted":           func(p *elicit.Prompt) { p.Fields[0], p.Fields[1] = p.Fields[1], p.Fields[0] },
		"unknown type":       func(p *elicit.Prompt) { p.Fields[0].Type = "object" },
		"enum on boolean":    func(p *elicit.Prompt) { p.Fields[1].Enum = []string{"yes"} },
		"empty enum entry":   func(p *elicit.Prompt) { p.Fields[2].Enum = []string{""} },
		"duplicate enum":     func(p *elicit.Prompt) { p.Fields[2].Enum = []string{"a", "a"} },
		"dirty enum":         func(p *elicit.Prompt) { p.Fields[2].Enum = []string{"a\u202e"} },
		"21 enum entries":    func(p *elicit.Prompt) { p.Fields[2].Enum = numbered(21) },
		"long title":         func(p *elicit.Prompt) { p.Fields[0].Title = strings.Repeat("a", 201) },
		"long description":   func(p *elicit.Prompt) { p.Fields[3].Description = strings.Repeat("a", 301) },
		"dirty description":  func(p *elicit.Prompt) { p.Fields[3].Description = "a\x07" },
		"eleven fields":      func(p *elicit.Prompt) { p.Fields = stringFields(11) },
		"persist with field": func(p *elicit.Prompt) { p.Persist = []string{"session"} },
	}
	for name, mutate := range badForm {
		p := form()
		mutate(&p)
		if p.Valid() == nil {
			t.Errorf("%s: Valid accepted %+v", name, p)
		}
	}
	if p := (elicit.Prompt{Message: "m", Fields: stringFields(10)}); p.Valid() != nil {
		t.Fatalf("ten fields rejected: %v", p.Valid())
	}
}

func numbered(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("v%02d", i)
	}
	return out
}

func stringFields(n int) []elicit.Field {
	out := make([]elicit.Field, n)
	for i := range out {
		out[i] = elicit.Field{Name: fmt.Sprintf("f%02d", i), Type: "string"}
	}
	return out
}

// A prompt at every cap, of characters JSON escapes to six bytes, either fits
// the frame body or fails Valid; it never makes an oversized frame.
func TestPromptSizeFitsFrame(t *testing.T) {
	for _, r := range []string{"<", "\U0001F600", "a"} {
		long := func(n int) string { return strings.Repeat(r, n) }
		p := elicit.Prompt{Message: long(500), Subtitle: long(200), RiskLevel: long(32), Details: long(500)}
		for i := range 10 {
			enum := make([]string, 20)
			for j := range enum {
				enum[j] = fmt.Sprintf("%02d", j) + long(98)
			}
			p.Fields = append(p.Fields, elicit.Field{
				Name: fmt.Sprintf("%02d", i) + strings.Repeat("n", 62), Title: long(200), Description: long(300),
				Type: "string", Enum: enum, Required: true,
			})
		}
		frame, _ := json.Marshal(struct {
			PromptID string `json:"promptId"`
			elicit.Prompt
		}{strings.Repeat("a", 32), p})
		if err := p.Valid(); err == nil && len(frame) > elicit.MaxBody {
			t.Fatalf("%q: valid prompt makes a %d-byte frame", r, len(frame))
		}
		small := elicit.Prompt{Message: long(500), Details: long(500), Persist: []string{"session"}}
		if err := small.Valid(); err != nil {
			t.Fatalf("%q: approval at caps rejected: %v", r, err)
		}
	}
}

func TestAnswerSizeFitsFrame(t *testing.T) {
	content := map[string]any{}
	for i := range 10 {
		content[fmt.Sprintf("%02d", i)+strings.Repeat("k", 62)] = strings.Repeat("<", 1000)
	}
	a := elicit.Answer{Action: "accept", Content: content}
	if err := a.Valid(); err != nil {
		t.Fatalf("maximal answer rejected: %v", err)
	}
	frame, _ := json.Marshal(struct {
		PromptID string `json:"promptId"`
		elicit.Answer
	}{strings.Repeat("a", 32), a})
	if len(frame) > elicit.MaxBody {
		t.Fatalf("maximal answer is %d bytes", len(frame))
	}
}

func TestAnswerValid(t *testing.T) {
	good := []elicit.Answer{
		{Action: "accept"},
		{Action: "decline"},
		{Action: "cancel"},
		{Action: "accept", Persist: "session"},
		{Action: "accept", Content: map[string]any{"a": "x", "b": 1.5, "c": true}},
	}
	for _, a := range good {
		if err := a.Valid(); err != nil {
			t.Fatalf("Valid(%+v) = %v", a, err)
		}
	}
	bad := []elicit.Answer{
		{},
		{Action: "allow"},
		{Action: "decline", Persist: "session"},
		{Action: "accept", Persist: "forever"},
		{Action: "accept", Persist: "always"},
		{Action: "cancel", Content: map[string]any{"a": "x"}},
		{Action: "accept", Content: map[string]any{"a": map[string]any{}}},
		{Action: "accept", Content: map[string]any{"a": nil}},
		{Action: "accept", Content: map[string]any{"a b": "x"}},
		{Action: "accept", Content: map[string]any{"a": "x\x1b"}},
		{Action: "accept", Content: map[string]any{"a": strings.Repeat("x", 1001)}},
		{Action: "accept", Content: func() map[string]any {
			m := map[string]any{}
			for i := range 11 {
				m[fmt.Sprint("k", i)] = true
			}
			return m
		}()},
	}
	for _, a := range bad {
		if a.Valid() == nil {
			t.Errorf("Valid accepted %+v", a)
		}
	}
}

func TestCheck(t *testing.T) {
	ok := func(p elicit.Prompt, a elicit.Answer) {
		t.Helper()
		if err := p.Check(a); err != nil {
			t.Fatalf("Check(%+v) = %v", a, err)
		}
	}
	fail := func(p elicit.Prompt, a elicit.Answer) {
		t.Helper()
		if p.Check(a) == nil {
			t.Fatalf("Check accepted %+v", a)
		}
	}
	one := approval()
	for _, a := range []elicit.Answer{{Action: "accept"}, {Action: "decline"}, {Action: "cancel"}, {Action: "accept", Persist: "session"}} {
		ok(one, a)
	}
	fail(one, elicit.Answer{Action: "accept", Persist: "always"})
	fail(elicit.Prompt{Message: "m"}, elicit.Answer{Action: "accept", Persist: "session"})
	fail(one, elicit.Answer{Action: "accept", Content: map[string]any{"allow": true}})
	fail(one, elicit.Answer{Action: "approve"})

	f := form()
	base := func() map[string]any { return map[string]any{"flag": true, "mode": "fast"} }
	ok(f, elicit.Answer{Action: "accept", Content: base()})
	full := base()
	full["count"], full["note"], full["ratio"] = float64(3), "hi", 0.5
	ok(f, elicit.Answer{Action: "accept", Content: full})
	ok(f, elicit.Answer{Action: "decline"})
	fail(f, elicit.Answer{Action: "accept"})
	fail(f, elicit.Answer{Action: "accept", Persist: "session", Content: base()})
	for name, change := range map[string]func(map[string]any){
		"missing required": func(m map[string]any) { delete(m, "flag") },
		"wrong type":       func(m map[string]any) { m["flag"] = "true" },
		"string number":    func(m map[string]any) { m["ratio"] = "1" },
		"non-integral":     func(m map[string]any) { m["count"] = 1.5 },
		"huge integer":     func(m map[string]any) { m["count"] = 1e300 },
		"enum miss":        func(m map[string]any) { m["mode"] = "medium" },
		"unknown key":      func(m map[string]any) { m["extra"] = "x" },
		"overlong string":  func(m map[string]any) { m["note"] = strings.Repeat("x", 1001) },
		"number for bool":  func(m map[string]any) { m["flag"] = float64(1) },
	} {
		m := base()
		change(m)
		if f.Check(elicit.Answer{Action: "accept", Content: m}) == nil {
			t.Errorf("%s: Check accepted %v", name, m)
		}
	}
}
