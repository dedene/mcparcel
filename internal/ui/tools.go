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

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// Tools are loaded only by an explicit Connect and kept for this session:
// there is no schema cache. The daemon hides disabled and source-denied
// tools, so a disabled name that is not loaded cannot be confirmed.

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

func (m *Model) openTools() {
	m.screen, m.toolCursor, m.status = screenTools, 0, ""
}

// toolRows is the union of the loaded names and the names disabled in the
// base or the draft, sorted by name.
func (m *Model) toolRows(id string) []toolRow {
	rows := map[string]toolRow{}
	for _, tool := range m.loaded[id].tools {
		rows[tool.name] = toolRow{toolInfo: tool, loaded: true}
	}
	disabled := m.draft.State().Selections.Connections[id].DisabledTools
	for _, names := range [][]string{m.draft.Base().Selections.Connections[id].DisabledTools, disabled} {
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
	body := []string{"Policy: " + m.policyText(row), m.revisionText()}
	switch {
	case m.loading == row.ID:
		body = append(body, "Connecting to "+rowLabel(row)+"... Credentials may be requested.")
	case len(m.loadErr[row.ID]) > 0:
		body = append(body, m.loadErr[row.ID]...)
	case m.loaded[row.ID].tools != nil:
		body = append(body, fmt.Sprintf("Loaded this session %s ago (%s).", ages(m.opts.Now().Sub(m.loaded[row.ID].at).Seconds()), plural(len(m.loaded[row.ID].tools), "tool", "tools")))
	}
	body = append(body, "")
	rows := m.toolRows(row.ID)
	nameWidth := 0
	for _, r := range rows {
		nameWidth = max(nameWidth, len(r.display))
	}
	nameWidth = min(nameWidth, max(w/3, 12))
	focus := -1
	for i, r := range rows {
		prefix := "  "
		if i == m.toolCursor {
			prefix, focus = "> ", len(body)
		}
		mark := "[ ]"
		if r.enabled {
			mark = "[x]"
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
	if _, ok := m.loaded[row.ID]; !ok && m.opts.LoadTools != nil && m.loading != row.ID {
		if len(rows) > 0 {
			body = append(body, "")
		}
		body = append(body, "Press l to connect and load tools.")
	}
	help := "Up/Down: tool   Space: toggle   Esc: back"
	if m.opts.LoadTools != nil {
		help = "Up/Down: tool   Space: toggle   l: connect and load tools   Esc: back"
	}
	return m.page("MCParcel / Setup / "+rowLabel(row)+" / Tools", body, focus, help)
}

func (m *Model) toolsKey(k string) tea.Cmd {
	rows := m.toolRows(m.detailID)
	switch k {
	case "up":
		m.toolCursor = max(m.toolCursor-1, 0)
	case "down":
		m.toolCursor = min(m.toolCursor+1, max(len(rows)-1, 0))
	case "space":
		if m.toolCursor < len(rows) {
			r := rows[m.toolCursor]
			if err := m.draft.SetTool(m.detailID, r.name, !r.enabled); err != nil {
				m.status = m.inline(err)
			} else {
				m.status = ""
			}
		}
	case "l":
		return m.connect()
	case "esc":
		m.cancelLoad()
		m.screen, m.status = screenDetails, ""
	}
	return nil
}

// connectBlocked explains why Connect cannot run yet: the runtime uses the
// saved configuration, so the saved row must be enabled and ready and the
// draft must not change it.
func (m *Model) connectBlocked(id string) string {
	base, err := config.Resolve(m.draft.Base())
	if err != nil {
		return m.failure(err).Message
	}
	row, ok := base.Connections[id]
	label := rowLabel(m.detailRow())
	switch {
	case m.draft.Pending(id):
		return "Save first: the runtime uses the saved configuration."
	case !ok || !row.Available:
		return unavailableText(m.draft.State(), m.detailRow())
	case !row.Enabled:
		return label + " is off. Turn it on and save first: the runtime uses the saved configuration."
	case slices.Contains(row.Blockers, "review_required"):
		return label + " needs review. Press Space to accept it, then save."
	case slices.Contains(row.Blockers, "config_required"):
		return label + " needs configuration. Fill it in, then save."
	}
	return ""
}

// connect starts an explicit tool load for the details row and shows the
// tools screen. It is the only action that contacts the runtime.
func (m *Model) connect() tea.Cmd {
	if m.opts.LoadTools == nil {
		return nil
	}
	id := m.detailID
	m.openTools()
	if blocked := m.connectBlocked(id); blocked != "" {
		m.status = blocked
		return nil
	}
	if m.loading == id {
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
	return func() tea.Msg {
		list, err := load(ctx, id)
		return toolsMsg{seq: seq, id: id, list: list, err: err}
	}
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
	if msg.err != nil {
		m.loadErr[msg.id] = m.connectFailure(msg.id, msg.err)
		return
	}
	tools := []toolInfo{}
	for _, raw := range msg.list.Items {
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
	m.loaded[msg.id] = loadedTools{tools: tools, at: m.opts.Now()}
}

// connectFailure is the error message, its next action, and for connection
// failures (exit 6) the reassurance that configured MCPs still work.
func (m *Model) connectFailure(id string, err error) []string {
	failure := m.failure(err)
	lines := []string{failure.Message}
	next := failure.NextAction
	if failure.Code == "auth_required" || failure.Code == "auth_expired" {
		next = "Run mcparcel auth " + output.DisplayMetadata(output.AuthTarget(id, id)) + "."
	}
	if next != "" {
		lines = append(lines, output.DisplayMetadata(next))
	}
	if output.ExitCode(failure) == 6 || errors.Is(err, context.DeadlineExceeded) {
		lines = append(lines, "Could not connect. Configured MCPs on this device still work.")
	}
	return lines
}
