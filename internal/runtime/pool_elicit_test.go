package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/args"
	"github.com/dedene/mcparcel/internal/elicit"
	"github.com/dedene/mcparcel/internal/mcpclient"
	"github.com/dedene/mcparcel/internal/output"
)

func TestCallDeadline(t *testing.T) {
	d, cancel := withCallDeadline(context.Background(), 20*time.Millisecond)
	defer cancel()
	child, stop := context.WithCancelCause(d)
	defer stop(nil)
	<-child.Done()
	if d.Err() != context.DeadlineExceeded || child.Err() != context.DeadlineExceeded || context.Cause(child) != context.DeadlineExceeded {
		t.Fatal(d.Err(), child.Err(), context.Cause(child))
	}
	if _, ok := d.Deadline(); ok {
		t.Fatal("deadline reported")
	}

	d, cancel = withCallDeadline(context.Background(), 100*time.Millisecond)
	defer cancel()
	resume := d.pause()
	inner := d.pause()
	time.Sleep(200 * time.Millisecond)
	inner()
	if d.Err() != nil {
		t.Fatal("fired while paused")
	}
	resume()
	if d.Err() != nil {
		t.Fatal("fired right after resume")
	}
	select {
	case <-d.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("did not fire after resume")
	}
	if d.Err() != context.DeadlineExceeded {
		t.Fatal(d.Err())
	}

	parent, forced := context.WithCancelCause(context.Background())
	d, cancel = withCallDeadline(parent, time.Hour)
	defer cancel()
	child, stop = context.WithCancelCause(d)
	defer stop(nil)
	forced(errForced)
	<-child.Done()
	if d.Err() != context.Canceled || !errors.Is(context.Cause(d), errForced) || !errors.Is(context.Cause(child), errForced) {
		t.Fatal(d.Err(), context.Cause(d), context.Cause(child))
	}

	d, cancel = withCallDeadline(context.Background(), 20*time.Millisecond)
	cancel()
	time.Sleep(50 * time.Millisecond)
	if d.Err() != context.Canceled {
		t.Fatal(d.Err())
	}
}

type fakePrompter struct {
	seen   chan elicit.Prompt
	answer func(context.Context) elicit.Answer
}

func (f *fakePrompter) prompter(forms bool) *mcpclient.Prompter {
	return &mcpclient.Prompter{Forms: forms, Ask: func(ctx context.Context, p elicit.Prompt) elicit.Answer {
		f.seen <- p
		return f.answer(ctx)
	}}
}

func answer(a elicit.Answer) func(context.Context) elicit.Answer {
	return func(context.Context) elicit.Answer { return a }
}

func elicitArgs(values map[string]string) args.Raw {
	raw := args.Raw{Values: map[string]args.Value{}}
	for k, v := range values {
		raw.Values[k] = args.Value{Text: v}
	}
	return raw
}

func (r *poolRig) elicit(ctx context.Context, p *mcpclient.Prompter, timeout string, values map[string]string) (Response, output.CallData) {
	r.t.Helper()
	if p != nil {
		ctx = mcpclient.WithPrompter(ctx, p)
	}
	res := r.h.Handle(ctx, testID, Request{Method: "call", Connection: "a", Tool: "elicit", Timeout: timeout, Arguments: elicitArgs(values)}, func() error { return nil })
	var d output.CallData
	if res.Data != nil {
		if e := json.Unmarshal(res.Data, &d); e != nil {
			r.t.Fatal(e)
		}
	}
	return res, d
}

func drain(events chan string) []string {
	var out []string
	for len(events) > 0 {
		out = append(out, <-events)
	}
	return out
}

