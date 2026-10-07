// Package edit holds the configuration mutations shared by the CLI commands
// and interactive setup, so both stage exactly the same changes.
package edit

import (
	"slices"
	"strings"

	"github.com/dedene/mcparcel/internal/args"
	"github.com/dedene/mcparcel/internal/config"
)

// ResolveIDs maps names to sorted, unique canonical connection IDs.
func ResolveIDs(state config.State, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, args.ErrInvalidArgs
	}
	effective, err := config.Resolve(state)
	if err != nil {
		return nil, err
	}
	all := make([]string, 0, len(effective.Connections))
	for id := range effective.Connections {
		all = append(all, id)
	}
	ids := make([]string, 0, len(names))
	for _, name := range names {
		id, err := config.ResolveID(name, effective.Aliases, all)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

// StageEnabled sets Enabled on the named connections. Enabling clears review
// and requires an available connection without missing configuration.
func StageEnabled(draft *config.State, names, expected []string, enabled bool) error {
	ids, err := ResolveIDs(*draft, names)
	if err != nil {
		return err
	}
	if !slices.Equal(ids, expected) {
		return config.ErrConfigConflict
	}
	for _, id := range ids {
		sel := draft.Selections.Connections[id]
		sel.Enabled = enabled
		if enabled {
			sel.ReviewRequired = false
		}
		draft.Selections.Connections[id] = sel
	}
	effective, err := config.Resolve(*draft)
	if err != nil {
		return err
	}
	if enabled {
		for _, id := range ids {
			row := effective.Connections[id]
			if !row.Available || row.Definition == nil {
				return config.ErrNotFound
			}
			if slices.Contains(row.Blockers, "config_required") {
				return config.ErrConfigRequired
			}
		}
	}
	return nil
}

// CheckToolNames rejects an empty list and empty or multi-line tool names.
func CheckToolNames(tools []string) error {
	if len(tools) == 0 {
		return args.ErrInvalidArgs
	}
	for _, tool := range tools {
		if tool == "" || strings.ContainsAny(tool, "\x00\r\n") {
			return args.ErrInvalidArgs
		}
	}
	return nil
}

// StageTools removes tools from (enabled) or adds them to (disabled) the
// connection's personal DisabledTools list.
func StageTools(draft *config.State, name string, expected, tools []string, enabled bool) error {
	if err := CheckToolNames(tools); err != nil {
		return err
	}
	current, err := ResolveIDs(*draft, []string{name})
	if err != nil {
		return err
	}
	if !slices.Equal(current, expected) {
		return config.ErrConfigConflict
	}
	sel := draft.Selections.Connections[current[0]]
	denied := slices.Clone(sel.DisabledTools)
	if enabled {
		denied = slices.DeleteFunc(denied, func(tool string) bool { return slices.Contains(tools, tool) })
	} else {
		denied = append(denied, tools...)
	}
	slices.Sort(denied)
	sel.DisabledTools = slices.Compact(denied)
	draft.Selections.Connections[current[0]] = sel
	return nil
}

// Definition returns the canonical ID and definition of an available connection.
func Definition(state config.State, name string) (string, *config.Connection, error) {
	effective, err := config.Resolve(state)
	if err != nil {
		return "", nil, err
	}
	ids := make([]string, 0, len(effective.Connections))
	for id := range effective.Connections {
		ids = append(ids, id)
	}
	id, err := config.ResolveID(name, effective.Aliases, ids)
	if err != nil {
		return "", nil, err
	}
	row := effective.Connections[id]
	if !row.Available || row.Definition == nil {
		return "", nil, config.ErrNotFound
	}
	return id, row.Definition, nil
}

// StageInput sets a declared input of an available connection.
func StageInput(draft *config.State, name, input, value string) error {
	id, d, err := Definition(*draft, name)
	if err != nil {
		return err
	}
	if _, ok := d.Inputs[input]; !ok {
		return config.ErrConfig
	}
	sel := draft.Selections.Connections[id]
	inputs := make(map[string]string, len(sel.Inputs)+1)
	for k, v := range sel.Inputs {
		inputs[k] = v
	}
	inputs[input] = value
	sel.Inputs = inputs
	draft.Selections.Connections[id] = sel
	_, err = config.Resolve(*draft)
	return err
}

// StageProfile binds an existing local profile to a connection that declares
// a credential profile requirement.
func StageProfile(draft *config.State, name, profile string) error {
	id, d, err := Definition(*draft, name)
	if err != nil {
		return err
	}
	if d.CredentialProfile == "" {
		return config.ErrConfig
	}
	if _, ok := draft.Local.CredentialProfiles[profile]; !ok {
		return config.ErrConfig
	}
	sel := draft.Selections.Connections[id]
	sel.CredentialProfile = profile
	draft.Selections.Connections[id] = sel
	return nil
}
