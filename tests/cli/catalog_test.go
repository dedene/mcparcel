package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/catalog"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dedene/mcparcel/internal/testutil"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

func metadataData(t *testing.T, v result) output.MetadataData {
	t.Helper()
	var d output.MetadataData
	if e := json.Unmarshal(v.envelope.Data, &d); e != nil {
		t.Fatal(e)
	}
	return d
}

func inspectData(t *testing.T, v result) output.InspectData {
	t.Helper()
	var d output.InspectData
	if e := json.Unmarshal(v.envelope.Data, &d); e != nil {
		t.Fatal(e)
	}
	return d
}

func seedMetadata(t *testing.T, r *metadataRig, includePersonal bool) config.State {
	t.Helper()
	state, e := config.NewStore(r.paths).Update(context.Background(), 0, func(s *config.State) error {
		if includePersonal {
			s.Personal.Connections["personal"] = config.Connection{Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal("fixture")}}}
		}
		source := config.Source{ID: "github-42", RepositoryID: 42, Owner: "fixture-owner", Repo: "fixture.repo", Path: "mcparcel.json", Ref: "main", Commit: strings.Repeat("a", 40)}
		s.Local.Sources = []config.Source{source}
		cat := config.Catalog{SchemaVersion: 1, Connections: map[string]config.Connection{}, CredentialProfiles: map[string]config.ProfileRequirement{"team": {}}}
		for _, id := range []string{"personal", "disabled", "missing", "review"} {
			c := config.Connection{Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal("fixture")}}}
			if id == "missing" {
				c.CredentialProfile = "team"
				c.Transport.Stdio.Env = map[string]config.Value{"TOKEN": {Secret: &config.SecretRef{Secret: "op://Fixture/item/token"}}}
			}
			cat.Connections[id] = c
			s.Selections.Connections["github:fixture-owner/fixture.repo#"+id] = config.Selection{Enabled: id != "disabled", ReviewRequired: id == "review"}
		}
		s.Catalogs[source.ID] = cat
		s.Personal.Connections["removed"] = config.Connection{Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal("fixture")}}}
		s.Selections.Connections["local:removed"] = config.Selection{Enabled: true, ReviewRequired: true}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	state, e = config.NewStore(r.paths).Update(context.Background(), state.Selections.Revision, func(s *config.State) error { delete(s.Personal.Connections, "removed"); return nil })
	if e != nil {
		t.Fatal(e)
	}
	return state
}

func TestCatalogEmptyBlackBox(t *testing.T) {
	r := newMetadataRig(t)
	d := metadataData(t, r.run(0, "", "catalog", "--json"))
	if d.Revision != 0 || d.Items == nil || len(d.Items) != 0 || d.Sources == nil || len(d.Sources) != 0 || d.Domains["other"].Label != "Other" || d.SourceRevisions["personal"] == "" {
		t.Fatal(d)
	}
	for _, p := range []string{r.paths.ConfigDir, r.paths.StateDir, r.paths.DataDir, r.paths.RuntimeDir} {
		files, e := os.ReadDir(p)
		if e != nil || len(files) != 0 {
			t.Fatal(p, files, e)
		}
	}
	r.offline()
}

func TestCatalogStatesBlackBox(t *testing.T) {
	r := newMetadataRig(t)
	seedMetadata(t, r, false)
	d := metadataData(t, r.run(0, "", "catalog", "--json"))
	if len(d.Items) != 5 {
		t.Fatal(d)
	}
	ids := []string{}
	for _, row := range d.Items {
		ids = append(ids, row.ID)
	}
	want := []string{"github:fixture-owner/fixture.repo#disabled", "github:fixture-owner/fixture.repo#missing", "github:fixture-owner/fixture.repo#personal", "github:fixture-owner/fixture.repo#review", "local:removed"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatal(ids)
	}
	if !reflect.DeepEqual(d.Items[1].Blockers, []string{"config_required"}) || !reflect.DeepEqual(d.Items[3].Blockers, []string{"review_required"}) || !reflect.DeepEqual(d.Items[4].Blockers, []string{"connection_unavailable", "review_required"}) {
		t.Fatal(d.Items)
	}
	list := metadataData(t, r.run(0, "", "list", "--json"))
	if len(list.Items) != 4 {
		t.Fatal(list)
	}
	for _, row := range list.Items {
		if !row.Enabled {
			t.Fatal(row)
		}
	}
	if len(d.Sources) != 1 || d.Sources[0].CacheAgeSeconds < 0 || d.Sources[0].CacheAgeSeconds > 30 {
		t.Fatal(d.Sources)
	}
	r.offline()
}

func TestInspectOriginBlackBox(t *testing.T) {
	r := newMetadataRig(t)
	state := seedMetadata(t, r, true)
	for _, id := range []string{"local:personal", "github:fixture-owner/fixture.repo#missing", "local:removed"} {
		d := inspectData(t, r.run(0, "", "inspect", id, "--json"))
		if d.Revision != state.Selections.Revision || d.Item.ID != id || !reflect.DeepEqual(d.Selection, state.Selections.Connections[id]) {
			t.Fatal(d)
		}
		if strings.HasPrefix(id, "local:") {
			if d.Source != nil {
				t.Fatal(d)
			}
		} else if d.Source == nil || d.Source.Source != state.Local.Sources[0] || d.Item.Definition.CredentialProfile != "team" {
			t.Fatal(d)
		}
		if id == "local:removed" && (d.Item.Definition != nil || d.Item.Connection != nil || d.Item.Available) {
			t.Fatal(d)
		}
	}
	v := r.run(2, "ambiguous_id", "inspect", "personal", "--json")
	want := []any{"github:fixture-owner/fixture.repo#personal", "local:personal"}
	if !reflect.DeepEqual(v.envelope.Error.Details["candidates"], want) {
		t.Fatal(v.stdout)
	}
	r.run(4, "connection_unavailable", "inspect", "absent", "--json")
	r.offline()
}

