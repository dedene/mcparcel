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
// screen then turns on the connections of the picked presets at once, in
// one change, as Space does for each. A local profile that already has the
// requirement's ID is never replaced: one with the suggested account counts
// as set up, whatever its mode; one with another account is named with
// both accounts. Requirements are named by their ID, with their
// description below.

// joinRun is the join screens' state.
type joinRun struct {
	Join
	catalog config.EffectiveCatalog
	servers int                           // connections the catalog defines
	asks    []config.EffectiveRequirement // suggestions still to answer, most used first
	total   int                           // suggestions asked about in all
	notes   []joinNote                    // how each requirement ended, display-safe
	editing bool                          // typing another account
	account field
	picked  []bool // by preset
	cursor  int    // preset row; len(picked) is Start empty
}

// ask is the suggestion asked about, or nil.
func (j *joinRun) ask() *config.EffectiveRequirement {
	if len(j.asks) == 0 {
		return nil
	}
	return &j.asks[0]
}

// joinNote is the intro's line for one requirement, and its description.
type joinNote struct{ text, about string }

func noteFor(r config.EffectiveRequirement, text string) joinNote {
	return joinNote{text: text, about: output.DisplayMetadata(firstLine(r.Description))}
}

// openJoin shows the join screens for j's catalog. A catalog the state does
// not hold opens the list.
func (m *Model) openJoin(j Join) {
	effective, state := m.live.Effective(), m.live.State()
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
			run.notes = append(run.notes, noteFor(r, g.todo+" 1Password "+output.DisplayMetadata(r.ID)+": not set up. Add it with mcparcel config profile set "+output.DisplayMetadata(r.ID)+"."))
		case m.opts.NoDesktopApp:
			// Only desktop can be suggested, and the desktop app cannot
			// read 1Password here.
			run.notes = append(run.notes, noteFor(r, g.todo+" 1Password "+output.DisplayMetadata(r.ID)+": the desktop app works on macOS only. Add a service-account profile with mcparcel config profile set "+output.DisplayMetadata(r.ID)+"."))
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
// set up when its account is the suggested one, whatever its mode, else
// both accounts, since the catalog's connections link to it by ID.
func (m *Model) existingNote(r config.EffectiveRequirement, p config.Profile) joinNote {
	g, id := m.theme.glyph, output.DisplayMetadata(r.ID)
	if r.Suggest == nil || p.Account == r.Suggest.Account {
		return noteFor(r, g.done+" 1Password: using your profile "+id+" ("+profileWords(p)+")")
	}
	uses := "a service-account token"
	if p.Account != "" {
		uses = output.DisplayMetadata(p.Account)
	}
	return noteFor(r, g.warn+" 1Password: your profile "+id+" uses "+uses+", the catalog suggests "+output.DisplayMetadata(r.Suggest.Account))
}

// profileWords is a profile's mode in words, and its account.
func profileWords(p config.Profile) string {
	words := "desktop app"
	if p.Mode != config.ProfileModeDesktop {
		words = "service account"
	}
	if p.Account != "" {
		words += ", " + output.DisplayMetadata(p.Account)
	}
	return words
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
		return m.quit()
	case ask == nil && k == "enter":
		m.joinNext()
	case ask == nil:
	case k == "enter":
		return m.createProfile(ask.Suggest.Account)
	case k == "e":
		j.editing, m.status = true, ""
		j.account.clear()
	case k == "esc":
		m.answered(noteFor(*ask, m.theme.glyph.off+" 1Password "+output.DisplayMetadata(ask.ID)+": skipped for now"))
	}
	return nil
}

// answered notes how the current suggestion ended and moves to the next
// one, or on to the presets after the last.
func (m *Model) answered(note joinNote) {
	j := m.join
	j.notes, j.asks, j.editing = append(j.notes, note), j.asks[1:], false
	j.account.clear()
	m.status = "" // the note shows it, above the next question
	if len(j.asks) == 0 {
		m.joinNext()
		m.status = note.text
	}
}

