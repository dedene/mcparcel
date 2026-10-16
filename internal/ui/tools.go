package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dedene/mcparcel/internal/output"
)

// Tools load when the tools page opens on a server that is on and ready
// (opening it is asking for them) and are kept for this session; nothing
// is cached on disk. The daemon hides disabled and source-denied tools, so
// a disabled name that is not loaded cannot be confirmed.

type toolInfo struct {
	name, display, description string
}

type loadedTools struct {
	tools []toolInfo
	at    time.Time
}

type toolRow struct {
	toolInfo
	loaded, enabled bool
}

// openTools shows the tools page and loads the tools, unless they loaded
// this session already.
func (m *Model) openTools() tea.Cmd {
	m.screen, m.toolCursor, m.status = screenTools, 0, ""
	if _, ok := m.loaded[m.detailID]; ok {
		return nil
	}
	return m.loadTools()
}

// toolRows is the union of the loaded names and the names disabled when
// setup opened or now, sorted by name, so a tool turned back on stays
// listed.
func (m *Model) toolRows(id string) []toolRow {
	rows := map[string]toolRow{}
	for _, tool := range m.loaded[id].tools {
		rows[tool.name] = toolRow{toolInfo: tool, loaded: true}
	}
	disabled := m.live.State().Selections.Connections[id].DisabledTools
	for _, names := range [][]string{m.opened.Selections.Connections[id].DisabledTools, disabled} {
		for _, name := range names {
			if _, ok := rows[name]; !ok {
				rows[name] = toolRow{toolInfo: toolInfo{name: name, display: output.DisplayMetadata(name)}}
			}
		}
	}
	out := make([]toolRow, 0, len(rows))
	for _, row := range rows {
		row.enabled = !slices.Contains(disabled, row.name)
		out = append(out, row)
	}
	slices.SortFunc(out, func(a, b toolRow) int { return strings.Compare(a.name, b.name) })
	return out
}

func (m *Model) toolsPage() []string {
	row := m.detailRow()
	w, _ := m.size()
	body := []string{"Tool policy: " + m.policyText(row)}
	switch blocked := m.toolsBlocked(); {
	case m.loading == row.ID:
		body = append(body, m.spinner()+" Loading the tools of "+rowLabel(row)+"... 1Password may ask you to approve.")
	case len(m.loadErr[row.ID]) > 0:
		body = append(body, m.loadErr[row.ID]...)
	case m.loaded[row.ID].tools != nil:
		body = append(body, fmt.Sprintf("Loaded %s ago (%s).", ages(m.opts.Now().Sub(m.loaded[row.ID].at).Seconds()), plural(len(m.loaded[row.ID].tools), "tool", "tools")))
	case blocked != "":
		body = append(body, blocked)
	}
	if text := m.signInText(); text != "" {
		body = append(body, "", text)
	}
	body = append(body, "")
	rows := m.toolRows(row.ID)
	nameWidth := 0
	for _, r := range rows {
		nameWidth = max(nameWidth, textWidth(r.display))
	}
	nameWidth = min(nameWidth, max(w/3, 12))
	focus := -1
	for i, r := range rows {
		prefix := "  "
		if i == m.toolCursor {
			prefix, focus = "> ", len(body)
		}
		mark := m.theme.glyph.off
		if r.enabled {
			mark = m.theme.glyph.done
		}
		text := prefix + mark + " " + pad(r.display, nameWidth)
		switch {
		case !r.loaded && !r.enabled:
			text += " (disabled)"
		case r.description != "":
			text += "  " + r.description
		}
		body = append(body, text)
	}
	help := "Up/Down: tool   Space: on/off   s: set up   u: undo   ?: help   Esc: back"
	switch {
	case m.finish.current() != nil:
		help = m.runHelp()
	case m.finish != nil:
		help = "Up/Down: tool   Space: on/off   q: stop setting up   Esc: back"
	}
	return m.page(m.crumb(row)+" / Tools", body, focus, help)
}

func (m *Model) toolsKey(k string) tea.Cmd {
	if cmd, ok := m.runKey(k); ok {
		return cmd
	}
	rows := m.toolRows(m.detailID)
	switch k {
	case "up":
		m.toolCursor = max(m.toolCursor-1, 0)
	case "down":
		m.toolCursor = min(m.toolCursor+1, max(len(rows)-1, 0))
	case "space":
		if m.toolCursor < len(rows) {
			r := rows[m.toolCursor]
			return m.apply(change{ops: []op{{kind: opTool, id: m.detailID, name: r.name, on: !r.enabled}}}, func(err error) tea.Cmd {
				m.status = ""
				if err != nil {
					m.status = m.inline(err)
				}
				return nil
			})
		}
	case "s": // sets this server up right here: its tools show once it starts
		m.cancelLoad()
		return m.setUpOne(m.detailRow())
	case "u":
		return m.undo()
	case "?":
		m.openHelp()
	case "q":
		return m.quit()
	case "left", "esc":
		m.cancelLoad()
		m.screen, m.status = screenDetails, ""
	}
	return nil
}