func TestToolsSyntaxPreserved(t *testing.T) {
	r := newMetadataRig(t)
	seedMetadata(t, r, false)
	r.run(6, "schema_cache_miss", "tools", "github:fixture-owner/fixture.repo#personal", "--cached", "--json")
	v := r.run(0, "", "tools", "disable", "github:fixture-owner/fixture.repo#personal", "read", "--json")
	var mutation struct {
		Revision uint64 `json:"revision"`
	}
	if e := json.Unmarshal(v.envelope.Data, &mutation); e != nil || mutation.Revision != 3 {
		t.Fatal(v.stdout, e)
	}
	r.run(0, "", "tools", "enable", "github:fixture-owner/fixture.repo#personal", "read", "--json")
	for _, argv := range [][]string{{"tools", "personal", "extra"}, {"tools", "enable"}, {"tools", "disable", "personal"}, {"tools", "enable", "personal", "read", "--cached"}, {"enable"}, {"disable"}} {
		r.run(2, "invalid_arguments", append(argv, "--json")...)
	}
	r.offline()
	t.Run("live", testToolsSyntaxLive)
}

func testToolsSyntaxLive(t *testing.T) {
	r := newMetadataRig(t)
	server := testutil.NewFixtureServer()
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	defer httpServer.Close()
	_, err := config.NewStore(r.paths).Update(context.Background(), 0, func(s *config.State) error {
		for _, id := range []string{"fixture", "enable", "disable"} {
			s.Personal.Connections[id] = config.Connection{Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal(httpServer.URL), AllowInsecureHTTP: "loopback"}}}
			s.Selections.Connections["local:"+id] = config.Selection{Enabled: true}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.run(0, "", "runtime", "stop", "--force", "--json") })
	for _, id := range []string{"fixture", "local:enable", "local:disable"} {
		v := r.run(0, "", "tools", id, "--json")
		var list output.ToolList
		if err := json.Unmarshal(v.envelope.Data, &list); err != nil || len(list.Items) == 0 || list.Connection != "local:"+strings.TrimPrefix(id, "local:") {
			t.Fatal(v.stdout, err)
		}
	}
}

func TestLocalCommandsBlackBox(t *testing.T) {
	r := newMetadataRig(t)
	file := r.file("definition.json", []byte(`{"id":"paper","transport":{"type":"stdio","command":"fixture"}}`))
	v := r.run(0, "", "local", "add", "--file", file, "--json")
	if string(v.envelope.Data) != `{"revision":1}` {
		t.Fatal(v.stdout)
	}
	d := inspectData(t, r.run(0, "", "inspect", "paper", "--json"))
	if d.Item.Enabled || !d.Item.Available || d.Source != nil {
		t.Fatal(d)
	}
	r.run(0, "", "enable", "paper", "--json")
	r.run(0, "", "local", "update", "local:paper", "--file", file, "--json")
	r.run(0, "", "local", "remove", "paper", "--json")
	d = inspectData(t, r.run(0, "", "inspect", "paper", "--json"))
	if d.Item.Available || d.Item.Definition != nil || d.Item.Connection != nil {
		t.Fatal(d)
	}
	r.run(4, "connection_unavailable", "local", "remove", "paper", "--json")
	for _, argv := range [][]string{{"local", "add", "--file", ""}, {"local", "add", "--file", file, "--file", file}, {"local", "update", "paper"}, {"local", "remove"}} {
		r.run(2, "invalid_arguments", append(argv, "--json")...)
	}
	r.offline()
}

func catalogFixture(t *testing.T) (*metadataRig, *testutil.CatalogAPI, catalog.Snapshot, *httptest.Server) {
	t.Helper()
	r := newMetadataRig(t)
	a := testutil.NewCatalogAPI()
	raw, e := os.ReadFile("../../testdata/catalogs/stage4-base.json")
	if e != nil {
		t.Fatal(e)
	}
	cat, e := config.DecodeCatalog(raw)
	if e != nil {
		t.Fatal(e)
	}
	snap := catalog.Snapshot{Source: config.Source{ID: "github-42", RepositoryID: 42, Owner: "fixture-owner", Repo: "fixture.repo", Path: "mcparcel.json", Ref: "main", Commit: strings.Repeat("a", 40)}, Catalog: cat}
	if e = a.SetSnapshot(snap); e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(a)
	t.Cleanup(server.Close)
	r.env = append(r.env, "MCPARCEL_TEST_GITHUB_API="+server.URL)
	return r, a, snap, server
}

func nextCatalog(t *testing.T, a *testutil.CatalogAPI, snap catalog.Snapshot) catalog.Snapshot {
	t.Helper()
	raw, e := os.ReadFile("../../testdata/catalogs/stage4-next.json")
	if e != nil {
		t.Fatal(e)
	}
	snap.Catalog, e = config.DecodeCatalog(raw)
	if e != nil {
		t.Fatal(e)
	}
	snap.Source.Commit = strings.Repeat("b", 40)
	if e = a.SetSnapshot(snap); e != nil {
		t.Fatal(e)
	}
	return snap
}

func sourceData(t *testing.T, v result) output.SourceMutationData {
	t.Helper()
	var d output.SourceMutationData
	if e := json.Unmarshal(v.envelope.Data, &d); e != nil {
		t.Fatal(e)
	}
	return d
}

func syncData(t *testing.T, v result) output.SyncData {
	t.Helper()
	var d output.SyncData
	if e := json.Unmarshal(v.envelope.Data, &d); e != nil {
		t.Fatal(e)
	}
	return d
}

func savedCatalog(t *testing.T, r *metadataRig) config.State {
	t.Helper()
	s, e := config.ReadState(context.Background(), r.paths)
	if e != nil {
		t.Fatal(e)
	}
	return s
}

type catalogFile struct {
	Bytes string
	Mode  os.FileMode
	Mtime time.Time
}

