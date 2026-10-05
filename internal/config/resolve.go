package config

import (
	"encoding/json"
	"strings"
)

type EffectiveConnection struct {
	ID             string      `json:"id"`
	SourceID       string      `json:"sourceId"`
	Definition     *Connection `json:"definition"`
	Connection     *Connection `json:"connection"`
	Enabled        bool        `json:"enabled"`
	Available      bool        `json:"available"`
	ReviewRequired bool        `json:"reviewRequired"`
	Blockers       []string    `json:"blockers"`
	Policy         ToolPolicy  `json:"toolPolicy"`
}
type EffectiveConfig struct {
	Revision        uint64                         `json:"revision"`
	Connections     map[string]EffectiveConnection `json:"connections"`
	Aliases         map[string]string              `json:"aliases"`
	Domains         map[string]Domain              `json:"domains"`
	SourceRevisions map[string]string              `json:"sourceRevisions"`
}

func cloneConnection(c Connection) Connection {
	b, _ := json.Marshal(c)
	var out Connection
	_ = json.Unmarshal(b, &out)
	return out
}

func Resolve(state State) (EffectiveConfig, error) {
	if err := ValidateState(state); err != nil {
		return EffectiveConfig{}, err
	}
	cloneCatalog := func(c Catalog) Catalog { b, _ := json.Marshal(c); v, _ := DecodeCatalog(b); return v }
	state.Personal = cloneCatalog(state.Personal)
	b, _ := json.Marshal(state.Local)
	state.Local, _ = DecodeLocal(b)
	b, _ = json.Marshal(state.Selections)
	state.Selections, _ = DecodeSelections(b)
	out := EffectiveConfig{Revision: state.Selections.Revision, Connections: map[string]EffectiveConnection{}, Aliases: state.Local.Aliases, Domains: map[string]Domain{"other": {Label: "Other"}}, SourceRevisions: map[string]string{}}
	visit := func(cat Catalog, source, owner, repo string) error {
		for id, d := range cat.Domains {
			if _, ok := out.Domains[id]; !ok {
				out.Domains[id] = d
			}
		}
		for _, id := range sortedKeys(cat.Connections) {
			canonical, _ := CanonicalLocal(id)
			if source != "personal" {
				canonical, _ = CanonicalGitHub(owner, repo, id)
			}
			definition := cloneConnection(cat.Connections[id])
			if definition.Label == "" {
				definition.Label = id
			}
			if len(definition.Domains) == 0 {
				definition.Domains = []string{"other"}
			}
			c := cloneConnection(definition)
			sel := state.Selections.Connections[canonical]
			row := EffectiveConnection{ID: canonical, SourceID: source, Definition: &definition, Connection: &c, Available: true, Enabled: sel.Enabled, ReviewRequired: sel.ReviewRequired, Blockers: []string{}, Policy: IntersectToolPolicy(c.ToolPolicy, sel.DisabledTools)}
			if !row.Enabled {
				row.Blockers = append(row.Blockers, "connection_disabled")
			}
			if row.ReviewRequired {
				row.Blockers = append(row.Blockers, "review_required")
			}
			missing := false
			bindings := map[string]string{}
			for _, name := range sortedKeys(c.Inputs) {
				input := c.Inputs[name]
				text, ok := sel.Inputs[name]
				if !ok && input.Default != nil {
					text = *input.Default
					ok = true
				}
				if !ok {
					missing = true
					continue
				}
				if err := validateInputText(text, input.Kind, "selections.connections."+canonical+".inputs."+name); err != nil {
					return err
				}
				bindings[name] = text
			}
			materialize := func(v Value) Value {
				if v.Input != nil {
					if text, ok := bindings[v.Input.Input]; ok {
						return Literal(text)
					}
				}
				return v
			}
			if s := c.Transport.Stdio; s != nil {
				s.Command = materialize(s.Command)
				for i, v := range s.Args {
					s.Args[i] = materialize(v)
				}
				if s.Cwd != nil {
					v := materialize(*s.Cwd)
					s.Cwd = &v
				}
				for k, v := range s.Env {
					s.Env[k] = materialize(v)
				}
			}
			if h := c.Transport.HTTP; h != nil {
				h.URL = materialize(h.URL)
				for k, v := range h.Headers {
					h.Headers[k] = materialize(v)
				}
			}
			if a := c.Auth; a != nil && a.ClientID != nil {
				v := materialize(*a.ClientID)
				a.ClientID = &v
			}
			if err := validateConnection(&c, "connections."+id, cat.CredentialProfiles); err != nil {
				return err
			}
			if c.CredentialProfile != "" {
				if _, ok := state.Local.CredentialProfiles[sel.CredentialProfile]; sel.CredentialProfile == "" || !ok {
					missing = true
				}
				c.CredentialProfile = sel.CredentialProfile
			}
			if missing {
				row.Blockers = append(row.Blockers, "config_required")
			}
			out.Connections[canonical] = row
		}
		return nil
	}
	for _, src := range state.Local.Sources {
		if err := visit(cloneCatalog(state.Catalogs[src.ID]), src.ID, src.Owner, src.Repo); err != nil {
			return EffectiveConfig{}, err
		}
		out.SourceRevisions[src.ID] = src.Commit
	}
	if err := visit(state.Personal, "personal", "", ""); err != nil {
		return EffectiveConfig{}, err
	}
	out.SourceRevisions["personal"], _ = hashJSON(state.Personal)
	for _, id := range sortedKeys(state.Selections.Connections) {
		if _, ok := out.Connections[id]; ok {
			continue
		}
		sel := state.Selections.Connections[id]
		sourceID := ""
		if strings.HasPrefix(id, "local:") {
			sourceID = "personal"
		} else {
			for _, src := range state.Local.Sources {
				if strings.HasPrefix(id, "github:"+src.Owner+"/"+src.Repo+"#") {
					sourceID = src.ID
					break
				}
			}
		}
		r := EffectiveConnection{ID: id, SourceID: sourceID, Enabled: sel.Enabled, ReviewRequired: sel.ReviewRequired, Blockers: []string{"connection_unavailable"}, Policy: IntersectToolPolicy(nil, sel.DisabledTools)}
		if !sel.Enabled {
			r.Blockers = append(r.Blockers, "connection_disabled")
		}
		if sel.ReviewRequired {
			r.Blockers = append(r.Blockers, "review_required")
		}
		out.Connections[id] = r
	}
	return out, nil
}
