package doctor

import (
	"os"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
)

// upgradeChecks returns runtime.binary and version.catalog (stage 10,
// packaging and upgrade); Offline puts them in contract order.
func upgradeChecks(in Input) []output.DoctorCheck {
	return append([]output.DoctorCheck{binaryCheck(in)}, catalogVersionChecks(in)...)
}

// binaryCheck is runtime.binary: whether a desktop runtime runs from its
// retained copy, so npm rewriting or cleaning its cache cannot touch it.
func binaryCheck(in Input) output.DoctorCheck {
	c := output.DoctorCheck{ID: "runtime.binary", Status: Skip}
	switch {
	case in.RuntimeErr != nil:
		c.Message = modeUnknown
	case !runtimeUsable(in):
		c.Message = "Desktop mode does not run here; see runtime.mode."
	case headless(in):
		c.Message = "Headless mode runs the installed binary in place."
	case in.Supervised:
		c.Message = "A supervisor runs the runtime from its own binary."
	case !in.Retain:
		c.Message = "This build runs the runtime from the CLI binary in place."
	case in.ProbeErr != nil:
		c.Message = "See runtime.version."
	}
	if c.Message != "" {
		return c
	}
	stat := in.Stat
	if stat == nil {
		stat = os.Stat
	}
	switch in.Probe.State {
	case runtimeclient.ProbeRunning:
		exe := ""
		if in.Probe.Status != nil {
			exe = in.Probe.Status.Executable
		}
		retained, err := runtimeclient.RetainedPath(in.Paths, in.Probe.DaemonVersion)
		if exe == "" {
			c.Message = "The runtime did not report its executable."
			return c
		}
		if _, statErr := stat(exe); statErr != nil {
			c.Status, c.NextAction = Warn, "Run mcparcel runtime restart when no calls are active."
			c.Message = "The runtime's executable file is gone (npm cache cleanup, or a pruned copy). It keeps running; restart it so it runs from a retained copy."
			return c
		}
		if err == nil && exe == retained {
			c.Status, c.Message = OK, "Runs from its retained copy "+show(exe)+"."
			return c
		}
		c.Status, c.NextAction = Warn, "Check free space and the data directory (storage.dir), then run mcparcel runtime restart."
		c.Message = "The runtime runs from " + show(exe) + ", not a retained copy (the copy failed when it started)."
	case runtimeclient.ProbeStopped, runtimeclient.ProbeStaleSocket:
		retained, err := runtimeclient.RetainedPath(in.Paths, in.Version)
		if err != nil {
			c.Message = "This version (" + show(in.Version) + ") cannot name a retained copy; the runtime runs from this binary."
			return c
		}
		c.Status = OK
		if _, err = stat(retained); err == nil {
			c.Message = "Retained copy present: " + show(retained) + "."
		} else {
			c.Message = "The next start copies this binary to " + show(retained) + "."
		}
	default:
		c.Message = "See runtime.version."
	}
	return c
}

// catalogVersionChecks is version.catalog: per registered source, its
// catalog's minVersion against this version. The config read path never
// refuses a newer catalog (an active snapshot after a rollback keeps
// working); this row reports it. Without a readable state there is no row.
func catalogVersionChecks(in Input) []output.DoctorCheck {
	state := in.Files.State
	if state == nil {
		return nil
	}
	checks := make([]output.DoctorCheck, 0, len(state.Local.Sources))
	for _, source := range state.Local.Sources {
		c := output.DoctorCheck{ID: "version.catalog", Subject: show(source.ID), Status: OK}
		minimum := state.Catalogs[source.ID].MinVersion
		order, comparable := config.CompareVersion(in.Version, minimum)
		switch {
		case minimum == "":
			c.Message = "No minimum version."
		case !comparable:
			c.Status, c.Message = Skip, "Development build; version not compared."
		case order < 0:
			c.Status, c.Code, c.NextAction = Fail, "catalog_requires_upgrade", "Upgrade MCParcel, then run mcparcel runtime restart."
			c.Message = "This catalog needs MCParcel " + show(minimum) + " or newer; this is " + show(in.Version) + "."
		default:
			c.Message = "Needs " + show(minimum) + " or newer; this is " + show(in.Version) + "."
		}
		checks = append(checks, c)
	}
	return checks
}
