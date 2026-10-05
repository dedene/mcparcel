package cmd

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/dedene/mcparcel/internal/args"
	"github.com/dedene/mcparcel/internal/config"
)

type (
	EnableCmd struct {
		MCPs []string `arg:"" name:"mcp" required:"" sep:"none" json:"-"`
	}
	DisableCmd struct {
		MCPs []string `arg:"" name:"mcp" required:"" sep:"none" json:"-"`
	}
)

func (c *EnableCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	return writeEnabled(ctx, s, opts, c.MCPs, true)
}

func (c *DisableCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	return writeEnabled(ctx, s, opts, c.MCPs, false)
}

func writeEnabled(ctx context.Context, s *Streams, opts *CommandOptions, names []string, enabled bool) error {
	store, state, err := metadataStore(ctx)
	if err != nil {
		return err
	}
	next, err := saveEnabled(ctx, store, state, names, enabled)
	if err != nil {
		return err
	}
	return writeMutation(s, opts, next)
}

func resolveSelectionIDs(state config.State, names []string) ([]string, error) {
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

func stageEnabled(draft *config.State, names, expected []string, enabled bool) error {
	ids, err := resolveSelectionIDs(*draft, names)
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

func saveEnabled(ctx context.Context, store *config.Store, state config.State, names []string, enabled bool) (config.State, error) {
	ids, err := resolveSelectionIDs(state, names)
	if err != nil {
		return config.State{}, err
	}
	// Prevalidation must not mutate the caller's loaded selections.
	raw, err := json.Marshal(state.Selections)
	if err != nil {
		return config.State{}, config.ErrConfig
	}
	draft := state
	draft.Selections, err = config.DecodeSelections(raw)
	if err != nil {
		return config.State{}, err
	}
	if err := stageEnabled(&draft, names, ids, enabled); err != nil {
		return config.State{}, err
	}
	mutate := func(draft *config.State) error { return stageEnabled(draft, names, ids, enabled) }
	if enabled {
		return store.UpdateAccepted(ctx, state.Selections.Revision, ids, mutate)
	}
	return store.Update(ctx, state.Selections.Revision, mutate)
}

func saveTools(ctx context.Context, store *config.Store, state config.State, name string, tools []string, enabled bool) (config.State, error) {
	if len(tools) == 0 {
		return config.State{}, args.ErrInvalidArgs
	}
	for _, tool := range tools {
		if tool == "" || strings.ContainsAny(tool, "\x00\r\n") {
			return config.State{}, args.ErrInvalidArgs
		}
	}
	ids, err := resolveSelectionIDs(state, []string{name})
	if err != nil {
		return config.State{}, err
	}
	return store.Update(ctx, state.Selections.Revision, func(draft *config.State) error {
		current, err := resolveSelectionIDs(*draft, []string{name})
		if err != nil {
			return err
		}
		if !slices.Equal(current, ids) {
			return config.ErrConfigConflict
		}
		sel := draft.Selections.Connections[ids[0]]
		denied := slices.Clone(sel.DisabledTools)
		if enabled {
			denied = slices.DeleteFunc(denied, func(tool string) bool { return slices.Contains(tools, tool) })
		} else {
			denied = append(denied, tools...)
		}
		slices.Sort(denied)
		sel.DisabledTools = slices.Compact(denied)
		draft.Selections.Connections[ids[0]] = sel
		return nil
	})
}
