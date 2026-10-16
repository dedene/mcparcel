package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/dedene/mcparcel/internal/catalog"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
	"github.com/dedene/mcparcel/internal/ui"
)

// JoinCmd adds a team catalog as add does, then opens setup on it: the
// 1Password accounts the catalog suggests, its presets, then the list. Until
// the user confirms a suggestion or picks presets, only the source
// registration is written.
type JoinCmd struct {
	Repository string `arg:"" required:"" json:"-"`
	Path       string `name:"path" json:"-"`
	Ref        string `name:"ref" json:"-"`
}

// joinTerminal is newSetupTerminal; tests replace it.
var joinTerminal = newSetupTerminal

func (c *JoinCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions, deps *CatalogDependencies) error {
	// Before any read, fetch or write: join asks questions.
	in, _ := s.In.(*os.File)
	errOut, _ := s.Err.(*os.File)
	if opts.JSON || opts.NoInput || !joinTerminal(in, errOut) {
		repository := "<owner/repo>"
		if _, _, err := catalog.ValidateRepositoryName(c.Repository); err == nil {
			repository = output.DisplayMetadata(c.Repository)
		}
		return output.JoinTerminalError(repository)
	}
	paths, err := commandPaths()
	if err != nil {
		return err
	}
	store := config.NewStore(paths)
	state, err := store.Read(ctx)
	if err != nil {
		return err
	}
	source, registered, err := sourceRequest(state, c.Repository, c.Path, c.Ref)
	if err != nil {
		return err
	}
	// A catalog that is already added is used as saved; sync updates it.
	note := "%s is already added (%s). Run mcparcel sync to update it.\n"
	if !registered {
		waiting := startProgress(s, opts, fetchLabel(source))
		state, source, err = registerSource(ctx, deps, store, state, source)
		waiting.stop()
		if err != nil {
			return err
		}
		note = "Added %s (%s).\n"
	}
	repository, commit := source.Owner+"/"+source.Repo, source.Commit[:min(7, len(source.Commit))]
	if err := writeSuccess(s, &CommandOptions{}, fmt.Sprintf(note, output.DisplayMetadata(repository), output.DisplayMetadata(commit))); err != nil {
		return err
	}
	options := setupOptions(paths, store, opts, in, errOut)
	options.Join = &ui.Join{SourceID: source.ID, Repository: repository, Commit: commit}
	result, err := runSetupUI(ctx, store, state, options)
	return finishSetup(s, result, err)
}