func catalogFiles(t *testing.T, r *metadataRig) map[string]catalogFile {
	t.Helper()
	files := map[string]catalogFile{}
	for _, root := range []string{r.paths.ConfigDir, r.paths.DataDir, r.paths.StateDir, r.paths.CacheDir, r.paths.RuntimeDir} {
		e := filepath.Walk(root, func(p string, i os.FileInfo, e error) error {
			if e != nil {
				return e
			}
			if i.Mode().IsRegular() {
				b, e := os.ReadFile(p)
				if e != nil {
					return e
				}
				files[p] = catalogFile{string(b), i.Mode(), i.ModTime()}
			}
			return nil
		})
		if e != nil {
			t.Fatal(e)
		}
	}
	return files
}

func TestAddIdempotentBlackBox(t *testing.T) {
	r, a, snap, _ := catalogFixture(t)
	d := sourceData(t, r.run(0, "", "add", "fixture-owner/fixture.repo", "--json"))
	if d.Revision != 1 || !d.Changed || d.Source != snap.Source {
		t.Fatal(d)
	}
	nextCatalog(t, a, snap)
	d = sourceData(t, r.run(0, "", "add", "fixture-owner/fixture.repo", "--json"))
	if d.Revision != 1 || d.Changed || d.Source != snap.Source {
		t.Fatal(d)
	}
	r.run(2, "catalog_source_conflict", "add", "fixture-owner/fixture.repo", "--path", "other.json", "--json")
	r.offline()
}

func TestSyncPreviewNoWrite(t *testing.T) {
	r, a, snap, _ := catalogFixture(t)
	r.run(0, "", "add", "fixture-owner/fixture.repo", "--json")
	candidate := nextCatalog(t, a, snap)
	before := catalogFiles(t, r)
	d := syncData(t, r.run(0, "", "sync", "--json"))
	plan, e := catalog.PlanUpdate(snap, candidate)
	if e != nil {
		t.Fatal(e)
	}
	plan.Current = catalog.Snapshot{}
	plan.Candidate = catalog.Snapshot{}
	if d.Apply || len(d.Results) != 1 || d.Results[0].Applied || !reflect.DeepEqual(d.Results[0].Plan, &plan) || len(d.Results[0].Accepted) != 0 {
		t.Fatal(d)
	}
	if !reflect.DeepEqual(before, catalogFiles(t, r)) {
		t.Fatal("preview wrote files")
	}
	if savedCatalog(t, r).Local.Sources[0].Commit != snap.Source.Commit {
		t.Fatal("preview advanced pointer")
	}
	r.offline()
}

func TestSyncApplyAcceptBlackBox(t *testing.T) {
	r, a, snap, _ := catalogFixture(t)
	r.run(0, "", "add", "fixture-owner/fixture.repo", "--json")
	r.run(0, "", "enable", "paper", "--json")
	nextCatalog(t, a, snap)
	d := syncData(t, r.run(0, "", "sync", "--apply", "--json"))
	if !d.Results[0].Applied || len(d.Results[0].Accepted) != 0 {
		t.Fatal(d)
	}
	if !inspectData(t, r.run(0, "", "inspect", "paper", "--json")).Item.ReviewRequired {
		t.Fatal("missing marker")
	}
	d = syncData(t, r.run(0, "", "sync", "--apply", "--accept", "paper", "--accept", "paper", "--json"))
	if !reflect.DeepEqual(d.Results[0].Accepted, []string{"github:fixture-owner/fixture.repo#paper"}) {
		t.Fatal(d)
	}
	if inspectData(t, r.run(0, "", "inspect", "paper", "--json")).Item.ReviewRequired {
		t.Fatal("marker remains")
	}
	r.offline()
}

func TestCatalogFailureBlackBox(t *testing.T) {
	for _, tc := range []struct {
		status, exit int
		code         string
		header       http.Header
	}{{401, 3, "catalog_auth_required", nil}, {404, 4, "catalog_unavailable", nil}, {429, 6, "catalog_rate_limited", nil}, {403, 6, "catalog_rate_limited", http.Header{"X-Ratelimit-Remaining": []string{"0"}}}, {500, 6, "catalog_offline", nil}} {
		t.Run(tc.code, func(t *testing.T) {
			r, a, _, _ := catalogFixture(t)
			r.run(0, "", "add", "fixture-owner/fixture.repo", "--json")
			before := catalogFiles(t, r)
			a.Set("/repos/fixture-owner/fixture.repo", tc.status, tc.header, []byte(`{"message":"PRIVATE-CANARY"}`))
			v := r.run(tc.exit, tc.code, "sync", "--json")
			if string(v.envelope.Data) != "null" || strings.Contains(v.stdout, "PRIVATE-CANARY") {
				t.Fatal(v.stdout)
			}
			if !reflect.DeepEqual(before, catalogFiles(t, r)) {
				t.Fatal("failed fetch wrote")
			}
			r.run(0, "", "list", "--json")
			r.run(0, "", "inspect", "paper", "--json")
			r.offline()
		})
	}
	t.Run("refused", func(t *testing.T) {
		r, _, _, server := catalogFixture(t)
		r.run(0, "", "add", "fixture-owner/fixture.repo", "--json")
		server.Close()
		r.run(6, "catalog_offline", "sync", "--json")
		r.offline()
	})
}

func TestCatalogFixtureFailClosed(t *testing.T) {
	for _, endpoint := range []string{"", "http://example.invalid:80", "http://127.0.0.1:80/path", "http://user@127.0.0.1:80", "https://127.0.0.1:80", "http://localhost:80", "http://127.0.0.1"} {
		t.Run(endpoint, func(t *testing.T) {
			r := newMetadataRig(t)
			r.env = append(r.env, "MCPARCEL_TEST_GITHUB_API="+endpoint)
			r.run(6, "catalog_offline", "add", "fixture-owner/fixture.repo", "--json")
			r.offline()
		})
	}
}

