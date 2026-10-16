package cmd

import (
	"fmt"
	"strings"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// Human output of a configuration change says what happened in words
// ("Context7 is on.", "Context7 was already off."); JSON keeps the revision.

// connectionNamer returns how messages name a connection: its label (the
// short ID when the definition has none), else the name the user typed when
// it is a safe connection name, else the canonical ID.
func connectionNamer(state config.State) func(id, typed string) string {
	effective, err := config.Resolve(state)
	return func(id, typed string) string {
		if err == nil {
			if d := effective.Connections[id].Definition; d != nil && d.Label != "" {
				return output.DisplayMetadata(d.Label)
			}
		}
		if output.SafeConnectionName(typed) {
			return typed
		}
		return output.DisplayMetadata(id)
	}
}

// joinNames joins names as a sentence: "a", "a and b", "a, b and c".
func joinNames(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// toolNames names the tools for a message: all of them when each passes the
// MCP tool name grammar, else only how many, since a name outside it could be
// a pasted secret.
func toolNames(tools []string) string {
	for _, tool := range tools {
		if !output.SafeToolName(tool) {
			if len(tools) == 1 {
				return "1 tool"
			}
			return fmt.Sprintf("%d tools", len(tools))
		}
	}
	return joinNames(tools)
}

// refuseReadOnly is --dry-run's headless check: a plan for a configuration
// nobody can write is refused like the write itself, with config_read_only.
func refuseReadOnly() error {
	_, rt, err := resolveCommandPaths()
	if err != nil {
		return err
	}
	if rt.Mode == config.ModeHeadless {
		return config.ErrConfigReadOnly
	}
	return nil
}

// writePlan prints a --dry-run result: summary and "Nothing was saved." in
// human output, the plan in JSON.
func writePlan(s *Streams, opts *CommandOptions, plan output.PlanData, summary string) error {
	plan.DryRun = true
	if plan.Changes == nil {
		plan.Changes = []output.PlanChange{}
	}
	if opts.JSON {
		return writeSuccess(s, opts, plan)
	}
	return writeSuccess(s, opts, summary+"Nothing was saved.\n")
}
