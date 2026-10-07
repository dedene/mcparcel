package doctor

import (
	"context"
	"errors"
	"fmt"

	"github.com/dedene/mcparcel/internal/output"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
)

// Live is live.tools for in.Target. Its gates keep it from waiting on an
// unusable runtime, from contacting the runtime for a connection that cannot
// be called, and from starting an unsupervised headless runtime with this
// shell's environment. tools connects and lists the target's tools.
func Live(ctx context.Context, in Input, tools func(context.Context) (output.ToolList, error)) output.DoctorCheck {
	c := output.DoctorCheck{ID: "live.tools", Subject: show(in.Target), Status: Skip}
	if in.Snapshot == nil || in.Target == "" {
		c.Message = "Configuration unreadable; see config.file."
		return c
	}
	if runtimeBlocked(in) {
		c.Message = "The runtime is not usable; see runtime.version."
		return c
	}
	for _, row := range subjects(in) {
		if row.ID != in.Target {
			continue
		}
		if cc := connectionCheck(in, row); cc.Status != OK {
			code := cc.Code
			if code == "" {
				code = "connection_disabled"
			}
			c.Status, c.Code, c.NextAction = Fail, code, cc.NextAction
			if c.NextAction == "" {
				c.NextAction = nextAction(code)
			}
			c.Message = "The connection cannot be called; see config.connection."
			return c
		}
	}
	if !MayStart(in) && (in.Probe.State == runtimeclient.ProbeStopped || in.Probe.State == runtimeclient.ProbeStaleSocket) {
		return notStarted(c)
	}
	list, err := tools(ctx)
	if errors.Is(err, runtimeclient.ErrNotRunning) {
		// The runtime exited after the probe; the client did not start one.
		return notStarted(c)
	}
	if err != nil {
		var oe *output.Error
		if !errors.As(err, &oe) || oe == nil {
			oe = output.NewError("internal_error", nil)
		}
		c.Status, c.Code, c.Message, c.NextAction = Fail, oe.Code, oe.Message, oe.NextAction
		return c
	}
	c.Status = OK
	c.Message = fmt.Sprintf("Connected and listed %d %s.", len(list.Items), plural(len(list.Items), "tool", "tools"))
	return c
}

// MayStart reports whether live.tools may start the runtime. An unsupervised
// headless runtime started from here would run with this shell's environment,
// not the wrapper's, so cmd's tools callback must not start one (NoStart).
func MayStart(in Input) bool {
	return !headless(in) || in.Supervised
}

func notStarted(c output.DoctorCheck) output.DoctorCheck {
	c.Status = Fail
	c.Message = "doctor --live does not start a runtime in headless mode: it would run with this shell's environment, not the wrapper's."
	c.NextAction = "Start the runtime through claw-wrap (any allowed call), then run doctor --live again."
	return c
}

// runtimeBlocked reports whether runtime.version rules out a live check: the
// mode is unknown, the row failed, or the runtime is starting. Desktop mode
// where it is unsupported is left to the runtime client (runtime_unsupported).
func runtimeBlocked(in Input) bool {
	if in.RuntimeErr != nil {
		return true
	}
	if !runtimeUsable(in) {
		return false
	}
	return versionCheck(in).Status == Fail || in.Probe.State == runtimeclient.ProbeStarting
}