func TestPartialMultiSourceSyncBlackBox(t *testing.T) {
	r, a, snap, _ := catalogFixture(t)
	sources := []catalog.Snapshot{}
	for i := 1; i <= 3; i++ {
		current := snap
		current.Source.ID = fmt.Sprintf("github-%d", i)
		current.Source.RepositoryID = uint64(i)
		current.Source.Repo = fmt.Sprintf("repo%d", i)
		if err := a.SetSnapshot(current); err != nil {
			t.Fatal(err)
		}
		r.run(0, "", "add", current.Source.Owner+"/"+current.Source.Repo, "--json")
		sources = append(sources, current)
	}
	for _, current := range sources {
		current.Source.Commit = strings.Repeat("b", 40)
		if err := a.SetSnapshot(current); err != nil {
			t.Fatal(err)
		}
	}
	a.Set("/repos/fixture-owner/repo2", 404, nil, []byte(`{}`))
	v := r.run(4, "catalog_unavailable", "sync", "--apply", "--json")
	var envelope struct {
		Data  json.RawMessage
		Error *output.Error
	}
	if err := json.Unmarshal([]byte(v.stdout), &envelope); err != nil {
		t.Fatal(err)
	}
	var d output.SyncData
	if err := json.Unmarshal(envelope.Error.Details.SyncReport, &d); err != nil {
		t.Fatal(err)
	}
	if string(envelope.Data) != "null" || len(d.Results) != 3 || !d.Results[0].Applied || d.Results[1].Error.Code != "catalog_unavailable" || !d.Results[2].Applied || d.Results[2].Revision != 5 {
		t.Fatal(v.stdout)
	}
	state := savedCatalog(t, r)
	if state.Local.Sources[1].Commit != snap.Source.Commit || state.Local.Sources[2].Commit != strings.Repeat("b", 40) {
		t.Fatal(state.Local.Sources)
	}
	r.offline()
}

func TestCatalogCachedNeverStarts(t *testing.T) {
	r, a, snap, _ := catalogFixture(t)
	r.run(0, "", "add", "fixture-owner/fixture.repo", "--json")
	r.run(0, "", "enable", "paper", "--json")
	nextCatalog(t, a, snap)
	r.run(0, "", "sync", "--apply", "--accept", "paper", "--json")
	r.run(6, "schema_cache_miss", "tools", "paper", "--cached", "--json")
	r.offline()
	for _, root := range []string{r.paths.CacheDir, r.paths.StateDir, r.paths.RuntimeDir} {
		entries, e := os.ReadDir(root)
		if e != nil || len(entries) != 0 {
			t.Fatal(root, entries, e)
		}
	}
}

func catalogExecute(t *testing.T, r *metadataRig, argv ...string) result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, binaryA, argv...)
	c.Env = r.env
	c.Dir = r.paths.Home
	var out, stderr bytes.Buffer
	c.Stdout = &out
	c.Stderr = &stderr
	err := c.Run()
	exit := 0
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			exit = e.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	return result{code: exit, stdout: out.String(), stderr: stderr.String()}
}

