package ui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

type screen int

const (
	screenList screen = iota
	screenSearch
	screenConfirm
	screenConflict
	screenHelp
	screenDetails
	screenField   // editing one input (details.go)
	screenProfile // choosing a credential profile (details.go)
	screenTools
	screenForm // personal connection form (personal.go)
)

const (
	focusList = iota
	focusSave
	focusCancel
)

type (
	savedMsg struct {
		state config.State
		err   error
	}
	reloadedMsg struct {
		state config.State
		err   error
	}
	toolsMsg struct {
		seq  int
		id   string
		list output.ToolList
		err  error
	}
)

// Model is the setup screen. All store access runs in commands; Update only
// changes the model.
type Model struct {
	ctx     context.Context
	store   *config.Store
	opts    Options
	style   style
	draft   *Draft
	sources []output.SourceMetadata

	width, height  int
	screen         screen
	back           screen // where No on the discard prompt returns to
	tabID          string // current tab; tabIndex is its position, kept when the tab disappears
	tabIndex       int
	cursor, offset int
	search         field
	focus          int
	status         string
	conflict       string

	busy        bool // a save or reload is running; only Ctrl+C is handled
	interrupted bool
	saves       int
	unconfirmed bool // the last save failed with config_write_failed; a reload settles it

	detailID      string // connection shown by details, field, profile, tools and edit
	detailCursor  int
	input         inputEdit
	profileCursor int
	toolCursor    int
	form          *personalForm
	helpOffset    int

	loaded     map[string]loadedTools // tools loaded this session, by connection
	loadErr    map[string][]string    // last Connect failure, by connection
	loading    string                 // connection being loaded
	loadSeq    int                    // identifies the current load; older results are ignored
	loadCancel context.CancelFunc
}

func (m *Model) Init() tea.Cmd { return nil }

func (m *Model) View() tea.View {
	return tea.View{Content: m.render(), AltScreen: true}
}

func (m *Model) Result() Result {
	return Result{Revision: m.draft.Base().Selections.Revision, Saves: m.saves, Interrupted: m.interrupted, Unconfirmed: m.unconfirmed}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clamp()
	case savedMsg:
		return m, m.saved(msg)
	case reloadedMsg:
		return m, m.reloaded(msg)
	case toolsMsg:
		m.toolsLoaded(msg)
	case tea.PasteMsg:
		m.paste(msg.Content)
	case tea.KeyPressMsg:
		return m, m.key(msg)
	}
	return m, nil
}

func (m *Model) paste(text string) {
	if m.busy {
		return
	}
	switch m.screen {
	case screenSearch:
		m.search.insert(text)
		m.cursor, m.offset = 0, 0
		m.clamp()
	case screenField:
		m.input.value.insert(text)
	case screenForm:
		if f := m.form.current(); f.editable() {
			f.value.insert(text)
		}
	}
}

// typed returns the text a key press types, or "" for control keys.
func typed(msg tea.KeyPressMsg) string {
	if msg.Mod&(tea.ModCtrl|tea.ModAlt) != 0 {
		return ""
	}
	return msg.Text
}

// keyName matches letters case-insensitively.
func keyName(msg tea.KeyPressMsg) string {
	k := msg.String()
	if len(k) == 1 {
		return strings.ToLower(k)
	}
	return k
}

func (m *Model) key(msg tea.KeyPressMsg) tea.Cmd {
	k := keyName(msg)
	if k == "ctrl+c" {
		m.interrupted = true
		if m.busy {
			return nil // quit once the running save or reload returns
		}
		return m.quit()
	}
	if m.busy {
		return nil
	}
	if m.tooSmall() && m.screen != screenConfirm {
		if k == "q" {
			return m.cancel()
		}
		return nil
	}
	switch m.screen {
	case screenSearch:
		m.searchKey(msg, k)
	case screenConfirm:
		switch k {
		case "y":
			return m.quit()
		case "n", "enter", "esc":
			m.screen = m.back
		}
	case screenConflict:
		switch k {
		case "r":
			return m.reload()
		case "esc":
			m.screen = screenList
		}
	case screenHelp:
		m.helpKey(k)
	case screenDetails:
		return m.detailsKey(k)
	case screenField:
		m.fieldKey(msg, k)
	case screenProfile:
		m.profileKey(k)
	case screenTools:
		return m.toolsKey(k)
	case screenForm:
		m.formKey(msg, k)
	default:
		return m.listKey(k)
	}
	return nil
}

// quit ends setup, canceling a running tool load first.
func (m *Model) quit() tea.Cmd {
	m.cancelLoad()
	return tea.Quit
}

func (m *Model) searchKey(msg tea.KeyPressMsg, k string) {
	switch k {
	case "enter", "down":
		m.screen = screenList
	case "esc":
		m.search.clear()
		m.screen = screenList
	case "backspace":
		m.search.backspace()
	case "ctrl+u":
		m.search.clear()
	default:
		text := typed(msg)
		if text == "" {
			return
		}
		m.search.insert(text)
	}
	m.cursor, m.offset = 0, 0
	m.clamp()
}

