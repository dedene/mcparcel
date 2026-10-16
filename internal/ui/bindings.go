package ui

import (
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/edit"
	"github.com/dedene/mcparcel/internal/output"
)

// Binding screens: editing one input (config input set), for an enable
// Space started on a row that misses it, and choosing a credential profile
// (config profile bind), from the server page or for such an enable. From
// the server page the profile is written at once; for Space the answers
// are written with the enable, in one change, once nothing is missing.

// startInput begins editing one input: string and path inputs are
// pre-filled; a url input (see urlInput) starts empty and shows only the
// current host.
func (m *Model) startInput(row config.EffectiveConnection, name string) {
	m.input = inputEdit{name: name}
	current, ok := m.live.State().Selections.Connections[row.ID].Inputs[name]
	switch {
	case urlInput(row.Definition, name) && ok:
		m.input.current = hostOnly(current)
	case ok:
		m.input.value.insert(current)
	}
}

func (m *Model) fieldPage() []string {
	row := m.detailRow()
	w, _ := m.size()
	name := output.DisplayMetadata(m.input.name)
	body := []string{"Input " + name}
	if row.Definition != nil {
		// Two lines at most, so the Value line stays on screen.
		if d := row.Definition.Inputs[m.input.name].Description; d != "" {
			body = append(body, wrapAtMost(output.DisplayMetadata(firstLine(d)), w, 2)...)
		}
	}
	body = append(body, "", "Value: "+m.input.value.render(w-7), "", m.input.err)
	return m.page(m.crumb(row)+" / "+name, body, -1, "Enter: use it   Ctrl+U: clear   Esc: cancel")
}

func (m *Model) fieldKey(msg tea.KeyPressMsg, k string) tea.Cmd {
	switch k {
	case "esc":
		m.stayOff()
	case "enter":
		o := op{kind: opInput, id: m.detailID, name: m.input.name, value: m.input.value.String()}
		if err := m.fill(o); err != nil {
			m.input.err = m.inline(err)
			return nil
		}
		return m.continueEnable("")
	case "backspace":
		m.input.value.backspace()
	case "ctrl+u":
		m.input.value.clear()
	default:
		m.input.value.insert(typed(msg))
	}
	return nil
}

func (m *Model) profileNames() []string {
	names := make([]string, 0, len(m.live.State().Local.CredentialProfiles))
	for name := range m.live.State().Local.CredentialProfiles {
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
		return m.page(m.crumb(row)+" / 1Password profile", body, -1, "Esc: back")
	}
	bound := m.live.State().Selections.Connections[row.ID].CredentialProfile
	body := []string{"Choose a 1Password profile for " + rowLabel(row) + ".", ""}
	focus := -1
	for i, name := range names {
		prefix := "  "
		if i == m.profileCursor {
			prefix, focus = "> ", len(body)
		}
		text := prefix + output.DisplayMetadata(name)
		if name == bound {
			text += " (linked)"
		}
		body = append(body, text)
	}
	return m.page(m.crumb(row)+" / 1Password profile", body, focus, "Up/Down: profile   Enter: link   Esc: back")
}

func (m *Model) profileKey(k string) tea.Cmd {
	names := m.profileNames()
	switch k {
	case "up":
		m.profileCursor = max(m.profileCursor-1, 0)
	case "down":
		m.profileCursor = min(m.profileCursor+1, max(len(names)-1, 0))
	case "esc", "left":
		if m.pending() {
			m.stayOff()
			return nil
		}
		m.screen = screenDetails
	case "enter", "right":
		if m.profileCursor >= len(names) {
			return nil
		}
		name := names[m.profileCursor]
		o := op{kind: opProfile, id: m.detailID, value: name}
		if m.pending() {
			if err := m.fill(o); err != nil {
				m.status = m.inline(err)
				return nil
			}
			return m.continueEnable(linkedText(name) + " ")
		}
		return m.apply(change{ops: []op{o}}, func(err error) tea.Cmd {
			if err != nil {
				m.status = m.inline(err)
				return nil
			}
			m.screen, m.detailCursor, m.status = screenDetails, 0, linkedText(name)
			return nil
		})
	}
	return nil
}

