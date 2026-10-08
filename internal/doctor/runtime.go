package doctor

import (
	"errors"
	"fmt"
	"os"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
)

const modeUnknown = "Runtime mode unknown; see runtime.mode."

func runtimeChecks(in Input) []output.DoctorCheck {
	return []output.DoctorCheck{modeCheck(in), versionCheck(in)}
}

func modeCheck(in Input) output.DoctorCheck {
	c := output.DoctorCheck{ID: "runtime.mode", Status: OK}
	switch {
	case in.RuntimeErr != nil:
		code, reason := "invalid_config", config.FieldReason(in.RuntimeErr)
		if errors.Is(in.RuntimeErr, config.ErrUnsafePath) {
			code, reason = "unsafe_local_path", "the file, a directory above it or the state root is not safe"
		}
		if reason == "" {
			reason = "the file could not be read"
		}
		c.Status, c.Code, c.NextAction = Fail, code, nextAction(code)
		c.Message = "Cannot read the runtime mode from config.json: " + reason + "."
	case in.Runtime.Mode == config.ModeHeadless:
		c.Message = "Headless mode (state root " + show(in.Paths.StateRoot) + ")."
		if in.Supervised {
			c.Message += " Supervised."
		}
	case !in.DesktopSupported:
		e := output.HeadlessOnlyError()
		c.Status, c.Code, c.Message, c.NextAction = Fail, e.Code, e.Message, e.NextAction
	case noSession(in):
		e := output.LinuxNoSessionError()
		c.Status, c.Code, c.Message, c.NextAction = Fail, e.Code, e.Message, e.NextAction
	default:
		c.Message = "Desktop mode."
	}
	return c
}

// runtimeUsable reports whether the runtime can be reached at all: the mode
// is known and supported here.
func runtimeUsable(in Input) bool {
	return in.RuntimeErr == nil && (headless(in) || in.DesktopSupported && !noSession(in))
}

// noSession reports desktop mode that runtime commands refuse: no desktop
// session and no config.json (Linux), most likely a container or service
// that misses its headless configuration. A config the lock kept doctor from
// reading counts as present.
func noSession(in Input) bool {
	if headless(in) || in.DesktopSession || in.FilesErr != nil {
		return false
	}
	for _, f := range in.Files.Files {
		if f.Name == "config.json" {
			return !f.Present
		}
	}
	return true
}

func versionCheck(in Input) output.DoctorCheck {
	c := output.DoctorCheck{ID: "runtime.version", Status: OK}
	if in.RuntimeErr != nil {
		c.Status, c.Message = Skip, modeUnknown
		return c
	}
	if !runtimeUsable(in) {
		c.Status, c.Message = Skip, "Desktop mode does not run here; see runtime.mode."
		return c
	}
	if in.ProbeErr != nil {
		code := "runtime_start_failed"
		c.Message = "Could not check the runtime."
		if errors.Is(in.ProbeErr, config.ErrUnsafePath) {
			code = "unsafe_local_path"
			c.Message = "The runtime directory or its socket is not safe: " + show(in.Paths.RuntimeDir) + "."
		}
		c.Status, c.Code, c.NextAction = Fail, code, nextAction(code)
		return c
	}
	p := in.Probe
	restart := "Run mcparcel runtime restart when no calls are active."
	if in.Supervised {
		restart = "Restart the runtime through its supervisor."
	}
	switch p.State {
	case runtimeclient.ProbeStopped, runtimeclient.ProbeStaleSocket:
		switch {
		case in.Supervised:
			c.Status, c.NextAction = Warn, nextAction("runtime_supervised")
			c.Message = "Not running; its supervisor (runtime serve) starts it."
		case p.State == runtimeclient.ProbeStaleSocket:
			c.Message = "Not running. A stale socket is left; the next start removes it."
		default:
			c.Message = "Not running. The next runtime command starts " + show(in.Version) + "."
		}
	case runtimeclient.ProbeStarting:
		c.Status, c.Code = Warn, "runtime_start_failed"
		c.Message = "The runtime holds its lock but did not answer within 2 s (starting, or not responding)."
		c.NextAction = "Run doctor again; if this persists, check the daemon log at " + show(in.Paths.LogFile) + "."
	case runtimeclient.ProbeRunning:
		c.Message = fmt.Sprintf("Running %s (PID %d).", show(p.DaemonVersion), p.PID)
	case runtimeclient.ProbeVersionMismatch:
		c.Status, c.Code, c.NextAction = Fail, "runtime_version_mismatch", restart
		c.Message = fmt.Sprintf("The runtime runs %s (PID %d); this CLI is %s.", show(p.DaemonVersion), p.PID, show(in.Version))
	case runtimeclient.ProbeConfigMismatch:
		c.Status, c.Code, c.NextAction = Fail, "runtime_config_mismatch", restart
		c.Message = fmt.Sprintf("The runtime (PID %d) uses another configuration directory.", p.PID)
	default: // unreadable
		c.Status, c.Code = Fail, "runtime_version_mismatch"
		c.Message = "A runtime answers on the socket, but its handshake could not be read (another MCParcel version, or not responding)."
		c.NextAction = "Run mcparcel runtime stop with the MCParcel version that started it; if that fails too, end the daemon process (its PID is in the daemon log)."
	}
	return c
}

func storageChecks(in Input) []output.DoctorCheck {
	if in.RuntimeErr != nil {
		return []output.DoctorCheck{{ID: "storage.dir", Status: Skip, Message: modeUnknown}}
	}
	dirs := []struct{ name, path string }{{"state", in.Paths.StateDir}, {"data", in.Paths.DataDir}, {"runtime", in.Paths.RuntimeDir}}
	checks := make([]output.DoctorCheck, 0, len(dirs))
	for _, d := range dirs {
		c := output.DoctorCheck{ID: "storage.dir", Subject: d.name, Status: OK}
		f, err := config.OpenPrivateDirUnder(in.Paths.StateRoot, d.path, false)
		switch {
		case err == nil:
			_ = f.Close()
			c.Message = "Private: " + show(d.path) + "."
		case errors.Is(err, os.ErrNotExist):
			c.Message = "Absent; created on first use: " + show(d.path) + "."
		default:
			c.Status, c.Code, c.NextAction = Fail, "unsafe_local_path", nextAction("unsafe_local_path")
			c.Message = "Not private to you (mode 700, owned by you, no symlinks): " + show(d.path) + "."
		}
		checks = append(checks, c)
	}
	return checks
}
