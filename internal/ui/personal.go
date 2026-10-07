package ui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// The personal form builds the same definition bytes `local add --file` and
// `local update --file` read. Editing keeps every field it does not show;
// arguments and the URL are replace-only, so their values are never shown.

type formField struct {
	key, label string
	value      field
	note       string // shown while the value is empty, e.g. "2 arguments (kept)"
	readOnly   string // shown instead of a value that cannot be edited here
}

func (f *formField) editable() bool { return f != nil && f.readOnly == "" && f.key != "transport" }

type personalForm struct {
	edit   string // canonical ID when editing; empty when adding
	http   bool
	fields []*formField
	cursor int // index into visible()
	kept   []string
	err    string
}

func (f *personalForm) visible() []*formField {
	var out []*formField
	for _, field := range f.fields {
		switch {
		case field.key == "url" && !f.http, (field.key == "command" || field.key == "args") && f.http:
			continue
		}
		out = append(out, field)
	}
	return out
}

func (f *personalForm) current() *formField {
	if f == nil {
		return nil
	}
	fields := f.visible()
	f.cursor = min(max(f.cursor, 0), len(fields)-1)
	return fields[f.cursor]
}

func (f *personalForm) get(key string) *formField {
	for _, field := range f.fields {
		if field.key == key {
			return field
		}
	}
	return nil
}

func newField(key, label, value string) *formField {
	f := &formField{key: key, label: label}
	f.value.insert(value)
	return f
}

func (m *Model) openAddForm() {
	domain := m.currentTab().id
	if domain == "other" {
		domain = ""
	}
	m.form = &personalForm{fields: []*formField{
		newField("id", "ID", ""), newField("label", "Label", ""), newField("description", "Description", ""),
		newField("domains", "Domains", domain),
		{key: "transport", label: "Transport"},
		newField("command", "Command", ""), newField("args", "Arguments", ""), newField("url", "URL", ""),
	}}
	m.screen, m.status = screenForm, ""
}

// openEditForm edits the stored personal definition, not the effective one,
// which fills in a label and the Other domain.
func (m *Model) openEditForm(row config.EffectiveConnection) {
	stored, ok := m.draft.State().Personal.Connections[strings.TrimPrefix(row.ID, "local:")]
	if !ok {
		return
	}
	d := &stored
	f := &personalForm{edit: row.ID, http: d.Transport.HTTP != nil, fields: []*formField{
		newField("label", "Label", d.Label), newField("description", "Description", d.Description),
		newField("domains", "Domains", strings.Join(d.Domains, ", ")),
		{key: "transport", label: "Transport"},
	}}
	if s := d.Transport.Stdio; s != nil {
		command := &formField{key: "command", label: "Command"}
		if s.Command.Literal != nil {
			command.value.insert(*s.Command.Literal)
		} else {
			command.readOnly = "not a literal; edit with mcparcel local update"
		}
		args := &formField{key: "args", label: "Arguments", note: plural(len(s.Args), "argument", "arguments") + " (kept)"}
		if slices.ContainsFunc(s.Args, func(v config.Value) bool { return v.Literal == nil }) {
			args.readOnly = plural(len(s.Args), "argument", "arguments") + "; edit with mcparcel local update"
		}
		f.fields = append(f.fields, command, args)
	} else {
		note := "current URL (kept)"
		if u := d.Transport.HTTP.URL.Literal; u != nil {
			note = hostOnly(*u) + " (kept)"
		}
		f.fields = append(f.fields, &formField{key: "url", label: "URL", note: note})
	}
	f.kept = keptFields(d)
	m.form, m.screen, m.status = f, screenForm, ""
}

// normalizedDefaults are the values validation fills in; they are not
// listed as kept fields.
var normalizedDefaults = map[string]string{"callTimeout": `"120s"`, "mode": `"auto"`, "allowInsecureHttp": `"never"`}

// keptFields names the definition's fields the form does not show.
func keptFields(d *config.Connection) []string {
	var kept []string
	collect := func(raw []byte, skip ...string) {
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil {
			return
		}
		for key, value := range obj {
			if !slices.Contains(skip, key) && normalizedDefaults[key] != string(value) {
				kept = append(kept, key)
			}
		}
	}
	whole, _ := json.Marshal(d)
	transport, _ := json.Marshal(d.Transport)
	collect(whole, "label", "description", "domains", "transport")
	collect(transport, "type", "command", "args", "url")
	slices.Sort(kept)
	return kept
}

