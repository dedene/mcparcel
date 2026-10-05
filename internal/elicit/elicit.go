// Package elicit holds the elicitation prompt the daemon forwards to a CLI, the
// answer it relays back, the cleaning of untrusted server text and the
// terminal and dialog rendering.
package elicit

import (
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// PromptTimeout closes an unanswered prompt with cancel.
	PromptTimeout = 5 * time.Minute
	// MaxBody caps an elicit or elicit_answer frame body.
	MaxBody = 64 << 10

	maxMessage     = 500
	maxSubtitle    = 200
	maxRisk        = 32
	maxDetails     = 500
	maxTitle       = 200
	maxDescription = 300
	maxEnumValue   = 100
	maxEnum        = 20
	maxFields      = 10
	maxValue       = 1000
)

// Prompt is a cleaned elicitation request; no Fields is the approval case.
type Prompt struct {
	Message   string   `json:"message"`
	Subtitle  string   `json:"subtitle,omitempty"`
	RiskLevel string   `json:"riskLevel,omitempty"`
	Details   string   `json:"details,omitempty"`
	Persist   []string `json:"persist,omitempty"`
	Fields    []Field  `json:"fields,omitempty"`
}

// Field is one flat primitive form field.
type Field struct {
	Name        string   `json:"name"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	Type        string   `json:"type"`
	Enum        []string `json:"enum,omitempty"`
	Required    bool     `json:"required,omitempty"`
}

// Answer is the user's reply to a Prompt.
type Answer struct {
	Action  string         `json:"action"`
	Persist string         `json:"persist,omitempty"`
	Content map[string]any `json:"content,omitempty"`
}

var (
	errInvalid = errors.New("invalid elicitation")
	fieldName  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// Clean makes untrusted text one trimmed line of at most limit runes, without
// terminal escape sequences, control, format or blank padding characters.
// Clean(Clean(s)) == Clean(s).
func Clean(text string, limit int) string {
	var b strings.Builder
	for i := 0; i < len(text); {
		r, n := utf8.DecodeRuneInString(text[i:])
		i += n
		switch {
		case r == 0x1b && i < len(text):
			next, m := utf8.DecodeRuneInString(text[i:])
			i += m
			switch next {
			case '[':
				i = skipCSI(text, i)
			case ']', 'P', 'X', '^', '_':
				i = skipString(text, i)
			}
		case r == 0x9b:
			i = skipCSI(text, i)
		case r == 0x90 || r == 0x98 || r == 0x9d || r == 0x9e || r == 0x9f:
			i = skipString(text, i)
		case unicode.IsSpace(r) || blank(r):
			b.WriteByte(' ')
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == utf8.RuneError:
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if runes := []rune(out); len(runes) > limit {
		if limit <= 3 {
			return string(runes[:limit])
		}
		out = string(runes[:limit-3]) + "..."
	}
	return out
}

// blank reports whether r renders as empty space without being whitespace,
// which could pad server text into a line of its own.
func blank(r rune) bool {
	return r == 0x2800 || unicode.In(r, unicode.Other_Default_Ignorable_Code_Point, unicode.Variation_Selector)
}

// skipCSI returns the index after a control sequence's final byte.
func skipCSI(text string, i int) int {
	for ; i < len(text); i++ {
		if text[i] >= 0x40 && text[i] <= 0x7e {
			return i + 1
		}
	}
	return i
}

// skipString returns the index after a string sequence's BEL or ST terminator.
func skipString(text string, i int) int {
	for i < len(text) {
		r, n := utf8.DecodeRuneInString(text[i:])
		i += n
		if r == 0x07 || r == 0x9c {
			return i
		}
		if r == 0x1b && i < len(text) && text[i] == '\\' {
			return i + 1
		}
	}
	return i
}

func clean(s string, limit int) bool { return s == Clean(s, limit) }

// Valid reports whether p is a well-formed, already cleaned prompt that fits
// a frame body.
func (p Prompt) Valid() error {
	if p.Message == "" || !clean(p.Message, maxMessage) || !clean(p.Subtitle, maxSubtitle) ||
		!clean(p.RiskLevel, maxRisk) || !clean(p.Details, maxDetails) || len(p.Fields) > maxFields {
		return errInvalid
	}
	switch strings.Join(p.Persist, ",") {
	case "":
	case "session":
		if len(p.Fields) > 0 {
			return errInvalid
		}
	default:
		return errInvalid
	}
	for i, f := range p.Fields {
		if !fieldName.MatchString(f.Name) || i > 0 && p.Fields[i-1].Name >= f.Name ||
			!clean(f.Title, maxTitle) || !clean(f.Description, maxDescription) || len(f.Enum) > maxEnum {
			return errInvalid
		}
		switch f.Type {
		case "string":
		case "number", "integer", "boolean":
			if f.Enum != nil {
				return errInvalid
			}
		default:
			return errInvalid
		}
		if f.Enum != nil && len(f.Enum) == 0 {
			return errInvalid
		}
		for j, v := range f.Enum {
			if v == "" || !clean(v, maxEnumValue) || slices.Contains(f.Enum[:j], v) {
				return errInvalid
			}
		}
	}
	// Leaves room for the frame's promptId.
	if b, err := json.Marshal(p); err != nil || len(b) > MaxBody-256 {
		return errInvalid
	}
	return nil
}

// Valid reports whether a is a well-formed answer, independent of its prompt.
func (a Answer) Valid() error {
	switch a.Action {
	case "accept":
	case "decline", "cancel":
		if a.Persist != "" || a.Content != nil {
			return errInvalid
		}
	default:
		return errInvalid
	}
	if a.Persist != "" && a.Persist != "session" || len(a.Content) > maxFields {
		return errInvalid
	}
	for k, v := range a.Content {
		if !fieldName.MatchString(k) {
			return errInvalid
		}
		switch v := v.(type) {
		case string:
			if utf8.RuneCountInString(v) > maxValue || strings.IndexFunc(v, func(r rune) bool {
				return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == utf8.RuneError
			}) >= 0 {
				return errInvalid
			}
		case float64:
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return errInvalid
			}
		case bool:
		default:
			return errInvalid
		}
	}
	return nil
}

// Check reports whether a is an answer p allows: an offered persistence, and
// content only for a form, matching its fields.
func (p Prompt) Check(a Answer) error {
	if err := a.Valid(); err != nil {
		return err
	}
	if a.Action != "accept" {
		return nil
	}
	if a.Persist != "" && !slices.Contains(p.Persist, a.Persist) {
		return errInvalid
	}
	if len(p.Fields) == 0 {
		if a.Content != nil {
			return errInvalid
		}
		return nil
	}
	for k := range a.Content {
		if !slices.ContainsFunc(p.Fields, func(f Field) bool { return f.Name == k }) {
			return errInvalid
		}
	}
	for _, f := range p.Fields {
		v, ok := a.Content[f.Name]
		if !ok {
			if f.Required {
				return errInvalid
			}
			continue
		}
		if !fits(f, v) {
			return errInvalid
		}
	}
	return nil
}

func fits(f Field, v any) bool {
	switch f.Type {
	case "string":
		s, ok := v.(string)
		return ok && (f.Enum == nil || slices.Contains(f.Enum, s))
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		n, ok := v.(float64)
		return ok && n == math.Trunc(n) && math.Abs(n) <= 1<<53
	case "boolean":
		_, ok := v.(bool)
		return ok
	}
	return false
}
