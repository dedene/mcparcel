package cmd

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/edit"
	"github.com/dedene/mcparcel/internal/output"
)

type (
	EnableCmd struct {
		MCPs []string `arg:"" name:"mcp" required:"" sep:"none" json:"-"`
	}
	DisableCmd struct {
		MCPs   []string `arg:"" name:"mcp" required:"" sep:"none" json:"-"`
		DryRun bool     `name:"dry-run" json:"-" help:"Show what would change without saving."`
	}
)

func (c *EnableCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	return writeEnabled(ctx, s, opts, c.MCPs, true)
}

func (c *DisableCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	if c.DryRun {
		return planDisable(ctx, s, opts, c.MCPs)
	}
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
	return writeLinkedMutation(s, opts, next, enabledSummary(state, next, names, enabled), linkedProfiles(state, next, names))
}

// enabledSummary is one line per connection, in the order typed: "X is on."
// or "X was already off.". Enable changes a connection that was off or
// awaited review.
func enabledSummary(before, after config.State, names []string, enabled bool) string {
	var b strings.Builder
	name := connectionNamer(after)
	for _, c := range typedConnections(before, names) {
		sel := before.Selections.Connections[c.id]
		changed := sel.Enabled != enabled || enabled && sel.ReviewRequired
		switch {
		case changed && enabled:
			b.WriteString(name(c.id, c.name) + " is on.\n")
		case changed:
			b.WriteString(name(c.id, c.name) + " is off.\n")
		case enabled:
			b.WriteString(name(c.id, c.name) + " was already on.\n")
		default:
			b.WriteString(name(c.id, c.name) + " was already off.\n")
		}
	}
	return b.String()
}

// planDisable is disable --dry-run: the same checks as disable, then what it
// would turn off, without saving.
func planDisable(ctx context.Context, s *Streams, opts *CommandOptions, names []string) error {
	if err := refuseReadOnly(); err != nil {
		return err
	}
	_, state, err := metadataStore(ctx)
	if err != nil {
		return err
	}
	if _, err = stageEnabled(state, names, false); err != nil {
		return err
	}
	plan := output.PlanData{Revision: state.Selections.Revision}
	var b strings.Builder
	name := connectionNamer(state)
	for _, c := range typedConnections(state, names) {
		on := state.Selections.Connections[c.id].Enabled
		change := output.PlanChange{Connection: c.id, Change: "none", Enabled: on}
		if on {
			change.Change = "turn_off"
			b.WriteString("Would turn off " + name(c.id, c.name) + ".\n")
		} else {
			b.WriteString(name(c.id, c.name) + " is already off.\n")
		}
		plan.Changes = append(plan.Changes, change)
	}
	return writePlan(s, opts, plan, b.String())
}

type typedConnection struct{ id, name string }

// typedConnections resolves each name to its canonical ID, in the order
// typed, keeping the first name typed for a connection named twice.
func typedConnections(state config.State, names []string) []typedConnection {
	var out []typedConnection
	for _, name := range names {
		ids, err := edit.ResolveIDs(state, []string{name})
		if err != nil || slices.ContainsFunc(out, func(c typedConnection) bool { return c.id == ids[0] }) {
			continue
		}
		out = append(out, typedConnection{id: ids[0], name: name})
	}
	return out
}

// linkedProfiles lists, sorted by canonical ID, each connection whose
// credential profile enable bound on its own (edit.AutoProfile). It keeps the
// first name the user typed for that connection, for the human output.
func linkedProfiles(before, after config.State, names []string) []linkedProfile {
	var out []linkedProfile
	for _, c := range typedConnections(before, names) {
		profile := after.Selections.Connections[c.id].CredentialProfile
		if profile == before.Selections.Connections[c.id].CredentialProfile {
			continue
		}
		out = append(out, linkedProfile{id: c.id, name: c.name, profile: profile})
	}
	slices.SortFunc(out, func(a, b linkedProfile) int { return strings.Compare(a.id, b.id) })
	return out
}

type linkedProfile struct{ id, name, profile string }

func saveEnabled(ctx context.Context, store *config.Store, state config.State, names []string, enabled bool) (config.State, error) {
	ids, err := stageEnabled(state, names, enabled)
	if err != nil {
		return config.State{}, err
	}
	mutate := func(draft *config.State) error { return edit.StageEnabled(draft, names, ids, enabled) }
	if enabled {
		return store.UpdateAccepted(ctx, state.Selections.Revision, ids, mutate)
	}
	return store.Update(ctx, state.Selections.Revision, mutate)
}

// stageEnabled checks that names resolve and that the change stages on a
// copy of state's selections, and returns their canonical IDs.
func stageEnabled(state config.State, names []string, enabled bool) ([]string, error) {
	ids, err := edit.ResolveIDs(state, names)
	if err != nil {
		return nil, err
	}
	// Prevalidation must not mutate the caller's loaded selections.
	raw, err := json.Marshal(state.Selections)
	if err != nil {
		return nil, config.ErrConfig
	}
	draft := state
	draft.Selections, err = config.DecodeSelections(raw)
	if err != nil {
		return nil, err
	}
	if err := edit.StageEnabled(&draft, names, ids, enabled); err != nil {
		return nil, err
	}
	return ids, nil
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
