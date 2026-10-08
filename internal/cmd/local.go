package cmd

import (
	"context"
	"strings"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/edit"
	"github.com/dedene/mcparcel/internal/output"
)

type LocalCmd struct {
	Add    LocalAddCmd    `cmd:"" json:"-" help:"Add a personal connection definition offline."`
	Update LocalUpdateCmd `cmd:"" json:"-" help:"Update a personal connection definition offline."`
	Remove LocalRemoveCmd `cmd:"" json:"-" help:"Remove a personal connection definition offline."`
}
type LocalAddCmd struct {
	File string `required:"" name:"file" json:"-"`
}
type LocalUpdateCmd struct {
	ID   string `arg:"" required:"" json:"-"`
	File string `required:"" name:"file" json:"-"`
}
type LocalRemoveCmd struct {
	ID string `arg:"" required:"" json:"-"`
}

var (
	localID               = edit.LocalID
	decodeLocalDefinition = edit.DecodeLocalDefinition
)

func (c *LocalAddCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	raw, err := readCommandFile(ctx, c.File)
	if err != nil {
		return err
	}
	store, state, err := metadataStore(ctx)
	if err != nil {
		return err
	}
	next, err := store.Update(ctx, state.Selections.Revision, func(draft *config.State) error {
		_, err := edit.StageLocalAdd(draft, raw)
		return definitionError(err)
	})
	if err != nil {
		return err
	}
	return writeMutation(s, opts, next)
}

func (c *LocalUpdateCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	id, err := localID(c.ID)
	if err != nil {
		return err
	}
	raw, err := readCommandFile(ctx, c.File)
	if err != nil {
		return err
	}
	store, state, err := metadataStore(ctx)
	if err != nil {
		return err
	}
	next, err := store.Update(ctx, state.Selections.Revision, func(draft *config.State) error {
		return definitionError(edit.StageLocalUpdate(draft, id, raw))
	})
	if err != nil {
		return err
	}
	return writeMutation(s, opts, next)
}

func (c *LocalRemoveCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	id, err := localID(c.ID)
	if err != nil {
		return err
	}
	store, state, err := metadataStore(ctx)
	if err != nil {
		return err
	}
	next, err := store.Update(ctx, state.Selections.Revision, func(draft *config.State) error {
		if _, exists := draft.Personal.Connections[id]; !exists {
			return config.ErrNotFound
		}
		delete(draft.Personal.Connections, id)
		return nil
	})
	if err != nil {
		return err
	}
	return writeMutation(s, opts, next)
}

// definitionError points a field error in the --file definition at that file
// rather than at personal.json. The decoder's "catalog." root is not part of
// the file the user wrote.
func definitionError(err error) error {
	reason := strings.TrimPrefix(config.FieldReason(err), "catalog.")
	if reason == "" {
		return err
	}
	failure := output.NewError("invalid_config", nil)
	failure.Message = "Invalid definition: " + reason + "."
	failure.NextAction = "Fix the definition file, then run the command again."
	return failure
}
