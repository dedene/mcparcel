package ui

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// Details shows only non-secret metadata: never argument, env or header
// values, never a URL path or query, never a profile's account.

type itemKind int

const (
	itemInput itemKind = iota
	itemProfile
	itemTools
	itemConnect
	itemEdit
)

type detailItem struct {
	kind itemKind
	name string // input name
}

// inputEdit is the input being edited on the field screen.
type inputEdit struct {
	name, kind string
	value      field
	current    string // display of the current url value; empty Enter keeps it
	err        string
}

func (m *Model) openDetails(id string) {
	m.screen, m.detailID, m.detailCursor, m.status = screenDetails, id, 0, ""
}

func (m *Model) detailRow() config.EffectiveConnection {
	row, ok := m.draft.Effective().Connections[m.detailID]
	if !ok {
		return config.EffectiveConnection{ID: m.detailID}
	}
	return row
}

func sortedInputs(d *config.Connection) []string {
	names := make([]string, 0, len(d.Inputs))
	for name := range d.Inputs {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func (m *Model) detailItems(row config.EffectiveConnection) []detailItem {
	if !row.Available || row.Definition == nil {
		return nil
	}
	var items []detailItem
	for _, name := range sortedInputs(row.Definition) {
		items = append(items, detailItem{kind: itemInput, name: name})
	}
	if row.Definition.CredentialProfile != "" {
		items = append(items, detailItem{kind: itemProfile})
	}
	items = append(items, detailItem{kind: itemTools})
	if m.opts.LoadTools != nil {
		items = append(items, detailItem{kind: itemConnect})
	}
	if row.SourceID == "personal" {
		items = append(items, detailItem{kind: itemEdit})
	}
	return items
}

// firstMissing is the index of the first item missing configuration.
func (m *Model) firstMissing(row config.EffectiveConnection) int {
	sel := m.draft.State().Selections.Connections[row.ID]
	for i, item := range m.detailItems(row) {
		switch item.kind {
		case itemInput:
			if _, ok := sel.Inputs[item.name]; !ok && row.Definition.Inputs[item.name].Default == nil {
				return i
			}
		case itemProfile:
			if _, ok := m.draft.State().Local.CredentialProfiles[sel.CredentialProfile]; !ok {
				return i
			}
		}
	}
	return 0
}

// hostOnly shows an HTTP(S) URL as scheme and host, never its path or query.
func hostOnly(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(invalid URL)"
	}
	return output.DisplayMetadata(u.Scheme + "://" + u.Host + "/...")
}

// urlInput reports whether an input is shown as host only: a url input, or
// any input the HTTP URL takes its value from, whatever its kind.
func urlInput(d *config.Connection, name string) bool {
	if d.Inputs[name].Kind == "url" {
		return true
	}
	h := d.Transport.HTTP
	return h != nil && h.URL.Input != nil && h.URL.Input.Input == name
}

func valueText(v config.Value) string {
	switch {
	case v.Literal != nil:
		return output.DisplayMetadata(*v.Literal)
	case v.Input != nil:
		return "input " + output.DisplayMetadata(v.Input.Input)
	}
	return "(secret)"
}

func transportText(row config.EffectiveConnection) string {
	c := row.Connection
	if c == nil {
		c = row.Definition
	}
	if c == nil {
		return "-"
	}
	if s := c.Transport.Stdio; s != nil {
		return fmt.Sprintf("stdio: %s (%s)", valueText(s.Command), plural(len(s.Args), "argument", "arguments"))
	}
	h := c.Transport.HTTP
	if h.URL.Literal == nil {
		return "HTTP: " + valueText(h.URL)
	}
	u, err := url.Parse(*h.URL.Literal)
	if err != nil {
		return "HTTP: (invalid URL)"
	}
	return "HTTP: " + output.DisplayMetadata(u.Host)
}

func (m *Model) inputText(row config.EffectiveConnection, name string) string {
	input := row.Definition.Inputs[name]
	value, ok := m.draft.State().Selections.Connections[row.ID].Inputs[name]
	suffix := ""
	if !ok && input.Default != nil {
		value, ok, suffix = *input.Default, true, " (default)"
	}
	switch {
	case !ok:
		value = "(not set)"
	case urlInput(row.Definition, name):
		value = hostOnly(value)
	default:
		value = output.DisplayMetadata(value)
	}
	return fmt.Sprintf("Input %s (%s): %s%s", output.DisplayMetadata(name), input.Kind, value, suffix)
}

func (m *Model) profileText(row config.EffectiveConnection) string {
	bound := m.draft.State().Selections.Connections[row.ID].CredentialProfile
	switch _, ok := m.draft.State().Local.CredentialProfiles[bound]; {
	case bound == "":
		return "Credential profile: (not bound)"
	case !ok:
		return "Credential profile: " + output.DisplayMetadata(bound) + " (missing)"
	}
	return "Credential profile: " + output.DisplayMetadata(bound)
}

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
	if n := len(m.draft.State().Selections.Connections[row.ID].DisabledTools); n > 0 {
		parts = append(parts, fmt.Sprintf("%d disabled by you", n))
	}
	return strings.Join(parts, ", ")
}

