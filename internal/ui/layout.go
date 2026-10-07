package ui

import (
	"fmt"
	"strings"

	"github.com/dedene/mcparcel/internal/config"
)

// Layout thresholds: the wide list needs 100x30; anything smaller down to
// 40x10 uses the compact list; below that setup only asks for a resize.
const (
	wideWidth, wideHeight = 100, 30
	minWidth, minHeight   = 40, 10
	compactChrome         = 8 // header, tabs, search, columns, summary, status, help, actions
)

func (m *Model) tooSmall() bool {
	w, h := m.size()
	return w < minWidth || h < minHeight
}

func (m *Model) compact() bool {
	w, h := m.size()
	return w < wideWidth || h < wideHeight
}

func (m *Model) render() string {
	w, h := m.size()
	var lines []string
	switch {
	case m.tooSmall():
		lines = wrap("Terminal too small (need 40x10). Resize, or press q.", w)
		if m.screen == screenConfirm {
			lines = append(lines, fit(m.statusLine(), w))
		}
	case m.screen == screenHelp:
		lines = m.helpPage()
	case m.screen == screenDetails:
		lines = m.detailsPage()
	case m.screen == screenField:
		lines = m.fieldPage()
	case m.screen == screenProfile:
		lines = m.profilePage()
	case m.screen == screenTools:
		lines = m.toolsPage()
	case m.screen == screenForm:
		lines = m.formPage()
	case m.compact():
		lines = m.compactList()
	default:
		lines = m.wideList()
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return strings.Join(lines, "\n")
}

// page renders a full-screen page: a bold title, the body scrolled so the
// focus line stays visible (reverse video, "> " is the caller's), then the
// status (up to two lines) and one help line at the bottom. Long lines wrap.
func (m *Model) page(title string, body []string, focus int, help string) []string {
	w, h := m.size()
	status := wrap(m.status, w)
	if len(status) > 2 {
		status = status[:2]
	}
	room := max(h-2-len(status), 1)
	var wrapped []string
	for i, line := range body {
		if i == focus {
			focus = len(wrapped)
			wrapped = append(wrapped, line)
			continue
		}
		wrapped = append(wrapped, wrap(line, w)...)
	}
	body = wrapped
	offset := 0
	if focus >= room {
		offset = focus - room + 1
	}
	lines := []string{m.style.bold(fit(title, w))}
	for i := offset; i < len(body) && i < offset+room; i++ {
		line := fit(body[i], w)
		if i == focus {
			line = m.style.reverse(pad(line, w))
		}
		lines = append(lines, line)
	}
	for len(lines) < 1+room {
		lines = append(lines, "")
	}
	for _, line := range status {
		lines = append(lines, fit(line, w))
	}
	return append(lines, fit(help, w))
}

// wrap breaks text into lines of at most n bytes at spaces, splitting words
// longer than a line.
func wrap(text string, n int) []string {
	if len(text) <= n || n <= 0 {
		return []string{text}
	}
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		for len(word) > n {
			if line != "" {
				lines, line = append(lines, line), ""
			}
			lines, word = append(lines, word[:n]), word[n:]
		}
		switch {
		case line == "":
			line = word
		case len(line)+1+len(word) <= n:
			line += " " + word
		default:
			lines, line = append(lines, line), word
		}
	}
	return append(lines, line)
}

// Compact list columns: MCP with a state tag, Enabled, Runs on and a
// truncated Source.
const (
	compactEnabled = 4
	compactRunsOn  = 13
)

func (m *Model) compactSource(w int) int { return min(max(w/5, 6), 20) }

func (m *Model) compactColumns(mcp, enabled, runs, source string, w int) string {
	mcpWidth := max(w-compactEnabled-compactRunsOn-m.compactSource(w), 8)
	return fit(pad(mcp, mcpWidth)+pad(enabled, compactEnabled)+pad(runs, compactRunsOn)+source, w)
}

func stateTag(row config.EffectiveConnection) string {
	switch readiness(row) {
	case "Unavailable":
		return " (unavailable)"
	case "Review required":
		return " (review)"
	case "Configuration required":
		return " (config)"
	}
	return ""
}

