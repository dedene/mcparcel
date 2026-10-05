package catalog

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

func fixtureSnapshot(t *testing.T, name, commit string) Snapshot {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/catalogs/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.DecodeCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	s := existingSource()
	s.Commit = commit
	return Snapshot{Source: s, Catalog: c}
}

func encoded(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func editedSnapshot(t *testing.T, s Snapshot, path []string, value any, remove bool) Snapshot {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(encoded(t, s.Catalog), &obj); err != nil {
		t.Fatal(err)
	}
	m := obj
	for _, key := range path[:len(path)-1] {
		m = m[key].(map[string]any)
	}
	if remove {
		delete(m, path[len(path)-1])
	} else {
		m[path[len(path)-1]] = value
	}
	c, err := config.DecodeCatalog(encoded(t, obj))
	if err != nil {
		t.Fatal(err)
	}
	s.Catalog = c
	s.Source.Commit = commitB
	return s
}

func assertChanges(t *testing.T, p UpdatePlan, id string, fields []string, review bool) {
	t.Helper()
	if len(p.Changed) != 1 || p.Changed[0].ID != id || !reflect.DeepEqual(p.Changed[0].Fields, fields) || p.Changed[0].ExecutionOrAuth != review || len(p.Added) != 0 || len(p.Removed) != 0 {
		t.Fatalf("%+v", p)
	}
}

func TestPlanUpdate(t *testing.T) {
	a := fixtureSnapshot(t, "stage4-base", commitA)
	b := fixtureSnapshot(t, "stage4-next", commitB)
	p, err := PlanUpdate(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Added, []string{"github:fixture-owner/fixture.repo#newcomer"}) || !reflect.DeepEqual(p.Removed, []string{"github:fixture-owner/fixture.repo#retired"}) ||
		!reflect.DeepEqual(p.Changed, []ConnectionChange{{ID: "github:fixture-owner/fixture.repo#paper", Fields: []string{"/transport/url"}, ExecutionOrAuth: true}}) ||
		p.CurrentCommit != commitA || p.CandidateCommit != commitB || p.SourceID != "github-42" || p.Repository != "fixture-owner/fixture.repo" || p.Pinned || len(p.CatalogFields) != 0 {
		t.Fatalf("%+v", p)
	}
}

func TestExecutionChangeClassification(t *testing.T) {
	a := fixtureSnapshot(t, "valid-catalog", commitA)
	for _, tc := range []struct {
		name, id, path string
		value          any
	}{
		{"command", "paper", "transport/command", "new-command"},
		{"args", "paper", "transport/args", []any{"second", "first"}},
		{"env", "paper", "transport/env/FIXTURE_LITERAL", "changed"},
		{"cwd", "paper", "transport/cwd", "/other"},
		{"inheritEnv", "paper", "transport/inheritEnv", []any{"LANG"}},
		{"url", "hosted", "transport/url", "https://changed.invalid/mcp"},
		{"headers", "hosted", "transport/headers/X-Fixture-Token/secret", "op://fixture/other/token"},
		{"mode", "hosted", "transport/mode", "sse"},
		{"insecure", "hosted", "transport/allowInsecureHttp", "explicit"},
		{"clientName", "oauth", "auth/clientName", "Changed"},
		{"scopes", "oauth", "auth/scopes", []any{"write", "read"}},
		{"clientId", "oauth", "auth/clientId", "changed-public"},
		{"clientSecret", "oauth", "auth/clientSecret/secret", "op://fixture/other/token"},
		{"tokenMethod", "oauth", "auth/tokenEndpointAuthMethod", "client_secret_post"},
		{"redirect", "oauth", "auth/redirectUrl", "http://127.0.0.1/other"},
		{"issuer", "oauth", "auth/issuerUrl", "https://other.invalid"},
		{"inputDefault", "paper", "inputs/argument/default", "new"},
		{"inputDescription", "paper", "inputs/argument/description", "new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := append([]string{"connections", tc.id}, strings.Split(tc.path, "/")...)
			b := editedSnapshot(t, a, path, tc.value, false)
			p, err := PlanUpdate(a, b)
			if err != nil {
				t.Fatal(err)
			}
			// Changing a Value union from an object to a string is atomic.
			assertChanges(t, p, "github:fixture-owner/fixture.repo#"+tc.id, []string{"/" + tc.path}, true)
		})
	}
	t.Run("inputKind", func(t *testing.T) {
		b := editedSnapshot(t, a, []string{"connections", "paper", "inputs", "workspace", "kind"}, "string", false)
		p, err := PlanUpdate(a, b)
		if err != nil {
			t.Fatal(err)
		}
		assertChanges(t, p, "github:fixture-owner/fixture.repo#paper", []string{"/inputs/workspace/kind"}, true)
	})
	t.Run("auth", func(t *testing.T) {
		b := editedSnapshot(t, a, []string{"connections", "oauth", "auth"}, nil, true)
		p, err := PlanUpdate(a, b)
		if err != nil {
			t.Fatal(err)
		}
		assertChanges(t, p, "github:fixture-owner/fixture.repo#oauth", []string{"/auth"}, true)
	})
	t.Run("profile", func(t *testing.T) {
		a := fixtureSnapshot(t, "valid-catalog", commitA)
		a.Catalog.CredentialProfiles["shared"] = config.ProfileRequirement{}
		b := editedSnapshot(t, a, []string{"connections", "paper", "credentialProfile"}, "shared", false)
		p, err := PlanUpdate(a, b)
		if err != nil {
			t.Fatal(err)
		}
		assertChanges(t, p, "github:fixture-owner/fixture.repo#paper", []string{"/credentialProfile"}, true)
	})
	t.Run("transportType", func(t *testing.T) {
		a := fixtureSnapshot(t, "stage4-base", commitA)
		b := editedSnapshot(t, a, []string{"connections", "paper", "transport"}, map[string]any{"type": "stdio", "command": "fixture"}, false)
		p, err := PlanUpdate(a, b)
		if err != nil {
			t.Fatal(err)
		}
		assertChanges(t, p, "github:fixture-owner/fixture.repo#paper", []string{"/transport/allowInsecureHttp", "/transport/command", "/transport/mode", "/transport/type", "/transport/url"}, true)
	})
}