func (m *Model) sourceText(row config.EffectiveConnection) string {
	if row.SourceID == "personal" {
		return "Source: Personal"
	}
	for _, s := range m.sources {
		if s.Source.ID == row.SourceID {
			return fmt.Sprintf("Source: %s, commit %s, snapshot %s old", output.DisplayMetadata(s.Source.Owner+"/"+s.Source.Repo), output.DisplayMetadata(s.Source.Commit[:min(7, len(s.Source.Commit))]), ages(s.CacheAgeSeconds))
		}
	}
	return "Source: " + sourceLabel(m.draft.State(), row)
}

func (m *Model) domainsText(row config.EffectiveConnection) string {
	if row.Definition == nil || len(row.Definition.Domains) == 0 {
		return "Domains: none"
	}
	names := make([]string, 0, len(row.Definition.Domains))
	for _, id := range row.Definition.Domains {
		label := m.draft.Effective().Domains[id].Label
		if label == "" {
			label = id
		}
		names = append(names, output.DisplayMetadata(label))
	}
	return "Domains: " + strings.Join(names, ", ")
}

func (m *Model) schemaText(id string) string {
	loaded, ok := m.loaded[id]
	if !ok {
		return "Schema: none cached"
	}
	return "Schema: loaded this session " + ages(m.opts.Now().Sub(loaded.at).Seconds()) + " ago"
}

func (m *Model) revisionText() string {
	text := fmt.Sprintf("Config revision %d, ", m.draft.Base().Selections.Revision)
	if n := m.draft.Changes(); n > 0 {
		return text + plural(n, "unsaved change", "unsaved changes")
	}
	return text + "no unsaved changes"
}

func (m *Model) detailsPage() []string {
	row := m.detailRow()
	body := []string{"ID: " + output.DisplayMetadata(row.ID), "Enabled: " + enabledMark(row) + "   State: " + readiness(row), m.sourceText(row)}
	if row.Definition != nil {
		body = append(body, m.domainsText(row), "Transport: "+transportText(row))
		if row.SourceID == "personal" {
			body = append(body, "Remove it with mcparcel local remove "+output.DisplayMetadata(row.ID)+".")
		} else {
			body = append(body, "Shared definition: read-only.")
		}
	}
	body = append(body, "")
	focus := -1
	for i, item := range m.detailItems(row) {
		var text string
		switch item.kind {
		case itemInput:
			text = m.inputText(row, item.name)
		case itemProfile:
			text = m.profileText(row)
		case itemTools:
			text = "Tools: " + m.policyText(row)
		case itemConnect:
			text = "Connect and load tools"
		case itemEdit:
			text = "Edit personal connection"
		}
		prefix := "  "
		if i == m.detailCursor {
			prefix, focus = "> ", len(body)
		}
		body = append(body, prefix+text)
	}
	state := m.stateText(row)
	if readiness(row) == "Review required" {
		state += " Run mcparcel sync to see what changed."
	}
	body = append(body, "", m.schemaText(row.ID), m.revisionText(), state)
	help := "Up/Down: field   Enter: choose   Space: toggle   t: tools"
	if m.opts.LoadTools != nil {
		help += "   l: connect"
	}
	if row.SourceID == "personal" && row.Available {
		help += "   e: edit"
	}
	return m.page("MCParcel / Setup / "+rowLabel(row), body, focus, help+"   Esc: back")
}

func (m *Model) detailsKey(k string) tea.Cmd {
	row := m.detailRow()
	items := m.detailItems(row)
	switch k {
	case "up":
		m.detailCursor = max(m.detailCursor-1, 0)
	case "down":
		m.detailCursor = min(m.detailCursor+1, max(len(items)-1, 0))
	case "space":
		m.status = ""
		m.toggle(row)
	case "t":
		if len(items) > 0 {
			m.openTools()
		}
	case "l":
		if len(items) > 0 {
			return m.connect()
		}
	case "e":
		if row.SourceID == "personal" && row.Available {
			m.openEditForm(row)
		}
	case "esc":
		m.screen, m.status = screenList, ""
		m.clamp()
	case "enter":
		if m.detailCursor >= len(items) {
			return nil
		}
		switch item := items[m.detailCursor]; item.kind {
		case itemInput:
			m.openField(row, item.name)
		case itemProfile:
			m.screen, m.profileCursor, m.status = screenProfile, 0, ""
		case itemTools:
			m.openTools()
		case itemConnect:
			return m.connect()
		case itemEdit:
			m.openEditForm(row)
		}
	}
	return nil
}

// inline describes an edit error for display next to the field: a
// configuration error as its field path and reason (never the value),
// anything else through Options.Describe.
func (m *Model) inline(err error) string {
	if errors.Is(err, errLocalExists) {
		return "A personal connection with this ID already exists."
	}
	if errors.Is(err, config.ErrConfig) {
		if detail, ok := strings.CutPrefix(err.Error(), config.ErrConfig.Error()+": "); ok {
			return "Invalid: " + output.DisplayMetadata(detail) + "."
		}
	}
	return m.failure(err).Message
}
