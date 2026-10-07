package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/args"
	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
	"github.com/dedene/mcparcel/internal/testutil"
)

func TestLeaseRotation(t *testing.T) {
	r := newRig(t)
	r.stdio("a", true)
	base := time.Now()
	var offset atomic.Int64
	now := func() time.Time { return base.Add(time.Duration(offset.Load())) }
	var value atomic.Int32
	r.opts.Now = now
	r.opts.Credentials = auth.NewResolver(auth.ResolverOptions{Now: now, Provider: testutil.FakeProvider{BootstrapFunc: func(context.Context, config.Profile) (auth.SecretClient, error) {
		return testutil.FakeSecretClient{ResolveFunc: func(context.Context, string) (string, error) {
			if value.Load() == 0 {
				return "old", nil
			}
			return "new", nil
		}}, nil
	}}})
	r.start()
	count(t, r.call(testCtx(t), "a", "counter"))
	count(t, r.call(testCtx(t), "a", "counter"))
	value.Store(1)
	offset.Store(int64(5 * time.Minute))
	if n := count(t, r.call(testCtx(t), "a", "counter")); n != 1 {
		t.Fatal("old state reused")
	}
	if r.connects.Load() != 2 || r.closed.Load() != 1 {
		t.Fatal("did not replace")
	}
	first, second := <-r.captured, <-r.captured
	if first.Env["SECRET"] != "old" || second.Env["SECRET"] != "new" {
		t.Fatal("bad rotation")
	}
}

func TestProtectedExpiry(t *testing.T) {
	started := make(chan string, 1)
	release := make(chan struct{})
	defer close(release)
	r := newRig(t)
	r.http("a", testutil.FixtureOptions{Started: started, Release: release}, true)
	r.http("b", testutil.FixtureOptions{}, false)
	profile := r.local.CredentialProfiles["shared"]
	profile.SessionDuration = "24h"
	r.local.CredentialProfiles["shared"] = profile
	base := time.Now()
	var offset atomic.Int64
	now := func() time.Time { return base.Add(time.Duration(offset.Load())) }
	r.opts.Now = now
	r.opts.ExpiryCheck = 10 * time.Millisecond
	r.opts.Credentials = auth.NewResolver(auth.ResolverOptions{Now: now, Provider: testutil.FakeProvider{BootstrapFunc: func(context.Context, config.Profile) (auth.SecretClient, error) {
		return testutil.FakeSecretClient{ResolveFunc: func(context.Context, string) (string, error) { return "value", nil }}, nil
	}}})
	r.start()
	a := asyncCall(r, testCtx(t), "a", "wait")
	select {
	case <-started:
	case res := <-a:
		t.Fatalf("expired before dispatch barrier: %+v", res)
	case <-time.After(time.Second):
		t.Fatal("no dispatch or response")
	}
	offset.Store(int64(24 * time.Hour))
	res := response(t, a)
	responseCode(t, res, "outcome_unknown", true)
	if r.closed.Load() != 1 {
		t.Fatal("expired session not closed")
	}
	count(t, r.call(testCtx(t), "b", "counter"))
	res = r.h.Handle(testCtx(t), testID, Request{Method: "call", Connection: "a", Tool: "counter", NoInput: true, Arguments: emptyArgs()}, nil)
	responseCode(t, res, "auth_expired", false)
}

func TestNoTokenInChildEnvironment(t *testing.T) {
	r := newRig(t)
	r.stdio("a", true)
	r.stdio("b", false)
	r.opts.LoginEnv["OP_SERVICE_ACCOUNT_TOKEN"] = "bootstrap-sentinel"
	r.opts.Credentials = auth.NewResolver(auth.ResolverOptions{Provider: testutil.FakeProvider{BootstrapFunc: func(context.Context, config.Profile) (auth.SecretClient, error) {
		return testutil.FakeSecretClient{ResolveFunc: func(context.Context, string) (string, error) { return "a-secret", nil }}, nil
	}}})
	r.start()
	for _, id := range []string{"a", "b"} {
		for _, name := range []string{"OP_SERVICE_ACCOUNT_TOKEN", "SECRET"} {
			req := Request{Method: "call", Connection: id, Tool: "env", Arguments: args.Raw{Values: map[string]args.Value{"name": {Text: name}}}}
			res := r.h.Handle(testCtx(t), testID, req, nil)
			success(t, res)
			var d output.CallData
			_ = json.Unmarshal(res.Data, &d)
			var v struct{ StructuredContent testutil.EnvOutput }
			_ = json.Unmarshal(d.Result, &v)
			want := ""
			if id == "a" && name == "SECRET" {
				want = "a-secret"
			}
			if v.StructuredContent.Value != want {
				t.Fatalf("%s %s %q", id, name, v.StructuredContent.Value)
			}
		}
	}
}

