package ui

import (
	"context"
	"errors"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/edit"
	"github.com/dedene/mcparcel/internal/output"
)

type screen int

const (
	screenList screen = iota
	screenSearch
	screenHelp
	screenDetails
	screenField   // editing one input (details.go)
	screenProfile // choosing a credential profile (details.go)
	screenTools
	screenForm    // personal connection form (personal.go)
	screenJoin    // mcparcel join: the catalog and its 1Password suggestions (join.go)
	screenPresets // mcparcel join: presets to start from
)

type toolsMsg struct {
	seq  int
	id   string
	list output.ToolList
	err  error
}

// Model is the setup screen. All store access runs in commands; Update only
// changes the model.
type Model struct {
	ctx     context.Context
	store   *config.Store
	opts    Options
	theme   theme
	live    *live
	opened  config.State // the state setup opened on
	sources []output.SourceMetadata

	width, height  int
	screen         screen
	back           screen // where help returns to
	tabID          string // current tab; tabIndex is its position, kept when the tab disappears
	tabIndex       int
	cursor, offset int
	search         field
	status         string

	busy        bool                // a change is being written; keys and pastes wait in queued
	writing     *change             // the change being written
	then        func(error) tea.Cmd // runs once it is written
	queued      []tea.Msg           // tea.KeyPressMsg and tea.PasteMsg
	interrupted bool
	unconfirmed bool // the last write failed part way and no read settled it

	detailID      string // connection shown by details, field, profile, tools and edit
	pendingEnable string // connection Space turns on once the field and profile screens fill what it misses
	enableOps     []op   // what those screens filled in so far, written with the enable
	enableBack    screen // where the pending enable returns: the list or the server page
	detailCursor  int    // the server page's action
	editing       bool   // an input is edited on the server page
	technical     bool   // the server page shows the technical facts (i)
	input         inputEdit
	profileCursor int
	toolCursor    int
	form          *personalForm
	helpOffset    int

	loaded     map[string]loadedTools // tools loaded this session, by connection
	loadErr    map[string][]string    // last load failure, by connection
	loading    string                 // connection being loaded
	loadSeq    int                    // identifies the current load; older results are ignored
	loadCancel context.CancelFunc

	finish   *finishRun             // setting up (s), while it runs (finish.go)
	outcomes map[string]finishState // sign-ins setting up left: finSkipped or finManual
	failed   map[string]string      // why a server could not start, short (Status)

	signIns       map[string]SignInState // stored sign-ins read, by signInKey (status.go)
	signInPending map[string]bool        // being read
	commands      map[string]bool        // whether each local command was found

	join     *joinRun // the join screens, while they show
	landing  []string // the presets join turned on, until a key: the list lands on them again once sign-ins are read
	landStep bool     // plain setup opens on the first step, again once sign-ins are read, until a key

	motion // toasts, the row flash and the spinner (motion.go)
}

func (m *Model) Init() tea.Cmd { return nil }

func (m *Model) View() tea.View {
	return tea.View{Content: m.render(), AltScreen: true}
}

// Result is how setup ends: what is on and what is left, as each Status
// reads. It reads the stored sign-ins setup has not read yet.
func (m *Model) Result() Result {
	m.readAllSignIns()
	r := Result{Interrupted: m.interrupted, Unconfirmed: m.unconfirmed}
	r.On, r.Steps = m.steps()
	r.Ready = r.On - len(r.Steps)
	return r
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	status, flash := m.status, m.flashSeq
	cmd := m.update(msg)
	return m, tea.Batch(cmd, m.animate(status, flash), m.readSignIns())
}

func (m *Model) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clamp()
	case tea.ColorProfileMsg:
		m.theme = newTheme(m.opts, lipgloss.Complete(msg.Profile))
	case writtenMsg:
		return m.written(msg)
	case toolsMsg:
		m.toolsLoaded(msg)
	case signInsMsg:
		m.signInsRead(msg)
	case finishMsg:
		return m.rowFinished(msg)
	case signInURLMsg:
		return m.signInURL(msg)
	case signInMsg:
		return m.signedIn(msg)
	case toastMsg:
		return m.toastExpired(msg)
	case flashMsg:
		m.flashEnded(msg)
	case spinMsg:
		return m.spun()
	case tea.PasteMsg:
		if m.busy {
			m.queued = append(m.queued, msg)
			return nil
		}
		m.paste(msg.Content)
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return nil
}

func (m *Model) paste(text string) {
	switch m.screen {
	case screenSearch:
		m.search.insert(text)
		m.jumpToMatches()
		m.cursor, m.offset = 0, 0
		m.clamp()
	case screenField:
		m.input.value.insert(text)
	case screenDetails:
		if m.editing {
			m.input.value.insert(text)
		}
	case screenJoin:
		if m.join.editing {
			m.join.account.insert(text)
		}
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
			return nil // quit once the change being written returns
		}
		return m.quit()
	}
	if m.busy {
		m.queued = append(m.queued, msg)
		return nil
	}
	m.landing, m.landStep = nil, false
	if m.tooSmall() {
		if k == "q" {
			return m.quit()
		}
		return nil
	}
	switch m.screen {
	case screenSearch:
		m.searchKey(msg, k)
	case screenHelp:
		m.helpKey(k)
	case screenDetails:
		return m.detailsKey(msg, k)
	case screenField:
		return m.fieldKey(msg, k)
	case screenProfile:
		return m.profileKey(k)
	case screenTools:
		return m.toolsKey(k)
	case screenForm:
		return m.formKey(msg, k)
	case screenJoin:
		return m.joinKey(msg, k)
	case screenPresets:
		return m.presetKey(k)
	default:
		return m.listKey(k)
	}
	return nil
}

