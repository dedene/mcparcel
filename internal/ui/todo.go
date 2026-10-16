package ui

import (
	"fmt"
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/edit"
	"github.com/dedene/mcparcel/internal/output"
)

// What the server page and the pane beside the wide list say about one
// server, in plain words: its state, its To do, where it runs and where it
// comes from. The To do reads the row's Status, so it knows what this
// session and this device know. Only non-secret metadata: never an
// argument, env or header value, never a URL path or query, never a
// profile's account.

type todoMark int

const (
	todoDone  todoMark = iota // ✓
	todoStep                  // ◆ left for you
	todoLater                 // ○ happens on its own, after the steps
)

type todoItem struct {
	mark todoMark
	text string
	role role // a step's color
}

// todo is the row's To do: on, a review, each input, the 1Password
// profile, its command, the sign-in and its tools. An unavailable row has
// none.
func (m *Model) todo(row config.EffectiveConnection) []todoItem {
	d := row.Definition
	if !row.Available || d == nil {
		return nil
	}
	state := m.live.State()
	st := m.statusOf(row)
	var items []todoItem
	add := func(mark todoMark, r role, text string) { items = append(items, todoItem{mark, text, r}) }
	switch {
	case !row.Enabled:
		add(todoStep, roleWarn, "Turn it on")
	case row.ReviewRequired:
		add(todoDone, roleOK, "On")
		add(todoStep, roleWarn, "Accept the change")
	default:
		add(todoDone, roleOK, "On")
	}
	missing, profileMissing := config.MissingConfig(state, row)
	for _, name := range sortedInputs(d) {
		if slices.Contains(missing, name) {
			add(todoStep, roleWarn, "Set "+output.DisplayMetadata(name))
		} else {
			add(todoDone, roleOK, output.DisplayMetadata(name)+" is set")
		}
	}
	if d.CredentialProfile != "" {
		switch auto := edit.AutoProfile(state, row); {
		case !profileMissing:
			add(todoDone, roleOK, "1Password: "+output.DisplayMetadata(state.Selections.Connections[row.ID].CredentialProfile))
		case auto != "" && enabling(row):
			add(todoDone, roleOK, "1Password: links "+output.DisplayMetadata(auto)+" when on")
		default:
			add(todoStep, roleWarn, "Link a 1Password profile")
		}
	}
	if command := m.missingCommand(row); command != "" {
		add(todoStep, roleBad, "Install "+command)
	}
	if signIn(d) {
		s, known := m.storedSignIn(row)
		switch {
		case st.kind == stManual || st.kind == stSignIn && m.opts.SignIn == nil:
			add(todoStep, roleWarn, "Sign in: run mcparcel auth "+authName(m.live.Effective(), row.ID))
		case st.kind == stSignIn || st.kind == stSigning:
			add(todoStep, roleAccent, "Sign in with your browser")
		case known && s.Unknown:
			add(todoLater, roleDim, "Sign-in unknown: it can't be read without a prompt")
		case known && !s.Needed:
			add(todoDone, roleOK, "Signed in")
		default:
			add(todoLater, roleDim, "Sign in when it first starts")
		}
	}
	switch tools := m.loaded[row.ID].tools; {
	case st.kind == stFailed && st.text == "could not start":
		add(todoStep, roleBad, "Could not start")
	case st.kind == stFailed:
		add(todoStep, roleBad, "Could not start: "+st.text)
	case tools != nil:
		add(todoDone, roleOK, plural(len(tools), "tool", "tools")+" loaded")
	case st.kind == stWorking:
		add(todoLater, roleDim, "Tools are loading")
	case st.kind == stSignIn || st.kind == stManual || st.kind == stSigning:
		add(todoLater, roleDim, "Tools load after sign-in")
	default:
		add(todoLater, roleDim, "Tools load on first use")
	}
	return items
}

// todoLine renders one To do item after an indent, colored when styled.
func (m *Model) todoLine(item todoItem, indent string, n int, styled bool) string {
	g := m.theme.glyph
	mark, r := g.later, roleDim
	switch item.mark {
	case todoDone:
		mark, r = g.done, roleOK
	case todoStep:
		mark, r = g.todo, item.role
	}
	text := fit(item.text, n-textWidth(indent+mark+" "))
	if !styled {
		return indent + mark + " " + text
	}
	return indent + m.theme.paint(r, mark) + " " + text
}

// stateWords is the row's state in a few words, beside its mark: off, what
// it needs, or that it is ready. It is colored by role.
func (m *Model) stateWords(row config.EffectiveConnection, st status) (string, role) {
	if !row.Enabled {
		if st.text != "" {
			return "off" + m.theme.glyph.sep + st.text, roleDim
		}
		return "off", roleDim
	}
	switch st.kind {
	case stReview:
		return "needs a review", st.role
	case stInput:
		return "needs " + st.detail, st.role
	case stProfile:
		return "needs a 1Password profile", st.role
	case stCommand:
		return st.detail + " not installed", st.role
	case stManual, stSignIn:
		return "needs a sign-in", st.role
	case stChecking:
		return "checking sign-in", roleDim
	case stSignInUnknown, stTools:
		return "ready" + m.theme.glyph.sep + st.text, roleOK
	case stFirstUse:
		return "ready", roleOK
	}
	return st.text, st.role // unavailable, a failure, setting up
}

