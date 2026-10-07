package cmd

import (
	"context"
	"encoding/json"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/edit"
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

func saveEnabled(ctx context.Context, store *config.Store, state config.State, names []string, enabled bool) (config.State, error) {
	ids, err := edit.ResolveIDs(state, names)
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
	if err := edit.StageEnabled(&draft, names, ids, enabled); err != nil {
		return config.State{}, err
	}
	mutate := func(draft *config.State) error { return edit.StageEnabled(draft, names, ids, enabled) }
	if enabled {
		return store.UpdateAccepted(ctx, state.Selections.Revision, ids, mutate)
	}
	return store.Update(ctx, state.Selections.Revision, mutate)
}

func saveTools(ctx context.Context, store *config.Store, state config.State, name string, tools []string, enabled bool) (config.State, error) {
	if err := edit.CheckToolNames(tools); err != nil {
		return config.State{}, err
	}
	ids, err := edit.ResolveIDs(state, []string{name})
	if err != nil {
		return config.State{}, err
	}
	return store.Update(ctx, state.Selections.Revision, func(draft *config.State) error {
		return edit.StageTools(draft, name, ids, tools, enabled)
	})
}
