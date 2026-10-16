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
	compactChrome         = 6 // header, tabs, search, columns, summary, status; then the bottom lines
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
	return m.titledPage(m.theme.paint(roleTitle, fit(title, w)), body, focus, help)
}

// titledPage is page with a title line the caller rendered.
func (m *Model) titledPage(title string, body []string, focus int, help string) []string {
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
	// A body that does not fit ends in a line that says how much is cut.
	shown := room
	if len(body) > room && room > 1 {
		shown--
	}
	offset := 0
	if at >= shown {
		offset = at - shown + 1
	}
	lines := []string{title}
	for i := offset; i < len(body) && i < offset+shown; i++ {
		line := fit(body[i], w)
		if i == at {
			line = m.theme.paint(roleSelected, pad(line, w))
		}
		lines = append(lines, line)
	}
	if cut := len(body) - offset - shown; cut > 0 && shown < room {
		lines = append(lines, m.theme.paint(roleDim, fit(plural(cut, "more line", "more lines")+" below.", w)))
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

// Compact list: the focus prefix, the glyph, MCP and a Status column. It
// fits 40 columns. MCP is as wide as the longest label needs, so Status gets
// the rest, and at least compactTag. The Status column ends in the gutter
// like the others.
func compactTag(w int) int { return min(max(w*2/5, 14), 30) }

func (m *Model) compactTagWidth(w int) int {
	lead := 2 + textWidth(m.theme.glyph.on) + 1
	return min(max(w-lead-m.labelWidth(), compactTag(w)), max(w-lead-8, 1))
}

// labelWidth is the cells the longest label of any server takes, with its
// gutter, so the columns stay put across domains.
func (m *Model) labelWidth() int {
	n := 0
	for _, row := range m.live.Effective().Connections {
		n = max(n, textWidth(rowLabel(row))+gutter)
	}
	return n
}

func (m *Model) compactRow(w int) rowFormat {
	lead := 2 + textWidth(m.theme.glyph.on) + 1
	tag := m.compactTagWidth(w)
	return func(row config.EffectiveConnection, prefix string, styled bool) string {
		st := m.statusOf(row)
		line := prefix + m.markText(st, styled) + " " + cell(rowLabel(row), max(w-lead-tag, 8)) + m.statusText(st, tag, styled)
		return fit(line, w)
	}
}

func (m *Model) compactList() []string {
	w, _ := m.size()
	lead := 2 + textWidth(m.theme.glyph.on) + 1
	columns := m.theme.paint(roleDim, fit(strings.Repeat(" ", lead)+cell("MCP", max(w-lead-m.compactTagWidth(w), 8))+"Status", w))
	lines := []string{m.header(w), m.scrollTabs(w), m.searchLine(w), columns}
	lines = append(lines, m.listBody(w, m.compactRow(w))...)
	summary := ""
	if row, ok := m.selected(); ok {
		summary = fit(m.stateText(row), w)
	}
	lines = append(lines, summary, m.statusLine(w))
	return append(lines, m.bottomLines(w)...)
}

// helpText is the English key reference shown with ?, with the theme's
// glyphs in its legend.
func helpText(g glyphs) []string {
	return []string{
		"Every change applies at once; u undoes it.",
		"List",
		"  Tab, Shift+Tab  next or previous domain",
		"  Up/Down         choose a server",
		"  Right, Enter    open the server",
		"  Space           turn on or off; asks for what is missing first",
		"  s               set up what is on: start it, sign in, load its tools",
		"  u               undo the last change, again and again",
		"  n               new personal connection",
		"  /               search every domain; Esc clears it",
		"  q               quit; while setting up, stop (Esc skips a sign-in)",
		"Server page",
		"  Up/Down         choose an action; the first is the next step",
		"  Enter           do it; Right only opens Tools or Edit",
		"  Space, s, u     on or off, set up this server, undo",
		"  i               technical facts",
		"  Left, Esc       back",
		"Marks",
		"  " + g.on + " on and ready     " + g.pending + " on, needs a step",
		"  " + g.unavailable + " on, can't run    " + g.off + " off",
		"  " + g.done + " done   " + g.todo + " to do   " + g.later + " happens later",
		"Status",
		"  review change   accept it on its page",
		"  needs <input>   set it on its page",
		"  link 1Password  link a profile on its page",
		"  not installed   install the command",
		"  sign in         s signs you in, in your browser",
		"  N tools         it works; it started this session",
		"  starts on first use: nothing left to do",
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
	return m.page("Help", text[m.helpOffset:], -1, "Up/Down: scroll   Esc or ?: close")
}

func (m *Model) helpKey(k string) {
	switch k {
	case "up":
		m.helpOffset--
	case "down":
		m.helpOffset++
	case "esc", "left", "?", "q", "enter":
		m.screen = m.back
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
