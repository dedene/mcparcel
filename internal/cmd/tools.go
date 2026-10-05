package cmd

import (
	"context"

	"github.com/dedene/mcparcel/internal/args"
	"github.com/dedene/mcparcel/internal/output"
)

type ToolsCmd struct {
	MCP    string   `arg:"" required:"" json:"-" help:"Connection ID, or enable/disable for personal tool selection."`
	Rest   []string `arg:"" optional:"" sep:"none" json:"-"`
	Cached bool     `json:"-" help:"Use only an existing cached schema."`
}

func (c *ToolsCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	if c.MCP == "enable" || c.MCP == "disable" {
		if c.Cached || len(c.Rest) < 2 {
			return args.ErrInvalidArgs
		}
		store, state, err := metadataStore(ctx)
		if err != nil {
			return err
		}
		next, err := saveTools(ctx, store, state, c.Rest[0], c.Rest[1:], c.MCP == "enable")
		if err != nil {
			return err
		}
		return writeMutation(s, opts, next)
	}
	if len(c.Rest) != 0 {
		return args.ErrInvalidArgs
	}
	if c.Cached {
		return output.NewError("schema_cache_miss", nil)
	}
	client, err := newRuntimeClient(opts)
	if err != nil {
		return err
	}
	data, err := client.Tools(ctx, c.MCP, false)
	if err != nil {
		return err
	}
	return writeSuccess(s, opts, data)
}