func catalogEnvelope(t *testing.T, v result, exit int, code string, keys ...string) map[string]json.RawMessage {
	t.Helper()
	if v.code != exit || v.stderr != "" || strings.ContainsAny(v.stdout, "\x1b") || !strings.HasSuffix(v.stdout, "\n") || strings.HasSuffix(v.stdout, "\n\n") {
		t.Fatal(v)
	}
	var e struct {
		SchemaVersion int
		OK            bool
		Data          json.RawMessage
		Error         *output.Error
	}
	decoder := json.NewDecoder(strings.NewReader(v.stdout))
	if err := decoder.Decode(&e); err != nil {
		t.Fatal(v, err)
	}
	if decoder.InputOffset() != int64(len(v.stdout)-1) {
		t.Fatal("unexpected trailing bytes", v.stdout)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		t.Fatal("extra envelope", err)
	}
	var outer map[string]json.RawMessage
	_ = json.Unmarshal([]byte(v.stdout), &outer)
	checkCatalogKeys(t, outer, "schemaVersion", "ok", "data", "error")
	if e.SchemaVersion != 1 || e.OK != (code == "") || code == "" && e.Error != nil || code != "" && (e.Error == nil || e.Error.Code != code || string(e.Data) != "null") {
		t.Fatal(v.stdout)
	}
	if code != "" {
		return nil
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(e.Data, &data); err != nil {
		t.Fatal(err)
	}
	checkCatalogKeys(t, data, keys...)
	return data
}

func checkCatalogKeys(t *testing.T, data map[string]json.RawMessage, keys ...string) {
	t.Helper()
	actual := []string{}
	for k := range data {
		actual = append(actual, k)
	}
	sort.Strings(actual)
	expected := append([]string{}, keys...)
	sort.Strings(expected)
	if !reflect.DeepEqual(actual, expected) {
		t.Fatal("JSON keys", actual, "want", expected)
	}
}

func catalogNoEffects(t *testing.T, r *metadataRig) {
	t.Helper()
	r.offline()
	for _, root := range []string{r.paths.CacheDir, r.paths.StateDir, r.paths.RuntimeDir} {
		entries, e := os.ReadDir(root)
		if e != nil || len(entries) != 0 {
			t.Fatal("metadata effects", root, entries, e)
		}
	}
	for _, name := range []string{"fixture-started", "fixture-child-start", "started", "writes"} {
		if _, e := os.Stat(filepath.Join(r.paths.Home, name)); !os.IsNotExist(e) {
			t.Fatal(name, e)
		}
	}
}

func TestCatalogOfflineOnboardingBlackBox(t *testing.T) {
	r, a, snap, server := catalogFixture(t)
	paper := snap.Catalog.Connections["paper"]
	paper.Domains = []string{"docs"}
	snap.Catalog.Connections["paper"] = paper
	if e := a.SetSnapshot(snap); e != nil {
		t.Fatal(e)
	}
	add := sourceData(t, r.run(0, "", "add", "fixture-owner/fixture.repo", "--json", "--no-input"))
	if add.Revision != 1 || !add.Changed || add.Source != snap.Source {
		t.Fatal(add)
	}
	domain := metadataData(t, r.run(0, "", "catalog", "--domain", "docs", "--json", "--no-input"))
	if len(domain.Items) != 1 || domain.Items[0].ID != "github:fixture-owner/fixture.repo#paper" || domain.Items[0].Enabled {
		t.Fatal(domain)
	}
	r.run(0, "", "enable", "github:fixture-owner/fixture.repo#paper", "--json", "--no-input")
	candidate := nextCatalog(t, a, snap)
	paper = candidate.Catalog.Connections["paper"]
	paper.Domains = []string{"docs"}
	candidate.Catalog.Connections["paper"] = paper
	if e := a.SetSnapshot(candidate); e != nil {
		t.Fatal(e)
	}
	before := catalogFiles(t, r)
	preview := syncData(t, r.run(0, "", "sync", "--json", "--no-input"))
	row := preview.Results[0]
	if row.Applied || row.Revision != 2 || row.Plan.CurrentCommit != snap.Source.Commit || row.Plan.CandidateCommit != candidate.Source.Commit || !reflect.DeepEqual(row.Plan.Added, []string{"github:fixture-owner/fixture.repo#newcomer"}) || !reflect.DeepEqual(row.Plan.Removed, []string{"github:fixture-owner/fixture.repo#retired"}) || len(row.Plan.Changed) != 1 || !reflect.DeepEqual(row.Plan.Changed[0].Fields, []string{"/transport/url"}) || !row.Plan.Changed[0].ExecutionOrAuth {
		t.Fatal(preview)
	}
	if !reflect.DeepEqual(before, catalogFiles(t, r)) || savedCatalog(t, r).Local.Sources[0].Commit != snap.Source.Commit {
		t.Fatal("preview changed installation")
	}
	server.Close()
	profileFile := r.file("profile.json", []byte(`{"mode":"desktop","account":"Fixture"}`))
	r.run(0, "", "config", "profile", "set", "work", "--file", profileFile, "--json", "--no-input")
	for _, argv := range [][]string{{"catalog", "--domain", "docs"}, {"list"}, {"inspect", "paper"}, {"disable", "paper"}, {"enable", "paper"}, {"config", "validate", "--file", filepath.Join(r.paths.DataDir, "catalogs", "github-42", strings.Repeat("a", 40)+".json")}, {"tools", "disable", "paper", "counter"}, {"tools", "enable", "paper", "counter"}} {
		r.run(0, "", append(argv, "--json", "--no-input")...)
	}
	state := savedCatalog(t, r)
	if len(state.Local.Sources) != 1 || state.Local.Sources[0].Commit != snap.Source.Commit || !state.Selections.Connections["github:fixture-owner/fixture.repo#paper"].Enabled {
		t.Fatal(state)
	}
	catalogNoEffects(t, r)
}

func TestCatalogFailureRetainsSnapshot(t *testing.T) {
	for _, kind := range []string{"offline", "unauthorized", "rate", "rename", "reused", "invalid-json", "nonregular", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			r, a, snap, _ := catalogFixture(t)
			paper := snap.Catalog.Connections["paper"]
			paper.Inputs = map[string]config.Input{"tag": {Kind: "string", Description: "Tag"}}
			paper.CredentialProfile = "team"
			snap.Catalog.Connections["paper"] = paper
			snap.Catalog.CredentialProfiles = map[string]config.ProfileRequirement{"team": {}}
			if e := a.SetSnapshot(snap); e != nil {
				t.Fatal(e)
			}
			r.run(0, "", "add", "fixture-owner/fixture.repo", "--json")
			state := savedCatalog(t, r)
			_, e := config.NewStore(r.paths).Update(context.Background(), state.Selections.Revision, func(s *config.State) error {
				s.Local.CredentialProfiles["work"] = config.Profile{Mode: "desktop", Account: "Fixture"}
				s.Local.Aliases["saved"] = "github:fixture-owner/fixture.repo#paper"
				s.Selections.Connections["github:fixture-owner/fixture.repo#paper"] = config.Selection{Enabled: true, Inputs: map[string]string{"tag": "bound"}, CredentialProfile: "work", DisabledTools: []string{"counter"}}
				return nil
			})
			if e != nil {
				t.Fatal(e)
			}
			before := catalogFiles(t, r)
			state = savedCatalog(t, r)
			exit, code := 2, "invalid_catalog"
			base := "/repos/fixture-owner/fixture.repo"
			switch kind {
			case "offline":
				a.Set(base, 500, nil, []byte("CANARY"))
				exit, code = 6, "catalog_offline"
			case "unauthorized":
				a.Set(base, 401, nil, []byte("CANARY"))
				exit, code = 3, "catalog_auth_required"
			case "rate":
				a.Set(base, 429, nil, []byte("CANARY"))
				exit, code = 6, "catalog_rate_limited"
			case "rename", "reused":
				body := map[string]any{"id": 42, "full_name": "fixture-owner/fixture.repo", "default_branch": "main", "private": false}
				if kind == "rename" {
					body["full_name"] = "fixture-owner/moved"
					exit, code = 4, "catalog_renamed"
				} else {
					body["id"] = 99
					exit, code = 4, "catalog_identity_changed"
				}
				raw, _ := json.Marshal(body)
				a.Set(base, 200, nil, raw)
			default:
				for _, endpoint := range a.Requests() {
					if !strings.Contains(endpoint, "/git/trees/") {
						continue
					}
					response, e := a.Get(context.Background(), endpoint)
					if e != nil {
						t.Fatal(e)
					}
					if kind == "invalid-json" {
						a.Set(endpoint, 200, nil, []byte(`{"broken":`))
					} else {
						var body map[string]any
						if e := json.Unmarshal(response.Body, &body); e != nil {
							t.Fatal(e)
						}
						entry := body["tree"].([]any)[0].(map[string]any)
						if kind == "nonregular" {
							entry["mode"] = "120000"
						} else {
							entry["size"] = 3 * 1024 * 1024
							code = "catalog_too_large"
						}
						raw, _ := json.Marshal(body)
						a.Set(endpoint, 200, nil, raw)
					}
					break
				}
			}
			for _, argv := range [][]string{{"sync"}, {"sync", "--apply"}} {
				v := r.run(exit, code, append(argv, "--json", "--no-input")...)
				if strings.Contains(v.stdout, "CANARY") {
					t.Fatal(v.stdout)
				}
				if !reflect.DeepEqual(before, catalogFiles(t, r)) || !reflect.DeepEqual(state, savedCatalog(t, r)) {
					t.Fatal("failed fetch changed snapshot/settings")
				}
			}
			r.run(0, "", "catalog", "--json")
			r.run(0, "", "inspect", "saved", "--json")
			catalogNoEffects(t, r)
		})
	}
	t.Run("timeout", func(t *testing.T) {
		r, _, _, old := catalogFixture(t)
		r.run(0, "", "add", "fixture-owner/fixture.repo", "--json")
		old.Close()
		before := catalogFiles(t, r)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.URL.Path != "/repos/fixture-owner/fixture.repo" {
				t.Error("unexpected timeout endpoint", req.URL.Path)
				w.WriteHeader(404)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			<-req.Context().Done()
		}))
		defer server.Close()
		r.env = replaceEnv(r.env, "MCPARCEL_TEST_GITHUB_API", server.URL)
		r.env = append(r.env, "MCPARCEL_TEST_FETCH_TIMEOUT=2s")
		catalogEnvelope(t, catalogExecute(t, r, "sync", "--json", "--no-input"), 6, "timeout")
		if !reflect.DeepEqual(before, catalogFiles(t, r)) {
			t.Fatal("timeout changed snapshot")
		}
		catalogNoEffects(t, r)
	})
}