// runsText says where a row runs and what: "this device · npx" or
// "remote · errors.example", never an argument, a URL path or a query.
func runsText(row config.EffectiveConnection, sep string) string {
	c := row.Connection
	if c == nil {
		c = row.Definition
	}
	switch {
	case c == nil:
		return "-"
	case c.Transport.Stdio != nil:
		command := valueText(c.Transport.Stdio.Command)
		if v := c.Transport.Stdio.Command.Literal; v != nil {
			command = output.DisplayMetadata(path.Base(*v))
		}
		return "this device" + sep + command
	case c.Transport.HTTP.URL.Literal == nil:
		return "remote" + sep + valueText(c.Transport.HTTP.URL)
	}
	u, err := url.Parse(*c.Transport.HTTP.URL.Literal)
	if err != nil {
		return "remote" + sep + "(invalid URL)"
	}
	where := "remote"
	if isLoopback(u.Hostname()) {
		where = "this device"
	}
	return where + sep + output.DisplayMetadata(u.Host)
}

// sourceName names the catalog a row comes from by its name, else its
// repository, or Personal.
func sourceName(state config.State, row config.EffectiveConnection) string {
	if name := state.Catalogs[row.SourceID].Name; name != "" && row.SourceID != "personal" {
		return output.DisplayMetadata(firstLine(name))
	}
	return sourceLabel(state, row)
}

// fromText names the catalog a row comes from (or Personal) and its
// domains.
func (m *Model) fromText(row config.EffectiveConnection) string {
	from := sourceName(m.live.State(), row)
	if row.Definition == nil {
		return from
	}
	names := make([]string, 0, len(row.Definition.Domains))
	for _, id := range row.Definition.Domains {
		names = append(names, m.domainLabel(id))
	}
	return from + m.theme.glyph.sep + strings.Join(names, ", ")
}

func (m *Model) domainLabel(id string) string {
	if label := m.live.Effective().Domains[id].Label; label != "" {
		return output.DisplayMetadata(label)
	}
	return output.DisplayMetadata(id)
}

// facts are the Runs and From lines, their names dim, in n cells.
func (m *Model) facts(row config.EffectiveConnection, n int) []string {
	field := func(name, value string) string {
		return m.theme.paint(roleDim, pad(name, 6)) + fit(value, n-6)
	}
	var lines []string
	if row.Available {
		lines = append(lines, field("Runs", runsText(row, m.theme.glyph.sep)))
	}
	return append(lines, field("From", m.fromText(row)))
}

// technicalLines are the facts i shows on the server page, dim: the ID, the
// source with its commit, the full transport, the tool policy and the
// 1Password requirement.
func (m *Model) technicalLines(row config.EffectiveConnection, n int) []string {
	var lines []string
	field := func(name, value string) {
		for i, part := range wrap(value, max(n-13, 1)) {
			if i > 0 {
				name = ""
			}
			lines = append(lines, m.theme.paint(roleDim, pad(name, 13)+fit(part, n-13)))
		}
	}
	field("ID", output.DisplayMetadata(row.ID))
	field("Source", m.sourceText(row))
	if row.Definition == nil {
		return lines
	}
	field("Transport", transportText(row))
	field("Tool policy", m.policyText(row))
	if req := row.Definition.CredentialProfile; req != "" {
		text := "needs profile " + output.DisplayMetadata(req)
		if bound := m.live.State().Selections.Connections[row.ID].CredentialProfile; bound != "" {
			text += ", linked to " + output.DisplayMetadata(bound)
		}
		field("1Password", text)
	}
	return lines
}

// sourceText is the row's source: Personal, or the catalog's repository,
// commit and how long ago it synced.
func (m *Model) sourceText(row config.EffectiveConnection) string {
	if row.SourceID == "personal" {
		return "Personal"
	}
	for _, s := range m.sources {
		if s.Source.ID == row.SourceID {
			return fmt.Sprintf("%s, commit %s, synced %s ago", output.DisplayMetadata(s.Source.Owner+"/"+s.Source.Repo), output.DisplayMetadata(s.Source.Commit[:min(7, len(s.Source.Commit))]), ages(s.CacheAgeSeconds))
		}
	}
	return sourceLabel(m.live.State(), row)
}

// policyText is the tool policy: what the source allows and denies, and
// how many tools you turned off.
func (m *Model) policyText(row config.EffectiveConnection) string {
	parts := []string{"all tools"}
	if p := row.Definition.ToolPolicy; p != nil {
		if p.Allow != nil {
			parts[0] = fmt.Sprintf("%d allowed by source", len(*p.Allow))
		}
		if len(p.Deny) > 0 {
			parts = append(parts, fmt.Sprintf("%d denied by source", len(p.Deny)))
		}
	}
	if n := len(m.live.State().Selections.Connections[row.ID].DisabledTools); n > 0 {
		parts = append(parts, fmt.Sprintf("%d off", n))
	}
	return strings.Join(parts, ", ")
}
