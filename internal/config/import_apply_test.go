package config

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func importStore(t *testing.T, work bool) (*Store, Paths) {
	t.Helper()
	p := storePaths(t)
	testWrite(t, p.PersonalFile, []byte(`{"schemaVersion":1,"connections":{}}`), 0o644)
	testWrite(t, p.SelectionsFile, []byte(`{"schemaVersion":1,"revision":3,"connections":{}}`), 0o644)
	local := Local{SchemaVersion: 1, Aliases: map[string]string{}, CredentialProfiles: map[string]Profile{}}
	if work {
		local.CredentialProfiles["work"] = Profile{Mode: "desktop-service-account", Account: "Fixture", BootstrapRef: "op://Fixture/bootstrap/token"}
	}
	b, e := json.Marshal(local)
	if e != nil {
		t.Fatal(e)
	}
	testWrite(t, p.ConfigFile, b, 0o644)
	return NewStore(p), p
}

func simpleImport(t *testing.T) ImportReport {
	t.Helper()
	return converted(t, []byte(`{"mcpServers":{"new":{"command":"fixture"}}}`), nil)
}

func TestApplyImportAll32(t *testing.T) {
	raw, b := importFixture(t)
	r := converted(t, raw, b)
	store, p := importStore(t, true)
	result, e := ApplyImport(context.Background(), store, 3, r, nil)
	if e != nil {
		t.Fatal(e)
	}
	s, e := store.Read(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if !result.Applied || result.Revision == nil || *result.Revision != 4 || len(s.Personal.Connections) != 32 || len(s.Local.Aliases) != 32 || len(s.Selections.Connections) != 32 {
		t.Fatal(result, s)
	}
	for _, row := range result.Entries {
		sel := s.Selections.Connections[row.CanonicalID]
		if !row.Selected || !row.Applied || !sel.Enabled || sel.ReviewRequired || sel.CredentialProfile != row.CredentialProfile || len(sel.Inputs) != 0 || len(sel.DisabledTools) != 0 {
			t.Fatal(row, sel)
		}
	}
	if !reflect.DeepEqual(s.Personal.Connections, r.Definitions.Connections) || r.Applied || r.Revision != nil {
		t.Fatal("definitions changed or input mutated")
	}
	snap, e := Load(p)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = snap.RuntimeConnection("front-mcp"); e != nil {
		t.Fatal(e)
	}
}

func TestApplyImportBlockedIsNoWrite(t *testing.T) {
	r := converted(t, []byte(blockedImportFixture), nil)
	store, p := importStore(t, true)
	before := stateFiles(t, p)
	result, e := ApplyImport(context.Background(), store, 3, r, nil)
	var blocked *ImportBlockedError
	if !errors.Is(e, ErrImportBlocked) || !errors.As(e, &blocked) || e.Error() != "import has blocked entries" || !reflect.DeepEqual(result, ImportReport{}) || !reflect.DeepEqual(before, stateFiles(t, p)) {
		t.Fatal(result, e)
	}
	n := 0
	for _, row := range blocked.Report.Entries {
		if row.Applicable {
			n++
		}
		if row.Applied {
			t.Fatal(row)
		}
	}
	if n != 1 || len(blocked.Report.Entries) != 2 || !importRow(t, blocked.Report, "good").Applicable {
		t.Fatal(n)
	}
}

// One literal OAuth client secret blocks the whole apply.
const blockedImportFixture = `{"mcpServers":{"bad":{"baseUrl":"https://x.invalid/mcp","auth":"oauth","oauthClientSecret":"literal-canary"},"good":{"command":"fixture"}}}`

func TestApplyImportOnly(t *testing.T) {
	raw, _ := importFixture(t)
	r := converted(t, raw, nil)
	store, _ := importStore(t, false)
	out, e := ApplyImport(context.Background(), store, 3, r, []string{"paper", "local:context7"})
	if e != nil {
		t.Fatal(e)
	}
	s, e := store.Read(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if len(s.Personal.Connections) != 2 || len(s.Local.Aliases) != 2 || len(s.Selections.Connections) != 2 || !s.Selections.Connections["local:paper"].Enabled || !s.Selections.Connections["local:context7"].Enabled {
		t.Fatal(s)
	}
	selected, omitted, blocked := 0, 0, 0
	for _, row := range out.Entries {
		if row.Selected {
			selected++
			if !row.Applied {
				t.Fatal(row)
			}
		} else {
			omitted++
			if row.Applied {
				t.Fatal(row)
			}
		}
		if !row.Applicable {
			blocked++
		}
	}
	if selected != 2 || omitted != 30 || blocked != 0 {
		t.Fatal(selected, omitted, blocked)
	}
}

func TestApplyImportMissingProfile(t *testing.T) {
	raw, b := importFixture(t)
	r := converted(t, raw, b)
	store, p := importStore(t, false)
	before := stateFiles(t, p)
	_, e := ApplyImport(context.Background(), store, 3, r, []string{"front-mcp"})
	var blocked *ImportBlockedError
	if !errors.As(e, &blocked) || !hasIssue(importRow(t, blocked.Report, "front-mcp").Unresolved, "mcpServers.front-mcp.credentialProfile", "missing_profile") || !reflect.DeepEqual(before, stateFiles(t, p)) {
		t.Fatal(e)
	}
	preview, e := PlanImport(mustImportState(t, store), r, []string{"paper"})
	if e != nil || importRow(t, preview, "front-mcp").Applicable || len(preview.Definitions.Connections) != 21 || !r.Entries[0].Applicable {
		t.Fatal(preview, e)
	}
}

func mustImportState(t *testing.T, store *Store) State {
	t.Helper()
	s, e := store.Read(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	return s
}

func TestApplyImportCollision(t *testing.T) {
	for _, kind := range []string{"definition", "alias", "requirement"} {
		t.Run(kind, func(t *testing.T) {
			store, p := importStore(t, true)
			r := simpleImport(t)
			if kind == "requirement" {
				r = converted(t, []byte(`{"mcpServers":{"new":{"command":"fixture","env":{"TOKEN":"${TOKEN}"}}}}`), map[string]CredentialBinding{"TOKEN": {Profile: "work", Secret: "op://v/i/f"}})
			}
			_, e := store.Update(context.Background(), 3, func(s *State) error {
				switch kind {
				case "definition":
					s.Personal.Connections["new"] = Connection{Transport: Transport{Stdio: &Stdio{Command: Literal("other")}}}
				case "alias":
					s.Local.Aliases["new"] = "github:example/tools#new"
				case "requirement":
					s.Personal.CredentialProfiles["work"] = ProfileRequirement{Description: "Existing contract"}
				}
				return nil
			})
			if e != nil {
				t.Fatal(e)
			}
			before := stateFiles(t, p)
			_, e = ApplyImport(context.Background(), store, 4, r, nil)
			var blocked *ImportBlockedError
			if !errors.As(e, &blocked) || !reflect.DeepEqual(before, stateFiles(t, p)) {
				t.Fatal(e)
			}
			path, code := "mcpServers.new", "existing_definition"
			if kind == "alias" {
				path, code = "aliases.new", "alias_collision"
			}
			if kind == "requirement" {
				path, code = "mcpServers.new.credentialProfile", "requirement_conflict"
			}
			if !hasIssue(importRow(t, blocked.Report, "new").Unresolved, path, code) {
				t.Fatal(blocked.Report)
			}
		})
	}
}

func TestApplyImportIdempotent(t *testing.T) {
	store, p := importStore(t, true)
	r := simpleImport(t)
	if _, e := ApplyImport(context.Background(), store, 3, r, nil); e != nil {
		t.Fatal(e)
	}
	_, e := store.Update(context.Background(), 4, func(s *State) error {
		s.Selections.Connections["local:new"] = Selection{Enabled: false, ReviewRequired: true, DisabledTools: []string{"write"}}
		s.Personal.Connections["unrelated"] = storeConnection()
		s.Personal.Domains["team"] = Domain{Label: "Team"}
		s.Personal.CredentialProfiles["other"] = ProfileRequirement{Description: "Keep"}
		s.Local.Runtime = &RuntimeDefaults{KeepAlive: true}
		s.Local.Aliases["other"] = "local:unrelated"
		addStoreSource(s)
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	before := mustImportState(t, store)
	files := stateFiles(t, p)
	out, e := ApplyImport(context.Background(), store, 5, r, nil)
	after := mustImportState(t, store)
	if e != nil || !out.Applied || !out.Entries[0].Applied || *out.Revision != 5 || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(files, stateFiles(t, p)) {
		t.Fatal(out, e)
	}
	// Explicit stage-3 absence of a selection also stays disabled.
	p = storePaths(t)
	data, _ := json.Marshal(r.Definitions)
	testWrite(t, p.PersonalFile, data, 0o644)
	testWrite(t, p.SelectionsFile, []byte(`{"schemaVersion":1,"revision":0,"connections":{}}`), 0o644)
	store = NewStore(p)
	out, e = ApplyImport(context.Background(), store, 0, r, nil)
	if e != nil || *out.Revision != 1 {
		t.Fatal(out, e)
	}
	if s := mustImportState(t, store); len(s.Selections.Connections) != 0 {
		t.Fatal(s)
	}
	out, e = ApplyImport(context.Background(), store, 1, r, nil)
	if e != nil || *out.Revision != 1 {
		t.Fatal(out, e)
	}
}

func TestApplyImportStale(t *testing.T) {
	store, p := importStore(t, true)
	r := simpleImport(t)
	preview, e := PlanImport(mustImportState(t, store), r, nil)
	if e != nil || *preview.Revision != 3 {
		t.Fatal(preview, e)
	}
	_, e = store.Update(context.Background(), 3, func(s *State) error {
		s.Local.Aliases["new"] = "github:example/tools#new"
		profile := s.Local.CredentialProfiles["work"]
		profile.Account = "Changed"
		s.Local.CredentialProfiles["work"] = profile
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	before := stateFiles(t, p)
	out, e := ApplyImport(context.Background(), store, 3, r, nil)
	if !errors.Is(e, ErrConfigConflict) || !reflect.DeepEqual(out, ImportReport{}) || !reflect.DeepEqual(before, stateFiles(t, p)) {
		t.Fatal(out, e)
	}
	// A fresh CAS must rerun destination checks against the changed alias.
	_, e = ApplyImport(context.Background(), store, 4, r, nil)
	if !errors.Is(e, ErrImportBlocked) {
		t.Fatal(e)
	}
}

func TestApplyImportFailureAfterPersonal(t *testing.T) {
	if os.Getenv("MCP_IMPORT_CRASH") == "1" {
		p := childPaths(t)
		r := simpleImport(t)
		var planned ImportReport
		_, e := NewStore(p).update(context.Background(), 0, importMutation(r, nil, &planned), storeHooks{AfterStep: func(step string) error {
			s, e := readStateUnlocked(p)
			if e != nil {
				t.Fatal(e)
			}
			if step == "reserve" {
				if s.Legacy || s.Selections.Revision != 1 || !s.Selections.Connections["local:paper"].Enabled || len(s.Personal.Connections) != 1 || s.Selections.Connections["local:new"].Enabled {
					t.Fatal("unsafe reservation", s)
				}
			}
			if step == "personal" {
				if _, ok := s.Personal.Connections["new"]; !ok || s.Selections.Connections["local:new"].Enabled {
					t.Fatal("unsafe personal prefix", s)
				}
				os.Exit(23)
			}
			return nil
		}})
		t.Fatal("missed crash", e)
		return
	}
	p := storePaths(t)
	seedStore(t, p, 0, true)
	cmd := exec.Command(os.Args[0], "-test.run=^TestApplyImportFailureAfterPersonal$")
	cmd.Env = append(storeChildEnv(p), "MCP_IMPORT_CRASH=1")
	data, e := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(e, &exit) || exit.ExitCode() != 23 {
		t.Fatalf("%v\n%s", e, data)
	}
	store := NewStore(p)
	s := mustImportState(t, store)
	if s.Legacy || s.Selections.Revision != 1 || len(s.Personal.Connections) != 2 || s.Selections.Connections["local:new"].Enabled || !s.Selections.Connections["local:paper"].Enabled {
		t.Fatal(s)
	}
	out, e := ApplyImport(context.Background(), store, 1, simpleImport(t), nil)
	if e != nil || !out.Applied {
		t.Fatal(out, e)
	}
	s = mustImportState(t, store)
	if s.Selections.Connections["local:new"].Enabled {
		t.Fatal("retry re-enabled crash prefix")
	}
	again, e := ApplyImport(context.Background(), store, *out.Revision, simpleImport(t), nil)
	if e != nil || *again.Revision != *out.Revision {
		t.Fatal(again, e)
	}
}

func TestImportSourceUntouched(t *testing.T) {
	raw, b := importFixture(t)
	store, p := importStore(t, true)
	source := filepath.Join(p.Home, "mcporter.json")
	testWrite(t, source, raw, 0o444)
	before, e := os.Stat(source)
	if e != nil {
		t.Fatal(e)
	}
	r := converted(t, testBytes(t, source), b)
	if _, e := ApplyImport(context.Background(), store, 3, r, nil); e != nil {
		t.Fatal(e)
	}
	after, e := os.Stat(source)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(testBytes(t, source), raw) || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("source changed")
	}
}

func TestPlanImportValidation(t *testing.T) {
	store, p := importStore(t, true)
	state := mustImportState(t, store)
	r := simpleImport(t)
	before := stateFiles(t, p)
	for _, only := range [][]string{{""}, {"missing"}, {"github:example/tools#new"}, {"new", "local:new"}} {
		_, e := PlanImport(state, r, only)
		if !errors.Is(e, ErrConfig) {
			t.Fatal(only, e)
		}
	}
	mutations := map[string]func(*ImportReport){
		"duplicate":          func(r *ImportReport) { r.Entries = append(r.Entries, r.Entries[0]) },
		"canonical":          func(r *ImportReport) { r.Entries[0].CanonicalID = "local:foreign" },
		"alias":              func(r *ImportReport) { r.Aliases["new"] = "local:foreign" },
		"foreign alias":      func(r *ImportReport) { r.Aliases["foreign"] = "local:foreign" },
		"foreign definition": func(r *ImportReport) { r.Definitions.Connections["foreign"] = storeConnection() },
		"missing definition": func(r *ImportReport) { delete(r.Definitions.Connections, "new") },
		"blocked boolean":    func(r *ImportReport) { r.Entries[0].Applicable = false },
		"unresolved boolean": func(r *ImportReport) {
			r.Entries[0].Unresolved = append(r.Entries[0].Unresolved, ImportIssue{Path: "mcpServers.new", Code: "invalid_field"})
		},
		"transport":          func(r *ImportReport) { r.Entries[0].Transport = "http" },
		"oauth":              func(r *ImportReport) { r.Entries[0].OAuth = true },
		"profile":            func(r *ImportReport) { r.Entries[0].CredentialProfile = "work" },
		"invalid definition": func(r *ImportReport) { r.Definitions.Connections["new"] = Connection{} },
		"extra requirement": func(r *ImportReport) {
			r.Definitions.CredentialProfiles = map[string]ProfileRequirement{"work": {}}
		},
		"unsupported imports": func(r *ImportReport) {
			r.Issues = append(r.Issues, ImportIssue{Path: "imports", Code: "unsupported_imports"})
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			data, _ := json.Marshal(r)
			var forged ImportReport
			if e := json.Unmarshal(data, &forged); e != nil {
				t.Fatal(e)
			}
			mutate(&forged)
			_, e := PlanImport(state, forged, nil)
			if !errors.Is(e, ErrConfig) {
				t.Fatal(e)
			}
		})
	}
	preview, e := PlanImport(state, r, nil)
	if e != nil || !preview.Entries[0].Selected || preview.Applied || preview.Revision == nil || *preview.Revision != 3 {
		t.Fatal(preview, e)
	}
	preview.Definitions.Connections["new"] = storeConnection()
	preview.Entries[0].Warnings = append(preview.Entries[0].Warnings, ImportIssue{Path: "changed", Code: "local_value"})
	preview.Aliases["new"] = "local:changed"
	if r.Aliases["new"] != "local:new" || len(r.Entries[0].Warnings) != 0 || !reflect.DeepEqual(before, stateFiles(t, p)) {
		t.Fatal("preview mutated input or filesystem")
	}
	r.Applied = true
	r.Entries[0].Applied = true
	r.Entries[0].Selected = false
	rev := uint64(999)
	r.Revision = &rev
	preview, e = PlanImport(state, r, nil)
	if e != nil || preview.Applied || preview.Entries[0].Applied || !preview.Entries[0].Selected || *preview.Revision != 3 {
		t.Fatal(preview, e)
	}
	// Root imports are a blocker even when the selected subset would otherwise be safe.
	external := converted(t, []byte(`{"mcpServers":{"new":{"command":"fixture"}},"imports":["ignored.json"]}`), nil)
	_, e = ApplyImport(context.Background(), store, 3, external, []string{"new"})
	if !errors.Is(e, ErrImportBlocked) {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(before, stateFiles(t, p)) {
		t.Fatal("invalid or blocked report wrote files")
	}
}

func TestPlanImportRejectsInjectedCredentials(t *testing.T) {
	store, _ := importStore(t, true)
	state := mustImportState(t, store)
	for _, entry := range []string{
		`{"command":"fixture","env":{"API_KEY":"canary"}}`,
		`{"command":"fixture","args":["--token=canary"]}`,
		`{"command":"fixture","args":["--env=API_KEY=canary"]}`,
		`{"command":"fixture","args":["--endpoint=https://x.invalid?auth=canary"]}`,
		`{"command":"fixture","args":["SERVICE_URL=https://user:canary@x.invalid"]}`,
		`{"command":"fixture","env":{"DATABASE_URL":"https://user:canary@x.invalid"}}`,
		`{"baseUrl":"https://x.invalid","headers":{"X-Endpoint":"https://x.invalid?auth=canary"}}`,
		`{"baseUrl":"https://x.invalid?token=canary"}`,
		`{"baseUrl":"https://x.invalid","headers":{"Authorization":"canary"}}`,
	} {
		r := simpleImport(t)
		var source map[string]any
		if e := json.Unmarshal([]byte(entry), &source); e != nil {
			t.Fatal(e)
		}
		var c Connection
		if command, ok := source["command"]; ok {
			c.Transport.Stdio = &Stdio{Command: Literal(command.(string))}
			if args, ok := source["args"].([]any); ok {
				for _, a := range args {
					c.Transport.Stdio.Args = append(c.Transport.Stdio.Args, Literal(a.(string)))
				}
			}
			if env, ok := source["env"].(map[string]any); ok {
				c.Transport.Stdio.Env = map[string]Value{}
				for k, v := range env {
					c.Transport.Stdio.Env[k] = Literal(v.(string))
				}
			}
		} else {
			r.Entries[0].Transport = "http"
			c.Transport.HTTP = &HTTP{URL: Literal(source["baseUrl"].(string))}
			if headers, ok := source["headers"].(map[string]any); ok {
				c.Transport.HTTP.Headers = map[string]Value{}
				for k, v := range headers {
					c.Transport.HTTP.Headers[k] = Literal(v.(string))
				}
			}
		}
		r.Definitions.Connections["new"] = c
		_, e := PlanImport(state, r, nil)
		if !errors.Is(e, ErrConfig) {
			t.Fatal("injected credentials accepted", e)
		}
	}
}

func TestApplyImportPreservesUnavailableReview(t *testing.T) {
	for _, previous := range []Selection{{Enabled: true}, {ReviewRequired: true}} {
		store, p := importStore(t, true)
		data, e := json.Marshal(Selections{SchemaVersion: 1, Revision: 3, Connections: map[string]Selection{"local:new": previous}})
		if e != nil {
			t.Fatal(e)
		}
		testWrite(t, p.SelectionsFile, data, 0o644)
		_, e = ApplyImport(context.Background(), store, 3, simpleImport(t), nil)
		if e != nil {
			t.Fatal(e)
		}
		sel := mustImportState(t, store).Selections.Connections["local:new"]
		if !sel.Enabled || !sel.ReviewRequired {
			t.Fatal("prior review protection lost", previous, sel)
		}
	}
}

func TestApplyImportRejectsCredentialURLAffixes(t *testing.T) {
	for _, header := range []bool{false, true} {
		store, p := importStore(t, true)
		raw := `{"mcpServers":{"new":{"command":"fixture","env":{"ENDPOINT":"${NAME}"}}}}`
		if header {
			raw = `{"mcpServers":{"new":{"baseUrl":"https://x.invalid","headers":{"X-Endpoint":"${NAME}"}}}}`
		}
		r := converted(t, []byte(raw), map[string]CredentialBinding{"NAME": {Profile: "work", Secret: "op://Fixture/service/key"}})
		c := r.Definitions.Connections["new"]
		v := Value{Secret: &SecretRef{Secret: "op://Fixture/service/key", Prefix: "https://user:AFFIX_CANARY@x.invalid/"}}
		if header {
			c.Transport.HTTP.Headers["X-Endpoint"] = v
		} else {
			c.Transport.Stdio.Env["ENDPOINT"] = v
		}
		r.Definitions.Connections["new"] = c
		before := stateFiles(t, p)
		_, err := ApplyImport(context.Background(), store, 3, r, nil)
		if !errors.Is(err, ErrConfig) || strings.Contains(err.Error(), "AFFIX_CANARY") {
			t.Fatalf("unsafe URL affix accepted: %v", err)
		}
		if !reflect.DeepEqual(before, stateFiles(t, p)) {
			t.Fatal("failed apply persisted credential")
		}
	}
}
