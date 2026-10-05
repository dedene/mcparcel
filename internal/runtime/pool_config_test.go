package runtime

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/catalog"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

func saveSelections(t *testing.T, r *poolRig, rows map[string]config.Selection) {
	t.Helper()
	b, e := json.Marshal(config.Selections{SchemaVersion: 1, Revision: 1, Connections: rows})
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(r.paths.SelectionsFile, b, 0o600); e != nil {
		t.Fatal(e)
	}
}

func TestResolvedRuntimeInput(t *testing.T) {
	r := newRig(t)
	r.stdio("a", false)
	c := r.personal.Connections["a"]
	exe := *c.Transport.Stdio.Command.Literal
	c.Transport.Stdio.Command = config.Value{Input: &config.InputRef{Input: "executable"}}
	c.Inputs = map[string]config.Input{"executable": {Kind: "path", Description: "Executable"}}
	r.personal.Connections["a"] = c
	r.http("b", testutil.FixtureOptions{}, false)
	c = r.personal.Connections["b"]
	url := *c.Transport.HTTP.URL.Literal
	c.Transport.HTTP.URL = config.Value{Input: &config.InputRef{Input: "endpoint"}}
	c.Inputs = map[string]config.Input{"endpoint": {Kind: "url", Description: "Endpoint"}}
	r.personal.Connections["b"] = c
	saveSelections(t, r, map[string]config.Selection{"local:a": {Enabled: true, Inputs: map[string]string{"executable": exe}}, "local:b": {Enabled: true, Inputs: map[string]string{"endpoint": url}}})
	r.start()
	count(t, r.call(testCtx(t), "a", "counter"))
	count(t, r.call(testCtx(t), "b", "counter"))
}

func TestRuntimeUnsupportedBeforeEffects(t *testing.T) {
	for _, kind := range []string{"sse", "desktop", "lifecycle"} {
		t.Run(kind, func(t *testing.T) {
			r := newRig(t)
			r.http("a", testutil.FixtureOptions{}, kind == "desktop")
			r.http("public", testutil.FixtureOptions{}, false)
			c := r.personal.Connections["a"]
			switch kind {
			case "sse":
				c.Transport.HTTP.Mode = "sse"
			case "desktop":
				r.local.CredentialProfiles["shared"] = config.Profile{Mode: "desktop", Account: "fixture"}
			case "lifecycle":
				c.Lifecycle = &config.Lifecycle{IdleTimeout: "1m"}
			}
			r.personal.Connections["a"] = c
			var boot atomic.Int32
			r.opts.Credentials = auth.NewResolver(auth.ResolverOptions{Provider: testutil.FakeProvider{BootstrapFunc: func(context.Context, config.Profile) (auth.SecretClient, error) {
				boot.Add(1)
				return nil, auth.ErrProvider
			}}})
			r.start()
			responseCode(t, r.call(testCtx(t), "a", "counter"), "runtime_unsupported", false)
			if boot.Load() != 0 || r.connects.Load() != 0 {
				t.Fatal("effects")
			}
			count(t, r.call(testCtx(t), "public", "counter"))
		})
	}
}

