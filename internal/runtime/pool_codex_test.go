package runtime

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/args"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

func elicitRequest(then string) Request {
	return Request{Method: "call", Connection: "a", Tool: "elicit", Arguments: args.Raw{Values: map[string]args.Value{"message": {Text: "Allow Computer Use to use Calculator?"}, "then": {Text: then}}}}
}

func TestPoolElicitationDeclinedNotice(t *testing.T) {
	r := newRig(t)
	events := make(chan string, 100)
	r.opts.Log = func(e string) { events <- e }
	r.stdio("a", false)
	r.personal.Connections["a"].Transport.Stdio.Env["MCP_POOL_LEGACY"] = config.Literal("1")
	r.start()
	notice := "The server asked for approval and MCParcel declined it: Allow Computer Use to use Calculator?"
	for _, then := range []string{"", "error"} {
		res := r.h.Handle(testCtx(t), testID, elicitRequest(then), func() error { return nil })
		var d output.CallData
		if e := json.Unmarshal(res.Data, &d); e != nil || len(d.Warnings) != 1 {
			t.Fatal(string(res.Data), e)
		}
		w := d.Warnings[0]
		if w.Code != "elicitation_declined" || w.Message != notice || w.NextAction != "Approve this in the server's own app, then retry." || !strings.Contains(string(d.Result), "action=decline") {
			t.Fatal(string(res.Data))
		}
		if then == "error" {
			responseCode(t, res, "tool_error", true)
		} else if res.Error != nil {
			t.Fatal(res.Error)
		}
	}
	res := r.h.Handle(testCtx(t), testID, elicitRequest("rpc"), func() error { return nil })
	responseCode(t, res, "elicitation_declined", true)
	if res.Error.Message != notice || res.Data != nil || res.Error.Details == nil || res.Error.Details.Outcome != "" {
		t.Fatalf("%+v", res.Error)
	}
	res = r.call(testCtx(t), "a", "counter")
	if res.Error != nil || strings.Contains(string(res.Data), "warnings") || r.connects.Load() != 1 {
		t.Fatal(string(res.Data), res.Error, r.connects.Load())
	}
	declined := 0
	for len(events) > 0 {
		if <-events == "elicitation_declined" {
			declined++
		}
	}
	if declined != 3 {
		t.Fatal("logged", declined)
	}
}

func TestPoolForwardsCallMeta(t *testing.T) {
	r := newRig(t)
	r.stdio("a", false)
	r.start()
	res := r.h.Handle(testCtx(t), testID, Request{Method: "call", Connection: "a", Tool: "meta", Arguments: emptyArgs(), Meta: json.RawMessage(`{"x-codex-turn-metadata":{"turn_id":"t1","n":5}}`)}, func() error { return nil })
	if res.Error != nil || !strings.Contains(string(res.Data), `"x-codex-turn-metadata":{"n":5,"turn_id":"t1"}`) {
		t.Fatal(string(res.Data), res.Error)
	}
}

func TestPoolStartupTimeoutPerConnection(t *testing.T) {
	r := newRig(t)
	r.stdio("a", false)
	r.stdio("b", false)
	b := r.personal.Connections["b"]
	b.StartupTimeout = "120s"
	r.personal.Connections["b"] = b
	r.start()
	for id, want := range map[string]time.Duration{"a": 30 * time.Second, "b": 120 * time.Second} {
		if res := r.call(testCtx(t), id, "counter"); res.Error != nil {
			t.Fatal(res.Error)
		}
		if o := <-r.captured; o.ConnectTimeout != want {
			t.Fatal(id, o.ConnectTimeout)
		}
	}
}
