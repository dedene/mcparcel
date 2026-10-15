package ui

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/edit"
	"github.com/dedene/mcparcel/internal/output"
)

// Binding screens opened from details, or by Space on a row that misses
// configuration: editing one input (config input set) and choosing a
// credential profile (config profile bind).

// openField edits one input: string and path inputs are pre-filled; a url
// input (see urlInput) starts empty and shows only the current host.
func (m *Model) openField(row config.EffectiveConnection, name string) {
	input := row.Definition.Inputs[name]
	m.input = inputEdit{name: name, kind: input.Kind}
	current, ok := m.draft.State().Selections.Connections[row.ID].Inputs[name]
	switch {
	case urlInput(row.Definition, name) && ok:
		m.input.current = hostOnly(current)
	case ok:
		m.input.value.insert(current)
	}
	m.screen, m.status = screenField, ""
}

func (m *Model) fieldPage() []string {
	row := m.detailRow()
	w, _ := m.size()
	name := output.DisplayMetadata(m.input.name)
	body := []string{fmt.Sprintf("Input %s (%s)", name, m.input.kind)}
	if row.Definition != nil {
		// Two lines at most, so the Value line stays on screen.
		if d := row.Definition.Inputs[m.input.name].Description; d != "" {
			body = append(body, wrapAtMost(output.DisplayMetadata(firstLine(d)), w, 2)...)
		}
	}
	help := "Enter: apply   Ctrl+U: clear   Esc: discard"
	if m.input.current != "" {
		body = append(body, "Current: "+m.input.current)
		help = "Enter: apply (empty keeps the current value)   Esc: discard"
	}
	body = append(body, "", "Value: "+m.input.value.render(w-7), "", m.input.err)
	return m.page("MCParcel / Setup / "+rowLabel(row)+" / "+name, body, -1, help)
}

func (m *Model) fieldKey(msg tea.KeyPressMsg, k string) {
	switch k {
	case "esc":
		if m.pending() {
			m.stayOff()
			return
		}
		m.screen = screenDetails
	case "enter":
		value := m.input.value.String()
		if value == "" && m.input.current != "" {
			m.screen, m.status = screenDetails, "Kept the current value."
			return
		}
		if err := m.draft.SetInput(m.detailID, m.input.name, value); err != nil {
			m.input.err = m.inline(err)
			return
		}
		if m.pending() {
			m.continueEnable("")
			return
		}
		m.screen = screenDetails
	case "backspace":
		m.input.value.backspace()
	case "ctrl+u":
		m.input.value.clear()
	default:
		m.input.value.insert(typed(msg))
	}
}

func (m *Model) profileNames() []string {
	names := make([]string, 0, len(m.draft.State().Local.CredentialProfiles))
	for name := range m.draft.State().Local.CredentialProfiles {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func (m *Model) profilePage() []string {
	row := m.detailRow()
	names := m.profileNames()
	if len(names) == 0 {
		body := []string{"No profiles yet. Create one: mcparcel config profile set <name> --file <profile.json>."}
		return m.page("MCParcel / Setup / "+rowLabel(row)+" / Credential profile", body, -1, "Esc: back")
	}
	bound := m.draft.State().Selections.Connections[row.ID].CredentialProfile
	body := []string{"Choose a credential profile for " + rowLabel(row) + ".", ""}
	focus := -1
	for i, name := range names {
		prefix := "  "
		if i == m.profileCursor {
			prefix, focus = "> ", len(body)
		}
		text := prefix + output.DisplayMetadata(name)
		if name == bound {
			text += " (bound)"
		}
		body = append(body, text)
	}
	return m.page("MCParcel / Setup / "+rowLabel(row)+" / Credential profile", body, focus, "Up/Down: profile   Enter: bind   Esc: back")
}

func (m *Model) profileKey(k string) {
	names := m.profileNames()
	switch k {
	case "up":
		m.profileCursor = max(m.profileCursor-1, 0)
	case "down":
		m.profileCursor = min(m.profileCursor+1, max(len(names)-1, 0))
	case "esc":
		if m.pending() {
			m.stayOff()
			return
		}
		m.screen = screenDetails
	case "enter":
		if m.profileCursor >= len(names) {
			return
		}
		if err := m.draft.BindProfile(m.detailID, names[m.profileCursor]); err != nil {
			m.status = m.inline(err)
			return
		}
		if m.pending() {
			m.continueEnable(linkedText(names[m.profileCursor]) + " ")
			return
		}
		m.screen = screenDetails
	}
}

// pending reports whether the field or profile screen belongs to an enable
// that Space started (see toggle).
func (m *Model) pending() bool { return m.pendingEnable != "" && m.pendingEnable == m.detailID }

// continueEnable moves a pending enable on after each fill: it opens the first
// missing input, then the credential profile, and turns the connection on
// once nothing is missing. A profile with the required ID is left to the
// enable itself; a single local profile is bound without asking; with more
// the picker opens, and with none its hint. note leads the status.
func (m *Model) continueEnable(note string) {
	row := m.detailRow()
	state := m.draft.State()
	inputs, profile := config.MissingConfig(state, row)
	linked := edit.AutoProfile(state, row)
	profile = profile && linked == ""
	names := m.profileNames()
	switch {
	case len(inputs) > 0:
		m.openField(row, inputs[0])
	case profile && len(names) == 1:
		if err := m.draft.BindProfile(row.ID, names[0]); err != nil {
			m.endEnable(note + m.inline(err))
			return
		}
		m.continueEnable(note + linkedText(names[0]) + " ")
		return
	case profile:
		m.screen, m.profileCursor = screenProfile, 0
	default:
		if err := m.draft.SetEnabled(row.ID, true); err != nil {
			m.endEnable(note + m.failure(err).Message)
			return
		}
		if linked != "" {
			note += linkedText(linked) + " "
		}
		m.endEnable(note + rowLabel(row) + " is on.")
		m.flash(row.ID)
		return
	}
	esc := "Esc keeps it off."
	if row.Enabled {
		esc = "Esc leaves it unchanged."
	}
	m.status = note + rowLabel(row) + " needs: " + strings.Join(missingConfig(state, row), ", ") + ". " + esc
}

// stayOff abandons a pending enable. A row that is on with a review to accept
// keeps it, review included.
func (m *Model) stayOff() {
	row := m.detailRow()
	if row.Enabled {
		m.endEnable(rowLabel(row) + " is unchanged.")
		return
	}
	m.endEnable(rowLabel(row) + " stays off.")
}

// endEnable ends a pending enable on the list, with the cursor on its row.
func (m *Model) endEnable(status string) {
	id := m.pendingEnable
	m.pendingEnable, m.screen, m.focus, m.status = "", screenList, focusList, status
	if i := slices.IndexFunc(m.rows(), func(row config.EffectiveConnection) bool { return row.ID == id }); i >= 0 {
		m.cursor = i
	}
	m.clamp()
}