// toolsBlocked says why the tools of the details row cannot load yet, and
// the keys that get it there, or "".
func (m *Model) toolsBlocked() string {
	row := m.detailRow()
	label := rowLabel(row)
	back := m.theme.glyph.left + " back"
	switch st := m.statusOf(row); {
	case m.opts.LoadTools == nil:
		return ""
	case !row.Available:
		return unavailableText(m.live.State(), row)
	case !row.Enabled:
		return label + " is off: " + back + ", then Space turns it on."
	case st.kind == stReview:
		return label + " changed upstream: " + back + ", then Space accepts it."
	case st.kind == stInput || st.kind == stProfile:
		return label + " needs " + needsText(missingConfig(m.live.State(), row)) + " first: " + back + " to set it."
	case st.kind == stCommand:
		return st.detail + " is not installed. Install it, then press s."
	case st.kind == stManual || st.kind == stSignIn && m.opts.SignIn == nil:
		return "Needs a sign-in first: run mcparcel auth " + authName(m.live.Effective(), row.ID) + " in a terminal."
	case st.kind == stSignIn:
		return "Needs a sign-in first: s signs you in."
	case st.kind == stSigning || st.kind == stWorking:
		return "Setting up: " + st.text
	}
	return ""
}

// loadTools starts a tool load for the details row, once it can load. It
// is the only part of browsing that contacts the runtime.
func (m *Model) loadTools() tea.Cmd {
	id := m.detailID
	if m.opts.LoadTools == nil || m.loading == id || m.toolsBlocked() != "" {
		return nil
	}
	m.cancelLoad()
	ctx, cancel := context.WithCancel(m.ctx)
	m.loadSeq++
	m.loading, m.loadCancel = id, cancel
	seq, load := m.loadSeq, m.opts.LoadTools
	if m.loadErr == nil {
		m.loadErr = map[string][]string{}
	}
	delete(m.loadErr, id)
	m.spinFrame = 0
	return tea.Batch(func() tea.Msg {
		list, err := load(ctx, id)
		return toolsMsg{seq: seq, id: id, list: list, err: err}
	}, m.spin())
}

// cancelLoad stops a running load; its result is then ignored.
func (m *Model) cancelLoad() {
	if m.loadCancel != nil {
		m.loadCancel()
	}
	m.loadCancel, m.loading = nil, ""
	m.loadSeq++
}

func (m *Model) toolsLoaded(msg toolsMsg) {
	if msg.seq != m.loadSeq {
		return
	}
	m.cancelLoad()
	if msg.err == nil {
		m.keepTools(msg.id, msg.list)
		return
	}
	switch m.failure(msg.err).Code {
	case "auth_required", "auth_expired":
		// As setting up does: a sign-in, not a failure. s signs in where
		// setup can; else mcparcel auth does.
		outcome := finManual
		if row, ok := m.live.Effective().Connections[msg.id]; ok && row.Connection != nil && signIn(row.Connection) && m.opts.SignIn != nil {
			outcome = finSkipped
		}
		m.keepOutcome(msg.id, outcome)
	default:
		m.keepFailure(msg.id, msg.err)
	}
}

// keepTools keeps a loaded tool list for this session: the tools page and
// setting up both load one.
func (m *Model) keepTools(id string, list output.ToolList) int {
	tools := []toolInfo{}
	for _, raw := range list.Items {
		var item struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if json.Unmarshal(raw, &item) != nil || item.Name == "" {
			continue
		}
		tools = append(tools, toolInfo{name: item.Name, display: output.DisplayMetadata(item.Name), description: output.DisplayMetadata(firstLine(item.Description))})
	}
	if m.loaded == nil {
		m.loaded = map[string]loadedTools{}
	}
	m.loaded[id] = loadedTools{tools: tools, at: m.opts.Now()}
	delete(m.loadErr, id)
	return len(tools)
}

// connectFailure is the error message and its next action; a server that
// could not start says so by its label and points at doctor. Other
// connection failures (exit 6) add the reassurance that configured MCPs
// still work.
func (m *Model) connectFailure(id string, err error) []string {
	failure := m.failure(err)
	lines := []string{failure.Message}
	next := failure.NextAction
	reassure := output.ExitCode(failure) == 6 || errors.Is(err, context.DeadlineExceeded)
	switch failure.Code {
	case "auth_required", "auth_expired":
		lines[0], next = "Needs a sign-in first: s signs you in.", ""
	case "connection_failed":
		label := output.DisplayMetadata(id)
		if row, ok := m.live.Effective().Connections[id]; ok {
			label = rowLabel(row)
		}
		lines[0], next, reassure = "Could not start "+label+".", "mcparcel doctor shows why.", false
	}
	if next != "" {
		lines = append(lines, output.DisplayMetadata(next))
	}
	if reassure {
		lines = append(lines, "Could not connect. Configured MCPs on this device still work.")
	}
	return lines
}