// pending reports whether the field or profile screen belongs to an enable
// that Space started (see toggle).
func (m *Model) pending() bool { return m.pendingEnable != "" && m.pendingEnable == m.detailID }

// enableState is the state shown with what the pending enable filled in so
// far, and the row it turns on.
func (m *Model) enableState() (config.State, config.EffectiveConnection, error) {
	state, err := config.CloneState(m.live.State())
	if err != nil {
		return config.State{}, config.EffectiveConnection{}, err
	}
	for _, o := range m.enableOps {
		if err := o.apply(&state); err != nil {
			return config.State{}, config.EffectiveConnection{}, err
		}
	}
	effective, err := config.Resolve(state)
	if err != nil {
		return config.State{}, config.EffectiveConnection{}, err
	}
	row, ok := effective.Connections[m.pendingEnable]
	if !ok {
		return config.State{}, config.EffectiveConnection{}, config.ErrNotFound
	}
	return state, row, nil
}

// fill adds an answer to the pending enable, once it applies.
func (m *Model) fill(o op) error {
	m.enableOps = append(m.enableOps, o)
	if _, _, err := m.enableState(); err != nil {
		m.enableOps = m.enableOps[:len(m.enableOps)-1]
		return err
	}
	return nil
}

// continueEnable moves a pending enable on after each answer: it opens the
// first missing input, then the credential profile, and once nothing is
// missing writes the answers and the enable as one change. A profile with
// the required ID is left to the enable itself; a single local profile is
// linked without asking; with more the picker opens, and with none its
// hint. note leads the status.
func (m *Model) continueEnable(note string) tea.Cmd {
	state, row, err := m.enableState()
	if err != nil {
		m.endEnable(note + m.inline(err))
		return nil
	}
	inputs, profile := config.MissingConfig(state, row)
	linked := edit.AutoProfile(state, row)
	profile = profile && linked == ""
	names := m.profileNames()
	switch {
	case len(inputs) > 0:
		m.startInput(row, inputs[0])
		m.screen = screenField
	case profile && len(names) == 1:
		if err := m.fill(op{kind: opProfile, id: row.ID, value: names[0]}); err != nil {
			m.endEnable(note + m.inline(err))
			return nil
		}
		return m.continueEnable(note + linkedText(names[0]) + " ")
	case profile:
		m.screen, m.profileCursor = screenProfile, 0
	default:
		ops := append(slices.Clone(m.enableOps), op{kind: opEnabled, id: row.ID, on: true})
		if linked != "" {
			note += linkedText(linked) + " "
		}
		return m.apply(change{ops: ops, accept: []string{row.ID}}, func(err error) tea.Cmd {
			if err != nil {
				m.endEnable(note + m.inline(err))
				return nil
			}
			m.endEnable(note + rowLabel(row) + " is on.")
			m.flash(row.ID)
			return nil
		})
	}
	esc := "Esc keeps it off."
	if row.Enabled {
		esc = "Esc leaves it unchanged."
	}
	m.status = note + rowLabel(row) + " needs " + needsText(missingConfig(state, row)) + ". " + esc
	return nil
}

// stayOff abandons a pending enable and what it filled in. A row that is on
// with a review to accept keeps it, review included.
func (m *Model) stayOff() {
	row := m.detailRow()
	if row.Enabled {
		m.endEnable(rowLabel(row) + " is unchanged.")
		return
	}
	m.endEnable(rowLabel(row) + " stays off.")
}

// endEnable ends a pending enable where it started: on the server page, or
// on the list with the cursor on its row.
func (m *Model) endEnable(status string) {
	id := m.pendingEnable
	m.pendingEnable, m.enableOps, m.status = "", nil, status
	if m.enableBack == screenDetails {
		m.screen, m.detailID, m.detailCursor = screenDetails, id, 0
		return
	}
	m.screen = screenList
	if i := slices.IndexFunc(m.rows(), func(row config.EffectiveConnection) bool { return row.ID == id }); i >= 0 {
		m.cursor = i
	}
	m.clamp()
}