func (m *Model) formPage() []string {
	f := m.form
	w, _ := m.size()
	title := "MCParcel / Setup / Add personal connection"
	if f.edit != "" {
		title = "MCParcel / Setup / Edit " + rowLabel(m.detailRow())
	}
	body := []string{}
	focus := -1
	for i, field := range f.visible() {
		prefix := "  "
		if i == f.cursor {
			prefix, focus = "> ", len(body)
		}
		label := prefix + pad(field.label+":", 13)
		var value string
		switch {
		case field.key == "transport" && f.edit != "":
			value = map[bool]string{false: "stdio", true: "http"}[f.http] + " (read-only)"
		case field.key == "transport":
			value = map[bool]string{false: "stdio", true: "http"}[f.http] + "   (Space switches)"
		case field.readOnly != "":
			value = field.readOnly
		case i == f.cursor:
			value = field.value.render(w - len(label))
		case field.value.String() == "":
			value = field.note
		default:
			value = output.DisplayMetadata(field.value.String())
		}
		body = append(body, label+value)
	}
	if f.edit != "" && len(f.kept) > 0 {
		body = append(body, "", "Kept: "+strings.Join(f.kept, ", "))
	}
	body = append(body, "", f.err)
	return m.page(title, body, focus, "Tab/Shift+Tab: field   Enter: apply   Esc: discard")
}

func (m *Model) formKey(msg tea.KeyPressMsg, k string) {
	f := m.form
	field := f.current()
	switch k {
	case "tab", "down":
		f.cursor = (f.cursor + 1) % len(f.visible())
	case "shift+tab", "up":
		f.cursor = (f.cursor + len(f.visible()) - 1) % len(f.visible())
	case "esc":
		m.closeForm("")
	case "enter":
		m.applyForm()
	case "space":
		if field.key == "transport" {
			if f.edit == "" {
				f.http = !f.http
			}
			return
		}
		if field.editable() {
			field.value.insert(" ")
		}
	case "backspace":
		if field.editable() {
			field.value.backspace()
		}
	case "ctrl+u":
		if field.editable() {
			field.value.clear()
		}
	default:
		if field.editable() {
			field.value.insert(typed(msg))
		}
	}
}

func (m *Model) closeForm(status string) {
	m.screen = screenList
	if m.form.edit != "" {
		m.screen = screenDetails
	}
	m.form, m.status = nil, status
}

func splitDomains(text string) []string {
	var out []string
	for _, part := range strings.Split(text, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// definition builds the flattened {id, ...} definition. Editing starts from
// the existing definition and overwrites only the edited keys.
func (f *personalForm) definition(existing *config.Connection) ([]byte, error) {
	obj := map[string]any{}
	transport := map[string]any{"type": "stdio"}
	if existing != nil {
		raw, err := json.Marshal(existing)
		if err != nil {
			return nil, err
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber() // keep numbers exactly as written
		if err = decoder.Decode(&obj); err != nil {
			return nil, err
		}
		transport, _ = obj["transport"].(map[string]any)
		obj["id"] = strings.TrimPrefix(f.edit, "local:")
	} else {
		obj["id"] = f.get("id").value.String()
		if f.http {
			transport["type"] = "http"
		}
	}
	for _, key := range []string{"label", "description"} {
		delete(obj, key)
		if text := f.get(key).value.String(); text != "" {
			obj[key] = text
		}
	}
	delete(obj, "domains")
	if domains := splitDomains(f.get("domains").value.String()); len(domains) > 0 {
		obj["domains"] = domains
	}
	if f.http {
		if text := f.get("url").value.String(); text != "" || existing == nil {
			transport["url"] = text
		}
	} else {
		if command := f.get("command"); command.readOnly == "" {
			transport["command"] = command.value.String()
		}
		if args := f.get("args"); args.readOnly == "" {
			if fields := strings.Fields(args.value.String()); len(fields) > 0 {
				transport["args"] = fields
			}
		}
	}
	obj["transport"] = transport
	return json.Marshal(obj)
}

func (m *Model) applyForm() {
	f := m.form
	if f.edit == "" {
		id := f.get("id").value.String()
		if _, err := config.CanonicalLocal(id); err != nil {
			f.err = "ID: use lowercase letters, digits and single hyphens."
			return
		}
		if _, exists := m.draft.State().Personal.Connections[id]; exists {
			f.err = fmt.Sprintf("A personal connection %s already exists.", quoted(id))
			return
		}
	}
	var existing *config.Connection
	if f.edit != "" {
		c := m.draft.State().Personal.Connections[strings.TrimPrefix(f.edit, "local:")]
		existing = &c
	}
	raw, err := f.definition(existing)
	if err != nil {
		f.err = m.inline(err)
		return
	}
	if f.edit != "" {
		if err := m.draft.UpdateLocal(f.edit, raw); err != nil {
			f.err = m.inline(err)
			return
		}
		status := "Updated " + rowLabel(m.detailRow()) + "."
		if m.detailRow().ReviewRequired {
			status += " Review required: Space accepts it."
		}
		m.closeForm(status)
		return
	}
	id, err := m.draft.AddLocal(raw)
	if errors.Is(err, errLocalExists) {
		f.err = fmt.Sprintf("A personal connection %s already exists.", quoted(f.get("id").value.String()))
		return
	}
	if err != nil {
		f.err = m.inline(err)
		return
	}
	m.closeForm("Added " + rowLabel(m.draft.Effective().Connections[id]) + ". It is off: press Space to turn it on.")
	for i, row := range m.rows() {
		if row.ID == id {
			m.cursor = i
		}
	}
	m.clamp()
}
