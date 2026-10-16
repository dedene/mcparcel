package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/edit"
	"github.com/dedene/mcparcel/internal/output"
)

type ConfigCmd struct {
	Validate ConfigValidateCmd `cmd:"" json:"-" help:"Validate a catalog file offline."`
	Input    ConfigInputCmd    `cmd:"" json:"-" help:"Set local connection inputs."`
	Profile  ConfigProfileCmd  `cmd:"" json:"-" help:"Save and bind credential profile metadata."`
}
type ConfigValidateCmd struct {
	File string `required:"" name:"file" json:"-"`
}
type ConfigInputCmd struct {
	Set ConfigInputSetCmd `cmd:"" json:"-" help:"Set a declared input without enabling the connection."`
}
type ConfigInputSetCmd struct {
	MCP   string `arg:"" required:"" json:"-"`
	Name  string `arg:"" required:"" json:"-"`
	Value string `arg:"" required:"" json:"-"`
}
type ConfigProfileCmd struct {
	Set  ConfigProfileSetCmd  `cmd:"" json:"-" help:"Save profile metadata from a file."`
	Bind ConfigProfileBindCmd `cmd:"" json:"-" help:"Bind an existing local profile to a connection."`
}
type ConfigProfileSetCmd struct {
	Name string `arg:"" required:"" json:"-"`
	File string `required:"" name:"file" json:"-"`
}
type ConfigProfileBindCmd struct {
	MCP     string `arg:"" required:"" json:"-"`
	Profile string `arg:"" required:"" json:"-"`
}
type ConfigValidationData struct {
	Valid bool `json:"valid"`
}
type ConfigMutationData struct {
	Revision uint64 `json:"revision"`
	// LinkedProfiles maps canonical connection IDs to the local credential
	// profile that enable bound on its own. Omitted when nothing was bound.
	LinkedProfiles map[string]string `json:"linkedProfiles,omitempty"`
}

func readCommandFile(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := config.OpenConfigFile(path)
	if err != nil {
		if errors.Is(err, config.ErrUnsafePath) {
			return nil, err
		}
		return nil, config.ErrConfig
	}
	defer f.Close()
	const limit = 2 * 1024 * 1024
	data := make([]byte, 0)
	buf := make([]byte, 32*1024)
	for len(data) <= limit {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, e := f.Read(buf[:min(len(buf), limit+1-len(data))])
		data = append(data, buf[:n]...)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, config.ErrConfig
		}
	}
	if len(data) > limit {
		return nil, config.ErrConfig
	}
	return data, nil
}

func (c *ConfigValidateCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	raw, err := readCommandFile(ctx, c.File)
	if err != nil {
		return err
	}
	if _, err = config.DecodeCatalog(raw); err != nil {
		return err
	}
	var payload any = ConfigValidationData{true}
	if !opts.JSON {
		payload = "Configuration is valid.\n"
	}
	return writeSuccess(s, opts, payload)
}

func metadataStore(ctx context.Context) (*config.Store, config.State, error) {
	paths, err := commandPaths()
	if err != nil {
		return nil, config.State{}, err
	}
	store := config.NewStore(paths)
	state, err := store.Read(ctx)
	return store, state, err
}

// writeMutation prints summary, what the change did in words (one or more
// lines); JSON has the new revision instead.
func writeMutation(s *Streams, opts *CommandOptions, state config.State, summary string) error {
	return writeLinkedMutation(s, opts, state, summary, nil)
}

// writeLinkedMutation also reports profiles that enable bound on its own, by
// profile ID only, never by account.
func writeLinkedMutation(s *Streams, opts *CommandOptions, state config.State, summary string, linked []linkedProfile) error {
	if opts.JSON {
		data := ConfigMutationData{Revision: state.Selections.Revision}
		for _, l := range linked {
			if data.LinkedProfiles == nil {
				data.LinkedProfiles = map[string]string{}
			}
			data.LinkedProfiles[l.id] = l.profile
		}
		return writeSuccess(s, opts, data)
	}
	text := summary
	for _, l := range linked {
		text += fmt.Sprintf("Linked credential profile %s to %s.\n", output.DisplayMetadata(l.profile), output.DisplayMetadata(l.name))
	}
	return writeSuccess(s, opts, text)
}

func (c *ConfigInputSetCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	store, state, err := metadataStore(ctx)
	if err != nil {
		return err
	}
	id, d, err := edit.Definition(state, c.MCP)
	if err != nil {
		return err
	}
	if _, ok := d.Inputs[c.Name]; !ok {
		return config.ErrConfig
	}
	next, err := store.Update(ctx, state.Selections.Revision, func(draft *config.State) error {
		return edit.StageInput(draft, id, c.Name, c.Value)
	})
	if err != nil {
		return err
	}
	name := connectionNamer(next)(id, c.MCP)
	return writeMutation(s, opts, next, fmt.Sprintf("Set input %s for %s.\n", output.DisplayMetadata(c.Name), name))
}

func (c *ConfigProfileSetCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	if _, err := config.CanonicalLocal(c.Name); err != nil {
		return config.ErrConfig
	}
	raw, err := readCommandFile(ctx, c.File)
	if err != nil {
		return err
	}
	wrapped, err := json.Marshal(map[string]any{"schemaVersion": 1, "credentialProfiles": map[string]json.RawMessage{c.Name: raw}})
	if err != nil {
		return config.ErrConfig
	}
	local, err := config.DecodeLocal(wrapped)
	if err != nil {
		return err
	}
	store, state, err := metadataStore(ctx)
	if err != nil {
		return err
	}
	next, err := store.Update(ctx, state.Selections.Revision, func(draft *config.State) error {
		draft.Local.CredentialProfiles[c.Name] = local.CredentialProfiles[c.Name]
		return nil
	})
	if err != nil {
		return err
	}
	return writeMutation(s, opts, next, fmt.Sprintf("Saved credential profile %s.\n", output.DisplayMetadata(c.Name)))
}

func (c *ConfigProfileBindCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	store, state, err := metadataStore(ctx)
	if err != nil {
		return err
	}
	id, d, err := edit.Definition(state, c.MCP)
	if err != nil {
		return err
	}
	if d.CredentialProfile == "" {
		return config.ErrConfig
	}
	if _, ok := state.Local.CredentialProfiles[c.Profile]; !ok {
		return config.ErrConfig
	}
	next, err := store.Update(ctx, state.Selections.Revision, func(draft *config.State) error {
		return edit.StageProfile(draft, id, c.Profile)
	})
	if err != nil {
		return err
	}
	name := connectionNamer(next)(id, c.MCP)
	return writeMutation(s, opts, next, fmt.Sprintf("Linked credential profile %s to %s.\n", output.DisplayMetadata(c.Profile), name))
}
