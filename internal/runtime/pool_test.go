package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dedene/mcparcel/internal/args"
	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/mcpclient"
	"github.com/dedene/mcparcel/internal/output"
	"github.com/dedene/mcparcel/internal/testutil"
)

func TestPoolStdioFixture(t *testing.T) {
	if os.Getenv("MCP_POOL_FIXTURE") != "1" {
		return
	}
	started := make(chan string, 1)
	go func() {
		for name := range started {
			if path := os.Getenv("MCP_POOL_STARTED"); path != "" {
				_ = os.WriteFile(path, []byte(name), 0o600)
			}
		}
	}()
	opts := testutil.FixtureOptions{Started: started, Release: make(chan struct{}), OnWrite: func() {
		f, _ := os.OpenFile(os.Getenv("MCP_POOL_MARKER"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if f != nil {
			_, _ = f.WriteString("write\n")
			_ = f.Close()
		}
	}, Crash: func() { os.Exit(0) }}
	e := testutil.NewFixtureServerWithOptions(opts).Run(context.Background(), &mcp.StdioTransport{})
	if e != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

type poolRig struct {
	t                              *testing.T
	paths                          config.Paths
	opts                           PoolOptions
	personal                       config.Personal
	local                          config.Local
	h                              Handler
	connects, calls, closed, loads atomic.Int32
	listStarted                    chan struct{}
	listRelease                    <-chan struct{}
	connectStarted                 chan struct{}
	connectRelease                 <-chan struct{}
	admissions                     chan struct{}
	captured                       chan mcpclient.ConnectOptions
}
type observedSession struct {
	mcpclient.Session
	r    *poolRig
	once sync.Once
}

func (s *observedSession) Tools(ctx context.Context) ([]json.RawMessage, error) {
	items, e := s.Session.Tools(ctx)
	if s.r.listStarted != nil {
		select {
		case s.r.listStarted <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		select {
		case <-s.r.listRelease:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return items, e
}

func (s *observedSession) Call(ctx context.Context, name string, a map[string]any, b func() error) (mcpclient.Result, error) {
	return s.Session.Call(ctx, name, a, func() error {
		if e := b(); e != nil {
			return e
		}
		s.r.calls.Add(1)
		return nil
	})
}

func (s *observedSession) Close(ctx context.Context) error {
	var e error
	s.once.Do(func() { s.r.closed.Add(1); e = s.Session.Close(ctx) })
	return e
}

func newRig(t *testing.T) *poolRig {
	t.Helper()
	p, env := testutil.IsolatedPaths(t)
	login := map[string]string{}
	for _, v := range env {
		k, val, _ := strings.Cut(v, "=")
		login[k] = val
	}
	r := &poolRig{t: t, paths: p, personal: config.Personal{SchemaVersion: 1, Connections: map[string]config.Connection{}, CredentialProfiles: map[string]config.ProfileRequirement{}}, local: config.Local{SchemaVersion: 1, CredentialProfiles: map[string]config.Profile{}}, admissions: make(chan struct{}, 100), captured: make(chan mcpclient.ConnectOptions, 100)}
	r.opts = PoolOptions{Paths: p, LoginEnv: login, Version: "test", Credentials: auth.NewResolver(auth.ResolverOptions{}), ShutdownTimeout: time.Second, Load: func(p config.Paths) (config.Snapshot, error) {
		r.loads.Add(1)
		select {
		case r.admissions <- struct{}{}:
		default:
		}
		return config.Load(p)
	}, Connect: func(ctx context.Context, o mcpclient.ConnectOptions) (mcpclient.Session, error) {
		r.connects.Add(1)
		r.captured <- o
		if r.connectStarted != nil {
			r.connectStarted <- struct{}{}
			select {
			case <-r.connectRelease:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		s, e := mcpclient.Connect(ctx, o)
		if e != nil {
			return nil, e
		}
		return &observedSession{Session: s, r: r}, nil
	}}
	t.Cleanup(func() {
		if r.h != nil {
			if e := r.h.Shutdown(testCtx(t), true); e != nil {
				t.Error(e)
			}
		}
	})
	return r
}

func (r *poolRig) save() {
	r.t.Helper()
	for _, v := range []struct {
		p string
		v any
	}{{r.paths.PersonalFile, r.personal}, {r.paths.ConfigFile, r.local}} {
		b, e := json.Marshal(v.v)
		if e != nil {
			r.t.Fatal(e)
		}
		f, e := os.CreateTemp(r.paths.ConfigDir, "edit-")
		if e != nil {
			r.t.Fatal(e)
		}
		_ = f.Chmod(0o600)
		_, e = f.Write(b)
		_ = f.Close()
		if e != nil {
			r.t.Fatal(e)
		}
		if e = os.Rename(f.Name(), v.p); e != nil {
			r.t.Fatal(e)
		}
	}
}
func (r *poolRig) start() { r.save(); r.h = NewPool(r.opts) }
func (r *poolRig) stdio(id string, secret bool) {
	exe, _ := os.Executable()
	c := config.Connection{Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal(exe), Args: []config.Value{config.Literal("-test.run=^TestPoolStdioFixture$")}, Env: map[string]config.Value{"MCP_POOL_FIXTURE": {Literal: ptrString("1")}}}}}
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "MCPARCEL_RUNTIME_DIR"} {
		v := r.opts.LoginEnv[key]
		c.Transport.Stdio.Env[key] = config.Value{Literal: &v}
	}
	if secret {
		r.protect(&c, id)
	}
	r.personal.Connections[id] = c
}

func (r *poolRig) http(id string, o testutil.FixtureOptions, secret bool) {
	server := testutil.NewFixtureServerWithOptions(o)
	hs := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	r.t.Cleanup(func() {
		if r.h != nil {
			if e := r.h.Shutdown(testCtx(r.t), true); e != nil {
				r.t.Error(e)
			}
		}
		hs.Close()
	})
	c := config.Connection{Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal(hs.URL), AllowInsecureHTTP: "loopback"}}}
	if secret {
		r.protect(&c, id)
	}
	r.personal.Connections[id] = c
}

func (r *poolRig) protect(c *config.Connection, id string) {
	c.CredentialProfile = "shared"
	r.personal.CredentialProfiles["shared"] = config.ProfileRequirement{}
	r.local.CredentialProfiles["shared"] = config.Profile{Mode: "desktop-service-account", Account: "fixture", BootstrapRef: "op://vault/bootstrap/token", SessionDuration: "24h"}
	v := config.Value{Secret: &config.SecretRef{Secret: "op://vault/" + id + "/value"}}
	if c.Transport.Stdio != nil {
		c.Transport.Stdio.Env["SECRET"] = v
	} else {
		c.Transport.HTTP.Headers = map[string]config.Value{"X-Secret": v}
	}
}

func (r *poolRig) call(ctx context.Context, id, tool string) Response {
	return r.h.Handle(ctx, testID, Request{Method: "call", Connection: id, Tool: tool, Arguments: emptyArgs()}, func() error { return nil })
}

func asyncCall(r *poolRig, ctx context.Context, id, tool string) <-chan Response {
	ch := make(chan Response, 1)
	go func() { ch <- r.call(ctx, id, tool) }()
	return ch
}

func response(t *testing.T, ch <-chan Response) Response {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("response timeout")
		return Response{}
	}
}

func success(t *testing.T, r Response) {
	t.Helper()
	if r.Error != nil {
		t.Fatalf("%s: %s", r.Error.Code, r.Error.Message)
	}
}

func responseCode(t *testing.T, r Response, code string, dispatched bool) {
	t.Helper()
	if r.Error == nil || r.Error.Code != code || r.Dispatched != dispatched {
		t.Fatalf("%+v want %s dispatched %v", r, code, dispatched)
	}
}

func count(t *testing.T, r Response) int {
	t.Helper()
	success(t, r)
	var d output.CallData
	_ = json.Unmarshal(r.Data, &d)
	var result struct{ StructuredContent struct{ Count int } }
	if e := json.Unmarshal(d.Result, &result); e != nil {
		t.Fatal(e)
	}
	return result.StructuredContent.Count
}

func awaitActive(t *testing.T, h Handler, n int) {
	t.Helper()
	ctx := testCtx(t)
	for h.Active() != n {
		select {
		case <-ctx.Done():
			t.Fatalf("active %d want %d", h.Active(), n)
		default:
			runtime.Gosched()
		}
	}
}

func TestPersistentServerState(t *testing.T) {
	r := newRig(t)
	r.stdio("a", false)
	r.start()
	for i := 1; i <= 2; i++ {
		if n := count(t, r.call(testCtx(t), "a", "counter")); n != i {
			t.Fatal(n)
		}
	}
	if r.connects.Load() != 1 {
		t.Fatal("reconnected")
	}
}

func TestPerConnectionSerialization(t *testing.T) {
	started := make(chan string, 1)
	release := make(chan struct{})
	r := newRig(t)
	r.http("a", testutil.FixtureOptions{Started: started, Release: release}, false)
	r.start()
	a := asyncCall(r, testCtx(t), "a", "wait")
	<-started
	b := asyncCall(r, testCtx(t), "local:a", "counter")
	awaitActive(t, r.h, 2)
	if r.calls.Load() != 1 {
		t.Fatal("overlapped")
	}
	close(release)
	success(t, response(t, a))
	if n := count(t, response(t, b)); n != 1 {
		t.Fatal(n)
	}
}

func TestDifferentConnectionsConcurrent(t *testing.T) {
	started := make(chan string, 1)
	release := make(chan struct{})
	r := newRig(t)
	r.http("a", testutil.FixtureOptions{Started: started, Release: release}, false)
	r.http("b", testutil.FixtureOptions{}, false)
	r.start()
	a := asyncCall(r, testCtx(t), "a", "wait")
	<-started
	if n := count(t, r.call(testCtx(t), "b", "counter")); n != 1 {
		t.Fatal(n)
	}
	close(release)
	success(t, response(t, a))
}

func TestCallerCancellationIsolation(t *testing.T) {
	started := make(chan string, 1)
	release := make(chan struct{})
	r := newRig(t)
	r.http("a", testutil.FixtureOptions{Started: started, Release: release}, false)
	r.http("b", testutil.FixtureOptions{}, false)
	r.start()
	a := asyncCall(r, testCtx(t), "a", "wait")
	<-started
	ctx, cancel := context.WithCancel(testCtx(t))
	queued := asyncCall(r, ctx, "a", "counter")
	awaitActive(t, r.h, 2)
	cancel()
	responseCode(t, response(t, queued), "canceled", false)
	count(t, r.call(testCtx(t), "b", "counter"))
	close(release)
	success(t, response(t, a))
	if r.calls.Load() != 2 {
		t.Fatal("queued dispatched")
	}
}

func TestDispatchedCancellationIsolation(t *testing.T) {
	started := make(chan string, 1)
	release := make(chan struct{})
	defer close(release)
	r := newRig(t)
	r.http("a", testutil.FixtureOptions{Started: started, Release: release}, false)
	r.http("b", testutil.FixtureOptions{}, false)
	r.start()
	ctx, cancel := context.WithCancel(testCtx(t))
	a := asyncCall(r, ctx, "a", "wait")
	<-started
	cancel()
	res := response(t, a)
	responseCode(t, res, "canceled", true)
	if res.Error.Details == nil || res.Error.Details.Outcome != "unknown" {
		t.Fatal("no uncertainty")
	}
	count(t, r.call(testCtx(t), "b", "counter"))
	if r.closed.Load() != 1 {
		t.Fatal("uncertain session not evicted")
	}
}

func TestSharedBootstrapDifferentConnections(t *testing.T) {
	r := newRig(t)
	r.http("a", testutil.FixtureOptions{}, true)
	r.http("b", testutil.FixtureOptions{}, true)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var boot atomic.Int32
	var mu sync.Mutex
	refs := map[string]int{}
	r.opts.Credentials = auth.NewResolver(auth.ResolverOptions{Provider: testutil.FakeProvider{BootstrapFunc: func(ctx context.Context, _ config.Profile) (auth.SecretClient, error) {
		boot.Add(1)
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return testutil.FakeSecretClient{ResolveFunc: func(_ context.Context, ref string) (string, error) {
			mu.Lock()
			refs[ref]++
			mu.Unlock()
			return ref + "-value", nil
		}}, nil
	}}})
	r.start()
	a := asyncCall(r, testCtx(t), "a", "counter")
	<-started
	b := asyncCall(r, testCtx(t), "b", "counter")
	awaitActive(t, r.h, 2)
	close(release)
	success(t, response(t, a))
	success(t, response(t, b))
	if boot.Load() != 1 {
		t.Fatal("two bootstraps")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(refs) != 2 {
		t.Fatal(refs)
	}
	for range 2 {
		o := <-r.captured
		if len(o.Headers) != 1 {
			t.Fatal(o.Headers)
		}
	}
}

func TestRawArgumentsCoercedAtDaemon(t *testing.T) {
	r := newRig(t)
	r.stdio("a", false)
	r.start()
	raw, e := args.Parse([]string{"limit=5", "enabled=true", "queries=[{\"a\":1}]"}, "", nil)
	if e != nil {
		t.Fatal(e)
	}
	req := Request{Method: "call", Connection: "a", Tool: "typed", Arguments: raw}
	res := r.h.Handle(testCtx(t), testID, req, func() error { return nil })
	success(t, res)
	var d output.CallData
	_ = json.Unmarshal(res.Data, &d)
	var result struct{ StructuredContent testutil.TypedInput }
	_ = json.Unmarshal(d.Result, &result)
	if result.StructuredContent.Limit != 5 || !result.StructuredContent.Enabled || len(result.StructuredContent.Queries) != 1 {
		t.Fatal(result)
	}
	raw.Values["limit"] = args.Value{Text: "007"}
	responseCode(t, r.h.Handle(testCtx(t), testID, req, nil), "invalid_arguments", false)
	if r.calls.Load() != 1 {
		t.Fatal("invalid dispatched")
	}
}

func TestMissingTool(t *testing.T) {
	r := newRig(t)
	r.http("a", testutil.FixtureOptions{}, false)
	r.start()
	responseCode(t, r.call(testCtx(t), "a", "missing"), "tool_not_found", false)
	if r.calls.Load() != 0 {
		t.Fatal("missing dispatched")
	}
}

func editAtList(t *testing.T, edit func(*poolRig), want string) {
	t.Helper()
	r := newRig(t)
	r.http("a", testutil.FixtureOptions{}, false)
	r.http("b", testutil.FixtureOptions{}, false)
	r.listStarted = make(chan struct{}, 1)
	release := make(chan struct{})
	r.listRelease = release
	r.start()
	ch := asyncCall(r, testCtx(t), "a", "counter")
	<-r.listStarted
	edit(r)
	r.save()
	close(release)
	res := response(t, ch)
	if want == "" {
		success(t, res)
	} else {
		responseCode(t, res, want, false)
		if r.calls.Load() != 0 {
			t.Fatal("changed dispatched")
		}
	}
}

func TestConfigChangeBeforeDispatch(t *testing.T) {
	editAtList(t, func(r *poolRig) {
		c := r.personal.Connections["a"]
		c.Label = "changed"
		r.personal.Connections["a"] = c
	}, "config_changed")
}

func TestRemovalBeforeDispatch(t *testing.T) {
	editAtList(t, func(r *poolRig) { delete(r.personal.Connections, "a") }, "connection_unavailable")
}

func TestUnrelatedConfigEdit(t *testing.T) {
	editAtList(t, func(r *poolRig) {
		c := r.personal.Connections["b"]
		c.Label = "changed"
		r.personal.Connections["b"] = c
	}, "")
}
