package cmd

import (
	"context"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/edit"
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
		return err
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
		return edit.StageLocalUpdate(draft, id, raw)
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
