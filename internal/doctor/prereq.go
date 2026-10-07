package doctor

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/mcpclient"
	"github.com/dedene/mcparcel/internal/output"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
)

// commandCheck looks the stdio command up on the PATH the runtime would use,
// as the runtime does (mcpclient.Executable). It never runs it.
func commandCheck(in Input, row config.EffectiveConnection) output.DoctorCheck {
	id := show(row.ID)
	c := output.DoctorCheck{ID: "prereq.command", Subject: id, Status: Skip}
	stdio := row.Connection.Transport.Stdio
	if stdio == nil {
		c.Message = "Remote connection; use --live to check it."
		return c
	}
	if stdio.Command.Literal == nil {
		c.Message = "The command comes from an input without a value; doctor cannot check it."
		return c
	}
	command := *stdio.Command.Literal
	path, which, strict := in.PATH, "this shell's PATH", false
	switch v, ok := stdio.Env["PATH"]; {
	case ok && v.Literal == nil:
		c.Message = "The connection's PATH is not a literal; doctor cannot check the command."
		return c
	case ok:
		path, which, strict = *v.Literal, "the connection's PATH", true
	case in.ProbeErr == nil && in.Probe.State == runtimeclient.ProbeRunning && in.Probe.Status != nil:
		path, which, strict = in.Probe.Status.CapturedPath, "the runtime's PATH", true
	case headless(in):
		path, which, strict = in.PATH, "this process's PATH", true
	}
	found, err := mcpclient.Executable(command, path, "")
	absolute := strings.ContainsRune(command, '/')
	if err == nil {
		c.Status = OK
		if absolute {
			c.Message = "Found " + show(found) + "."
		} else {
			c.Message = "Found " + show(command) + " at " + show(found) + " on " + which + "."
		}
		return c
	}
	c.Status, c.Code, c.NextAction = Warn, "connection_failed", installHint(command, !headless(in))
	if strict || absolute {
		c.Status = Fail
	}
	if absolute {
		c.Message = show(command) + " does not exist or is not an executable file."
	} else {
		c.Message = show(command) + " was not found on " + which + "."
	}
	return c
}

// installHint is prereq.command's next action. On desktop the daemon captures
// PATH when it starts, so a new install needs a runtime restart.
func installHint(command string, desktop bool) string {
	var hint string
	switch base := filepath.Base(command); {
	case base == "npx" || base == "node":
		hint = "Install Node.js 20 or newer"
	case base == "uvx" || base == "uv":
		hint = "Install uv"
	case base == "docker":
		hint = "Install and start Docker"
	case filepath.IsAbs(command):
		hint = "Install the program at this path or fix the definition"
	default:
		hint = "Install " + show(command) + " or add it to your PATH"
	}
	if desktop {
		return hint + ", then run mcparcel runtime restart."
	}
	return hint + "."
}

// onePasswordAppCheck is prereq.onepassword: on desktop, when an enabled
// connection or its bound profile uses an op:// reference, 1Password.app must
// exist in one of the app directories. It only stats; it never calls the SDK
// or runs op.
func onePasswordAppCheck(in Input) []output.DoctorCheck {
	if Mode(in) != config.ModeDesktop || in.Snapshot == nil || in.Snapshot.Effective == nil {
		return nil
	}
	needed := false
	for _, row := range in.Snapshot.Effective.Connections {
		if row.Enabled && row.Available && row.Connection != nil && len(onePasswordRefs(in, row.Connection)) > 0 {
			needed = true
			break
		}
	}
	if !needed {
		return nil
	}
	stat := in.Stat
	if stat == nil {
		stat = os.Stat
	}
	for _, dir := range in.AppDirs {
		if info, err := stat(filepath.Join(dir, "1Password.app")); err == nil && info.IsDir() {
			return []output.DoctorCheck{{ID: "prereq.onepassword", Status: OK, Message: `1Password app found; doctor does not check that "Integrate with other apps" is on.`}}
		}
	}
	return []output.DoctorCheck{{ID: "prereq.onepassword", Status: Fail, Code: "auth_failed", Message: "The 1Password desktop app was not found; 1Password references need it.", NextAction: "Install the 1Password desktop app and turn on Settings > Developer > Integrate with other apps."}}
}
