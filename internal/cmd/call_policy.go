package cmd

import (
	"context"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// newDialogFactory builds the approval dialog; tests replace it with a spy.
var newDialogFactory = newDialog

// checkCallOffline checks a call target against the config before the
// runtime is contacted, so a denied tool or a disabled, unreviewed, unknown
// or ambiguous connection starts no daemon, opens no connection and mints no
// token (D9). It returns the canonical connection ID and the tool name.
// The daemon repeats these checks at admission and before dispatch; this is
// an earlier, cheaper copy, not a replacement.
func checkCallOffline(ctx context.Context, paths config.Paths, target string) (canonicalID, tool string, err error) {
	connection, tool, err := splitCallTarget(target)
	if err != nil {
		return "", "", err
	}
	if ctx.Err() != nil {
		return "", "", output.NewError("canceled", nil)
	}
	snapshot, err := config.Load(paths)
	if err != nil {
		return "", "", safeFailure(err)
	}
	canonical, _, err := snapshot.RuntimeConnection(connection)
	if err != nil {
		return "", "", safeFailure(err)
	}
	if err = snapshot.CheckTool(canonical, tool); err != nil {
		return "", "", safeFailure(err)
	}
	return canonical, tool, nil
}