func TestCatalogAllCommandsJSON(t *testing.T) {
	r, a, snap, _ := catalogFixture(t)
	metadataKeys := []string{"revision", "items", "domains", "sourceRevisions", "sources"}
	inspectKeys := []string{"revision", "item", "selection", "source", "sourceRevisions"}
	mutationKeys := []string{"revision"}
	for _, argv := range [][]string{{"catalog"}, {"list"}, {"sync"}, {"sync", "--apply"}} {
		keys := metadataKeys
		if argv[0] == "sync" {
			keys = []string{"apply", "results"}
		}
		catalogEnvelope(t, catalogExecute(t, r, append(argv, "--json", "--no-input")...), 0, "", keys...)
	}
	for _, argv := range [][]string{{"inspect", "unknown"}, {"enable", "unknown"}, {"disable", "unknown"}} {
		catalogEnvelope(t, catalogExecute(t, r, append(argv, "--json", "--no-input")...), 4, "connection_unavailable")
	}
	catalogEnvelope(t, catalogExecute(t, r, "remove", "fixture-owner/fixture.repo", "--json", "--no-input"), 4, "catalog_unavailable")
	data := catalogEnvelope(t, catalogExecute(t, r, "add", "fixture-owner/fixture.repo", "--json", "--no-input"), 0, "", "revision", "source", "changed")
	var source map[string]json.RawMessage
	_ = json.Unmarshal(data["source"], &source)
	checkCatalogKeys(t, source, "id", "repositoryId", "owner", "repo", "path", "ref", "commit", "pinned")
	for _, argv := range [][]string{{"catalog"}, {"list"}, {"inspect", "paper"}, {"enable", "paper"}, {"disable", "paper"}, {"tools", "disable", "paper", "counter"}, {"tools", "enable", "paper", "counter"}} {
		keys := mutationKeys
		if argv[0] == "catalog" || argv[0] == "list" {
			keys = metadataKeys
		}
		if argv[0] == "inspect" {
			keys = inspectKeys
		}
		catalogEnvelope(t, catalogExecute(t, r, append(argv, "--json", "--no-input")...), 0, "", keys...)
	}
	r.run(0, "", "enable", "paper", "--json")
	nextCatalog(t, a, snap)
	for _, argv := range [][]string{{"sync"}, {"sync", "--apply"}, {"sync", "--apply", "--accept", "paper"}} {
		data := catalogEnvelope(t, catalogExecute(t, r, append(argv, "--json", "--no-input")...), 0, "", "apply", "results")
		var rows []map[string]json.RawMessage
		_ = json.Unmarshal(data["results"], &rows)
		if len(rows) != 1 {
			t.Fatal(data)
		}
		checkCatalogKeys(t, rows[0], "repository", "sourceId", "plan", "applied", "accepted", "revision", "error")
	}
	file := r.file("local.json", []byte(`{"id":"paper","transport":{"type":"stdio","command":"never-execute"}}`))
	for _, argv := range [][]string{{"local", "add", "--file", file}, {"local", "update", "local:paper", "--file", file}} {
		catalogEnvelope(t, catalogExecute(t, r, append(argv, "--json", "--no-input")...), 0, "", mutationKeys...)
	}
	for _, verb := range []string{"inspect", "enable", "disable"} {
		catalogEnvelope(t, catalogExecute(t, r, verb, "paper", "--json", "--no-input"), 2, "ambiguous_id")
	}
	catalogEnvelope(t, catalogExecute(t, r, "local", "remove", "paper", "--json", "--no-input"), 0, "", mutationKeys...)
	catalogEnvelope(t, catalogExecute(t, r, "remove", "fixture-owner/fixture.repo", "--json", "--no-input"), 0, "", "revision", "source", "changed")
	for _, argv := range [][]string{{"add"}, {"remove"}, {"sync", "a/b", "c/d"}, {"catalog", "extra"}, {"list", "extra"}, {"inspect"}, {"enable"}, {"disable"}, {"tools", "enable"}, {"tools", "disable", "paper"}, {"local", "add"}, {"local", "update", "paper"}, {"local", "remove"}} {
		catalogEnvelope(t, catalogExecute(t, r, append(argv, "--json", "--no-input")...), 2, "invalid_arguments")
	}
	for _, argv := range [][]string{{"add"}, {"remove"}, {"sync"}, {"catalog"}, {"list"}, {"inspect"}, {"enable"}, {"disable"}, {"tools"}, {"local", "add"}, {"local", "update"}, {"local", "remove"}} {
		v := catalogExecute(t, r, append(argv, "--help")...)
		if v.code != 0 || v.stderr != "" || !strings.Contains(v.stdout, "Usage:") {
			t.Fatal(v)
		}
		if argv[0] == "local" {
			description := map[string]string{"add": "Add", "update": "Update", "remove": "Remove"}[argv[1]] + " a personal connection definition offline."
			if !strings.Contains(v.stdout, description) {
				t.Fatalf("help missing %q: %s", description, v.stdout)
			}
		}
		if (argv[0] == "enable" || argv[0] == "disable") && !strings.Contains(v.stdout, "<mcp> ...") {
			t.Fatalf("selection help has incorrect positional: %s", v.stdout)
		}
	}
	for _, argv := range [][]string{{"catalog"}, {"list"}, {"inspect", "local:paper"}, {"disable", "local:paper"}, {"sync"}, {"sync", "--apply"}} {
		v := catalogExecute(t, r, argv...)
		if v.code != 0 || v.stderr != "" || strings.ContainsAny(v.stdout, "\x1b") {
			t.Fatal(v)
		}
		if argv[0] == "sync" && v.stdout != "No catalogs are registered. Run 'mcparcel add <owner/repo>' to register one.\n" {
			t.Fatalf("empty sync guidance: %q", v.stdout)
		}
	}

	for _, argv := range [][]string{{"add", "fixture-owner/fixture.repo"}, {"enable", "github:fixture-owner/fixture.repo#paper"}, {"tools", "disable", "github:fixture-owner/fixture.repo#paper", "counter"}, {"tools", "enable", "github:fixture-owner/fixture.repo#paper", "counter"}, {"sync"}, {"sync", "--apply"}, {"disable", "github:fixture-owner/fixture.repo#paper"}, {"local", "add", "--file", file}, {"local", "update", "local:paper", "--file", file}, {"local", "remove", "paper"}, {"remove", "fixture-owner/fixture.repo"}} {
		v := catalogExecute(t, r, argv...)
		if v.code != 0 || v.stderr != "" || strings.ContainsAny(v.stdout, "\x1b") {
			t.Fatal(v)
		}
		if argv[0] == "add" && (!strings.HasPrefix(v.stdout, "Source: fixture-owner/fixture.repo mcparcel.json @ "+strings.Repeat("b", 40)+"\nRevision: ") || !strings.HasSuffix(v.stdout, "Registered.\n")) {
			t.Fatal(v.stdout)
		}
		if argv[0] == "remove" && !strings.HasPrefix(v.stdout, "Removed fixture-owner/fixture.repo.\nRevision: ") {
			t.Fatal(v.stdout)
		}
	}
	catalogNoEffects(t, r)
}

