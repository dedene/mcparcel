package doctor

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
)

func listing(n int) func(context.Context) (output.ToolList, error) {
	return func(context.Context) (output.ToolList, error) {
		return output.ToolList{Items: make([]json.RawMessage, n)}, nil
	}
}

func refuse(t *testing.T) func(context.Context) (output.ToolList, error) {
	return func(context.Context) (output.ToolList, error) {
		t.Fatal("the runtime was contacted")
		return output.ToolList{}, nil
	}
}

func TestLiveOK(t *testing.T) {
	in := withTarget(inputFor(t, docs{personal: stdioPaper, selections: enabledPaper}), "local:paper")
	c := Live(context.Background(), in, listing(3))
	if c.ID != "live.tools" || c.Subject != "local:paper" || c.Status != OK || c.Message != "Connected and listed 3 tools." {
		t.Fatal(c)
	}
	if c = Live(context.Background(), in, listing(1)); c.Message != "Connected and listed 1 tool." {
		t.Fatal(c.Message)
	}
}

func TestLiveErrorKeepsCode(t *testing.T) {
	in := withTarget(inputFor(t, docs{personal: stdioPaper, selections: enabledPaper}), "local:paper")
	e := output.NewError("connection_failed", nil)
	c := Live(context.Background(), in, func(context.Context) (output.ToolList, error) { return output.ToolList{}, e })
	want(t, c, Fail, "connection_failed")
	if c.Message != e.Message || c.NextAction != e.NextAction {
		t.Fatal(c)
	}
	c = Live(context.Background(), in, func(context.Context) (output.ToolList, error) { return output.ToolList{}, context.Canceled })
	want(t, c, Fail, "internal_error")
}

func TestLiveGates(t *testing.T) {
	ctx := context.Background()
	base := func() Input {
		return withTarget(inputFor(t, docs{personal: stdioPaper, selections: enabledPaper}), "local:paper")
	}
	// Runtime unusable: skip, no wait.
	for _, p := range []runtimeclient.Probe{{State: runtimeclient.ProbeStarting}, {State: runtimeclient.ProbeVersionMismatch, DaemonVersion: "0.9"}, {State: runtimeclient.ProbeUnreadable}} {
		in := base()
		in.Probe = p
		want(t, Live(ctx, in, refuse(t)), Skip, "")
	}
	in := base()
	in.RuntimeErr = config.ErrUnsafePath
	want(t, Live(ctx, in, refuse(t)), Skip, "")
	in = base()
	in.Snapshot = nil
	if c := Live(ctx, in, refuse(t)); c.Status != Skip || c.Message != "Configuration unreadable; see config.file." {
		t.Fatal(c)
	}
	// Not callable offline: fail with that code, runtime not contacted.
	in = withTarget(inputFor(t, docs{personal: stdioPaper, selections: `{"schemaVersion":1,"revision":1,"connections":{"local:paper":{"enabled":true,"reviewRequired":true}}}`}), "local:paper")
	want(t, Live(ctx, in, refuse(t)), Fail, "review_required")
	in = withTarget(inputFor(t, docs{personal: stdioPaper, selections: `{"schemaVersion":1,"revision":1,"connections":{}}`}), "local:paper")
	if c := Live(ctx, in, refuse(t)); c.Status != Fail || c.Code != "connection_disabled" || c.NextAction == "" {
		t.Fatal(c)
	}
	// Headless, unsupervised, nothing running: never starts a runtime.
	in = headlessInput(base())
	for _, state := range []string{runtimeclient.ProbeStopped, runtimeclient.ProbeStaleSocket} {
		in.Probe = runtimeclient.Probe{State: state}
		c := Live(ctx, in, refuse(t))
		want(t, c, Fail, "")
		if c.NextAction != "Start the runtime through claw-wrap (any allowed call), then run doctor --live again." {
			t.Fatal(c)
		}
	}
	// Headless supervised, or a running headless runtime: tools is called.
	in.Supervised = true
	if !MayStart(in) {
		t.Fatal("a supervised runtime may be waited for")
	}
	want(t, Live(ctx, in, listing(2)), OK, "")
	in.Supervised = false
	in.Probe = runtimeclient.Probe{State: runtimeclient.ProbeRunning, DaemonVersion: "1.2.3", Status: &runtimeclient.Status{}}
	want(t, Live(ctx, in, listing(2)), OK, "")
	// The runtime exited after the probe: the client must not start one, and
	// its refusal is the same row.
	if MayStart(in) {
		t.Fatal("an unsupervised headless runtime may be started")
	}
	c := Live(ctx, in, func(context.Context) (output.ToolList, error) { return output.ToolList{}, runtimeclient.ErrNotRunning })
	want(t, c, Fail, "")
	if c.NextAction != "Start the runtime through claw-wrap (any allowed call), then run doctor --live again." {
		t.Fatal(c)
	}
	// Desktop where desktop does not run: the client refuses it.
	in = base()
	in.DesktopSupported = false
	if !MayStart(in) {
		t.Fatal("desktop may start its runtime")
	}
	called := false
	c = Live(ctx, in, func(context.Context) (output.ToolList, error) {
		called = true
		return output.ToolList{}, output.HeadlessOnlyError()
	})
	if !called || c.Code != "runtime_unsupported" {
		t.Fatal(c)
	}
}
