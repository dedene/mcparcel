package ui

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// The server page is a to-do list for one server: its first action is the
// next step for its state, then what else can be done with it. Enter does
// the selected action. Inputs are edited right on the page; every action
// that changes something writes it at once. The page itself is drawn in
// details_view.go.

type actionKind int

const (
	actTurnOn actionKind = iota
	actAccept
	actInput   // set or change an input, on the page
	actProfile // link or change the 1Password profile
	actCommand // its command is not found: look again
	actSignIn
	actStart // start it and load its tools; again after a failure or to test it
	actTools
	actTurnOff
	actEdit
	actRemove
)

type action struct {
	kind        actionKind
	label, note string
	name        string // the input, or the command not installed
}

// inputEdit is the input being edited, on the server page or, for an
// enable Space started, on the field screen.
type inputEdit struct {
	name    string
	value   field
	current string // display of the current url value; empty Enter keeps it
	err     string
}

// openDetails also abandons a pending enable, so a later edit never turns
// that connection on.
func (m *Model) openDetails(id string) {
	m.screen, m.detailID, m.detailCursor, m.status, m.pendingEnable, m.enableOps = screenDetails, id, 0, "", "", nil
	m.editing = false
}

func (m *Model) detailRow() config.EffectiveConnection {
	row, ok := m.live.Effective().Connections[m.detailID]
	if !ok {
		return config.EffectiveConnection{ID: m.detailID}
	}
	return row
}