func TestDisplayChangeClassification(t *testing.T) {
	a := fixtureSnapshot(t, "valid-catalog", commitA)
	for _, tc := range []struct {
		path  string
		value any
	}{
		{"label", "New"}, {"description", "New"}, {"domains", []any{}}, {"lifecycle/idleTimeout", "10m"}, {"callTimeout", "90s"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			b := editedSnapshot(t, a, append([]string{"connections", "paper"}, strings.Split(tc.path, "/")...), tc.value, false)
			p, err := PlanUpdate(a, b)
			if err != nil {
				t.Fatal(err)
			}
			assertChanges(t, p, "github:fixture-owner/fixture.repo#paper", []string{"/" + tc.path}, false)
		})
	}
	for _, tc := range []struct {
		path    []string
		pointer string
	}{
		{[]string{"domains", "docs", "label"}, "/domains/docs/label"},
		{[]string{"credentialProfiles", "team", "description"}, "/credentialProfiles/team/description"},
		{[]string{"name"}, "/name"},
	} {
		t.Run(tc.pointer, func(t *testing.T) {
			b := editedSnapshot(t, a, tc.path, "New", false)
			p, err := PlanUpdate(a, b)
			if err != nil || len(p.Changed) != 0 || !reflect.DeepEqual(p.CatalogFields, []string{tc.pointer}) {
				t.Fatal(p, err)
			}
		})
	}
}

func TestPolicyReviewClassification(t *testing.T) {
	empty := []string{}
	read := []string{"read"}
	both := []string{"read", "write"}
	for _, tc := range []struct {
		name          string
		before, after *config.ToolPolicy
		fields        []string
		review        bool
	}{
		{"empty-to-omitted", &config.ToolPolicy{Allow: &empty}, &config.ToolPolicy{}, []string{"/toolPolicy/allow"}, true},
		{"deny-removal", &config.ToolPolicy{Deny: read}, &config.ToolPolicy{}, []string{"/toolPolicy/deny"}, true},
		{"narrowing", &config.ToolPolicy{Allow: &both}, &config.ToolPolicy{Allow: &read}, []string{"/toolPolicy/allow"}, false},
		{"redundant-deny", &config.ToolPolicy{Allow: &empty, Deny: read}, &config.ToolPolicy{Allow: &empty}, []string{"/toolPolicy/deny"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := fixtureSnapshot(t, "stage4-base", commitA)
			c := a.Catalog.Connections["paper"]
			c.ToolPolicy = tc.before
			a.Catalog.Connections["paper"] = c
			b := editedSnapshot(t, a, []string{"connections", "paper", "toolPolicy"}, tc.after, false)
			p, err := PlanUpdate(a, b)
			if err != nil {
				t.Fatal(err)
			}
			assertChanges(t, p, "github:fixture-owner/fixture.repo#paper", tc.fields, tc.review)
		})
	}
}

func TestRevisionComparison(t *testing.T) {
	a := fixtureSnapshot(t, "stage4-base", commitA)
	p, err := PlanUpdate(a, a)
	if err != nil || len(p.Added)+len(p.Removed)+len(p.Changed)+len(p.CatalogFields) != 0 {
		t.Fatal(p, err)
	}
	b := fixtureSnapshot(t, "stage4-description", commitA)
	if _, err = PlanUpdate(a, b); !errors.Is(err, ErrContentInvalid) {
		t.Fatal(err)
	}
	b = a
	b.Source.Commit = commitB
	p, err = PlanUpdate(a, b)
	if err != nil || p.CandidateCommit != commitB || len(p.Added)+len(p.Removed)+len(p.Changed)+len(p.CatalogFields) != 0 {
		t.Fatal(p, err)
	}
	for _, key := range []string{"added", "removed", "changed", "catalogFields"} {
		var obj map[string]any
		_ = json.Unmarshal(encoded(t, p), &obj)
		if obj[key] == nil {
			t.Fatal(key, "nil array")
		}
	}
}