func TestPolicyBeforeAuthAndDispatch(t *testing.T) {
	t.Run("initial denial", func(t *testing.T) {
		r := newRig(t)
		r.http("a", testutil.FixtureOptions{}, true)
		c := r.personal.Connections["a"]
		c.ToolPolicy = &config.ToolPolicy{Deny: []string{"counter"}}
		r.personal.Connections["a"] = c
		var boot atomic.Int32
		r.opts.Credentials = auth.NewResolver(auth.ResolverOptions{Provider: testutil.FakeProvider{BootstrapFunc: func(context.Context, config.Profile) (auth.SecretClient, error) {
			boot.Add(1)
			return nil, auth.ErrProvider
		}}})
		r.start()
		responseCode(t, r.call(testCtx(t), "a", "counter"), "tool_denied", false)
		if boot.Load() != 0 || r.connects.Load() != 0 {
			t.Fatal("effects")
		}
	})
	t.Run("discovery barrier", func(t *testing.T) {
		r := newRig(t)
		r.http("a", testutil.FixtureOptions{}, false)
		r.listStarted = make(chan struct{}, 1)
		release := make(chan struct{})
		r.listRelease = release
		r.start()
		ch := asyncCall(r, testCtx(t), "a", "counter")
		<-r.listStarted
		saveSelections(t, r, map[string]config.Selection{"local:a": {Enabled: true, DisabledTools: []string{"counter"}}})
		close(release)
		responseCode(t, response(t, ch), "tool_denied", false)
		if r.calls.Load() != 0 {
			t.Fatal("dispatched")
		}
	})
	t.Run("queued disable", func(t *testing.T) {
		started := make(chan string, 1)
		release := make(chan struct{})
		r := newRig(t)
		r.http("a", testutil.FixtureOptions{Started: started, Release: release}, false)
		var loads atomic.Int32
		queuedLoaded := make(chan struct{})
		r.opts.Load = func(paths config.Paths) (config.Snapshot, error) {
			snapshot, err := config.Load(paths)
			if loads.Add(1) == 4 {
				close(queuedLoaded)
			}
			return snapshot, err
		}
		r.start()
		first := asyncCall(r, testCtx(t), "a", "wait")
		<-started
		queued := asyncCall(r, testCtx(t), "a", "counter")
		select {
		case <-queuedLoaded:
		case <-testCtx(t).Done():
			t.Fatal("queued admission did not load")
		}
		saveSelections(t, r, map[string]config.Selection{"local:a": {Enabled: false}})
		close(release)
		success(t, response(t, first))
		responseCode(t, response(t, queued), "connection_disabled", false)
		if r.calls.Load() != 1 {
			t.Fatal("queued dispatched")
		}
	})
}

func TestReviewBlocksRuntime(t *testing.T) {
	r := newRig(t)
	r.http("a", testutil.FixtureOptions{}, true)
	saveSelections(t, r, map[string]config.Selection{"local:a": {Enabled: true, ReviewRequired: true, CredentialProfile: "shared"}})
	var boot atomic.Int32
	r.opts.Credentials = auth.NewResolver(auth.ResolverOptions{Provider: testutil.FakeProvider{BootstrapFunc: func(context.Context, config.Profile) (auth.SecretClient, error) {
		boot.Add(1)
		return nil, auth.ErrProvider
	}}})
	r.start()
	responseCode(t, r.call(testCtx(t), "a", "counter"), "review_required", false)
	if r.connects.Load() != 0 || r.calls.Load() != 0 || boot.Load() != 0 {
		t.Fatal("effects")
	}
}

func TestToolsPolicyRecheck(t *testing.T) {
	r := newRig(t)
	r.http("a", testutil.FixtureOptions{}, false)
	r.listStarted = make(chan struct{}, 1)
	release := make(chan struct{})
	r.listRelease = release
	r.start()
	ch := make(chan Response, 1)
	go func() { ch <- r.h.Handle(testCtx(t), testID, Request{Method: "tools", Connection: "a"}, nil) }()
	<-r.listStarted
	empty := []string{}
	c := r.personal.Connections["a"]
	c.ToolPolicy = &config.ToolPolicy{Allow: &empty}
	r.personal.Connections["a"] = c
	r.save()
	close(release)
	res := response(t, ch)
	success(t, res)
	var data struct {
		Items []json.RawMessage `json:"items"`
	}
	if e := json.Unmarshal(res.Data, &data); e != nil || data.Items == nil || len(data.Items) != 0 {
		t.Fatal(string(res.Data), e)
	}
	responseCode(t, r.call(testCtx(t), "a", "counter"), "tool_denied", false)
}

func TestAmbiguousPoolError(t *testing.T) {
	e := poolError(&config.AmbiguousIDError{Candidates: []string{"github:example/tools#paper", "local:paper"}}, nil, testID, false)
	if e.Code != "ambiguous_id" || e.Details == nil || len(e.Details.Candidates) != 2 {
		t.Fatal(e)
	}
}

