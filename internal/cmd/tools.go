package cmd

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/alecthomas/kong"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/edit"
	"github.com/dedene/mcparcel/internal/output"
)

type ToolsCmd struct {
	MCP     ToolsListCmd    `arg:"" json:"-" help:"List a connection's tools with their arguments."`
	List    ToolsListCmd    `cmd:"" json:"-" help:"List a connection's tools; also reaches a connection named list, enable or disable."`
	Enable  ToolsEnableCmd  `cmd:"" json:"-" help:"Turn tools on in your personal tool selection offline."`
	Disable ToolsDisableCmd `cmd:"" json:"-" help:"Turn tools off in your personal tool selection offline."`
	// Cached is hidden until there is a schema cache: it is accepted and
	// always fails schema_cache_miss. It lives here, not on ToolsListCmd,
	// so kong parses it before <mcp> too (tools --cached <mcp>).
	Cached bool `hidden:"" json:"-" help:"Use only an existing cached schema."`
}

// AfterApply hands --cached to the list command and refuses it on enable
// and disable, which never took it, as kong refuses a flag it does not know.
func (c *ToolsCmd) AfterApply(kctx *kong.Context) error {
	if !c.Cached {
		return nil
	}
	switch kctx.Selected().Name {
	case "enable", "disable":
		return errors.New("unknown flag --cached")
	}
	c.MCP.cached, c.List.cached = true, true
	return nil
}

type (
	// ToolsListCmd is tools <mcp>, a branching argument: kong tries the
	// subcommands first, so a connection named list, enable or disable is
	// reached by tools list <mcp> or by its canonical ID. Its first
	// positional must carry the parent field's name.
	ToolsListCmd struct {
		MCP    string `arg:"" json:"-" help:"Connection whose tools to list."`
		cached bool   // ToolsCmd.Cached, handed down by its AfterApply
	}
	ToolsEnableCmd struct {
		MCP   string   `arg:"" json:"-" help:"Connection whose tool selection to change."`
		Tools []string `arg:"" name:"tool" sep:"none" json:"-" help:"Tools to turn on."`
	}
	ToolsDisableCmd struct {
		MCP   string   `arg:"" json:"-" help:"Connection whose tool selection to change."`
		Tools []string `arg:"" name:"tool" sep:"none" json:"-" help:"Tools to turn off."`
	}
)

func (c *ToolsListCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	if c.cached {
		return output.NewError("schema_cache_miss", nil)
	}
	client, err := newRuntimeClient(opts)
	if err != nil {
		return err
	}
	waiting := startProgress(s, opts, toolsLabel(c.MCP))
	data, err := client.Tools(ctx, c.MCP, false)
	waiting.stop()
	if err != nil {
		return err
	}
	return writeSuccess(s, opts, data)
}

// toolsLabel is the progress line for tools <mcp>; a connection name that is
// not safe to show is left out.
func toolsLabel(connection string) string {
	if output.SafeConnectionName(connection) {
		return "Loading " + connection + " tools" + ellipsis()
	}
	return "Loading tools" + ellipsis()
}

func (c *ToolsEnableCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	return selectTools(ctx, s, opts, c.MCP, c.Tools, true)
}

func (c *ToolsDisableCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	return selectTools(ctx, s, opts, c.MCP, c.Tools, false)
}

func selectTools(ctx context.Context, s *Streams, opts *CommandOptions, mcp string, tools []string, enabled bool) error {
	store, state, err := metadataStore(ctx)
	if err != nil {
		return err
	}
	next, err := saveTools(ctx, store, state, mcp, tools, enabled)
	if err != nil {
		return err
	}
	return writeMutation(s, opts, next, toolsSummary(state, next, mcp, tools, enabled))
}

// toolsSummary says which tools turned on or off for the connection, which
// already were, and which the connection's tool policy keeps off whatever
// the personal selection says: "Turned off counter for Paper.".
func toolsSummary(before, after config.State, mcp string, tools []string, enabled bool) string {
	ids, err := edit.ResolveIDs(before, []string{mcp})
	if err != nil {
		return ""
	}
	id := ids[0]
	off := before.Selections.Connections[id].DisabledTools
	var policy config.ToolPolicy
	if effective, err := config.Resolve(after); err == nil {
		policy = effective.Connections[id].Policy
	}
	var changed, unchanged, denied []string
	for _, tool := range tools {
		if slices.Contains(changed, tool) || slices.Contains(unchanged, tool) || slices.Contains(denied, tool) {
			continue
		}
		switch {
		case enabled && !config.ToolAllowed(policy, tool):
			denied = append(denied, tool)
		case slices.Contains(off, tool) == enabled:
			changed = append(changed, tool)
		default:
			unchanged = append(unchanged, tool)
		}
	}
	state, verb := "off", "Turned off"
	if enabled {
		state, verb = "on", "Turned on"
	}
	name := connectionNamer(after)(id, mcp)
	var b strings.Builder
	if len(changed) > 0 {
		fmt.Fprintf(&b, "%s %s for %s.\n", verb, toolNames(changed), name)
	}
	if len(unchanged) > 0 {
		fmt.Fprintf(&b, "%s %s already %s for %s.\n", toolNames(unchanged), wasWere(unchanged), state, name)
	}
	switch {
	case len(denied) == 1:
		fmt.Fprintf(&b, "%s stays off for %s: its tool policy does not allow it.\n", toolNames(denied), name)
	case len(denied) > 1:
		fmt.Fprintf(&b, "%s stay off for %s: its tool policy does not allow them.\n", toolNames(denied), name)
	}
	return b.String()
}

func wasWere(items []string) string {
	if len(items) == 1 {
		return "was"
	}
	return "were"
}
