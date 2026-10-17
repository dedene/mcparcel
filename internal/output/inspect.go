package output

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/dedene/mcparcel/internal/config"
)

type InspectData struct {
	Revision        uint64                     `json:"revision"`
	Item            config.EffectiveConnection `json:"item"`
	Selection       config.Selection           `json:"selection"`
	Source          *SourceMetadata            `json:"source"`
	SourceRevisions map[string]string          `json:"sourceRevisions"`
}

// humanInspect prints a connection as "Label: value" lines, one fact per
// line so a screen reader reads it in order; a group (environment, headers,
// inputs, sign-in, tool policy) is a label line followed by indented
// "name: value" lines. It shows what kind of value each field has, never the
// value itself: no argument, env or header value, no URL path or query, no
// secret reference and no input value. Text is terminal-safe
// (DisplayMetadata).
func humanInspect(data InspectData) string {
	var b strings.Builder
	line := func(label, value string) { fmt.Fprintf(&b, "%s: %s\n", label, value) }
	heading := func(label string) { fmt.Fprintf(&b, "%s:\n", label) }
	sub := func(label, value string) { fmt.Fprintf(&b, "  %s: %s\n", label, value) }
	group := func(label string, values map[string]config.Value) {
		if len(values) == 0 {
			return
		}
		heading(label)
		for _, name := range sortedNames(values) {
			sub(DisplayMetadata(name), valueKind(values[name]))
		}
	}
	row := data.Item
	source := "Personal"
	if data.Source != nil {
		source = data.Source.Source.Owner + "/" + data.Source.Source.Repo
	} else if repository, github := strings.CutPrefix(row.ID, "github:"); github {
		source, _, _ = strings.Cut(repository, "#")
	}
	line("ID", DisplayMetadata(row.ID))
	d := row.Definition
	if d != nil {
		line("Label", DisplayMetadata(d.Label))
		if d.Description != "" {
			line("Description", DisplayMetadata(d.Description))
		}
	}
	line("Source", DisplayMetadata(source))
	if data.Source != nil {
		line("Catalog commit", DisplayMetadata(data.Source.Source.Commit))
	}
	line("State", metadataState(row))
	line("Enabled", yesNo(row.Enabled))
	line("Blockers", listOr(row.Blockers, "none"))
	if d == nil {
		// An unavailable connection has no definition left to describe.
		return b.String()
	}
	line("Domains", listOr(d.Domains, "none"))
	line("Transport", transportSummary(d.Transport))
	if s := d.Transport.Stdio; s != nil {
		if s.Cwd != nil {
			line("Working directory", valueKind(*s.Cwd))
		}
		if len(s.InheritEnv) > 0 {
			line("Inherited environment", listOr(s.InheritEnv, "none"))
		}
		group("Environment", s.Env)
	}
	if h := d.Transport.HTTP; h != nil {
		if h.Mode != "" {
			line("HTTP mode", DisplayMetadata(h.Mode))
		}
		if h.AllowInsecureHTTP != "" {
			line("Insecure HTTP", DisplayMetadata(h.AllowInsecureHTTP))
		}
		group("Headers", h.Headers)
	}
	if len(d.Inputs) > 0 {
		heading("Inputs")
		for _, name := range sortedNames(d.Inputs) {
			state := "not set"
			if _, ok := data.Selection.Inputs[name]; ok {
				state = "set"
			} else if d.Inputs[name].Default != nil {
				state = "default"
			}
			sub(DisplayMetadata(name), DisplayMetadata(d.Inputs[name].Kind)+", "+state)
		}
	}
	if d.CredentialProfile != "" {
		bound := "not linked"
		if data.Selection.CredentialProfile != "" {
			bound = "linked to " + DisplayMetadata(data.Selection.CredentialProfile)
		}
		line("Credential profile", DisplayMetadata(d.CredentialProfile)+", "+bound)
	}
	if a := d.Auth; a != nil {
		grant := "authorization code"
		if a.Grant == config.GrantClientCredentials {
			grant = "client credentials"
		}
		line("Sign-in", "OAuth, "+grant)
		if len(a.Scopes) > 0 {
			sub("Scopes", listOr(a.Scopes, "none"))
		}
		if a.ClientID != nil {
			sub("Client ID", valueKind(*a.ClientID))
		}
		if a.ClientSecret != nil {
			sub("Client secret", valueKind(*a.ClientSecret))
		}
	}
	// The definition's policy; the personal choices are Tools off.
	if policy := d.ToolPolicy; policy != nil && (policy.Allow != nil || len(policy.Deny) > 0) {
		heading("Tool policy")
		if policy.Allow != nil {
			sub("Allow", listOr(*policy.Allow, "none"))
		}
		if len(policy.Deny) > 0 {
			sub("Deny", listOr(policy.Deny, "none"))
		}
	}
	if len(data.Selection.DisabledTools) > 0 {
		line("Tools off", toolList(data.Selection.DisabledTools))
	}
	if l := d.Lifecycle; l != nil {
		if l.IdleTimeout != "" {
			line("Idle timeout", DisplayMetadata(l.IdleTimeout))
		}
		if l.KeepAlive != "" {
			line("Keep-alive", DisplayMetadata(l.KeepAlive))
		}
	}
	if d.CallTimeout != "" {
		line("Call timeout", DisplayMetadata(d.CallTimeout))
	}
	if d.StartupTimeout != "" {
		line("Startup timeout", DisplayMetadata(d.StartupTimeout))
	}
	return b.String()
}

// transportSummary is the transport as the setup server page shows it: the
// command and how many arguments, or the URL's host only.
func transportSummary(t config.Transport) string {
	if s := t.Stdio; s != nil {
		command := valueKind(s.Command)
		if s.Command.Literal != nil {
			command = DisplayMetadata(*s.Command.Literal)
		}
		return fmt.Sprintf("stdio, %s, %s", command, plural(len(s.Args), "argument", "arguments"))
	}
	if h := t.HTTP; h != nil {
		if h.URL.Literal == nil {
			return "HTTP, URL " + valueKind(h.URL)
		}
		u, err := url.Parse(*h.URL.Literal)
		if err != nil || u.Host == "" {
			return "HTTP, invalid URL"
		}
		return "HTTP, " + DisplayMetadata(u.Host)
	}
	return "none"
}

// valueKind names where a value comes from, never the value: a literal,
// an input by name, an environment variable by name, or 1Password.
func valueKind(v config.Value) string {
	switch {
	case v.Input != nil:
		return "from input " + DisplayMetadata(v.Input.Input)
	case v.Secret != nil:
		if name, env := strings.CutPrefix(v.Secret.Secret, "env:"); env {
			return "from environment variable " + DisplayMetadata(name)
		}
		return "from 1Password"
	}
	return "set (value hidden)"
}

// toolList names the tools that pass the MCP tool name grammar and counts the
// rest, which could be a pasted secret.
func toolList(tools []string) string {
	var named []string
	for _, tool := range tools {
		if SafeToolName(tool) {
			named = append(named, tool)
		}
	}
	text := strings.Join(named, ", ")
	if other := len(tools) - len(named); other > 0 {
		if text != "" {
			text += ", "
		}
		text += plural(other, "other", "others")
	}
	return text
}

func listOr(items []string, empty string) string {
	if len(items) == 0 {
		return empty
	}
	safe := make([]string, len(items))
	for i, item := range items {
		safe[i] = DisplayMetadata(item)
	}
	return strings.Join(safe, ", ")
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func sortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