func TestDiffReferencePaths(t *testing.T) {
	a := fixtureSnapshot(t, "valid-catalog", commitA)
	b := editedSnapshot(t, a, []string{"connections", "paper", "transport", "env", "FIXTURE_TOKEN", "secret"}, "op://fixture/OTHER/token", false)
	p, err := PlanUpdate(a, b)
	if err != nil {
		t.Fatal(err)
	}
	assertChanges(t, p, "github:fixture-owner/fixture.repo#paper", []string{"/transport/env/FIXTURE_TOKEN/secret"}, true)
	raw := string(encoded(t, p))
	for _, forbidden := range []string{"op://", "OTHER", "fixture.invalid", `"connections"`, "Original"} {
		if strings.Contains(raw, forbidden) {
			t.Fatal(raw)
		}
	}
	paths := changedPaths(map[string]any{"a/b~c": map[string]any{"secret": "before"}}, map[string]any{"a/b~c": map[string]any{"secret": "after"}}, "")
	if !reflect.DeepEqual(paths, []string{"/a~1b~0c/secret"}) {
		t.Fatal(paths)
	}
}

func TestPlanPureAndStable(t *testing.T) {
	a := fixtureSnapshot(t, "stage4-base", commitA)
	b := fixtureSnapshot(t, "stage4-next", commitB)
	oldA, oldB := encoded(t, a.Catalog), encoded(t, b.Catalog)
	p, err := PlanUpdate(a, b)
	if err != nil {
		t.Fatal(err)
	}
	want := string(encoded(t, p))
	for range 10 {
		reordered := map[string]config.Connection{}
		keys := []string{}
		for k := range b.Catalog.Connections {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		slices.Reverse(keys)
		for _, k := range keys {
			reordered[k] = b.Catalog.Connections[k]
		}
		c := b
		c.Catalog.Connections = reordered
		next, err := PlanUpdate(a, c)
		if err != nil || string(encoded(t, next)) != want {
			t.Fatal(next, err)
		}
	}
	if string(encoded(t, a.Catalog)) != string(oldA) || string(encoded(t, b.Catalog)) != string(oldB) {
		t.Fatal("mutated")
	}
	p.Candidate.Catalog.Connections["paper"] = config.Connection{}
	if string(encoded(t, b.Catalog)) != string(oldB) {
		t.Fatal("plan aliases candidate")
	}
}

func TestPlanEqualSnapshotCopies(t *testing.T) {
	a := fixtureSnapshot(t, "stage4-base", commitA)
	before := string(encoded(t, a.Catalog))
	plan, err := PlanUpdate(a, a)
	if err != nil {
		t.Fatal(err)
	}
	plan.Current.Catalog.Connections["paper"].Transport.HTTP.URL = config.Literal("https://current.changed.invalid")
	if string(encoded(t, a.Catalog)) != before || string(encoded(t, plan.Candidate.Catalog)) != before {
		t.Fatal("current snapshot aliases caller or candidate")
	}
	plan.Candidate.Catalog.Connections["paper"].Transport.HTTP.URL = config.Literal("https://candidate.changed.invalid")
	if string(encoded(t, a.Catalog)) != before {
		t.Fatal("candidate snapshot aliases caller")
	}
}

func TestPlanSourceConstraints(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*config.Source)
		want error
	}{
		{"identity", func(s *config.Source) { s.ID = "github-43"; s.RepositoryID = 43 }, ErrRepositoryReused},
		{"rename", func(s *config.Source) { s.Repo = "other" }, ErrSourceRenamed},
		{"path", func(s *config.Source) { s.Path = "other.json" }, ErrSourceConflict},
		{"ref", func(s *config.Source) { s.Ref = "other" }, ErrSourceConflict},
		{"pin", func(s *config.Source) { s.Pinned = true }, ErrSourceConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := fixtureSnapshot(t, "stage4-base", commitA)
			b := fixtureSnapshot(t, "stage4-next", commitB)
			tc.edit(&b.Source)
			if _, err := PlanUpdate(a, b); !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
		})
	}
	t.Run("pinnedCommit", func(t *testing.T) {
		a := fixtureSnapshot(t, "stage4-base", commitA)
		a.Source.Pinned = true
		b := a
		b.Source.Commit = commitB
		if _, err := PlanUpdate(a, b); !errors.Is(err, ErrSourceConflict) {
			t.Fatal(err)
		}
	})
}