func TestNoCallReplay(t *testing.T) {
	r := newRig(t)
	r.stdio("a", false)
	marker := filepath.Join(r.paths.Home, "writes")
	c := r.personal.Connections["a"]
	c.Transport.Stdio.Env["MCP_POOL_MARKER"] = config.Value{Literal: &marker}
	r.personal.Connections["a"] = c
	r.start()
	res := r.call(testCtx(t), "a", "write_drop")
	responseCode(t, res, "outcome_unknown", true)
	if res.Error.Details == nil || res.Error.Details.RequestID != testID {
		t.Fatal("missing request id")
	}
	b, e := os.ReadFile(marker)
	if e != nil || string(b) != "write\n" {
		t.Fatalf("%q %v", b, e)
	}
	count(t, r.call(testCtx(t), "a", "counter"))
	b, _ = os.ReadFile(marker)
	if string(b) != "write\n" || r.calls.Load() != 2 {
		t.Fatal("replayed")
	}
}

func TestTimeoutBeforeAndAfterDispatch(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	defer close(release)
	r := newRig(t)
	r.http("a", testutil.FixtureOptions{Started: started, Release: release}, false)
	r.start()
	ctx, cancel := context.WithCancel(testCtx(t))
	first := asyncCall(r, ctx, "a", "wait")
	<-started
	short, stop := context.WithTimeout(testCtx(t), 30*time.Millisecond)
	defer stop()
	queued := asyncCall(r, short, "a", "counter")
	responseCode(t, response(t, queued), "timeout", false)
	cancel()
	responseCode(t, response(t, first), "canceled", true)
	req := Request{Method: "call", Connection: "a", Tool: "wait", Timeout: "50ms", Arguments: emptyArgs()}
	responseCode(t, r.h.Handle(testCtx(t), testID, req, nil), "outcome_unknown", true)
}

func TestForceShutdown(t *testing.T) {
	r := newRig(t)
	markers := map[string]string{}
	for _, id := range []string{"a", "b"} {
		r.stdio(id, false)
		marker := filepath.Join(r.paths.Home, "started-"+id)
		markers[id] = marker
		c := r.personal.Connections[id]
		c.Transport.Stdio.Env["MCP_POOL_STARTED"] = config.Value{Literal: &marker}
		r.personal.Connections[id] = c
	}
	r.start()
	waitStarted := func(id string) {
		ctx := testCtx(t)
		for {
			if b, e := os.ReadFile(markers[id]); e == nil && string(b) == "wait" {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal("fixture did not start")
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	a := asyncCall(r, testCtx(t), "a", "wait")
	waitStarted("a")
	queued := asyncCall(r, testCtx(t), "a", "counter")
	b := asyncCall(r, testCtx(t), "b", "wait")
	waitStarted("b")
	awaitActive(t, r.h, 3)
	if e := r.h.Shutdown(testCtx(t), false); e == nil {
		t.Fatal("ordinary shutdown admitted")
	}
	if e := r.h.Shutdown(testCtx(t), true); e != nil {
		t.Fatal(e)
	}
	responseCode(t, response(t, a), "outcome_unknown", true)
	responseCode(t, response(t, b), "outcome_unknown", true)
	responseCode(t, response(t, queued), "canceled", false)
	if r.closed.Load() != 2 {
		t.Fatal("not closed exactly once")
	}
	before := r.connects.Load()
	responseCode(t, r.call(testCtx(t), "a", "counter"), "connection_failed", false)
	if r.connects.Load() != before {
		t.Fatal("new work after shutdown")
	}
}

func TestCachedToolsNoEffects(t *testing.T) {
	r := newRig(t)
	r.start()
	res := r.h.Handle(testCtx(t), testID, Request{Method: "tools", Cached: true, Connection: "missing", Arguments: emptyArgs()}, nil)
	responseCode(t, res, "schema_cache_miss", false)
	if r.loads.Load() != 0 || r.connects.Load() != 0 {
		t.Fatal("cached side effects")
	}
}

func TestPoolResultEnvelopes(t *testing.T) {
	r := newRig(t)
	r.stdio("a", false)
	r.start()
	failed := r.call(testCtx(t), "a", "fail")
	responseCode(t, failed, "tool_error", true)
	if failed.Data == nil || failed.Error.Details == nil || failed.Error.Details.RequestID != testID {
		t.Fatal("lost tool error data")
	}
	rich := r.call(testCtx(t), "a", "rich")
	success(t, rich)
	var d output.CallData
	_ = json.Unmarshal(rich.Data, &d)
	var result map[string]json.RawMessage
	_ = json.Unmarshal(d.Result, &result)
	if len(result["structuredContent"]) == 0 || len(result["_meta"]) == 0 {
		t.Fatal("lost rich result")
	}
	if r.connects.Load() != 1 {
		t.Fatal("replaced certain-result session")
	}
}

func TestSingleInitialization(t *testing.T) {
	r := newRig(t)
	r.stdio("a", false)
	r.connectStarted = make(chan struct{}, 1)
	release := make(chan struct{})
	r.connectRelease = release
	r.start()
	a := asyncCall(r, testCtx(t), "a", "counter")
	<-r.connectStarted
	b := make(chan Response, 1)
	go func() {
		b <- r.h.Handle(testCtx(t), testID, Request{Method: "tools", Connection: "a", Arguments: emptyArgs()}, nil)
	}()
	awaitActive(t, r.h, 2)
	close(release)
	success(t, response(t, a))
	success(t, response(t, b))
	if r.connects.Load() != 1 {
		t.Fatal("double init")
	}
}
