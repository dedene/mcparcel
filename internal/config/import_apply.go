package config

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
)

func PlanImport(state State, report ImportReport, only []string) (ImportReport, error) {
	out, err := validateImportReport(report)
	if err != nil {
		return ImportReport{}, err
	}
	selected := map[string]bool{}
	known := map[string]bool{}
	for _, row := range out.Entries {
		known[row.ID] = true
	}
	for _, name := range only {
		id := strings.TrimPrefix(name, "local:")
		if !identifier.MatchString(id) || !known[id] || selected[id] {
			return ImportReport{}, ErrConfig
		}
		selected[id] = true
	}
	current, err := cloneState(state)
	if err != nil {
		return ImportReport{}, err
	}
	revision := current.Selections.Revision
	out.Revision = &revision
	out.Applied = false
	for i := range out.Entries {
		row := &out.Entries[i]
		row.Selected = len(only) == 0 || selected[row.ID]
		row.Applied = false
		if !row.Applicable {
			continue
		}
		block := func(path, code string) { row.Unresolved = append(row.Unresolved, ImportIssue{Path: path, Code: code}) }
		path := "mcpServers." + row.ID
		c := out.Definitions.Connections[row.ID]
		if old, exists := current.Personal.Connections[row.ID]; exists && !reflect.DeepEqual(old, c) {
			block(path, "existing_definition")
		}
		aliases := current.Local.Aliases
		if err := AddAlias(aliases, row.Alias, row.CanonicalID); err != nil {
			block("aliases."+row.ID, "alias_collision")
		}
		if row.CredentialProfile != "" {
			profile := row.CredentialProfile
			if _, exists := current.Local.CredentialProfiles[profile]; !exists {
				block(path+".credentialProfile", "missing_profile")
			}
			requirement := out.Definitions.CredentialProfiles[profile]
			if old, exists := current.Personal.CredentialProfiles[profile]; exists && old.Description != "" && old != requirement {
				block(path+".credentialProfile", "requirement_conflict")
			}
		}
		if len(row.Unresolved) > 0 {
			row.Applicable = false
			delete(out.Definitions.Connections, row.ID)
		}
		sortImportIssues(row.Unresolved)
	}
	// Declarations remaining in the proposal belong only to remaining definitions.
	for id := range out.Definitions.CredentialProfiles {
		used := false
		for _, c := range out.Definitions.Connections {
			if c.CredentialProfile == id {
				used = true
				break
			}
		}
		if !used {
			delete(out.Definitions.CredentialProfiles, id)
		}
	}
	return out, nil
}

func validateImportReport(report ImportReport) (ImportReport, error) {
	if report.Entries == nil || report.Aliases == nil || report.Issues == nil || report.Definitions.Connections == nil || report.Definitions.Name != "" || len(report.Definitions.Domains) != 0 {
		return ImportReport{}, ErrConfig
	}
	data, err := json.Marshal(report)
	if err != nil {
		return ImportReport{}, ErrConfig
	}
	var out ImportReport
	if err := json.Unmarshal(data, &out); err != nil {
		return ImportReport{}, ErrConfig
	}
	data, err = json.Marshal(out.Definitions)
	if err != nil {
		return ImportReport{}, ErrConfig
	}
	out.Definitions, err = DecodeCatalog(data)
	if err != nil {
		return ImportReport{}, ErrConfig
	}
	external := false
	for _, issue := range out.Issues {
		switch issue.Code {
		case "unsupported_imports":
			if issue.Path != "imports" {
				return ImportReport{}, ErrConfig
			}
			external = true
		case "unused_binding":
			if !strings.HasPrefix(issue.Path, "bindings.") || !envName.MatchString(strings.TrimPrefix(issue.Path, "bindings.")) {
				return ImportReport{}, ErrConfig
			}
		default:
			return ImportReport{}, ErrConfig
		}
	}
	seen, aliases, profiles := map[string]bool{}, map[string]string{}, map[string]bool{}
	applicable := 0
	for _, row := range out.Entries {
		if seen[row.ID] || row.Fields == nil || row.Unresolved == nil || row.Warnings == nil || row.Alias != row.ID || row.Applicable != (len(row.Unresolved) == 0) {
			return ImportReport{}, ErrConfig
		}
		seen[row.ID] = true
		if identifier.MatchString(row.ID) {
			if row.CanonicalID != "local:"+row.ID {
				return ImportReport{}, ErrConfig
			}
			aliases[row.ID] = row.CanonicalID
		} else if row.CanonicalID != "" || !hasImportIssue(row.Unresolved, "mcpServers."+row.ID, "invalid_id") {
			return ImportReport{}, ErrConfig
		}
		if external && !hasImportIssue(row.Unresolved, "imports", "unsupported_imports") {
			return ImportReport{}, ErrConfig
		}
		for _, issue := range row.Unresolved {
			switch issue.Code {
			case "invalid_field", "invalid_id", "transport_conflict", "auth_required_in_definition", "unknown_field", "unresolved_credential", "profile_conflict", "potential_secret", "secret_in_args", "invalid_reference", "multiple_references", "invalid_binding", "unsupported_expansion", "invalid_definition":
			case "unsupported_imports":
				if !external || issue.Path != "imports" {
					return ImportReport{}, ErrConfig
				}
			default:
				return ImportReport{}, ErrConfig
			}
		}
		c, exists := out.Definitions.Connections[row.ID]
		if exists != row.Applicable {
			return ImportReport{}, ErrConfig
		}
		if !row.Applicable {
			continue
		}
		applicable++
		if !identifier.MatchString(row.ID) || row.CredentialProfile != c.CredentialProfile || row.OAuth != (c.Auth != nil) || (row.Transport == "stdio") != (c.Transport.Stdio != nil) || (row.Transport == "http") != (c.Transport.HTTP != nil) {
			return ImportReport{}, ErrConfig
		}
		if !safeImportDefinition(c) {
			return ImportReport{}, ErrConfig
		}
		if c.CredentialProfile != "" {
			profiles[c.CredentialProfile] = true
		}
	}
	if applicable != len(out.Definitions.Connections) || !reflect.DeepEqual(aliases, out.Aliases) || len(profiles) != len(out.Definitions.CredentialProfiles) {
		return ImportReport{}, ErrConfig
	}
	for profile := range out.Definitions.CredentialProfiles {
		if !profiles[profile] {
			return ImportReport{}, ErrConfig
		}
	}
	return out, nil
}

