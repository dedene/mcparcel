package cmd

import (
	"context"
	"fmt"
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
	ID     string `arg:"" required:"" json:"-"`
	DryRun bool   `name:"dry-run" json:"-" help:"Show what would change without saving."`
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
	var id string
	next, err := store.Update(ctx, state.Selections.Revision, func(draft *config.State) error {
		var err error
		id, err = edit.StageLocalAdd(draft, raw)
		return definitionError(err)
	})
	if err != nil {
		return err
	}
	name := connectionNamer(next)("local:"+id, id)
	summary := fmt.Sprintf("Added personal connection %s. It is off: run mcparcel enable local:%s to turn it on.\n", name, id)
	if _, existed := state.Personal.Connections[id]; existed {
		summary = fmt.Sprintf("Personal connection %s already exists with this definition. Nothing changed.\n", name)
	}
	return writeMutation(s, opts, next, summary)
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
	canonical := "local:" + id
	summary := fmt.Sprintf("Updated personal connection %s.\n", connectionNamer(next)(canonical, id))
	if next.Selections.Connections[canonical].ReviewRequired && !state.Selections.Connections[canonical].ReviewRequired {
		summary += fmt.Sprintf("Its command or sign-in changed, so it needs review: run mcparcel enable %s to accept it.\n", canonical)
	}
	return writeMutation(s, opts, next, summary)
}

func (c *LocalRemoveCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	id, err := localID(c.ID)
	if err != nil {
		return err
	}
	if c.DryRun {
		if err := refuseReadOnly(); err != nil {
			return err
		}
	}
	store, state, err := metadataStore(ctx)
	if err != nil {
		return err
	}
	canonical := "local:" + id
	name := connectionNamer(state)(canonical, id)
	on := state.Selections.Connections[canonical].Enabled
	if c.DryRun {
		if _, exists := state.Personal.Connections[id]; !exists {
			return config.ErrNotFound
		}
		summary := fmt.Sprintf("Would remove personal connection %s.\n", name)
		if on {
			summary = fmt.Sprintf("Would remove personal connection %s and turn it off.\n", name)
		}
		plan := output.PlanData{Revision: state.Selections.Revision, Changes: []output.PlanChange{{Connection: canonical, Change: "remove", Enabled: on}}}
		return writePlan(s, opts, plan, summary)
	}
	next, err := store.Update(ctx, state.Selections.Revision, func(draft *config.State) error {
		return edit.StageLocalRemove(draft, id)
	})
	if err != nil {
		return err
	}
	summary := fmt.Sprintf("Removed personal connection %s.\n", name)
	if on {
		summary = fmt.Sprintf("Removed personal connection %s. It is off now.\n", name)
	}
	return writeMutation(s, opts, next, summary)
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
