package ui

import (
	"slices"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

// Needs come from the definition's metadata and the selection only: the
// transport, its auth, the credential profile requirement and the inputs.
func TestNeedsFollowTransportAuthProfileInputs(t *testing.T) {
	state := config.State{}
	state.Local.CredentialProfiles = map[string]config.Profile{"team": {Mode: "desktop"}, "work": {Mode: "desktop"}}
	state.Selections.Connections = map[string]config.Selection{"bound": {CredentialProfile: "work", Inputs: map[string]string{"root": "/srv"}}, "linked": {CredentialProfile: "team"}}
	row := func(id string, c config.Connection, blockers ...string) config.EffectiveConnection {
		return config.EffectiveConnection{ID: id, SourceID: "personal", Definition: &c, Connection: &c, Available: true, Blockers: blockers}
	}
	command := func(v config.Value) config.Transport { return config.Transport{Stdio: &config.Stdio{Command: v}} }
	http := config.Transport{HTTP: &config.HTTP{URL: config.Literal("https://mcp.example/")}}
	type check struct {
		name, note string
		role       role
		done       bool
	}
	for _, tc := range []struct {
		name      string
		row       config.EffectiveConnection
		list      []string
		state     rowState
		checklist []check
	}{
		{
			name: "stdio shows its command", row: row("npx", config.Connection{Transport: command(config.Literal("/opt/homebrew/bin/npx"))}),
			list: []string{"npx"}, state: rowOff, checklist: []check{{"npx", "runs on this device", roleDim, false}},
		},
		{
			name: "a command from an input", row: row("bin", config.Connection{Inputs: map[string]config.Input{"bin": {Kind: "path"}}, Transport: command(config.Value{Input: &config.InputRef{Input: "bin"}})}, "config_required"),
			list: []string{"input bin", "local command"}, state: rowNeedsConfig,
			checklist: []check{{"input bin", "not set", roleWarn, false}, {"local command", "runs on this device", roleDim, false}},
		},
		{
			name: "OAuth signs in", row: row("oauth", config.Connection{Transport: http, Auth: &config.OAuth{Type: "oauth"}}),
			list: []string{"sign-in"}, state: rowOff, checklist: []check{{"sign-in", "in a browser on first connect", roleAccent, false}},
		},
		{
			name: "authorization code signs in", row: row("code", config.Connection{Transport: http, Auth: &config.OAuth{Type: "oauth", Grant: config.GrantAuthorizationCode}}),
			list: []string{"sign-in"}, state: rowOff, checklist: []check{{"sign-in", "in a browser on first connect", roleAccent, false}},
		},
		{
			name: "client credentials need no sign-in", row: row("machine", config.Connection{Transport: http, Auth: &config.OAuth{Type: "oauth", Grant: config.GrantClientCredentials}}),
			state: rowOff,
		},
		{name: "plain HTTP needs nothing", row: row("plain", config.Connection{Transport: http}), state: rowOff},
		{
			name: "a missing profile", row: row("vault", config.Connection{CredentialProfile: "vault", Transport: http}, "config_required"),
			list: []string{"1Password"}, state: rowNeedsConfig, checklist: []check{{"1Password vault", "not linked", roleWarn, false}},
		},
		{
			name: "a profile Space links by itself is ready", row: row("auto", config.Connection{CredentialProfile: "team", Transport: http}, "config_required"),
			list: []string{"1Password"}, state: rowOff, checklist: []check{{"1Password team", "links automatically", roleInfo, true}},
		},
		{
			name: "a bound profile and a set input", row: row("bound", config.Connection{CredentialProfile: "team", Inputs: map[string]config.Input{"root": {Kind: "path"}}, Transport: http}),
			list: []string{"1Password"}, state: rowOff,
			checklist: []check{{"input root", "set", roleDim, true}, {"1Password team", "linked to work", roleInfo, true}},
		},
		{
			// The profile with the requirement's name reads "linked", and the list
			// shows what is left (the sign-in) before what is done.
			name: "a linked profile and a sign-in", row: row("linked", config.Connection{CredentialProfile: "team", Transport: http, Auth: &config.OAuth{Type: "oauth"}}),
			list: []string{"sign-in", "1Password"}, state: rowOff,
			checklist: []check{{"1Password team", "linked", roleInfo, true}, {"sign-in", "in a browser on first connect", roleAccent, false}},
		},
	} {
		look := lookOf(state, tc.row)
		var list []string
		for _, n := range look.listNeeds() {
			list = append(list, n.text)
		}
		if !slices.Equal(list, tc.list) || look.state != tc.state {
			t.Errorf("%s: list %q state %d, want %q %d", tc.name, list, look.state, tc.list, tc.state)
		}
		var got []check
		for _, n := range look.checklist() {
			name := n.text
			if n.name != "" {
				name = n.name
			}
			got = append(got, check{name, n.note, n.role, n.done})
		}
		if !slices.Equal(got, tc.checklist) {
			t.Errorf("%s: checklist %+v, want %+v", tc.name, got, tc.checklist)
		}
	}

	review := row("review", config.Connection{Transport: http})
	review.Enabled, review.ReviewRequired = true, true
	gone := config.EffectiveConnection{ID: "local:gone", SourceID: "personal", Enabled: true}
	for _, tc := range []struct {
		row   config.EffectiveConnection
		state rowState
		first need
	}{
		{review, rowReview, need{kind: needStatus, text: "review", role: roleWarn}},
		{gone, rowUnavailable, need{kind: needStatus, text: "unavailable", role: roleBad}},
	} {
		look := lookOf(state, tc.row)
		if look.state != tc.state || look.ready() || len(look.listNeeds()) == 0 || look.listNeeds()[0] != tc.first || len(look.checklist()) != 0 {
			t.Errorf("%s: %+v", tc.row.ID, look)
		}
	}
}

// Every state has its glyph in both sets, and the ASCII set draws only ASCII.
func TestMarksPerState(t *testing.T) {
	for _, tc := range []struct {
		look         rowLook
		ascii, uni   string
		role         role
		label        string
		attentionToo bool
	}{
		{rowLook{state: rowOn, on: true}, "[x]", "●", roleOK, "On", false},
		{rowLook{state: rowOff}, "[ ]", "○", roleDim, "Off", false},
		{rowLook{state: rowNeedsConfig}, "[?]", "◌", roleDim, "Off, configuration required", false},
		{rowLook{state: rowNeedsConfig, on: true}, "[~]", "◐", roleWarn, "On, configuration required", true},
		{rowLook{state: rowReview, on: true}, "[!]", "!", roleWarn, "On, review required", true},
		{rowLook{state: rowUnavailable, on: true}, "[-]", "✕", roleBad, "On, unavailable", true},
		{rowLook{state: rowUnavailable}, "[ ]", "○", roleDim, "Off, unavailable", false},
	} {
		ascii, r := tc.look.mark(asciiGlyphs)
		uni, _ := tc.look.mark(unicodeGlyphs)
		if ascii != tc.ascii || uni != tc.uni || r != tc.role || tc.look.label() != tc.label || tc.look.on && tc.look.ready() == tc.attentionToo {
			t.Errorf("%+v: %q %q %d %q", tc.look, ascii, uni, r, tc.look.label())
		}
	}
}

// seedMarks turns Notes on without its configuration, so the fixture shows
// every mark: on, off, on with configuration missing, review and unavailable.
func seedMarks(t *testing.T, store *config.Store) {
	seedFixture(t, store)
	mustUpdate(t, store, func(s *config.State) { s.Selections.Connections[idNotes] = config.Selection{Enabled: true} })
}

func TestUnicodeFalseRendersASCIIMarks(t *testing.T) {
	for _, tc := range []struct {
		unicode                                           bool
		on, off, pending, needsConfig, review, gone, help string
	}{
		{false, "[x] Excalidraw", "[ ] Paper", "[~] Notes", "[?] Notes", "[!] Reviewer", "[-] local:gone", "[?] off, needs configuration"},
		{true, "● Excalidraw", "○ Paper", "◐ Notes", "◌ Notes", "! Reviewer", "✕ local:gone", "◌ off, needs configuration"},
	} {
		h := newHarness(t, seedMarks, 120, 35, func(o *Options) { o.Color, o.Unicode = true, tc.unicode })
		h.selectTab("Other")
		h.contains(tc.on, tc.gone)
		h.selectTab("Development")
		h.contains(tc.off, tc.review)
		h.selectTab("Research")
		h.focused(tc.pending)
		h.press("space")
		h.focused(tc.needsConfig)
		h.press("?")
		for range 40 {
			h.press("down")
		}
		h.contains(tc.help)
		if !tc.unicode {
			for _, r := range h.view() {
				if r > 126 {
					t.Fatalf("non-ASCII %q", r)
				}
			}
		}
		h.press("esc")
		if !tc.unicode && strings.ContainsAny(h.plain(), "●○◐◌✕◆✓·‹›│") {
			t.Fatal(h.plain())
		}
	}
}