// Native catalogs permit literals that import must reject as possible credentials.
func safeImportDefinition(c Connection) bool {
	literal := func(v Value, protected bool) bool {
		if v.Literal == nil {
			if v.Secret != nil {
				return !secretURL(v.Secret.Prefix + "reference" + v.Secret.Suffix)
			}
			return true
		}
		return !protected && !secretURL(*v.Literal) && !strings.Contains(*v.Literal, "${") && !shellExpansion.MatchString(*v.Literal)
	}
	if s := c.Transport.Stdio; s != nil {
		if !literal(s.Command, false) {
			return false
		}
		for _, arg := range s.Args {
			text, err := LiteralText(arg)
			if err != nil || secretArgument(text) {
				return false
			}
			if flag, value, ok := strings.Cut(text, "="); ok && (strings.EqualFold(flag, "--header") || strings.EqualFold(flag, "-H")) && secretHeaderArgument(value) {
				return false
			}
		}
		for name, value := range s.Env {
			if !literal(value, suspiciousName(name)) {
				return false
			}
		}
	}
	if h := c.Transport.HTTP; h != nil {
		text, err := LiteralText(h.URL)
		if err != nil || !literal(h.URL, false) || secretURL(text) {
			return false
		}
		for name, value := range h.Headers {
			if !literal(value, protectedHeader(name)) {
				return false
			}
		}
	}
	if a := c.Auth; a != nil && a.ClientID != nil && !literal(*a.ClientID, false) {
		return false
	}
	return true
}

func hasImportIssue(issues []ImportIssue, path, code string) bool {
	for _, issue := range issues {
		if issue.Path == path && issue.Code == code {
			return true
		}
	}
	return false
}

func importMutation(report ImportReport, only []string, planned *ImportReport) func(*State) error {
	return func(state *State) error {
		out, err := PlanImport(*state, report, only)
		if err != nil {
			return err
		}
		for _, issue := range out.Issues {
			if issue.Code != "unused_binding" {
				return &ImportBlockedError{Report: out}
			}
		}
		for _, row := range out.Entries {
			if row.Selected && !row.Applicable {
				return &ImportBlockedError{Report: out}
			}
		}
		for _, row := range out.Entries {
			if !row.Selected {
				continue
			}
			c := out.Definitions.Connections[row.ID]
			if _, exists := state.Personal.Connections[row.ID]; !exists {
				state.Personal.Connections[row.ID] = cloneConnection(c)
				previous := state.Selections.Connections[row.CanonicalID]
				state.Selections.Connections[row.CanonicalID] = Selection{Enabled: true, CredentialProfile: row.CredentialProfile, Inputs: map[string]string{}, DisabledTools: []string{}, ReviewRequired: previous.ReviewRequired}
			}
			if state.Local.Aliases == nil {
				state.Local.Aliases = map[string]string{}
			}
			if err := AddAlias(state.Local.Aliases, row.Alias, row.CanonicalID); err != nil {
				return err
			}
			if c.CredentialProfile != "" {
				if state.Personal.CredentialProfiles == nil {
					state.Personal.CredentialProfiles = map[string]ProfileRequirement{}
				}
				if _, exists := state.Personal.CredentialProfiles[c.CredentialProfile]; !exists {
					state.Personal.CredentialProfiles[c.CredentialProfile] = out.Definitions.CredentialProfiles[c.CredentialProfile]
				}
			}
		}
		*planned = out
		return nil
	}
}

func ApplyImport(ctx context.Context, store *Store, expectedRevision uint64, report ImportReport, only []string) (ImportReport, error) {
	if store == nil {
		return ImportReport{}, ErrConfig
	}
	var planned ImportReport
	state, err := store.Update(ctx, expectedRevision, importMutation(report, only, &planned))
	if err != nil {
		return ImportReport{}, err
	}
	planned.Applied = true
	revision := state.Selections.Revision
	planned.Revision = &revision
	for i := range planned.Entries {
		planned.Entries[i].Applied = planned.Entries[i].Selected
	}
	return planned, nil
}