func TestCatalogSnapshotPermissions(t *testing.T) {
	r, a, snap, _ := catalogFixture(t)
	target := r.file("config-target.json", []byte(`{"schemaVersion":1}`))
	if e := os.Chmod(target, 0o644); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(target, r.paths.ConfigFile); e != nil {
		t.Fatal(e)
	}
	r.run(0, "", "add", "fixture-owner/fixture.repo", "--json")
	nextCatalog(t, a, snap)
	r.run(0, "", "sync", "--apply", "--json")
	linked, e := os.Readlink(r.paths.ConfigFile)
	if e != nil || linked != target {
		t.Fatal(linked, e)
	}
	info, e := os.Stat(target)
	if e != nil || info.Mode().Perm() != 0o644 {
		t.Fatal(info, e)
	}
	e = filepath.Walk(filepath.Join(r.paths.DataDir, "catalogs"), func(p string, i os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		want := os.FileMode(0o600)
		if i.IsDir() {
			want = 0o700
		}
		if i.Mode().Perm() != want {
			t.Errorf("%s mode=%o want=%o", p, i.Mode().Perm(), want)
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	for _, commit := range []string{snap.Source.Commit, strings.Repeat("b", 40)} {
		raw, e := os.ReadFile(filepath.Join(r.paths.DataDir, "catalogs", snap.Source.ID, commit+".json"))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = config.DecodeCatalog(raw); e != nil {
			t.Fatal(e)
		}
	}
	catalogNoEffects(t, r)
}

func TestCatalogConcurrentProcesses(t *testing.T) {
	r, a, snap, old := catalogFixture(t)
	r.run(0, "", "add", "fixture-owner/fixture.repo", "--json")
	r.run(0, "", "enable", "paper", "--json")
	r.run(0, "", "tools", "disable", "paper", "counter", "--json")
	nextCatalog(t, a, snap)
	old.Close()
	before := savedCatalog(t, r)
	var requests atomic.Int32
	arrived := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "/git/blobs/") {
			if requests.Add(1) == 2 {
				close(arrived)
			}
			select {
			case <-release:
			case <-req.Context().Done():
				return
			}
		}
		a.ServeHTTP(w, req)
	}))
	defer func() { once.Do(func() { close(release) }); server.Close() }()
	r.env = replaceEnv(r.env, "MCPARCEL_TEST_GITHUB_API", server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	results := make(chan result, 2)
	for range 2 {
		c := exec.CommandContext(ctx, binaryA, "sync", "--apply", "--json")
		c.Env = r.env
		c.Dir = r.paths.Home
		var out, stderr bytes.Buffer
		c.Stdout = &out
		c.Stderr = &stderr
		if e := c.Start(); e != nil {
			t.Fatal(e)
		}
		go func() {
			e := c.Wait()
			exit := 0
			if e != nil {
				exit = c.ProcessState.ExitCode()
			}
			results <- result{code: exit, stdout: out.String(), stderr: stderr.String()}
		}()
	}
	select {
	case <-arrived:
	case <-ctx.Done():
		once.Do(func() { close(release) })
		t.Fatal("both commands did not reach fetch barrier")
	}
	once.Do(func() { close(release) })
	first, second := <-results, <-results
	if first.code != 0 {
		first, second = second, first
	}
	catalogEnvelope(t, first, 0, "", "apply", "results")
	catalogEnvelope(t, second, 7, "config_conflict")
	state := savedCatalog(t, r)
	if state.Selections.Revision != before.Selections.Revision+1 || state.Local.Sources[0].Commit != strings.Repeat("b", 40) || !state.Selections.Connections["github:fixture-owner/fixture.repo#paper"].ReviewRequired || !reflect.DeepEqual(state.Selections.Connections["github:fixture-owner/fixture.repo#paper"].DisabledTools, []string{"counter"}) {
		t.Fatal(state)
	}
	if _, e := config.Resolve(state); e != nil {
		t.Fatal(e)
	}
	catalogNoEffects(t, r)
}

