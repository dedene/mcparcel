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
	case m.screen == screenFinish:
		lines = m.finishPage()
	case m.screen == screenJoin:
		lines = m.joinPage()
	case m.screen == screenPresets:
		lines = m.presetPage()
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
	w, _ := m.size()
	status, room := m.pageStatus()
	// at is the focus line's index once the lines before it have wrapped;
	// the focus line itself is cut, not wrapped, so it stays one line.
	at := -1
	var wrapped []string
	for i, line := range body {
		if i == focus {
			at = len(wrapped)
			wrapped = append(wrapped, line)
			continue
		}
		wrapped = append(wrapped, wrap(line, w)...)
	}
	body = wrapped
	offset := 0
	if at >= room {
		offset = at - room + 1
	}
	lines := []string{m.theme.paint(roleTitle, fit(title, w))}
	for i := offset; i < len(body) && i < offset+room; i++ {
		line := fit(body[i], w)
		if i == at {
			line = m.theme.paint(roleSelected, pad(line, w))
		}
		lines = append(lines, line)
	}
	for len(lines) < 1+room {
		lines = append(lines, "")
	}
	for _, line := range status {
		line = fit(line, w)
		if m.toastFading {
			line = m.theme.paint(roleDim, line)
		}
		lines = append(lines, line)
	}
	return append(lines, m.keys(help, w))
}

// pageStatus is a page's status, wrapped to at most two lines, and the room
// left for its body between the title and the status.
func (m *Model) pageStatus() ([]string, int) {
	w, h := m.size()
	status := wrap(m.status, w)
	if len(status) > 2 {
		status = status[:2]
	}
	return status, max(h-2-len(status), 1)
}

// wrapAtMost wraps text to n cells in at most most lines; the last line
// holds the rest, cut with "...".
func wrapAtMost(text string, n, most int) []string {
	lines := wrap(text, n)
	if len(lines) > most {
		lines = append(lines[:most-1], fit(strings.Join(lines[most-1:], " "), n))
	}
	return lines
}

// wrap breaks text into lines of at most n cells at spaces, splitting words
// longer than a line.
func wrap(text string, n int) []string {
	if textWidth(text) <= n || n <= 0 {
		return []string{text}
	}
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		for textWidth(word) > n {
			if line != "" {
				lines, line = append(lines, line), ""
			}
			head := truncate(word, n, "")
			if textWidth(head) == 0 {
				break // a character wider than the line; fit cuts it later
			}
			lines, word = append(lines, head), cutLeft(word, textWidth(head))
		}
		switch {
		case line == "":
			line = word
		case textWidth(line)+1+textWidth(word) <= n:
			line += " " + word
		default:
			lines, line = append(lines, line), word
		}
	}
	return append(lines, line)
}

// Compact list: the focus prefix, the glyph, MCP and a short Needs column
// (as many of the row's needs as fit, open ones first, see
// rowLook.listNeeds). It fits 40 columns. The Needs column ends in the gutter
// like the others.
func compactTag(w int) int { return min(max(w*2/5, 14), 30) }

func (m *Model) compactRow(w int) rowFormat {
	state := m.draft.State()
	lead := 2 + textWidth(m.theme.glyph.on) + 1
	tag := compactTag(w)
	return func(row config.EffectiveConnection, prefix string, styled bool) string {
		look := lookOf(state, row)
		line := prefix + m.markText(look, styled) + " " + cell(rowLabel(row), max(w-lead-tag, 8)) + m.needsText(look.listNeeds(), tag, styled)
		return fit(line, w)
	}
}

func (m *Model) compactList() []string {
	w, _ := m.size()
	lead := 2 + textWidth(m.theme.glyph.on) + 1
	columns := m.theme.paint(roleDim, fit(strings.Repeat(" ", lead)+cell("MCP", max(w-lead-compactTag(w), 8))+"Needs", w))
	lines := []string{m.header(w), m.scrollTabs(w), m.searchLine(w), columns}
	lines = append(lines, m.listBody(w, m.compactRow(w))...)
	summary, status := "", m.statusText(w)
	if row, ok := m.selected(); ok {
		summary = fit(m.stateText(row), w)
	}
	help := "Space: on/off   Enter: details   /: search   Ctrl+S: save   ?: help"
	switch m.screen {
	case screenSearch:
		help = searchHelp
	case screenConflict: // the message takes the summary and status lines
		conflict := wrapAtMost(m.conflict, w, 2)
		summary, status = conflict[0], ""
		if len(conflict) > 1 {
			status = conflict[1]
		}
		help = conflictKeys
	}
	return append(lines, summary, status, m.keys(help, w), m.actionLine(w))
}

