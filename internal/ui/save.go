package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/dedene/mcparcel/internal/output"
)

// Save, reload and cancel. Store access runs inside the returned commands; the
// messages they return update the model.

func (m *Model) save() tea.Cmd {
	save := m.draft.prepareSave()
	if save == nil {
		m.status = "Nothing to save."
		return nil
	}
	m.busy, m.status = true, "Saving..."
	ctx, store := m.ctx, m.store
	return func() tea.Msg {
		next, err := save(ctx, store)
		return savedMsg{state: next, err: err}
	}
}

func (m *Model) saved(msg savedMsg) tea.Cmd {
	m.busy = false
	err := msg.err
	if err == nil {
		err = m.draft.commit(msg.state)
	}
	var failure *output.Error
	if err == nil {
		m.saves, m.unconfirmed = m.saves+1, false
		m.refreshSources()
		m.status = fmt.Sprintf("Saved at revision %d.", m.draft.Base().Selections.Revision)
		m.clamp()
	} else if failure = m.failure(err); failure.Code == "config_write_failed" {
		m.unconfirmed = true
	}
	if m.interrupted {
		return m.quit()
	}
	if failure != nil {
		switch failure.Code {
		case "config_conflict":
			m.screen = screenConflict
			m.conflict = fmt.Sprintf("The configuration changed since setup loaded it. Your %s kept.", plural(m.draft.Changes(), "change is", "changes are"))
		case "config_write_failed":
			m.screen = screenConflict
			m.conflict = failure.Message + " Press r to reload."
		default:
			m.status = failure.Message
		}
	}
	return nil
}

func (m *Model) reload() tea.Cmd {
	m.busy, m.status = true, "Reloading..."
	ctx, store := m.ctx, m.store
	return func() tea.Msg {
		state, err := store.Read(ctx)
		return reloadedMsg{state: state, err: err}
	}
}

func (m *Model) reloaded(msg reloadedMsg) tea.Cmd {
	m.busy = false
	if m.interrupted {
		return m.quit()
	}
	err := msg.err
	var dropped []Dropped
	if err == nil {
		dropped, err = m.draft.Rebase(msg.state)
	}
	if err != nil {
		m.status = ""
		m.conflict = m.failure(err).Message + " Press r to reload."
		return nil
	}
	m.screen = screenList
	m.refreshSources()
	m.clamp()
	m.status = fmt.Sprintf("Reloaded at revision %d. Reapplied %s.", m.draft.Base().Selections.Revision, plural(m.draft.Changes(), "change", "changes"))
	for _, reason := range []string{reasonPresent, reasonChanged, reasonStale} {
		var names []string
		for _, d := range dropped {
			if d.Reason == reason {
				names = append(names, d.Change)
			}
		}
		switch {
		case len(names) == 0:
		case reason == reasonPresent:
			m.status += " Already in the configuration: " + strings.Join(names, ", ") + "."
			if m.unconfirmed { // the unconfirmed save landed
				m.saves++
			}
		default:
			m.status += fmt.Sprintf(" Dropped (%s): %s.", reason, strings.Join(names, ", "))
		}
	}
	m.unconfirmed = false
	return nil
}

// cancel asks before discarding unsaved changes; No returns to the screen it
// was asked from.
func (m *Model) cancel() tea.Cmd {
	if m.draft.Changes() > 0 {
		m.back, m.screen = m.screen, screenConfirm
		return nil
	}
	return m.quit()
}

func (m *Model) refreshSources() {
	if m.opts.SourceAges == nil {
		return
	}
	if sources, err := m.opts.SourceAges(m.draft.Base()); err == nil {
		m.sources = sources
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