func TestCatalogLegacyAndImportBlackBox(t *testing.T) {
	r, a, snap, _ := catalogFixture(t)
	server := testutil.NewFixtureServer()
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	defer httpServer.Close()
	personal := config.Catalog{SchemaVersion: 1, Connections: map[string]config.Connection{"counter": {Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal(httpServer.URL), AllowInsecureHTTP: "loopback"}}}}}
	raw, e := json.Marshal(personal)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(r.paths.PersonalFile, raw, 0o600); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { r.run(0, "", "runtime", "stop", "--force", "--json") })
	first := r.run(0, "", "call", "counter.counter", "--json")
	if structured(t, first)["count"] != float64(1) {
		t.Fatal(first.stdout)
	}
	var status struct{ PID int }
	v := r.run(0, "", "runtime", "status", "--json")
	_ = json.Unmarshal(v.envelope.Data, &status)
	r.run(0, "", "add", "fixture-owner/fixture.repo", "--json")
	nextCatalog(t, a, snap)
	r.run(0, "", "sync", "--apply", "--json")
	second := r.run(0, "", "call", "counter.counter", "--json")
	if structured(t, second)["count"] != float64(2) {
		t.Fatal(second.stdout)
	}
	var later struct{ PID int }
	v = r.run(0, "", "runtime", "status", "--json")
	_ = json.Unmarshal(v.envelope.Data, &later)
	if status.PID == 0 || status.PID != later.PID {
		t.Fatal(status, later)
	}
	before := catalogFiles(t, r)
	fixture := r.fixture()
	report := reportData(t, r.run(0, "", "import", "mcporter", "--file", fixture, "--json", "--no-input"))
	if len(report.Entries) != 32 || report.Applied {
		t.Fatal(report)
	}
	bytes, e := os.ReadFile(fixture)
	if e != nil {
		t.Fatal(e)
	}
	var source struct {
		Servers map[string]map[string]json.RawMessage `json:"mcpServers"`
	}
	if e = json.Unmarshal(bytes, &source); e != nil {
		t.Fatal(e)
	}
	stdio, httpCount, oauth, refs := 0, 0, 0, 0
	for _, item := range source.Servers {
		if _, ok := item["command"]; ok {
			stdio++
		} else {
			httpCount++
		}
		var auth string
		_ = json.Unmarshal(item["auth"], &auth)
		if auth == "oauth" {
			oauth++
		}
		for _, value := range item {
			refs += strings.Count(string(value), "${")
		}
	}
	if stdio != 17 || httpCount != 15 || oauth != 10 || refs != 14 {
		t.Fatal(stdio, httpCount, oauth, refs)
	}
	if !reflect.DeepEqual(before, catalogFiles(t, r)) {
		t.Fatal("import preview changed files")
	}
	third := r.run(0, "", "call", "counter.counter", "--json")
	if structured(t, third)["count"] != float64(3) {
		t.Fatal(third.stdout)
	}
	state := savedCatalog(t, r)
	if !state.Selections.Connections["local:counter"].Enabled || state.Local.Sources[0].Commit != strings.Repeat("b", 40) {
		t.Fatal(state)
	}
	if _, e = os.Stat(r.paths.StateDir + "/fixture-auth-events"); !os.IsNotExist(e) {
		t.Fatal("public metadata accessed auth", e)
	}
}

func TestCatalogPrivacyAndNoEffects(t *testing.T) {
	r, a, snap, _ := catalogFixture(t)
	paper := snap.Catalog.Connections["paper"]
	paper.Label = "Label\x1b[31m\nInjected"
	paper.Description = "Description\x1b\nInjected"
	snap.Catalog.Connections["paper"] = paper
	if e := a.SetSnapshot(snap); e != nil {
		t.Fatal(e)
	}
	r.run(0, "", "add", "fixture-owner/fixture.repo", "--json")
	for _, argv := range [][]string{{"catalog"}, {"inspect", "paper"}} {
		v := catalogExecute(t, r, argv...)
		if v.code != 0 || v.stderr != "" || strings.ContainsAny(v.stdout, "\x1b") || !strings.Contains(v.stdout, `\x1b`) {
			t.Fatal(v)
		}
	}
	a.Set("/repos/fixture-owner/fixture.repo", 401, nil, []byte(`{"message":"GH-STDERR-CANARY op://Private/secret token-canary"}`))
	v := r.run(3, "catalog_auth_required", "sync", "--apply", "--json")
	for _, secret := range []string{"GH-STDERR-CANARY", "op://Private", "token-canary"} {
		if strings.Contains(v.stdout, secret) {
			t.Fatal(v.stdout)
		}
	}
	for _, argv := range [][]string{{"catalog"}, {"list"}, {"inspect", "paper"}, {"disable", "paper"}, {"enable", "paper"}, {"tools", "disable", "paper", "counter"}, {"tools", "enable", "paper", "counter"}, {"config", "validate", "--file", filepath.Join(r.paths.DataDir, "catalogs", "github-42", strings.Repeat("a", 40)+".json")}} {
		r.run(0, "", append(argv, "--json", "--no-input")...)
	}
	catalogNoEffects(t, r)
	for path := range catalogFiles(t, r) {
		if !strings.HasPrefix(path, r.paths.ConfigDir+"/") && !strings.HasPrefix(path, r.paths.DataDir+"/catalogs/") {
			t.Fatal("unexpected write", path)
		}
	}
}