func TestPoolForwardsElicitation(t *testing.T) {
	r := newRig(t)
	events := make(chan string, 100)
	r.opts.Log = func(e string) { events <- e }
	r.stdio("a", false)
	r.start()
	f := &fakePrompter{seen: make(chan elicit.Prompt, 10), answer: answer(elicit.Answer{Action: "accept", Persist: "always"})}
	approval := map[string]string{"message": "Allow Computer Use to use Calculator?", "schema": "none", "persist": "session,always", "risk": "high", "subtitle": "Sub", "display": "click"}
	res, d := r.elicit(testCtx(t), f.prompter(false), "", approval)
	if res.Error != nil || len(d.Warnings) != 0 || !strings.Contains(string(d.Result), "action=accept persist=always") {
		t.Fatal(string(res.Data), res.Error)
	}
	want := elicit.Prompt{Message: "Allow Computer Use to use Calculator?", Subtitle: "Sub", RiskLevel: "high", Details: "click", Persist: []string{"session", "always"}}
	if p := <-f.seen; !reflect.DeepEqual(p, want) {
		t.Fatalf("%#v", p)
	}
	if got := drain(events); !reflect.DeepEqual(got[len(got)-2:], []string{"elicitation_forwarded", "elicitation_accepted"}) {
		t.Fatal(got)
	}
	for action, want := range map[string][2]string{
		"decline": {"You declined the server's request: Allow Computer Use to use Calculator?", "elicitation_declined"},
		"cancel":  {"The server's request was canceled without an answer: Allow Computer Use to use Calculator?", "elicitation_canceled"},
	} {
		f.answer = answer(elicit.Answer{Action: action})
		res, d = r.elicit(testCtx(t), f.prompter(false), "", approval)
		<-f.seen
		if res.Error != nil || len(d.Warnings) != 1 || d.Warnings[0].Code != "elicitation_declined" || d.Warnings[0].Message != want[0] || !strings.Contains(string(d.Result), "action="+action) {
			t.Fatal(string(res.Data), res.Error)
		}
		if got := drain(events); !reflect.DeepEqual(got, []string{"elicitation_forwarded", want[1]}) {
			t.Fatal(got)
		}
	}
	unsupported := "The server asked for input MCParcel cannot show, so MCParcel declined it: Allow?"
	for _, c := range []struct {
		forms  bool
		schema string
	}{{false, ""}, {true, "nested"}} {
		res, d = r.elicit(testCtx(t), f.prompter(c.forms), "", map[string]string{"message": "Allow?", "schema": c.schema})
		if res.Error != nil || len(d.Warnings) != 1 || d.Warnings[0].Message != unsupported || len(f.seen) != 0 {
			t.Fatal(string(res.Data), res.Error)
		}
		if got := drain(events); !reflect.DeepEqual(got, []string{"elicitation_declined"}) {
			t.Fatal(got)
		}
	}
	f.answer = answer(elicit.Answer{Action: "accept", Content: map[string]any{"allow": true}})
	res, d = r.elicit(testCtx(t), f.prompter(true), "", map[string]string{"message": "Allow?"})
	if <-f.seen; res.Error != nil || len(d.Warnings) != 0 || !strings.Contains(string(d.Result), `content={\"allow\":true}`) {
		t.Fatal(string(res.Data), res.Error)
	}
}

func TestPoolElicitationPausesDeadline(t *testing.T) {
	r := newRig(t)
	r.stdio("a", false)
	r.start()
	success(t, r.call(testCtx(t), "a", "counter"))
	f := &fakePrompter{seen: make(chan elicit.Prompt, 1), answer: func(ctx context.Context) elicit.Answer {
		select {
		case <-time.After(1500 * time.Millisecond):
			return elicit.Answer{Action: "accept"}
		case <-ctx.Done():
			return elicit.Answer{Action: "cancel"}
		}
	}}
	res, d := r.elicit(testCtx(t), f.prompter(false), "500ms", map[string]string{"message": "Allow?", "schema": "none"})
	if res.Error != nil || !strings.Contains(string(d.Result), "action=accept") {
		t.Fatal(string(res.Data), res.Error)
	}
}

func TestPoolElicitationDoesNotBlockOthers(t *testing.T) {
	r := newRig(t)
	r.stdio("a", false)
	r.stdio("b", false)
	r.start()
	release := make(chan struct{})
	f := &fakePrompter{seen: make(chan elicit.Prompt, 1), answer: func(ctx context.Context) elicit.Answer {
		select {
		case <-release:
			return elicit.Answer{Action: "accept"}
		case <-ctx.Done():
			return elicit.Answer{Action: "cancel"}
		}
	}}
	done := make(chan Response, 1)
	go func() {
		res, _ := r.elicit(testCtx(t), f.prompter(false), "", map[string]string{"message": "Allow?", "schema": "none"})
		done <- res
	}()
	select {
	case <-f.seen:
	case <-time.After(10 * time.Second):
		t.Fatal("prompt not forwarded")
	}
	if n := count(t, r.call(testCtx(t), "b", "counter")); n != 1 {
		t.Fatal(n)
	}
	close(release)
	if res := response(t, done); res.Error != nil || !strings.Contains(string(res.Data), "action=accept") {
		t.Fatal(string(res.Data), res.Error)
	}
}