// quit ends setup, canceling a running tool load or set-up run first.
func (m *Model) quit() tea.Cmd {
	m.cancelLoad()
	m.stopRun()
	return tea.Quit
}

func (m *Model) searchKey(msg tea.KeyPressMsg, k string) {
	switch k {
	case "enter", "down":
		m.screen = screenList
	case "esc":
		m.screen = screenList
		m.clearSearch()
		return
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
	m.jumpToMatches()
	m.cursor, m.offset = 0, 0
	m.clamp()
}

// clearSearch clears the search and keeps the cursor on the server it was
// on, now among every server of its tab.
func (m *Model) clearSearch() {
	row, ok := m.selected()
	m.search.clear()
	m.cursor, m.offset = 0, 0
	if i := slices.IndexFunc(m.rows(), func(r config.EffectiveConnection) bool { return r.ID == row.ID }); ok && i >= 0 {
		m.cursor = i
	}
	m.clamp()
}

// jumpToMatches moves a search with no match in the current tab to the first
// tab, in tab order, that has one. Without matches anywhere it stays.
func (m *Model) jumpToMatches() {
	query := m.search.String()
	if query == "" || len(m.rows()) > 0 {
		return
	}
	effective := m.live.Effective()
	for i, t := range tabs(effective) {
		if len(rowsFor(effective, t.id, query)) > 0 {
			m.tabIndex, m.tabID = i, t.id
			return
		}
	}
}

func (m *Model) listKey(k string) tea.Cmd {
	if cmd, ok := m.runKey(k); ok {
		return cmd
	}
	page := m.listHeight()
	switch k {
	case "tab", "shift+tab":
		n := len(tabs(m.live.Effective()))
		step := 1
		if k == "shift+tab" {
			step = n - 1
		}
		m.tabIndex = (m.tabIndex + step) % n
		m.tabID = tabs(m.live.Effective())[m.tabIndex].id
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
		m.move(-len(m.live.Effective().Connections))
	case "end":
		m.move(len(m.live.Effective().Connections))
	case "space":
		if row, ok := m.selected(); ok {
			return m.toggle(row)
		}
	case "right", "enter":
		if row, ok := m.selected(); ok {
			m.openDetails(row.ID)
		}
	case "n":
		m.openAddForm()
	case "u":
		return m.undo()
	case "s":
		return m.setUpAll()
	case "?":
		m.openHelp()
	case "/":
		m.screen = screenSearch
	case "q":
		return m.quit()
	case "left", "esc":
		if m.search.String() != "" {
			m.clearSearch()
			return nil
		}
		m.status = "Changes apply at once; u undoes them. q quits."
	}
	return nil
}

func (m *Model) openHelp() {
	m.back, m.screen, m.helpOffset, m.status = m.screen, screenHelp, 0, ""
}

// currentTab follows the current tab by ID when changes add or remove
// domains; a tab that disappeared falls back to the nearest position.
func (m *Model) currentTab() tab {
	all := tabs(m.live.Effective())
	if i := slices.IndexFunc(all, func(t tab) bool { return t.id == m.tabID }); i >= 0 {
		m.tabIndex = i
	}
	m.tabIndex = min(max(m.tabIndex, 0), len(all)-1)
	m.tabID = all[m.tabIndex].id
	return all[m.tabIndex]
}

func (m *Model) rows() []config.EffectiveConnection {
	return rowsFor(m.live.Effective(), m.currentTab().id, m.search.String())
}

func (m *Model) selected() (config.EffectiveConnection, bool) {
	rows := m.rows()
	if len(rows) == 0 {
		return config.EffectiveConnection{}, false
	}
	return rows[min(max(m.cursor, 0), len(rows)-1)], true // safe before clamp
}

func (m *Model) move(delta int) {
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

// toggle turns a row on or off, or accepts its review, and writes that at
// once. An enable that needs answers first asks for each missing item, then
// turns the row on (see continueEnable).
func (m *Model) toggle(row config.EffectiveConnection) tea.Cmd {
	o := op{kind: opEnabled, id: row.ID}
	switch {
	case !row.Available && !row.Enabled:
		m.status = unavailableText(m.live.State(), row)
		return nil
	case !row.Available: // it can only be turned off, even with review required
	case row.Enabled && row.ReviewRequired:
		o.on = true
	default:
		o.on = !row.Enabled
	}
	c := change{ops: []op{o}}
	linked := ""
	if o.on {
		c.accept, linked = []string{row.ID}, edit.AutoProfile(m.live.State(), row)
	}
	from := m.screen
	return m.apply(c, func(err error) tea.Cmd {
		switch {
		case errors.Is(err, config.ErrConfigRequired):
			m.pendingEnable, m.detailID, m.enableOps, m.enableBack = row.ID, row.ID, nil, from
			return m.continueEnable("")
		case err != nil:
			m.status = m.inline(err)
			return nil
		case linked != "":
			m.status = linkedText(linked)
		default:
			m.status = ""
		}
		if from == screenList {
			m.flash(row.ID)
		}
		return nil
	})
}

func unavailableText(state config.State, row config.EffectiveConnection) string {
	return sourceLabel(state, row) + " no longer has this server. Setup keeps your choice for it in case it comes back."
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