func sortedInputs(d *config.Connection) []string {
	names := make([]string, 0, len(d.Inputs))
	for name := range d.Inputs {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// actions lists what the page offers for the row: first what its state
// needs next (turn it on, accept a change, each missing input, a profile,
// its command, a sign-in, a start that failed), then its tools, a start or
// test, the inputs and profile to change, turn off, and for a personal
// connection edit and remove.
func (m *Model) actions(row config.EffectiveConnection) []action {
	if !row.Available || row.Definition == nil {
		if row.Enabled {
			return []action{{kind: actTurnOff, label: "Turn off"}}
		}
		return nil
	}
	state := m.live.State()
	st := m.statusOf(row)
	var out []action
	switch {
	case !row.Enabled && len(missingConfig(state, row)) > 0:
		out = append(out, action{kind: actTurnOn, label: "Turn on", note: "asks for what it misses first"})
	case !row.Enabled:
		out = append(out, action{kind: actTurnOn, label: "Turn on"})
	case row.ReviewRequired:
		out = append(out, action{kind: actAccept, label: "Accept the change", note: "mcparcel sync shows what changed"})
	}
	missing, profileMissing := config.MissingConfig(state, row)
	for _, name := range missing {
		out = append(out, action{kind: actInput, name: name, label: "Set " + output.DisplayMetadata(name), note: "not set"})
	}
	if slices.Contains(missingConfig(state, row), profileNeed) {
		out = append(out, action{kind: actProfile, label: "Link 1Password profile", note: "not linked"})
	}
	if command := m.missingCommand(row); command != "" {
		out = append(out, action{kind: actCommand, name: command, label: command + " not installed", note: "install it, then Enter looks again"})
	}
	switch st.kind {
	case stSignIn, stManual:
		a := action{kind: actSignIn, label: "Sign in to " + rowLabel(row), note: "opens your browser"}
		if st.kind == stManual || m.opts.SignIn == nil {
			a.note = "run mcparcel auth " + authName(m.live.Effective(), row.ID)
		}
		out = append(out, a)
	case stFailed:
		out = append(out, action{kind: actStart, label: "Start again", note: st.text})
	}
	out = append(out, action{kind: actTools, label: "Choose tools", note: m.toolsNote(row, st)})
	if row.Enabled && st.kind >= stChecking {
		a := action{kind: actStart, label: "Start", note: "loads its tools"}
		if loaded := m.loaded[row.ID]; loaded.tools != nil {
			a.label, a.note = "Test again", "last ok "+agoText(m.opts.Now().Sub(loaded.at).Seconds())
		}
		out = append(out, a)
	}
	for _, name := range sortedInputs(row.Definition) {
		if !slices.Contains(missing, name) {
			out = append(out, action{kind: actInput, name: name, label: "Change " + output.DisplayMetadata(name), note: m.inputValue(row, name)})
		}
	}
	switch auto := autoLinks(state, row); {
	case row.Definition.CredentialProfile != "" && !profileMissing:
		out = append(out, action{kind: actProfile, label: "Change 1Password profile", note: output.DisplayMetadata(state.Selections.Connections[row.ID].CredentialProfile)})
	case auto != "": // a profile turning it on links: another can be linked first
		out = append(out, action{kind: actProfile, label: "Link 1Password profile", note: output.DisplayMetadata(auto) + " links when on"})
	}
	if row.Enabled {
		out = append(out, action{kind: actTurnOff, label: "Turn off"})
	}
	if row.SourceID == "personal" {
		out = append(out, action{kind: actEdit, label: "Edit"}, action{kind: actRemove, label: "Remove", note: "u brings it back"})
	}
	return out
}

func agoText(seconds float64) string {
	if seconds < 60 {
		return "just now"
	}
	return ages(seconds) + " ago"
}

// toolsNote is what Choose tools says: how many of the loaded tools are
// on, else how many are off, and whether they wait for a sign-in.
func (m *Model) toolsNote(row config.EffectiveConnection, st status) string {
	if m.loaded[row.ID].tools != nil {
		rows := m.toolRows(row.ID)
		on := 0
		for _, r := range rows {
			if r.enabled {
				on++
			}
		}
		return fmt.Sprintf("%d of %d on", on, len(rows))
	}
	note := "all"
	if n := len(m.live.State().Selections.Connections[row.ID].DisabledTools); n > 0 {
		note = plural(n, "tool off", "tools off")
	}
	if st.kind == stSignIn || st.kind == stManual {
		note += ", after sign-in"
	}
	return note
}

// inputValue is an input's value as shown: a url input as host only, a
// default marked as one.
func (m *Model) inputValue(row config.EffectiveConnection, name string) string {
	input := row.Definition.Inputs[name]
	value, ok := m.live.State().Selections.Connections[row.ID].Inputs[name]
	suffix := ""
	if !ok && input.Default != nil {
		value, suffix = *input.Default, " (default)"
	}
	if urlInput(row.Definition, name) {
		return hostOnly(value) + suffix
	}
	return output.DisplayMetadata(value) + suffix
}

// hostOnly shows an HTTP(S) URL as scheme and host, never its path or query.
func hostOnly(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(invalid URL)"
	}
	return output.DisplayMetadata(u.Scheme + "://" + u.Host + "/...")
}

// urlInput reports whether an input is shown as host only: a url input, or
// any input the HTTP URL takes its value from, whatever its kind.
func urlInput(d *config.Connection, name string) bool {
	if d.Inputs[name].Kind == "url" {
		return true
	}
	h := d.Transport.HTTP
	return h != nil && h.URL.Input != nil && h.URL.Input.Input == name
}

func valueText(v config.Value) string {
	switch {
	case v.Literal != nil:
		return output.DisplayMetadata(*v.Literal)
	case v.Input != nil:
		return "input " + output.DisplayMetadata(v.Input.Input)
	}
	return "(secret)"
}

func transportText(row config.EffectiveConnection) string {
	c := row.Connection
	if c == nil {
		c = row.Definition
	}
	if c == nil {
		return "-"
	}
	if s := c.Transport.Stdio; s != nil {
		return fmt.Sprintf("stdio: %s (%s)", valueText(s.Command), plural(len(s.Args), "argument", "arguments"))
	}
	h := c.Transport.HTTP
	if h.URL.Literal == nil {
		return "HTTP: " + valueText(h.URL)
	}
	u, err := url.Parse(*h.URL.Literal)
	if err != nil {
		return "HTTP: (invalid URL)"
	}
	return "HTTP: " + output.DisplayMetadata(u.Host)
}

func (m *Model) detailsKey(msg tea.KeyPressMsg, k string) tea.Cmd {
	if m.editing {
		return m.editKey(msg, k)
	}
	if cmd, ok := m.runKey(k); ok {
		return cmd
	}
	row := m.detailRow()
	actions := m.actions(row)
	switch k {
	case "up":
		m.detailCursor = max(m.detailCursor-1, 0)
	case "down":
		m.detailCursor = min(m.detailCursor+1, max(len(actions)-1, 0))
	case "space":
		m.status, m.detailCursor = "", 0
		return m.toggle(row)
	case "u":
		return m.undo()
	case "s":
		return m.setUpOne(row)
	case "i":
		m.technical = !m.technical
	case "?":
		m.openHelp()
	case "q":
		return m.quit()
	case "left", "esc":
		m.screen, m.status = screenList, ""
		m.clamp()
	case "enter":
		if m.detailCursor < len(actions) {
			return m.act(row, actions[m.detailCursor])
		}
	case "right":
		// Right only goes deeper. A stray arrow must never sign in, start,
		// turn on or remove anything.
		if m.detailCursor < len(actions) && opensPage(actions[m.detailCursor].kind) {
			return m.act(row, actions[m.detailCursor])
		}
	}
	return nil
}

// opensPage reports whether an action only opens another page.
func opensPage(k actionKind) bool { return k == actTools || k == actEdit }

// act does an action. One that changes the row moves the cursor to the
// top, where the next step then shows.
func (m *Model) act(row config.EffectiveConnection, a action) tea.Cmd {
	m.status = ""
	switch a.kind {
	case actTurnOn, actAccept, actTurnOff:
		m.detailCursor = 0
		return m.toggle(row)
	case actInput:
		m.startInput(row, a.name)
		m.editing = true
	case actProfile:
		if names := m.profileNames(); len(names) == 1 {
			m.detailCursor = 0
			return m.apply(change{ops: []op{{kind: opProfile, id: row.ID, value: names[0]}}}, func(err error) tea.Cmd {
				m.status = linkedText(names[0])
				if err != nil {
					m.status = m.inline(err)
				}
				return nil
			})
		}
		m.screen, m.profileCursor = screenProfile, 0
	case actCommand:
		command := a.name
		m.lookAgain(row)
		if m.missingCommand(row) == "" {
			m.status, m.detailCursor = "Found "+command+".", 0
		} else {
			m.status = command + " is still not installed: it is not found on PATH."
		}
	case actSignIn:
		if m.statusOf(row).kind == stManual || m.opts.SignIn == nil {
			m.status = "Setup can't sign in here: " + a.note + " in a terminal."
			return nil
		}
		return m.setUpOne(row)
	case actStart:
		return m.setUpOne(row)
	case actTools:
		return m.openTools()
	case actEdit:
		m.openEditForm(row)
	case actRemove:
		return m.remove(row)
	}
	return nil
}

// editKey edits an input on the server page: Enter writes it at once and
// moves on to the next step; Esc leaves it as it was.
func (m *Model) editKey(msg tea.KeyPressMsg, k string) tea.Cmd {
	switch k {
	case "esc":
		m.editing = false
	case "enter":
		value := m.input.value.String()
		if value == "" && m.input.current != "" {
			m.editing, m.status = false, "Kept the current value."
			return nil
		}
		name := m.input.name
		return m.apply(change{ops: []op{{kind: opInput, id: m.detailID, name: name, value: value}}}, func(err error) tea.Cmd {
			if err != nil {
				m.input.err = m.inline(err)
				return nil
			}
			m.editing, m.detailCursor, m.status = false, 0, output.DisplayMetadata(name)+" is set."
			return nil
		})
	case "backspace":
		m.input.value.backspace()
	case "ctrl+u":
		m.input.value.clear()
	default:
		m.input.value.insert(typed(msg))
	}
	return nil
}

// remove removes a personal connection as mcparcel local remove does: its
// definition goes, its selection stays. u brings it back.
func (m *Model) remove(row config.EffectiveConnection) tea.Cmd {
	label := rowLabel(row)
	stored := m.live.State().Personal.Connections[strings.TrimPrefix(row.ID, "local:")]
	return m.apply(change{ops: []op{{kind: opLocalRemove, id: row.ID, was: definitionPrint(stored)}}}, func(err error) tea.Cmd {
		if err != nil {
			m.status = m.inline(err)
			return nil
		}
		m.screen, m.status = screenList, "Removed "+label+". u brings it back."
		m.clamp()
		return nil
	})
}

// inline describes an edit error for display next to the field: a
// configuration error as its field path and reason (never the value),
// anything else through Options.Describe.
func (m *Model) inline(err error) string {
	if errors.As(err, new(elsewhereError)) || errors.Is(err, errChangedElsewhere) {
		return "This changed elsewhere meanwhile, so the change no longer applies."
	}
	if errors.Is(err, config.ErrConfig) {
		if detail, ok := strings.CutPrefix(err.Error(), config.ErrConfig.Error()+": "); ok {
			return "Invalid: " + output.DisplayMetadata(detail) + "."
		}
	}
	return m.failure(err).Message
}