func TestImportPreservesLivePool(t *testing.T) {
	r := newRig(t)
	r.stdio("old", false)
	r.start()
	first := count(t, r.call(testCtx(t), "old", "counter"))
	if first != 1 {
		t.Fatal(first)
	}
	state, e := config.ReadState(testCtx(t), r.paths)
	if e != nil {
		t.Fatal(e)
	}
	old := state.Personal.Connections["old"]
	raw, e := json.Marshal(map[string]any{"mcpServers": map[string]any{"new": map[string]any{"command": *old.Transport.Stdio.Command.Literal, "args": []string{"-test.run=^TestPoolStdioFixture$"}, "env": map[string]string{"MCP_POOL_FIXTURE": "1"}}}})
	if e != nil {
		t.Fatal(e)
	}
	report, e := config.ImportMcporter(raw, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = config.ApplyImport(testCtx(t), config.NewStore(r.paths), state.Selections.Revision, report, nil); e != nil {
		t.Fatal(e)
	}
	if next := count(t, r.call(testCtx(t), "old", "counter")); next != 2 || r.connects.Load() != 1 {
		t.Fatal(next, r.connects.Load())
	}
	if next := count(t, r.call(testCtx(t), "new", "counter")); next != 1 || r.connects.Load() != 2 {
		t.Fatal(next, r.connects.Load())
	}
}

func TestStoreSelectionBlocksLivePool(t *testing.T) {
	for _, code := range []string{"connection_disabled", "review_required", "tool_denied"} {
		t.Run(code, func(t *testing.T) {
			r := newRig(t)
			r.http("a", testutil.FixtureOptions{}, false)
			r.start()
			count(t, r.call(testCtx(t), "a", "counter"))
			state, e := config.ReadState(testCtx(t), r.paths)
			if e != nil {
				t.Fatal(e)
			}
			_, e = config.NewStore(r.paths).Update(testCtx(t), state.Selections.Revision, func(s *config.State) error {
				sel := s.Selections.Connections["local:a"]
				switch code {
				case "connection_disabled":
					sel.Enabled = false
				case "review_required":
					sel.ReviewRequired = true
				case "tool_denied":
					sel.DisabledTools = []string{"counter"}
				}
				s.Selections.Connections["local:a"] = sel
				return nil
			})
			if e != nil {
				t.Fatal(e)
			}
			responseCode(t, r.call(testCtx(t), "a", "counter"), code, false)
			if r.calls.Load() != 1 || r.connects.Load() != 1 {
				t.Fatal("blocked call had effects")
			}
		})
	}
}

const catalogPoolID = "github:fixture-owner/fixture.repo#paper"

type catalogPoolRig struct {
	*poolRig
	service       *catalog.Service
	state         config.State
	boot, secrets atomic.Int32
}

func newCatalogPoolRig(t *testing.T, protected bool) *catalogPoolRig {
	t.Helper()
	r := &catalogPoolRig{poolRig: newRig(t)}
	r.http("a", testutil.FixtureOptions{}, protected)
	r.http("replacement", testutil.FixtureOptions{}, false)
	r.opts.Credentials = auth.NewResolver(auth.ResolverOptions{Provider: testutil.FakeProvider{BootstrapFunc: func(context.Context, config.Profile) (auth.SecretClient, error) {
		r.boot.Add(1)
		return testutil.FakeSecretClient{ResolveFunc: func(context.Context, string) (string, error) {
			r.secrets.Add(1)
			return "fixture-value", nil
		}}, nil
	}}})
	r.start()
	r.service = &catalog.Service{Store: config.NewStore(r.paths)}
	state, err := r.service.Store.Read(testCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	candidate := catalog.Snapshot{
		Source:  config.Source{ID: "github-42", RepositoryID: 42, Owner: "fixture-owner", Repo: "fixture.repo", Path: "mcparcel.json", Ref: "main", Commit: strings.Repeat("a", 40)},
		Catalog: config.Catalog{SchemaVersion: 1, Connections: map[string]config.Connection{"paper": r.personal.Connections["a"]}, CredentialProfiles: r.personal.CredentialProfiles},
	}
	state, err = r.service.Register(testCtx(t), candidate, state.Selections.Revision)
	if err != nil {
		t.Fatal(err)
	}
	r.state, err = r.service.Store.Update(testCtx(t), state.Selections.Revision, func(draft *config.State) error {
		draft.Selections.Connections[catalogPoolID] = config.Selection{Enabled: true, CredentialProfile: r.personal.Connections["a"].CredentialProfile}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *catalogPoolRig) effects() [4]int32 {
	return [4]int32{r.boot.Load(), r.secrets.Load(), r.connects.Load(), r.calls.Load()}
}

func (r *catalogPoolRig) apply(edit func(*config.Catalog), accept bool) {
	r.t.Helper()
	current, err := catalog.SnapshotFromState(r.state, "github-42")
	if err != nil {
		r.t.Fatal(err)
	}
	candidate, err := catalog.SnapshotFromState(r.state, "github-42")
	if err != nil {
		r.t.Fatal(err)
	}
	candidate.Source.Commit = strings.Repeat("b", 40)
	if current.Source.Commit == candidate.Source.Commit {
		candidate.Source.Commit = strings.Repeat("c", 40)
	}
	edit(&candidate.Catalog)
	plan, err := catalog.PlanUpdate(current, candidate)
	if err != nil {
		r.t.Fatal(err)
	}
	var accepted []string
	if accept {
		accepted = []string{catalogPoolID}
	}
	r.state, err = r.service.ApplyUpdateState(testCtx(r.t), plan, accepted, r.state.Selections.Revision)
	if err != nil {
		r.t.Fatal(err)
	}
}

func (r *catalogPoolRig) changeEndpoint(accept bool) {
	r.apply(func(cat *config.Catalog) {
		c := cat.Connections["paper"]
		c.Transport.HTTP.URL = r.personal.Connections["replacement"].Transport.HTTP.URL
		cat.Connections["paper"] = c
	}, accept)
}

func TestCatalogReviewBeforeEffects(t *testing.T) {
	r := newCatalogPoolRig(t, true)
	if n := count(t, r.call(testCtx(t), catalogPoolID, "counter")); n != 1 {
		t.Fatal(n)
	}
	before := r.effects()
	r.changeEndpoint(false)
	responseCode(t, r.call(testCtx(t), catalogPoolID, "counter"), "review_required", false)
	if r.effects() != before {
		t.Fatal("apply or blocked call had effects", before, r.effects())
	}
}

func TestCatalogAcceptedReopensLazily(t *testing.T) {
	r := newCatalogPoolRig(t, true)
	count(t, r.call(testCtx(t), catalogPoolID, "counter"))
	before := r.effects()
	r.changeEndpoint(true)
	if r.effects() != before || r.closed.Load() != 0 {
		t.Fatal("apply had runtime effects")
	}
	if n := count(t, r.call(testCtx(t), catalogPoolID, "counter")); n != 1 {
		t.Fatal(n)
	}
	if r.connects.Load() != 2 || r.closed.Load() != 1 || r.boot.Load() != before[0] {
		t.Fatal("replacement was not lazy", r.effects(), r.closed.Load())
	}
	first, second := <-r.captured, <-r.captured
	if *first.Connection.Transport.HTTP.URL.Literal == *second.Connection.Transport.HTTP.URL.Literal || *second.Connection.Transport.HTTP.URL.Literal != *r.personal.Connections["replacement"].Transport.HTTP.URL.Literal {
		t.Fatal("wrong endpoint")
	}
	if n := count(t, r.call(testCtx(t), catalogPoolID, "counter")); n != 2 || r.connects.Load() != 2 {
		t.Fatal("replacement repeated", n)
	}
}

func TestCatalogRemovalBlocksRuntime(t *testing.T) {
	for _, kind := range []string{"source", "id"} {
		t.Run(kind, func(t *testing.T) {
			r := newCatalogPoolRig(t, true)
			count(t, r.call(testCtx(t), catalogPoolID, "counter"))
			before := r.effects()
			if kind == "source" {
				var err error
				r.state, err = r.service.Remove(testCtx(t), "fixture-owner/fixture.repo", r.state.Selections.Revision)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				r.apply(func(cat *config.Catalog) { delete(cat.Connections, "paper") }, false)
			}
			responseCode(t, r.call(testCtx(t), catalogPoolID, "counter"), "connection_unavailable", false)
			if r.effects() != before {
				t.Fatal("removal or blocked call had effects")
			}
		})
	}
}

func TestCatalogDescriptionKeepsSession(t *testing.T) {
	r := newCatalogPoolRig(t, true)
	if n := count(t, r.call(testCtx(t), catalogPoolID, "counter")); n != 1 {
		t.Fatal(n)
	}
	for i, field := range []string{"description", "label"} {
		before := r.effects()
		r.apply(func(cat *config.Catalog) {
			c := cat.Connections["paper"]
			if field == "description" {
				c.Description = "Revised"
			} else {
				c.Label = "Revised label"
			}
			cat.Connections["paper"] = c
		}, false)
		if r.effects() != before || r.state.Selections.Connections[catalogPoolID].ReviewRequired {
			t.Fatal("display edit had effects or marker")
		}
		if n := count(t, r.call(testCtx(t), catalogPoolID, "counter")); n != i+2 {
			t.Fatal(n)
		}
		if r.connects.Load() != 1 || r.closed.Load() != 0 || r.boot.Load() != before[0] || r.secrets.Load() != before[1] {
			t.Fatal("display edit restarted session or auth")
		}
	}
}

func TestSyncDuringDiscovery(t *testing.T) {
	for _, change := range []string{"endpoint", "profile", "secret", "selected-input"} {
		t.Run(change, func(t *testing.T) {
			r := newCatalogPoolRig(t, true)
			if change == "selected-input" {
				r.apply(func(cat *config.Catalog) {
					c := cat.Connections["paper"]
					c.Inputs = map[string]config.Input{"endpoint": {Kind: "url", Description: "Fixture endpoint", Default: c.Transport.HTTP.URL.Literal}}
					c.Transport.HTTP.URL = config.Value{Input: &config.InputRef{Input: "endpoint"}}
					cat.Connections["paper"] = c
				}, true)
			}
			r.listStarted = make(chan struct{}, 1)
			release := make(chan struct{})
			r.listRelease = release
			ch := make(chan Response, 1)
			go func() { ch <- r.h.Handle(testCtx(t), testID, Request{Method: "tools", Connection: catalogPoolID}, nil) }()
			select {
			case <-r.listStarted:
			case <-testCtx(t).Done():
				t.Fatal("discovery did not start")
			}
			before := r.effects()
			switch change {
			case "endpoint":
				r.changeEndpoint(true)
			case "secret":
				r.apply(func(cat *config.Catalog) {
					c := cat.Connections["paper"]
					c.Transport.HTTP.Headers["X-Secret"] = config.Value{Secret: &config.SecretRef{Secret: "op://vault/changed/value"}}
					cat.Connections["paper"] = c
				}, true)
			case "profile", "selected-input":
				var err error
				r.state, err = r.service.Store.UpdateAccepted(testCtx(t), r.state.Selections.Revision, []string{catalogPoolID}, func(draft *config.State) error {
					if change == "profile" {
						profile := draft.Local.CredentialProfiles["shared"]
						profile.Account = "changed-fixture"
						draft.Local.CredentialProfiles["shared"] = profile
					} else {
						sel := draft.Selections.Connections[catalogPoolID]
						sel.Inputs = map[string]string{"endpoint": *r.personal.Connections["replacement"].Transport.HTTP.URL.Literal}
						draft.Selections.Connections[catalogPoolID] = sel
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			if r.effects() != before {
				t.Fatal("apply had runtime effects")
			}
			close(release)
			res := response(t, ch)
			responseCode(t, res, "config_changed", false)
			if len(res.Data) != 0 || r.connects.Load() != 1 || r.calls.Load() != 0 {
				t.Fatal("stale schemas escaped or discovery retried", res)
			}
		})
	}
}

func TestCatalogUnrelatedRuntimeSurvives(t *testing.T) {
	r := newCatalogPoolRig(t, true)
	if n := count(t, r.call(testCtx(t), "local:a", "counter")); n != 1 {
		t.Fatal(n)
	}
	before := r.effects()
	r.changeEndpoint(false)
	if r.effects() != before {
		t.Fatal("sync had effects")
	}
	if n := count(t, r.call(testCtx(t), "local:a", "counter")); n != 2 {
		t.Fatal(n)
	}
	before = r.effects()
	var err error
	r.state, err = r.service.Remove(testCtx(t), "fixture-owner/fixture.repo", r.state.Selections.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if r.effects() != before {
		t.Fatal("remove had effects")
	}
	if n := count(t, r.call(testCtx(t), "local:a", "counter")); n != 3 {
		t.Fatal(n)
	}
	if r.connects.Load() != 1 || r.closed.Load() != 0 || r.boot.Load() != 1 {
		t.Fatal("unrelated connection restarted", r.effects())
	}
}

func TestCatalogCachedNeverStarts(t *testing.T) {
	r := newCatalogPoolRig(t, true)
	r.changeEndpoint(false)
	before := r.effects()
	loads := r.loads.Load()
	responseCode(t, r.h.Handle(testCtx(t), testID, Request{Method: "tools", Connection: catalogPoolID, Cached: true}, nil), "schema_cache_miss", false)
	if r.effects() != before || r.loads.Load() != loads {
		t.Fatal("cached tools had effects")
	}
}
