package ui

import (
	"cmp"
	"errors"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// The join screens (mcparcel join) come before the list. The intro asks,
// one at a time, about each 1Password account the catalog suggests for a
// credential profile the teammate has no local profile for yet. Only Enter
// on that question writes anything: the local profile, mode and account
// only, in its own store update, as config profile set would. The presets
// screen then turns on the connections of the picked presets in the draft,
// through the same path as Space; nothing is saved until Ctrl+S. A local
// profile that already has the requirement's ID is never replaced; where it
// differs from the suggestion the intro says which one the catalog uses.

// joinRun is the join screens' state.
type joinRun struct {
	Join
	catalog config.EffectiveCatalog
	servers int                           // connections the catalog defines
	asks    []config.EffectiveRequirement // suggestions still to answer, most used first
	total   int                           // suggestions asked about in all
	notes   []string                      // how earlier suggestions ended, display-safe
	editing bool                          // typing another account
	account field
	picked  []bool // by preset
	cursor  int    // preset row; len(picked) is Start empty
}

type profileMsg struct {
	id, account string
	state       config.State // the saved state, or a fresh read after an error
	err         error
	fresh       bool // state holds a fresh read after err
}

// ask is the suggestion asked about, or nil.
func (j *joinRun) ask() *config.EffectiveRequirement {
	if len(j.asks) == 0 {
		return nil
	}
	return &j.asks[0]
}

// requirementLabel names a credential profile requirement: its description,
// else its ID.
func requirementLabel(r config.EffectiveRequirement) string {
	if r.Description != "" {
		return output.DisplayMetadata(firstLine(r.Description))
	}
	return output.DisplayMetadata(r.ID)
}

// openJoin shows the join screens for j's catalog. A catalog the state does
// not hold opens the list.
func (m *Model) openJoin(j Join) {
	effective, state := m.draft.Effective(), m.draft.State()
	cat, ok := effective.Catalogs[j.SourceID]
	if !ok {
		return
	}
	run := &joinRun{Join: j, catalog: cat, picked: make([]bool, len(cat.Presets))}
	for _, row := range effective.Connections {
		if row.SourceID == j.SourceID && row.Definition != nil {
			run.servers++
		}
	}
	g := m.theme.glyph
	for _, r := range cat.Requirements {
		profile, exists := state.Local.CredentialProfiles[r.ID]
		switch {
		case len(r.Connections) == 0: // nothing would use it
		case exists:
			run.notes = append(run.notes, m.existingNote(r, profile))
		case r.Suggest == nil:
			run.notes = append(run.notes, g.todo+" 1Password for "+requirementLabel(r)+": not set up. Add it with mcparcel config profile set "+output.DisplayMetadata(r.ID)+".")
		case m.opts.NoDesktopApp:
			// Only desktop can be suggested, and the desktop app cannot
			// read 1Password here.
			run.notes = append(run.notes, g.todo+" 1Password for "+requirementLabel(r)+": the desktop app works on macOS only. Add a service-account profile with mcparcel config profile set "+output.DisplayMetadata(r.ID)+".")
		default:
			run.asks = append(run.asks, r)
		}
	}
	slices.SortStableFunc(run.asks, func(a, b config.EffectiveRequirement) int {
		return cmp.Compare(len(b.Connections), len(a.Connections))
	})
	run.total = len(run.asks)
	m.join, m.screen, m.status = run, screenJoin, ""
}

// existingNote is the intro's line for a requirement with a local profile:
// set up when it is the one suggested, else the mode and account of the
// profile the catalog's connections link to instead.
func (m *Model) existingNote(r config.EffectiveRequirement, p config.Profile) string {
	g, label := m.theme.glyph, requirementLabel(r)
	if r.Suggest == nil || p.Mode == r.Suggest.Mode && p.Account == r.Suggest.Account {
		return g.done + " 1Password for " + label + ": already set up"
	}
	uses := p.Mode
	if p.Account != "" {
		uses += ", " + p.Account
	}
	return g.review + " 1Password for " + label + ": uses your profile " + output.DisplayMetadata(r.ID) + " (" + output.DisplayMetadata(uses) + "), not the suggested " + output.DisplayMetadata(r.Suggest.Account)
}

// normalAccount is a typed sign-in address as stored: trimmed, lowercase,
// without a scheme or a trailing slash, as a browser's address bar shows it.
func normalAccount(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	for _, scheme := range []string{"https://", "http://"} {
		text = strings.TrimPrefix(text, scheme)
	}
	return strings.TrimRight(text, "/")
}

func (m *Model) joinKey(msg tea.KeyPressMsg, k string) tea.Cmd {
	j := m.join
	if j.editing {
		switch k {
		case "enter":
			if account := normalAccount(j.account.String()); config.ValidSuggestedAccount(account) {
				return m.createProfile(account)
			}
			m.status = "Enter a 1Password sign-in address, such as example.1password.com."
		case "esc":
			j.editing, m.status = false, ""
		case "backspace":
			j.account.backspace()
		case "ctrl+u":
			j.account.clear()
		default:
			j.account.insert(typed(msg))
		}
		return nil
	}
	ask := j.ask()
	switch {
	case k == "q" || k == "esc" && ask == nil:
		return m.cancel()
	case ask == nil && k == "enter":
		m.joinNext()
	case ask == nil:
	case k == "enter":
		return m.createProfile(ask.Suggest.Account)
	case k == "e":
		j.editing, m.status = true, ""
		j.account.clear()
	case k == "esc":
		m.answered(m.theme.glyph.off + " 1Password for " + requirementLabel(*ask) + ": skipped for now")
	}
	return nil
}

// answered notes how the current suggestion ended and moves to the next
// one, or on to the presets after the last.
func (m *Model) answered(note string) {
	j := m.join
	j.notes, j.asks, j.editing = append(j.notes, note), j.asks[1:], false
	j.account.clear()
	m.status = "" // the note shows it, above the next question
	if len(j.asks) == 0 {
		m.joinNext()
		m.status = note
	}
}

// createProfile saves the local profile for the current suggestion: its
// mode, and the suggested account or the one typed. A profile with that ID
// that appeared since setup read the state is kept, not replaced.
func (m *Model) createProfile(account string) tea.Cmd {
	ask := m.join.ask()
	profile := config.ProfileSuggestion{Mode: ask.Suggest.Mode, Account: account}.Profile()
	m.busy, m.status = true, "Saving..."
	ctx, store, id, revision := m.ctx, m.store, ask.ID, m.draft.Base().Selections.Revision
	return func() tea.Msg {
		next, err := store.Update(ctx, revision, func(s *config.State) error {
			if _, ok := s.Local.CredentialProfiles[id]; ok {
				return config.ErrConfigConflict
			}
			if s.Local.CredentialProfiles == nil {
				s.Local.CredentialProfiles = map[string]config.Profile{}
			}
			s.Local.CredentialProfiles[id] = profile
			return nil
		})
		if err == nil {
			return profileMsg{id: id, account: account, state: next}
		}
		fresh, readErr := store.Read(ctx)
		return profileMsg{id: id, account: account, state: fresh, err: err, fresh: readErr == nil}
	}
}

func (m *Model) profileSaved(msg profileMsg) tea.Cmd {
	m.busy = false
	saved := msg.err == nil
	if saved {
		m.saves++
	}
	if saved || msg.fresh {
		if _, err := m.draft.Rebase(msg.state); err != nil && saved {
			msg.err = err
		}
		m.refreshSources()
	}
	ask := m.join.ask()
	profile, exists := m.draft.State().Local.CredentialProfiles[msg.id]
	switch {
	case ask == nil || ask.ID != msg.id:
	case msg.err == nil:
		m.answered(m.theme.glyph.done + " 1Password for " + requirementLabel(*ask) + ": " + output.DisplayMetadata(msg.account))
	case exists: // created elsewhere meanwhile; that one stays
		m.answered(m.existingNote(*ask, profile))
	default:
		m.status = m.failure(msg.err).Message
	}
	if m.interrupted {
		return m.quit()
	}
	return nil
}

// showRow moves the list to the first tab that holds id, with the cursor on
// it. An empty or unknown id leaves the list as it is.
func (m *Model) showRow(id string) {
	effective := m.draft.Effective()
	for i, t := range tabs(effective) {
		if at := slices.IndexFunc(rowsFor(effective, t.id, ""), func(r config.EffectiveConnection) bool { return r.ID == id }); at >= 0 {
			m.tabIndex, m.tabID, m.cursor = i, t.id, at
			return
		}
	}
}

// joinNext leaves the intro for the presets, or for the list when the
// catalog has none.
func (m *Model) joinNext() {
	m.status = ""
	if len(m.join.catalog.Presets) > 0 {
		m.screen = screenPresets
		return
	}
	m.join, m.screen = nil, screenList
}

func (m *Model) presetKey(k string) tea.Cmd {
	j := m.join
	switch k {
	case "up":
		j.cursor = max(j.cursor-1, 0)
	case "down":
		j.cursor = min(j.cursor+1, len(j.picked))
	case "space":
		if j.cursor < len(j.picked) {
			j.picked[j.cursor] = !j.picked[j.cursor]
		} else {
			clear(j.picked)
		}
	case "enter":
		if j.cursor == len(j.picked) {
			clear(j.picked)
		}
		m.stagePresets()
	case "q", "esc":
		return m.cancel()
	}
	return nil
}

// presetIDs lists the connections of the picked presets, in preset order,
// each once.
func (j *joinRun) presetIDs() []string {
	var ids []string
	for i, p := range j.catalog.Presets {
		if !j.picked[i] {
			continue
		}
		for _, id := range p.Connections {
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// stagePresets turns on the picked presets' connections in the draft, as
// Space does, and opens the list. One that is already on, under review
// included, is left as it is; one that misses configuration stays off.
func (m *Model) stagePresets() {
	ids := m.join.presetIDs()
	m.join, m.screen, m.focus = nil, screenList, focusList
	m.cursor, m.offset = 0, 0
	turned, missing, failed, first := 0, 0, "", ""
	for _, id := range ids {
		row, ok := m.draft.Effective().Connections[id]
		if !ok || !row.Available || row.Enabled {
			continue
		}
		err := m.draft.SetEnabled(id, true)
		switch {
		case errors.Is(err, config.ErrConfigRequired):
			missing++
		case err != nil:
			failed = m.failure(err).Message
		default:
			turned++
			if first == "" {
				first = id
			}
		}
	}
	m.showRow(first)
	m.status = ""
	switch {
	case failed != "":
		m.status = failed
	case turned > 0 && missing > 0:
		m.status = "Turned on " + plural(turned, "server", "servers") + ". " + plural(missing, "needs", "need") + " setup first: Space on " + m.theme.glyph.needsConfig + " asks for it."
	case turned > 0:
		m.status = "Turned on " + plural(turned, "server", "servers") + ". Ctrl+S saves."
	case missing > 0:
		m.status = plural(missing, "server needs", "servers need") + " setup first: Space on " + m.theme.glyph.needsConfig + " asks for it."
	}
	m.clamp()
}