func (m *Model) listKey(k string) tea.Cmd {
	page := m.listHeight()
	switch k {
	case "left", "right":
		n := len(tabs(m.draft.Effective()))
		step := 1
		if k == "left" {
			step = n - 1
		}
		m.tabIndex = (m.tabIndex + step) % n
		m.tabID = tabs(m.draft.Effective())[m.tabIndex].id
		m.cursor, m.offset = 0, 0
	case "up":
		m.move(-1)
	case "down":
		m.move(1)
	case "pgup":
		m.move(-page)
	case "pgdown":
		m.move(page)
	case "home":
		m.move(-len(m.draft.Effective().Connections))
	case "end":
		m.move(len(m.draft.Effective().Connections))
	case "space":
		m.focus = focusList
		if row, ok := m.selected(); ok {
			m.toggle(row)
		}
	case "enter":
		switch m.focus {
		case focusSave:
			return m.save()
		case focusCancel:
			return m.cancel()
		}
		if row, ok := m.selected(); ok {
			m.openDetails(row.ID)
		}
	case "a":
		m.openAddForm()
	case "?":
		m.screen, m.helpOffset, m.status = screenHelp, 0, ""
	case "/":
		m.focus = focusList
		m.screen = screenSearch
	case "tab":
		m.focus = (m.focus + 1) % 3
	case "shift+tab":
		m.focus = (m.focus + 2) % 3
	case "ctrl+s":
		return m.save()
	case "q":
		return m.cancel()
	case "esc":
		if m.search.String() != "" {
			m.search.clear()
			m.cursor, m.offset = 0, 0
			m.clamp()
			return nil
		}
		return m.cancel()
	}
	return nil
}

// currentTab follows the current tab by ID when draft changes add or remove
// domains; a tab that disappeared falls back to the nearest position.
func (m *Model) currentTab() tab {
	all := tabs(m.draft.Effective())
	if i := slices.IndexFunc(all, func(t tab) bool { return t.id == m.tabID }); i >= 0 {
		m.tabIndex = i
	}
	m.tabIndex = min(max(m.tabIndex, 0), len(all)-1)
	m.tabID = all[m.tabIndex].id
	return all[m.tabIndex]
}

func (m *Model) rows() []config.EffectiveConnection {
	return rowsFor(m.draft.Effective(), m.currentTab().id, m.search.String())
}

func (m *Model) selected() (config.EffectiveConnection, bool) {
	rows := m.rows()
	if len(rows) == 0 {
		return config.EffectiveConnection{}, false
	}
	return rows[m.cursor], true
}

func (m *Model) move(delta int) {
	m.focus = focusList
	m.cursor += delta
	m.clamp()
}

// clamp keeps the cursor on a row and inside the visible window.
func (m *Model) clamp() {
	n := len(m.rows())
	m.cursor = min(max(m.cursor, 0), max(n-1, 0))
	page := m.listHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+page {
		m.offset = m.cursor - page + 1
	}
	m.offset = min(max(m.offset, 0), max(n-page, 0))
}

// toggle turns a row on or off, or accepts its review. An enable that needs
// configuration opens details on the first missing field.
func (m *Model) toggle(row config.EffectiveConnection) {
	var err error
	switch {
	case !row.Available && !row.Enabled:
		m.status = unavailableText(m.draft.State(), row)
		return
	case !row.Available: // it can only be turned off, even with review required
		err = m.draft.SetEnabled(row.ID, false)
	case row.Enabled && row.ReviewRequired:
		err = m.draft.SetEnabled(row.ID, true)
	default:
		err = m.draft.SetEnabled(row.ID, !row.Enabled)
	}
	switch {
	case errors.Is(err, config.ErrConfigRequired):
		m.openDetails(row.ID)
		m.detailCursor = m.firstMissing(row)
		m.status = fmt.Sprintf("%s needs: %s. Fill them in, then press Space.", rowLabel(row), strings.Join(missingConfig(m.draft.State(), row), ", "))
	case err != nil:
		m.status = m.failure(err).Message
	default:
		m.status = ""
	}
}

func unavailableText(state config.State, row config.EffectiveConnection) string {
	return sourceLabel(state, row) + " no longer defines this connection. Your selection is kept for a future restoration."
}

// failure maps an error through Options.Describe to display-safe text.
func (m *Model) failure(err error) *output.Error {
	var failure *output.Error
	if m.opts.Describe != nil {
		failure = m.opts.Describe(err)
	}
	if failure == nil {
		failure = output.NewError("internal_error", nil)
	}
	clone := *failure
	clone.Message = output.DisplayMetadata(clone.Message)
	return &clone
}