// helpText is the English key reference shown with ?, with the theme's
// glyphs in its legend.
func helpText(g glyphs) []string {
	return []string{
		"List",
		"  Left/Right      domain tab",
		"  Up/Down         MCP; PgUp/PgDn, Home/End jump",
		"  Space           turn on or off; on " + g.review + " it accepts the review",
		"                  asks for missing configuration first; Esc keeps it off",
		"  Enter           details",
		"  /               search label, ID and description in every tab",
		"  Tab, Shift+Tab  focus [Save] or [Cancel]; Enter activates",
		"  A               add a personal connection",
		"  Ctrl+S          save; setup stays open",
		"  q, Esc          cancel; Esc clears an active search first",
		"  Ctrl+C          quit now and discard unsaved changes",
		"Search",
		"  Typing          filters label, ID and description in every tab;",
		"                  with no match here it moves to the first tab with one",
		"  Backspace       delete a character; Ctrl+U clears the query",
		"  Enter, Down     back to the list, filter kept; Esc clears it",
		"Details",
		"  Up/Down, Enter  choose a field: input, profile, tools",
		"                  a single credential profile is linked without asking",
		"  Space           turn on or off",
		"  t               tools   l: connect and load tools",
		"  e               edit a personal connection",
		"  Esc             back",
		"Tools",
		"  Up/Down, Space  choose and turn a tool on or off",
		"  l               connect and load tools; Esc cancels a load and goes back",
		"Forms and fields",
		"  Tab, Shift+Tab  next or previous field; Enter applies; Esc discards",
		"Finish (after a save turns MCPs on)",
		"  Enter           start them, sign in where needed, load their tools",
		"  Esc             later; on the running screen it stops",
		"  Enter, s        sign in to the one asked for, or skip it",
		"  r               retry the ones that failed",
		"Join (mcparcel join, before the list)",
		"  Enter           use the suggested 1Password account",
		"  e               type another account; Esc: back",
		"  Esc             skip 1Password for now",
		"  Space           pick a preset; Enter: on to the list",
		"  q               quit; a profile you confirmed stays",
		"Conflict",
		"  r               reload and reapply your changes; Esc: back to the draft",
		"Confirm",
		"  y               discard; n, Enter or Esc keep editing",
		"Marks",
		"  " + g.on + " on and ready      " + g.off + " off",
		"  " + g.needsConfig + " off, needs configuration",
		"  " + g.pending + " on, needs configuration",
		"  " + g.review + " on, review required",
		"  " + g.unavailable + " on, but its source no longer defines it",
		"Needs",
		"  1Password       a credential profile to link",
		"  sign-in         an OAuth sign-in in a browser on first connect",
		"  input <name>    a value to fill in",
		"  a command       runs on this device (npx, docker, uvx, ...)",
		"Setup never contacts GitHub. While it is open, only Connect and the",
		"finish step contact the runtime, and credentials may then be",
		"requested. On exit setup checks stored sign-ins, as auth status does.",
		"Every change has a command: run mcparcel --help.",
	}
}

// helpKeyColumn is where a key row's description starts.
const helpKeyColumn = 18

// helpLines is the help text wrapped to w cells. A key row that does not
// fit wraps under its own description, so the key column stays aligned.
func helpLines(g glyphs, w int) []string {
	indent := strings.Repeat(" ", helpKeyColumn)
	var out []string
	for _, line := range helpText(g) {
		key, description, ok := keyRow(line)
		if !ok || textWidth(line) <= w || w-helpKeyColumn < 12 {
			out = append(out, wrap(line, w)...)
			continue
		}
		for i, part := range wrap(description, w-helpKeyColumn) {
			if i == 0 {
				out = append(out, key+part)
			} else {
				out = append(out, indent+part)
			}
		}
	}
	return out
}

// keyRow splits a help row into its key column and its description: a row
// indented two spaces whose key column is ASCII and ends in two spaces.
func keyRow(line string) (key, description string, ok bool) {
	if len(line) <= helpKeyColumn || !strings.HasPrefix(line, "  ") || line[helpKeyColumn-2:helpKeyColumn] != "  " {
		return "", "", false
	}
	for i := range helpKeyColumn {
		if line[i] > 126 {
			return "", "", false
		}
	}
	return line[:helpKeyColumn], line[helpKeyColumn:], true
}

func (m *Model) helpPage() []string {
	w, _ := m.size()
	text := helpLines(m.theme.glyph, w)
	_, room := m.pageStatus()
	m.helpOffset = min(max(m.helpOffset, 0), max(len(text)-room, 0))
	return m.page("MCParcel / Setup / Help", text[m.helpOffset:], -1, "Up/Down: scroll   Esc or ?: close")
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
