package ui

import (
	"slices"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

// The To do comes from the definition's metadata and the selection, and
// from what the Status knows: the transport, its auth, the credential
// profile requirement and the inputs.
func TestToDoFollowsTransportAuthProfileInputs(t *testing.T) {
	state := config.State{}
	state.Local.CredentialProfiles = map[string]config.Profile{"team": {Mode: "desktop"}, "work": {Mode: "desktop"}}
	state.Selections.Connections = map[string]config.Selection{"bound": {CredentialProfile: "work", Inputs: map[string]string{"root": "/srv"}}, "linked": {CredentialProfile: "team"}}
	m := &Model{live: &live{state: state}, theme: newTheme(Options{}, nil)}
	row := func(id string, c config.Connection, blockers ...string) config.EffectiveConnection {
		return config.EffectiveConnection{ID: id, SourceID: "personal", Definition: &c, Connection: &c, Available: true, Blockers: blockers}
	}
	command := func(v config.Value) config.Transport { return config.Transport{Stdio: &config.Stdio{Command: v}} }
	http := config.Transport{HTTP: &config.HTTP{URL: config.Literal("https://mcp.example/")}}
	on, tools := "[*] Turn it on", "[.] Tools load on first use"
	browser := "[.] Sign in when it first starts"
	for _, tc := range []struct {
		name  string
		row   config.EffectiveConnection
		state statusKind
		todo  []string
	}{
		{name: "stdio", row: row("npx", config.Connection{Transport: command(config.Literal("/opt/homebrew/bin/npx"))}), state: stNone, todo: []string{on, tools}},
		{
			name: "a command from an input", row: row("bin", config.Connection{Inputs: map[string]config.Input{"bin": {Kind: "path"}}, Transport: command(config.Value{Input: &config.InputRef{Input: "bin"}})}, "config_required"),
			state: stInput, todo: []string{on, "[*] Set bin", tools},
		},
		{name: "OAuth signs in", row: row("oauth", config.Connection{Transport: http, Auth: &config.OAuth{Type: "oauth"}}), state: stNone, todo: []string{on, browser, tools}},
		{
			name: "authorization code signs in", row: row("code", config.Connection{Transport: http, Auth: &config.OAuth{Type: "oauth", Grant: config.GrantAuthorizationCode}}),
			state: stNone, todo: []string{on, browser, tools},
		},
		{
			name: "client credentials need no sign-in", row: row("machine", config.Connection{Transport: http, Auth: &config.OAuth{Type: "oauth", Grant: config.GrantClientCredentials}}),
			state: stNone, todo: []string{on, tools},
		},
		{
			name: "a missing profile", row: row("vault", config.Connection{CredentialProfile: "vault", Transport: http}, "config_required"),
			state: stProfile, todo: []string{on, "[*] Link a 1Password profile", tools},
		},
		{
			name: "a profile Space links by itself is ready", row: row("auto", config.Connection{CredentialProfile: "team", Transport: http}, "config_required"),
			state: stNone, todo: []string{on, "[+] 1Password: links team when on", tools},
		},
		{
			name: "a bound profile and a set input", row: row("bound", config.Connection{CredentialProfile: "team", Inputs: map[string]config.Input{"root": {Kind: "path"}}, Transport: http}),
			state: stNone, todo: []string{on, "[+] root is set", "[+] 1Password: work", tools},
		},
		{
			name: "a linked profile and a sign-in", row: row("linked", config.Connection{CredentialProfile: "team", Transport: http, Auth: &config.OAuth{Type: "oauth"}}),
			state: stNone, todo: []string{on, "[+] 1Password: team", browser, tools},
		},
	} {
		if got := m.statusOf(tc.row).kind; got != tc.state {
			t.Errorf("%s: state %d, want %d", tc.name, got, tc.state)
		}
		var got []string
		for _, item := range m.todo(tc.row) {
			got = append(got, m.todoLine(item, "", 80, false))
		}
		if !slices.Equal(got, tc.todo) {
			t.Errorf("%s: To do %q, want %q", tc.name, got, tc.todo)
		}
	}

	review := row("review", config.Connection{Transport: http})
	review.Enabled, review.ReviewRequired = true, true
	if got := m.statusOf(review).kind; got != stReview || !slices.Contains(lines(m, review), "[*] Accept the change") {
		t.Errorf("review: %d %q", got, lines(m, review))
	}
	gone := config.EffectiveConnection{ID: "local:gone", SourceID: "personal", Enabled: true}
	if got := m.statusOf(gone).kind; got != stUnavailable || len(m.todo(gone)) != 0 {
		t.Errorf("unavailable: %d %q", got, lines(m, gone))
	}
}

func lines(m *Model, row config.EffectiveConnection) []string {
	var out []string
	for _, item := range m.todo(row) {
		out = append(out, m.todoLine(item, "", 80, false))
	}
	return out
}

// Every Status has its mark in both glyph sets, and the ASCII set draws
// only ASCII: on with nothing to do, on with a step left, on but it cannot
// run, and off.
func TestMarksPerStatus(t *testing.T) {
	for _, tc := range []struct {
		status     status
		ascii, uni string
		role       role
		step       bool
	}{
		{status{on: true, kind: stFirstUse}, "[x]", "●", roleOK, false},
		{status{on: true, kind: stTools}, "[x]", "●", roleOK, false},
		{status{on: true, kind: stSignInUnknown}, "[x]", "●", roleOK, false},
		{status{}, "[ ]", "○", roleDim, false},
		{status{kind: stInput}, "[ ]", "○", roleDim, false},
		{status{on: true, kind: stInput}, "[~]", "◐", roleWarn, true},
		{status{on: true, kind: stReview}, "[~]", "◐", roleWarn, true},
		{status{on: true, kind: stSignIn}, "[~]", "◐", roleWarn, true},
		{status{on: true, kind: stUnavailable}, "[-]", "✕", roleBad, true},
		{status{on: true, kind: stCommand}, "[-]", "✕", roleBad, true},
		{status{on: true, kind: stFailed}, "[-]", "✕", roleBad, true},
	} {
		h := newHarness(t, nil, 80, 24)
		ascii, r := h.m.mark(tc.status)
		h.m.theme.glyph = unicodeGlyphs
		uni, _ := h.m.mark(tc.status)
		if ascii != tc.ascii || uni != tc.uni || r != tc.role || tc.status.step() != tc.step {
			t.Errorf("%+v: %q %q %d %v", tc.status, ascii, uni, r, tc.status.step())
		}
	}
}

// seedMarks turns Notes on without its input and profile, so the fixture
// shows every mark: on, off, on with something missing, review and
// unavailable.
func seedMarks(t *testing.T, store *config.Store) {
	seedFixture(t, store)
	mustUpdate(t, store, func(s *config.State) { s.Selections.Connections[idNotes] = config.Selection{Enabled: true} })
}

func TestUnicodeFalseRendersASCIIMarks(t *testing.T) {
	for _, tc := range []struct {
		unicode                                        bool
		on, off, pending, offNotes, review, gone, help string
	}{
		{false, "[x] Excalidraw", "[ ] Paper", "[~] Notes", "[ ] Notes", "[~] Reviewer", "[-] local:gone", "[-] on, can't run"},
		{true, "● Excalidraw", "○ Paper", "◐ Notes", "○ Notes", "◐ Reviewer", "✕ local:gone", "✕ on, can't run"},
	} {
		h := newHarness(t, seedMarks, 120, 35, func(o *Options) { o.Color, o.Unicode = true, tc.unicode })
		h.selectTab("Other")
		h.contains(tc.on, tc.gone)
		h.selectTab("Development")
		h.contains(tc.off, tc.review)
		h.selectTab("Research")
		h.focused(tc.pending)
		h.press("space")
		h.focused(tc.offNotes, "needs workspace")
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
		if !tc.unicode && strings.ContainsAny(h.plain(), "●○◐✕◆✓·‹›│←→") {
			t.Fatal(h.plain())
		}
	}
}

// The server page leaves out a To do step an action above it takes; the
// pane leaves out what its state line says. Facts and steps no action takes
// stay.
func TestToDoLeavesOutWhatIsSaidAbove(t *testing.T) {
	m := &Model{theme: newTheme(Options{}, nil)}
	items := []todoItem{
		{mark: todoStep, text: "Turn it on", does: actTurnOn, state: true},
		{mark: todoStep, text: "Set workspace", does: actInput, name: "workspace"},
		{mark: todoStep, text: "Set root", does: actInput, name: "root"},
		{mark: todoDone, text: "token is set"},
		{mark: todoStep, text: "Sign in with your browser", does: actSignIn},
		{mark: todoStep, text: "Could not start: timed out", does: actStart, state: true},
		{mark: todoLater, text: "Tools load on first use"},
	}
	text := func(items []todoItem) []string {
		var out []string
		for _, item := range items {
			out = append(out, m.todoLine(item, "", 80, false))
		}
		return out
	}
	actions := []action{{kind: actTurnOn}, {kind: actInput, name: "workspace"}, {kind: actStart}, {kind: actTools}}
	want := []string{"[*] Set root", "[+] token is set", "[*] Sign in with your browser", "[.] Tools load on first use"}
	if got := text(pageTodo(items, actions)); !slices.Equal(got, want) {
		t.Errorf("page %q, want %q", got, want)
	}
	want = []string{"[*] Set workspace", "[*] Set root", "[+] token is set", "[*] Sign in with your browser", "[.] Tools load on first use"}
	if got := text(paneTodo(items)); !slices.Equal(got, want) {
		t.Errorf("pane %q, want %q", got, want)
	}
	if len(items) != 7 || items[0].text != "Turn it on" {
		t.Fatalf("the filters changed the To do: %q", text(items))
	}
	if got := pageTodo(items[:1], actions); len(got) != 0 {
		t.Fatalf("%q", text(got))
	}
}