func (m *Model) compactList() []string {
	w, _ := m.size()
	lines := []string{m.style.bold(spread("MCParcel / Setup", m.sourceSummary(), w)), m.scrollTabs(w), m.searchLine(w), m.compactColumns("  MCP", "On", "Runs on", "Source", w)}
	state := m.draft.State()
	lines = append(lines, m.listBody(w, func(row config.EffectiveConnection, prefix string) string {
		return m.compactColumns(prefix+rowLabel(row)+stateTag(row), enabledMark(row), runsOn(row), fit(sourceLabel(state, row), m.compactSource(w)), w)
	})...)
	summary, status := "", m.statusLine()
	if m.screen == screenConflict {
		summary, status = m.conflict, "r: reload and reapply   Esc: back"
	} else if row, ok := m.selected(); ok {
		summary = m.stateText(row)
	}
	help := "Space: toggle  Enter: details  /: search  Ctrl+S: save  ?: help"
	if m.screen == screenSearch {
		help = searchHelp
	}
	return append(lines, fit(summary, w), fit(status, w), fit(help, w), m.actionLine(w))
}

// scrollTabs shows as many tabs as fit around the current one, with < and >
// marking tabs scrolled off either side.
func (m *Model) scrollTabs(w int) string {
	effective := m.draft.Effective()
	current := m.currentTab()
	all := tabs(effective)
	labels := make([]string, len(all))
	at := 0
	for i, t := range all {
		label := tabLabel(effective, t, m.search.String())
		if t.id == current.id {
			at, label = i, "["+label+"]"
		} else {
			label = " " + label + " "
		}
		labels[i] = label
	}
	width := func(from, to int) int {
		n := len(strings.Join(labels[from:to+1], " "))
		if from > 0 {
			n += 2
		}
		if to < len(labels)-1 {
			n += 2
		}
		return n
	}
	from, to := at, at
	for from > 0 && width(from-1, to) <= w {
		from--
	}
	for to < len(labels)-1 && width(from, to+1) <= w {
		to++
	}
	line := strings.Join(labels[from:to+1], " ")
	if from > 0 {
		line = "< " + line
	}
	if to < len(labels)-1 {
		line += " >"
	}
	return fit(line, w)
}

// helpText is the English key reference shown with ?.
var helpText = []string{
	"List",
	"  Left/Right      domain tab",
	"  Up/Down         MCP; PgUp/PgDn, Home/End jump",
	"  Space           turn on or off; on [!] it accepts the review",
	"  Enter           details",
	"  /               search label, ID and description in every tab",
	"  Tab, Shift+Tab  focus [Save] or [Cancel]; Enter activates",
	"  A               add a personal connection",
	"  Ctrl+S          save; setup stays open",
	"  q, Esc          cancel; Esc clears an active search first",
	"  Ctrl+C          quit now and discard unsaved changes",
	"Search",
	"  Typing          filters label, ID and description in every tab",
	"  Backspace       delete a character; Ctrl+U clears the query",
	"  Enter, Down     back to the list, filter kept; Esc clears it",
	"Details",
	"  Up/Down, Enter  choose a field: input, profile, tools",
	"  Space           turn on or off",
	"  t               tools   l: connect and load tools",
	"  e               edit a personal connection",
	"  Esc             back",
	"Tools",
	"  Up/Down, Space  choose and turn a tool on or off",
	"  l               connect and load tools; Esc cancels a load and goes back",
	"Forms and fields",
	"  Tab, Shift+Tab  next or previous field; Enter applies; Esc discards",
	"Conflict",
	"  r               reload and reapply your changes; Esc: back to the draft",
	"Confirm",
	"  y               discard; n, Enter or Esc keep editing",
	"Marks",
	"  [x] on   [ ] off   [!] on, review required",
	"Setup never contacts GitHub. Only Connect contacts the runtime,",
	"and credentials may then be requested. Every change has a command:",
	"run mcparcel --help.",
}

func (m *Model) helpPage() []string {
	_, h := m.size()
	m.helpOffset = min(max(m.helpOffset, 0), max(len(helpText)-(h-3), 0)) // the help page has no status
	return m.page("MCParcel / Setup / Help", helpText[m.helpOffset:], -1, "Up/Down: scroll   Esc or ?: close")
}

func (m *Model) helpKey(k string) {
	switch k {
	case "up":
		m.helpOffset--
	case "down":
		m.helpOffset++
	case "esc", "?", "q", "enter":
		m.screen = screenList
	}
	_ = m.helpPage() // clamps the offset
}

func ages(seconds float64) string {
	switch {
	case seconds < 60:
		return "less than 1m"
	case seconds < 3600:
		return fmt.Sprintf("%dm", int(seconds/60))
	case seconds < 48*3600:
		return fmt.Sprintf("%dh", int(seconds/3600))
	}
	return fmt.Sprintf("%dd", int(seconds/86400))
}