// createProfile writes the local profile for the current suggestion: its
// mode, and the suggested account or the one typed. A profile with that ID
// that appeared since setup read the state is kept, not replaced.
func (m *Model) createProfile(account string) tea.Cmd {
	ask := m.join.ask()
	id := ask.ID
	profile := config.ProfileSuggestion{Mode: ask.Suggest.Mode, Account: account}.Profile()
	return m.apply(change{ops: []op{{kind: opNewProfile, id: id, profile: profile}}}, func(err error) tea.Cmd {
		ask := m.join.ask()
		existing, exists := m.live.State().Local.CredentialProfiles[id]
		switch {
		case ask == nil || ask.ID != id:
		case err == nil:
			m.answered(m.existingNote(*ask, profile))
		case exists: // created elsewhere meanwhile; that one stays
			m.answered(m.existingNote(*ask, existing))
		default:
			m.status = m.inline(err)
		}
		return nil
	})
}

// land moves the list to the first domain, in tab order, that holds one of
// ids, with the cursor on the first of them there that needs a step, else
// on the first of them.
func (m *Model) land(ids []string) {
	effective := m.live.Effective()
	for i, t := range tabs(effective) {
		at := -1
		for j, row := range rowsFor(effective, t.id, "") {
			if !slices.Contains(ids, row.ID) {
				continue
			}
			if st := m.statusOf(row); st.kind != stNone && st.kind <= stSigning {
				at = j
				break
			}
			if at < 0 {
				at = j
			}
		}
		if at >= 0 {
			m.tabIndex, m.tabID, m.cursor = i, t.id, at
			m.clamp()
			return
		}
	}
}

// landOnStep moves the list to the first server, in tab order, that needs
// a step. With none, the list stays where it is.
func (m *Model) landOnStep() {
	effective := m.live.Effective()
	for i, t := range tabs(effective) {
		for j, row := range rowsFor(effective, t.id, "") {
			if m.statusOf(row).step() {
				m.tabIndex, m.tabID, m.cursor = i, t.id, j
				m.clamp()
				return
			}
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
		return m.applyPresets()
	case "q", "esc":
		return m.quit()
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

// applyPresets turns on the picked presets' connections, as Space does for
// each, in one change, and opens the list where they are (see land). One
// that is already on, under review included, is left as it is; one that
// misses something stays off. Once the stored sign-ins of those turned on
// are read, the list lands again, unless a key was pressed.
func (m *Model) applyPresets() tea.Cmd {
	ids := m.join.presetIDs()
	m.join, m.screen = nil, screenList
	m.cursor, m.offset = 0, 0
	m.status = ""
	trial, err := config.CloneState(m.live.State())
	if err != nil {
		m.status = m.inline(err)
		return nil
	}
	m.landing = ids
	var c change
	missing, failed := 0, ""
	for _, id := range ids {
		row, ok := m.live.Effective().Connections[id]
		if !ok || !row.Available || row.Enabled {
			continue
		}
		next, err := config.CloneState(trial)
		if err == nil {
			err = op{kind: opEnabled, id: id, on: true}.apply(&next)
		}
		switch {
		case errors.Is(err, config.ErrConfigRequired):
			missing++
		case err != nil:
			failed = m.inline(err)
		default:
			trial = next
			c.ops = append(c.ops, op{kind: opEnabled, id: id, on: true})
			c.accept = append(c.accept, id)
		}
	}
	report := func(turned int) {
		switch {
		case failed != "":
			m.status = failed
		case turned > 0 && missing > 0:
			m.status = "Turned on " + plural(turned, "server", "servers") + ". " + plural(missing, "needs", "need") + " something first; Space asks for it."
		case turned > 0:
			m.status = "Turned on " + plural(turned, "server", "servers") + "."
		case missing > 0:
			m.status = plural(missing, "server needs", "servers need") + " something first; Space asks for it."
		}
		m.land(ids)
	}
	if len(c.ops) == 0 {
		report(0)
		return nil
	}
	return m.apply(c, func(err error) tea.Cmd {
		if err != nil {
			failed = m.inline(err)
			report(0)
			return nil
		}
		report(len(c.ops))
		return nil
	})
}
