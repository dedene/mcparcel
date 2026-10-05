package auth

import (
	_ "embed"
	"errors"
	"html/template"
	"io"
	"strings"
	"unicode"
)

//go:embed callback.html
var callbackHTML string

var callbackTemplate = template.Must(template.New("callback").Parse(callbackHTML))

// callbackPage is the loopback sign-in page. State is signed_in, failed,
// expired or mismatch; Code and Detail are provider-supplied.
type callbackPage struct{ State, Label, Name, Code, Detail string }

var callbackStates = map[string]struct{ class, title, status string }{
	"signed_in": {"ok", "Signed in · MCParcel", "Signed in"},
	"failed":    {"bad", "Sign-in failed · MCParcel", "Sign-in failed"},
	"expired":   {"warn", "Link expired · MCParcel", "Link expired"},
	"mismatch":  {"bad", "Wrong browser session · MCParcel", "Wrong browser session"},
}

func renderCallback(w io.Writer, p callbackPage) error {
	s, ok := callbackStates[p.State]
	if !ok {
		return errors.New("unknown sign-in page state")
	}
	return callbackTemplate.Execute(w, struct {
		callbackPage
		Class, Title, Status string
	}{callbackPage{p.State, pageText(p.Label, -1), pageText(p.Name, -1), pageText(p.Code, 64), pageText(p.Detail, 300)}, s.class, s.title, s.status})
}

// pageText drops control characters and keeps at most limit runes (-1: all).
func pageText(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	if r := []rune(s); limit >= 0 && len(r) > limit {
		return string(r[:limit])
	}
	return s
}
