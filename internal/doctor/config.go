package doctor

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

const configUnread = "Configuration not read; see config.file."

func configChecks(in Input) []output.DoctorCheck {
	if in.FilesErr != nil {
		c := output.DoctorCheck{ID: "config.file", Subject: "-", Status: Fail}
		switch {
		case errors.Is(in.FilesErr, context.DeadlineExceeded), errors.Is(in.FilesErr, config.ErrConfigConflict):
			c.Code, c.Message, c.NextAction = "config_conflict", "Another mcparcel command holds the configuration lock.", "Run doctor again when it has finished."
		case errors.Is(in.FilesErr, config.ErrUnsafePath):
			c.Code, c.NextAction = "unsafe_local_path", nextAction("unsafe_local_path")
			c.Message = "The configuration directory is not safe: " + show(in.Paths.ConfigDir) + "."
		default:
			c.Code, c.NextAction = "invalid_config", nextAction("invalid_config")
			c.Message = "The configuration could not be read: " + show(in.Paths.ConfigDir) + "."
		}
		return []output.DoctorCheck{
			c,
			{ID: "config.state", Status: Skip, Message: configUnread},
			{ID: "config.summary", Status: Skip, Message: configUnread},
		}
	}
	checks := make([]output.DoctorCheck, 0, len(in.Files.Files)+2)
	valid := true
	for _, f := range in.Files.Files {
		c := output.DoctorCheck{ID: "config.file", Subject: show(f.Name), Status: OK, Message: "Valid."}
		switch {
		case f.Err == nil && !f.Present:
			c.Message = "Not present."
		case errors.Is(f.Err, config.ErrUnsafePath):
			c.Status, c.Code, c.NextAction = Fail, "unsafe_local_path", nextAction("unsafe_local_path")
			c.Message = "The file or a directory above it is not safe to read."
		case f.Err != nil:
			reason := config.FieldReason(f.Err)
			if reason == "" {
				reason = "the file could not be read"
			}
			c.Status, c.Code, c.NextAction = Fail, "invalid_config", nextAction("invalid_config")
			c.Message = show(reason) + "."
		}
		valid = valid && f.Err == nil
		checks = append(checks, c)
	}
	return append(checks, stateCheck(in, valid), summaryCheck(in))
}

func stateCheck(in Input, valid bool) output.DoctorCheck {
	c := output.DoctorCheck{ID: "config.state", Status: OK, Message: "The documents agree."}
	switch {
	case !valid:
		c.Status, c.Message = Skip, "A document is invalid; see config.file."
	case in.Files.StateErr != nil || in.Snapshot == nil:
		reason := config.FieldReason(in.Files.StateErr)
		if reason == "" {
			reason = "the documents do not resolve together"
		}
		c.Status, c.Code, c.NextAction = Fail, "invalid_config", nextAction("invalid_config")
		c.Message = show(reason) + "."
	}
	return c
}

func summaryCheck(in Input) output.DoctorCheck {
	c := output.DoctorCheck{ID: "config.summary", Status: OK}
	if in.Snapshot == nil || in.Snapshot.Effective == nil {
		c.Status, c.Message = Skip, "Configuration not resolved; see config.state."
		return c
	}
	var enabled, disabled, review, unavailable int
	for _, row := range in.Snapshot.Effective.Connections {
		switch {
		case !row.Available:
			unavailable++
		case !row.Enabled:
			disabled++
		case row.ReviewRequired:
			review++
		default:
			enabled++
		}
	}
	if enabled+review == 0 {
		c.Status, c.Message, c.NextAction = Warn, "No connections enabled.", "Run mcparcel catalog, then mcparcel enable <mcp>."
		return c
	}
	c.Message = fmt.Sprintf("%d enabled, %d disabled, %d review required, %d unavailable.", enabled, disabled, review, unavailable)
	return c
}

// subjects returns the connections that get per-connection rows: the target,
// or every enabled one.
func subjects(in Input) []config.EffectiveConnection {
	if in.Snapshot == nil || in.Snapshot.Effective == nil || in.Files.State == nil {
		return nil
	}
	var rows []config.EffectiveConnection
	for _, row := range in.Snapshot.Effective.Connections {
		if in.Target != "" && row.ID == in.Target || in.Target == "" && row.Enabled {
			rows = append(rows, row)
		}
	}
	slices.SortFunc(rows, func(a, b config.EffectiveConnection) int { return strings.Compare(a.ID, b.ID) })
	return rows
}

func connectionChecks(in Input) []output.DoctorCheck {
	var checks []output.DoctorCheck
	for _, row := range subjects(in) {
		c := connectionCheck(in, row)
		checks = append(checks, c)
		if c.Status == Skip || !row.Available || row.Connection == nil {
			continue
		}
		checks = append(checks, credentialChecks(in, row)...)
		checks = append(checks, commandCheck(in, row))
	}
	return checks
}

func connectionCheck(in Input, row config.EffectiveConnection) output.DoctorCheck {
	id := show(row.ID)
	c := output.DoctorCheck{ID: "config.connection", Subject: id, Status: OK, Message: "Ready."}
	switch {
	case !row.Enabled:
		c.Status, c.Message = Skip, "Disabled."
	case !row.Available:
		source := "Its source"
		if row.SourceID != "" {
			source = show(row.SourceID)
		}
		c.Status, c.Code = Warn, "connection_unavailable"
		c.Message, c.NextAction = source+" no longer defines this connection.", "Run mcparcel disable "+id+"."
	case row.ReviewRequired:
		c.Status, c.Code = Warn, "review_required"
		c.Message, c.NextAction = "Review required.", "Run mcparcel sync to see what changed, then mcparcel enable "+id+"."
	case slices.Contains(row.Blockers, "config_required"):
		c.Status, c.Code = Fail, "config_required"
		c.Message, c.NextAction = missingText(*in.Files.State, row)
	default:
		if _, _, err := in.Snapshot.RuntimeConnection(row.ID); err != nil {
			c.Status = Fail
			switch {
			case errors.Is(err, config.ErrHeadlessOnePassword):
				e := output.HeadlessOnePasswordError()
				c.Code, c.Message, c.NextAction = e.Code, e.Message, e.NextAction
			case errors.Is(err, config.ErrRuntimeUnsupported):
				c.Code = "runtime_unsupported"
				c.Message = "This connection needs a runtime feature MCParcel does not have yet (SSE transport, or an idleTimeout other than session)."
				c.NextAction = nextAction("runtime_unsupported")
			default:
				c.Code, c.Message, c.NextAction = "invalid_config", "The connection cannot be prepared for the runtime.", nextAction("invalid_config")
			}
		}
	}
	return c
}

// missingText is config.connection's message and next action for a
// connection that needs inputs or a credential profile.
func missingText(state config.State, row config.EffectiveConnection) (string, string) {
	inputs, profile := config.MissingConfig(state, row)
	id := show(row.ID)
	var needs, steps []string
	for _, name := range inputs {
		needs = append(needs, "input "+show(name))
	}
	switch len(inputs) {
	case 0:
	case 1:
		steps = append(steps, "mcparcel config input set "+id+" "+show(inputs[0])+" <value>")
	default:
		steps = append(steps, "mcparcel config input set "+id+" <name> <value> for each input")
	}
	if profile {
		needs = append(needs, "credential profile")
		steps = append(steps, "mcparcel config profile bind "+id+" <profile>")
	}
	if len(needs) == 0 {
		return "Configuration required.", nextAction("config_required")
	}
	return "Needs: " + strings.Join(needs, ", ") + ".", "Run " + strings.Join(steps, ", then ") + "."
}
