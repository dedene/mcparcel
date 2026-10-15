package ui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/dedene/mcparcel/internal/output"
)

// The join screens' look: the intro names the catalog and asks about one
// suggested 1Password account at a time, with how earlier ones ended above
// it; the presets screen is a list of presets to pick, then Start empty.

const joinTitle = "MCParcel / Join"

// joinHeading is the catalog's name, repository and commit on one line.
func (m *Model) joinHeading() string {
	j := m.join
	var parts []string
	if j.catalog.Name != "" {
		parts = append(parts, output.DisplayMetadata(firstLine(j.catalog.Name)))
	}
	for _, part := range []string{j.Repository, j.Commit} {
		if part != "" {
			parts = append(parts, output.DisplayMetadata(part))
		}
	}
	return strings.Join(parts, m.theme.glyph.sep)
}

func (m *Model) joinPage() []string {
	w, _ := m.size()
	j := m.join
	body := []string{m.joinHeading(), plural(j.servers, "MCP server", "MCP servers") + " picked by the team."}
	if len(j.notes) > 0 {
		body = append(append(body, ""), j.notes...)
	}
	ask := j.ask()
	if ask == nil {
		return m.page(joinTitle, body, -1, "Enter: continue   q: quit")
	}
	question := "This catalog asks for 1Password access to " + requirementLabel(*ask)
	if j.total > 1 {
		question += fmt.Sprintf(" (%d of %d)", j.total-len(j.asks)+1, j.total)
	}
	body = append(body, "", question+".")
	help := "Enter: use it   e: use another account   Esc: skip 1Password for now"
	if textWidth(strings.ReplaceAll(help, ": ", " ")) > w {
		help = "Enter: use it   e: other   Esc: skip"
	}
	// The account is the focus line, so it stays on screen when the rest
	// scrolls. It is never cut: one too wide for the line goes below
	// "Account:", wrapped, with its last part as the focus line.
	body = append(body, "Use this 1Password account? The catalog suggests it.")
	var focus int
	if j.editing {
		body[len(body)-1] = "Type the sign-in address of your 1Password account."
		help = "Enter: use this account   Esc: back"
		focus = len(body)
		body = append(body, "Account: "+j.account.render(w-len("Account: ")))
	} else {
		account, lead := output.DisplayMetadata(ask.Suggest.Account), "Account: "
		parts := []string{account}
		if textWidth(lead+account) > w {
			body, lead = append(body, "Account:"), ""
			parts = wrap(account, w)
		}
		for _, part := range parts[:len(parts)-1] {
			body = append(body, m.theme.paint(roleInfo, part))
		}
		focus = len(body)
		body = append(body, lead+m.theme.paint(roleInfo, parts[len(parts)-1]))
	}
	body = append(body, plural(len(ask.Connections), "server uses", "servers use")+" it. Nothing is read from 1Password until a server starts.")
	return m.page(joinTitle, body, focus, help)
}

func (m *Model) presetPage() []string {
	w, _ := m.size()
	j, g := m.join, m.theme.glyph
	body := []string{"What do you work on? Pick any. You can change everything on the next screen.", ""}
	effective := m.draft.Effective()
	markWidth := textWidth(g.on)
	lead := 2 + markWidth + 1
	labelWidth, servers := len("Start empty")+gutter, make([]string, len(j.picked))
	for i, p := range j.catalog.Presets {
		labelWidth = max(labelWidth, textWidth(output.DisplayMetadata(p.Label))+gutter)
		labels := make([]string, 0, len(p.Connections))
		for _, id := range p.Connections {
			labels = append(labels, rowLabel(effective.Connections[id]))
		}
		servers[i] = strings.Join(labels, ", ")
	}
	labelWidth = min(labelWidth, max(w/3, 12))
	// The count follows the longest list of servers, or the line's end.
	const countWidth = 4
	middle := 0
	for _, text := range servers {
		middle = max(middle, textWidth(text)+gutter)
	}
	room := w - lead - labelWidth - countWidth
	middle = min(middle, room)
	focus := -1
	for i, p := range j.catalog.Presets {
		styled := i != j.cursor
		paint := func(r role, text string) string {
			if !styled {
				return text
			}
			return m.theme.paint(r, text)
		}
		prefix, mark, markRole := "  ", g.off, roleDim
		if !styled {
			prefix, focus = "> ", len(body)
		}
		if j.picked[i] {
			mark, markRole = g.on, roleOK
		}
		line := prefix + paint(markRole, pad(mark, markWidth)) + " " + cell(output.DisplayMetadata(p.Label), labelWidth)
		if room >= 8 {
			line += paint(roleDim, cell(servers[i], middle))
			line += fmt.Sprintf("%*d", countWidth, len(p.Connections))
		}
		body = append(body, fit(line, w))
	}
	prefix := "  "
	if j.cursor == len(j.picked) {
		prefix, focus = "> ", len(body)
	}
	body = append(body, fit(prefix+strings.Repeat(" ", markWidth+1)+"Start empty", w))
	body = append(body, "", plural(len(j.presetIDs()), "server", "servers")+" selected")
	if j.cursor < len(j.picked) {
		if d := j.catalog.Presets[j.cursor].Description; d != "" {
			body = append(body, m.theme.paint(roleDim, output.DisplayMetadata(firstLine(d))))
		}
	}
	help := "Space: pick   Enter: continue   q: quit"
	if j.cursor == len(j.picked) && slices.Contains(j.picked, true) {
		help = "Enter: start empty, without the picks   q: quit"
	}
	return m.page(joinTitle, body, focus, help)
}
