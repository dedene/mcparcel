package config

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func importFixture(t *testing.T) ([]byte, map[string]CredentialBinding) {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/mcporter/all-32.json")
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[string]CredentialBinding{}
	for _, m := range sourceReference.FindAllStringSubmatch(string(raw), -1) {
		bindings[m[1]] = CredentialBinding{Profile: "work", Secret: "op://Fixture/import/" + m[1]}
	}
	return raw, bindings
}

func converted(t *testing.T, raw []byte, bindings map[string]CredentialBinding) ImportReport {
	t.Helper()
	r, e := ImportMcporter(raw, bindings)
	if e != nil {
		t.Fatal(e)
	}
	return r
}

func importRow(t *testing.T, r ImportReport, id string) ImportEntry {
	t.Helper()
	for _, v := range r.Entries {
		if v.ID == id {
			return v
		}
	}
	t.Fatal("missing", id)
	return ImportEntry{}
}

func hasIssue(issues []ImportIssue, path, code string) bool {
	return slices.ContainsFunc(issues, func(v ImportIssue) bool { return v.Path == path && v.Code == code })
}

func assertBlocked(t *testing.T, raw string, code, path string) ImportReport {
	t.Helper()
	r := converted(t, []byte(raw), nil)
	row := importRow(t, r, "bad")
	if row.Applicable || !hasIssue(row.Unresolved, path, code) {
		t.Fatalf("row: %+v", row)
	}
	if _, ok := r.Definitions.Connections["bad"]; ok {
		t.Fatal("blocked definition retained")
	}
	b, e := json.Marshal(r)
	if e != nil || strings.Contains(string(b), "canary") {
		t.Fatalf("leaked or invalid report: %s %v", b, e)
	}
	return r
}

func TestImportAll32(t *testing.T) {
	assertImportFieldFidelity(t)
	assertImportNames(t)
	raw, b := importFixture(t)
	r := converted(t, raw, b)
	counts := map[string]int{}
	oauth := 0
	for _, v := range r.Entries {
		counts[v.Transport]++
		if v.OAuth {
			oauth++
		}
		if !v.Applicable || !v.Selected || v.Applied {
			t.Fatalf("row %+v", v)
		}
		if !slices.IsSorted(v.Fields) {
			t.Fatal(v.Fields)
		}
	}
	if len(r.Entries) != 32 || len(r.Definitions.Connections) != 32 || len(r.Aliases) != 32 || counts["stdio"] != 17 || counts["http"] != 15 || oauth != 10 || len(r.Issues) != 0 || r.Applied || r.Revision != nil {
		t.Fatalf("bad report %+v", r)
	}
	bytes, e := json.Marshal(r.Definitions)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = DecodeCatalog(bytes); e != nil {
		t.Fatal(e)
	}
	data, _ := json.Marshal(r)
	if strings.Contains(string(data), ":null") {
		t.Fatal(string(data))
	}
}

func TestImportNames(t *testing.T) { assertImportNames(t) }
func assertImportNames(t *testing.T) {
	t.Helper()
	raw, _ := importFixture(t)
	r := converted(t, raw, nil)
	names := []string{}
	for _, v := range r.Entries {
		names = append(names, v.ID)
		if v.CanonicalID != "local:"+v.ID || r.Aliases[v.ID] != v.CanonicalID || v.Alias != v.ID {
			t.Fatal(v)
		}
	}
	want := strings.Fields("blender browserstack bugsnag chrome-devtools context-mode context7 excalidraw exa figma firecrawl front-mcp glitchtip higgsfield home-assistant lighthouse maestro mobbin neuronwriter notion octocode ovh-logs paper perplexity proxmox-mcp-plus rails-blocks rubrikit-staging se-ranking sequential-thinking slack treg typefully uptimerobot")
	slices.Sort(want)
	if !reflect.DeepEqual(names, want) {
		t.Fatal(names)
	}
}

func TestImportFieldFidelity(t *testing.T) { assertImportFieldFidelity(t) }
func assertImportFieldFidelity(t *testing.T) {
	t.Helper()
	raw, b := importFixture(t)
	r := converted(t, raw, b)
	t.Logf("fixture before: %x", sha256.Sum256(raw))
	t.Cleanup(func() {
		after, e := os.ReadFile("../../testdata/mcporter/all-32.json")
		if e != nil {
			t.Error(e)
			return
		}
		t.Logf("fixture after: %x", sha256.Sum256(after))
		if !bytes.Equal(raw, after) {
			t.Error("fixture bytes changed")
		}
	})
	var source struct {
		Servers map[string]map[string]json.RawMessage `json:"mcpServers"`
	}
	if e := json.Unmarshal(raw, &source); e != nil {
		t.Fatal(e)
	}
	counts := map[string]int{}
	refs := map[string]int{}
	for id, fields := range source.Servers {
		c := r.Definitions.Connections[id]
		if _, ok := fields["description"]; !ok && c.Description != "" {
			t.Fatal(id, "invented description")
		}
		if d := c.Transport.Stdio; d != nil {
			if _, ok := fields["args"]; !ok && len(d.Args) != 0 {
				t.Fatal(id, "invented args")
			}
			if _, ok := fields["env"]; !ok && len(d.Env) != 0 {
				t.Fatal(id, "invented env")
			}
		}
		if h := c.Transport.HTTP; h != nil {
			if _, ok := fields["headers"]; !ok && len(h.Headers) != 0 {
				t.Fatal(id, "invented headers")
			}
			consent := "never"
			if strings.HasPrefix(*h.URL.Literal, "http:") {
				consent = "explicit"
			}
			if h.AllowInsecureHTTP != consent {
				t.Fatal(id, "consent")
			}
		}
		if a := c.Auth; a != nil {
			for key, empty := range map[string]bool{"clientName": a.ClientName == "", "oauthClientId": a.ClientID == nil, "oauthClientSecret": a.ClientSecret == nil, "oauthRedirectUrl": a.RedirectURL == "", "oauthTokenEndpointAuthMethod": a.TokenEndpointAuthMethod == "", "oauthScope": len(a.Scopes) == 0} {
				if _, ok := fields[key]; !ok && !empty {
					t.Fatal(id, "invented", key)
				}
			}
		} else if _, ok := fields["auth"]; ok {
			t.Fatal(id, "lost auth")
		}
		for key, value := range fields {
			counts[key]++
			var text string
			switch key {
			case "command":
				_ = json.Unmarshal(value, &text)
				got, _ := LiteralText(c.Transport.Stdio.Command)
				if got != text {
					t.Fatal(id, key)
				}
			case "args":
				var args []string
				_ = json.Unmarshal(value, &args)
				if len(args) != len(c.Transport.Stdio.Args) {
					t.Fatal(id, key)
				}
				for i, a := range args {
					got, _ := LiteralText(c.Transport.Stdio.Args[i])
					if got != a {
						t.Fatal(id, i)
					}
				}
			case "description":
				_ = json.Unmarshal(value, &text)
				if c.Description != text {
					t.Fatal(id, key)
				}
			case "baseUrl":
				_ = json.Unmarshal(value, &text)
				if *c.Transport.HTTP.URL.Literal != text || c.Transport.HTTP.Mode != "auto" {
					t.Fatal(id, key)
				}
			case "env", "headers":
				var values map[string]string
				_ = json.Unmarshal(value, &values)
				var got map[string]Value
				if key == "env" {
					got = c.Transport.Stdio.Env
				} else {
					got = c.Transport.HTTP.Headers
				}
				if len(got) != len(values) {
					t.Fatal(id, key)
				}
				for name, text := range values {
					want := expectedImportValue(text)
					if !reflect.DeepEqual(want, got[name]) {
						t.Fatal(id, name)
					}
					if want.Secret != nil {
						refs[key]++
					}
				}
			case "auth":
				_ = json.Unmarshal(value, &text)
				if c.Auth == nil || c.Auth.Type != text {
					t.Fatal(id, key)
				}
			case "oauthScope":
				_ = json.Unmarshal(value, &text)
				if !reflect.DeepEqual(c.Auth.Scopes, strings.Split(text, " ")) {
					t.Fatal(id, key)
				}
			case "oauthRedirectUrl":
				_ = json.Unmarshal(value, &text)
				if c.Auth.RedirectURL != text {
					t.Fatal(id, key)
				}
			case "oauthTokenEndpointAuthMethod":
				_ = json.Unmarshal(value, &text)
				if c.Auth.TokenEndpointAuthMethod != text {
					t.Fatal(id, key)
				}
			case "clientName":
				_ = json.Unmarshal(value, &text)
				if c.Auth.ClientName != text {
					t.Fatal(id, key)
				}
			case "oauthClientId", "oauthClientSecret":
				_ = json.Unmarshal(value, &text)
				want := expectedImportValue(text)
				var got *Value
				if key == "oauthClientId" {
					got = c.Auth.ClientID
				} else {
					got = c.Auth.ClientSecret
				}
				if got == nil || !reflect.DeepEqual(want, *got) {
					t.Fatal(id, key)
				}
				if want.Secret != nil {
					refs["oauth"]++
				}
			default:
				t.Fatal("unasserted source field", id, key)
			}
		}
	}
	for key, want := range map[string]int{"command": 17, "args": 16, "env": 14, "headers": 2, "description": 28, "clientName": 4, "oauthClientId": 2, "oauthClientSecret": 1, "baseUrl": 15, "auth": 10, "oauthScope": 1, "oauthRedirectUrl": 1, "oauthTokenEndpointAuthMethod": 1} {
		if counts[key] != want {
			t.Fatal(key, counts[key])
		}
	}
	if refs["env"] != 10 || refs["headers"] != 2 || refs["oauth"] != 2 {
		t.Fatal(refs)
	}
	if !reflect.DeepEqual(r.Definitions.Connections["higgsfield"].Auth.Scopes, []string{"scope-0", "scope-1", "scope-2"}) {
		t.Fatal("scopes")
	}
	for _, name := range []string{"OPENSEARCH_DISABLED_TOOLS", "OPENSEARCH_SETTINGS_ALLOW_WRITE", "OPENSEARCH_SETTINGS_ALLOW_WRITE_CATEGORIES", "OPENSEARCH_DYNAMIC_CONNECTION"} {
		v := r.Definitions.Connections["ovh-logs"].Transport.Stdio.Env[name]
		if v.Literal == nil || *v.Literal != "literal-value" {
			t.Fatal(name, v)
		}
	}
	slack := r.Definitions.Connections["slack"].Auth
	if slack.RedirectURL != "http://127.0.0.1:3118/callback" || slack.TokenEndpointAuthMethod != "none" || *slack.ClientID.Literal != "client-id-slack" {
		t.Fatal(slack)
	}
	if r.Definitions.Connections["front-mcp"].Auth.ClientID.Secret == nil {
		t.Fatal("front ID must be a reference")
	}
}

func TestImportUnresolvedSecret(t *testing.T) {
	raw, b := importFixture(t)
	for name := range b {
		t.Setenv(name, "credential-canary")
	}
	r := converted(t, raw, nil)
	applicable, blocked, refs := 0, 0, 0
	for _, v := range r.Entries {
		if v.Applicable {
			applicable++
		} else {
			blocked++
		}
		for _, i := range v.Unresolved {
			if i.Code == "unresolved_credential" {
				refs++
			}
		}
	}
	bytes, _ := json.Marshal(r)
	if applicable != 32 || blocked != 0 || refs != 0 || strings.Contains(string(bytes), "credential-canary") {
		t.Fatal(applicable, blocked, refs)
	}
}

func TestImportBlockedNames(t *testing.T) {
	raw, _ := importFixture(t)
	r := converted(t, raw, nil)
	got := []string{}
	for _, v := range r.Entries {
		if !v.Applicable {
			got = append(got, v.ID)
		}
	}
	if !reflect.DeepEqual(got, []string{}) {
		t.Fatal(got)
	}
}

func TestImportUnknownField(t *testing.T) {
	r := assertBlocked(t, `{"mcpServers":{"bad":{"baseUrl":"https://x.invalid","oauthScopes":[],"typo":false},"good":{"command":"fixture"}}}`, "unknown_field", "mcpServers.bad.oauthScopes")
	if !importRow(t, r, "good").Applicable || !hasIssue(importRow(t, r, "bad").Unresolved, "mcpServers.bad.typo", "unknown_field") {
		t.Fatal(r)
	}
}

func TestImportRootRules(t *testing.T) {
	for _, raw := range []string{`{}`, `null`, `{"mcpServers":null}`, `{"mcpServers":{},"typo":true}`, `{"mcpServers":{"bad":{"command":"a","command":"b"}}}`, `{"mcpServers":{},"imports":null}`, `{"mcpServers":{},"imports":[1]}`, `{"mcpServers":{}} {}`} {
		_, e := ImportMcporter([]byte(raw), nil)
		if !errors.Is(e, ErrConfig) {
			t.Fatal(raw, e)
		}
	}
	r := converted(t, []byte(`{"mcpServers":{"good":{"command":"fixture"}},"imports":["/must-not-follow"]}`), nil)
	if !hasIssue(r.Issues, "imports", "unsupported_imports") || importRow(t, r, "good").Applicable || !hasIssue(importRow(t, r, "good").Unresolved, "imports", "unsupported_imports") {
		t.Fatal(r)
	}
}

func TestImportSecretInArgs(t *testing.T) {
	cases := [][]string{{"--token=canary"}, {"--password", "canary"}, {"-e", "API_KEY=canary"}, {"${TOKEN}"}, {"https://user:canary@x.invalid"}, {"https://x.invalid?access_token=canary"}, {"op://Fixture/import/canary"}, {"-H", "Authorization: canary"}, {"--header=X-API-Key: canary"}, {"authorization: canary"}, {"--CLIENT-SECRET", "canary"}, {"-e", "NAME=${TOKEN}"}}
	for _, args := range cases {
		t.Run(args[0], func(t *testing.T) {
			a, _ := json.Marshal(args)
			r := assertBlocked(t, `{"mcpServers":{"bad":{"command":"fixture","args":`+string(a)+`},"good":{"command":"fixture","args":["-e","arg-4","--autoConnect"]}}}`, "secret_in_args", "mcpServers.bad.args[0]")
			if !importRow(t, r, "good").Applicable {
				t.Fatal(r)
			}
		})
	}
}

func TestImportLiteralsAndProfiles(t *testing.T) {
	for _, entry := range []string{`{"baseUrl":"https://x.invalid","auth":"oauth","oauthClientSecret":"canary"}`, `{"command":"fixture","env":{"API_KEY":"canary"}}`, `{"baseUrl":"https://x.invalid","headers":{"aPi-KeY":"canary"}}`} {
		r := converted(t, []byte(`{"mcpServers":{"bad":`+entry+`}}`), nil)
		row := importRow(t, r, "bad")
		if row.Applicable || !slices.ContainsFunc(row.Unresolved, func(i ImportIssue) bool { return i.Code == "potential_secret" }) {
			t.Fatal(row)
		}
		data, _ := json.Marshal(r)
		if strings.Contains(string(data), "canary") {
			t.Fatal(string(data))
		}
	}
	r := converted(t, []byte(`{"mcpServers":{"good":{"command":"fixture","env":{"PROXMOX_TOKEN_NAME":"public"}}}}`), nil)
	if !importRow(t, r, "good").Applicable {
		t.Fatal(r)
	}
	r = converted(t, []byte(`{"mcpServers":{"bad":{"command":"fixture","env":{"A":"${A}","B":"${B}"}}}}`), map[string]CredentialBinding{"A": {Profile: "a", Secret: "op://v/i/a"}, "B": {Profile: "b", Secret: "op://v/i/b"}})
	if importRow(t, r, "bad").Applicable || !hasIssue(importRow(t, r, "bad").Unresolved, "mcpServers.bad.credentialProfile", "profile_conflict") {
		t.Fatal(r)
	}
}

func TestImportHTTPAndSSE(t *testing.T) {
	raw, b := importFixture(t)
	r := converted(t, raw, b)
	if r.Definitions.Connections["paper"].Transport.HTTP.AllowInsecureHTTP != "explicit" || !hasIssue(importRow(t, r, "paper").Warnings, "mcpServers.paper.baseUrl", "insecure_http") {
		t.Fatal("paper")
	}
	r = converted(t, []byte(`{"mcpServers":{"loop":{"baseUrl":"http://127.0.0.1:1234/mcp"},"sse":{"baseUrl":"https://x.invalid/sse"}}}`), nil)
	if r.Definitions.Connections["loop"].Transport.HTTP.AllowInsecureHTTP != "loopback" || !hasIssue(importRow(t, r, "loop").Warnings, "mcpServers.loop.baseUrl", "insecure_http") {
		t.Fatal(r)
	}
	h := r.Definitions.Connections["sse"].Transport.HTTP
	if *h.URL.Literal != "https://x.invalid/sse" || h.Mode != "auto" || !hasIssue(importRow(t, r, "sse").Warnings, "mcpServers.sse.baseUrl", "transport_unverified") {
		t.Fatal(r)
	}
}

func TestImportNoCredentialAccess(t *testing.T) {
	raw, b := importFixture(t)
	for name := range b {
		t.Setenv(name, "")
	}
	before := converted(t, raw, nil)
	for name := range b {
		t.Setenv(name, "credential-canary")
	}
	after := converted(t, raw, nil)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("environment affected conversion")
	}
	data, _ := json.Marshal(after)
	if strings.Contains(string(data), "credential-canary") {
		t.Fatal("leak")
	}
}

func TestImportEntryRules(t *testing.T) {
	for _, tc := range []struct{ entry, code, path string }{
		{`null`, "invalid_field", ""},
		{`{"command":"fixture","baseUrl":"https://x.invalid"}`, "transport_conflict", ""},
		{`{"command":"fixture","headers":{}}`, "transport_conflict", ".headers"},
		{`{"baseUrl":"https://x.invalid","args":[]}`, "transport_conflict", ".args"},
		{`{"command":"fixture","auth":"oauth"}`, "transport_conflict", ".auth"},
		{`{"baseUrl":"https://x.invalid","clientName":"public"}`, "auth_required_in_definition", ".clientName"},
		{`{"command":"fixture","args":null}`, "invalid_field", ".args"},
		{`{"command":"fixture","env":{"A":"$NAME"}}`, "unsupported_expansion", ".env.A"},
		{`{"command":"fixture","env":{"A":"$(command)"}}`, "unsupported_expansion", ".env.A"},
		{"{\"command\":\"fixture\",\"env\":{\"A\":\"`command`\"}}", "unsupported_expansion", ".env.A"},
		{`{"baseUrl":"https://x.invalid?token=canary"}`, "secret_in_args", ".baseUrl"},
		{`{"baseUrl":"https://x.invalid","headers":{"Authorization":"","authorization":""}}`, "invalid_field", ".headers.authorization"},
		{`{"baseUrl":"https://x.invalid","headers":{"A":"line\r\nbreak"}}`, "invalid_field", ".headers.A"},
		{`{"baseUrl":"https://x.invalid","auth":"oauth","oauthScope":"scope\tother"}`, "invalid_definition", ""},
	} {
		t.Run(tc.code+tc.path, func(t *testing.T) {
			assertBlocked(t, `{"mcpServers":{"bad":`+tc.entry+`}}`, tc.code, "mcpServers.bad"+tc.path)
		})
	}
	r := converted(t, []byte(`{"mcpServers":{"Invalid Name":{"command":"fixture"}}}`), nil)
	if len(r.Aliases) != 0 || !hasIssue(r.Entries[0].Unresolved, "mcpServers.Invalid Name", "invalid_id") {
		t.Fatal(r)
	}
	r = converted(t, []byte(`{"mcpServers":{"good":{"command":"fixture"}}}`), map[string]CredentialBinding{"UNUSED": {Profile: "work", Secret: "op://v/i/f"}})
	if !hasIssue(r.Issues, "bindings.UNUSED", "unused_binding") || !r.Entries[0].Applicable {
		t.Fatal(r)
	}
}

func TestImportInspectsAllFields(t *testing.T) {
	r := converted(t, []byte(`{"mcpServers":{"bad":{"command":"fixture","env":{"A":null,"TOKEN":"${MISSING}"},"headers":{"Authorization":"canary"},"oauthClientSecret":"canary","typo":true}}}`), nil)
	row := importRow(t, r, "bad")
	for _, want := range []ImportIssue{{Path: "mcpServers.bad.env.A", Code: "invalid_field"}, {Path: "mcpServers.bad.headers.Authorization", Code: "potential_secret"}, {Path: "mcpServers.bad.oauthClientSecret", Code: "potential_secret"}, {Path: "mcpServers.bad.typo", Code: "unknown_field"}} {
		if !slices.Contains(row.Unresolved, want) {
			t.Fatalf("missing %+v in %+v", want, row)
		}
	}
	if !slices.Contains(row.Warnings, ImportIssue{Path: "mcpServers.bad.env.TOKEN", Code: "environment_reference", Variable: "MISSING"}) || row.Applicable {
		t.Fatalf("env reference: %+v", row)
	}
}

func TestImportInspectsConflictingArgs(t *testing.T) {
	r := assertBlocked(t, `{"mcpServers":{"bad":{"command":"fixture","baseUrl":"https://x.invalid","args":["--token=canary"]}}}`, "secret_in_args", "mcpServers.bad.args[0]")
	if !hasIssue(importRow(t, r, "bad").Unresolved, "mcpServers.bad", "transport_conflict") {
		t.Fatal(r)
	}
}

func TestImportExpansionInOAuthFields(t *testing.T) {
	for _, field := range []string{"clientName", "oauthScope", "oauthRedirectUrl", "oauthTokenEndpointAuthMethod"} {
		assertBlocked(t, `{"mcpServers":{"bad":{"baseUrl":"https://x.invalid","auth":"oauth","`+field+`":"$NAME"}}}`, "unsupported_expansion", "mcpServers.bad."+field)
	}
}

func TestImportCombinedCatalogLimit(t *testing.T) {
	servers := map[string]any{}
	for i := range 100 {
		servers[fmt.Sprintf("server-%d", i)] = map[string]any{"command": "fixture", "description": strings.Repeat("x", maxConfigBytes/100-60)}
	}
	raw, e := json.Marshal(map[string]any{"mcpServers": servers})
	if e != nil || len(raw) > maxConfigBytes {
		t.Fatal(len(raw), e)
	}
	r := converted(t, raw, nil)
	data, e := json.Marshal(r.Definitions)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = DecodeCatalog(data); e != nil {
		t.Fatal("combined definitions invalid", len(data), e)
	}
	if len(r.Definitions.Connections) == 0 || len(r.Definitions.Connections) == 100 {
		t.Fatal("expected safe subset at catalog byte limit")
	}
	for _, row := range r.Entries {
		if !row.Applicable && !hasIssue(row.Unresolved, "mcpServers."+row.ID, "invalid_definition") {
			t.Fatal(row)
		}
	}
}

func expectedImportValue(text string) Value {
	prefix, rest, ok := strings.Cut(text, "${")
	if !ok {
		return Literal(text)
	}
	variable, suffix, _ := strings.Cut(rest, "}")
	return Value{Secret: &SecretRef{Secret: "op://Fixture/import/" + variable, Prefix: prefix, Suffix: suffix}}
}

func TestImportAliasSurvivesCatalogCollision(t *testing.T) {
	store, _ := importStore(t, false)
	report := converted(t, []byte(`{"mcpServers":{"paper":{"command":"fixture"}}}`), nil)
	if _, e := ApplyImport(context.Background(), store, 3, report, nil); e != nil {
		t.Fatal(e)
	}
	_, e := store.Update(context.Background(), 4, func(s *State) error {
		addStoreSource(s)
		c := s.Catalogs["github-1"]
		c.Connections = map[string]Connection{"paper": storeConnection()}
		s.Catalogs["github-1"] = c
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	state, e := store.Read(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	effective, e := Resolve(state)
	if e != nil {
		t.Fatal(e)
	}
	ids := sortedKeys(effective.Connections)
	id, e := ResolveID("paper", effective.Aliases, ids)
	if e != nil || id != "local:paper" {
		t.Fatal(id, e)
	}
	id, e = ResolveID("github:example/tools#paper", effective.Aliases, ids)
	if e != nil || id != "github:example/tools#paper" {
		t.Fatal(id, e)
	}
}

func TestImportPrivacyCorpus(t *testing.T) {
	for _, entry := range []string{`{"command":"fixture","env":{"API_KEY":"ENV-CANARY"}}`, `{"baseUrl":"https://x.invalid","headers":{"Authorization":"HEADER-CANARY"}}`, `{"baseUrl":"https://x.invalid","auth":"oauth","oauthClientSecret":"OAUTH-CANARY"}`, `{"command":"fixture","args":["--password","ARG-CANARY"]}`, `{"command":"fixture","unknown":"UNKNOWN-CANARY"}`} {
		t.Run(entry, func(t *testing.T) {
			report := converted(t, []byte(`{"mcpServers":{"bad":`+entry+`,"good":{"command":"fixture"}}}`), nil)
			if importRow(t, report, "bad").Applicable || !importRow(t, report, "good").Applicable {
				t.Fatal(report)
			}
			store, p := importStore(t, false)
			failed, e := ApplyImport(context.Background(), store, 3, report, nil)
			var blocked *ImportBlockedError
			if !errors.As(e, &blocked) || failed.Revision != nil || failed.Applied {
				t.Fatal(failed, e)
			}
			raw, _ := json.Marshal(blocked.Report)
			if strings.Contains(string(raw)+e.Error(), "CANARY") {
				t.Fatal("report leak")
			}
			if _, e = ApplyImport(context.Background(), store, 3, report, []string{"good"}); e != nil {
				t.Fatal(e)
			}
			for _, data := range stateFiles(t, p) {
				if strings.Contains(data, "CANARY") {
					t.Fatal("destination leak")
				}
			}
		})
	}
}

func TestImportEmptyOptionalFields(t *testing.T) {
	report := converted(t, []byte(`{"mcpServers":{"stdio":{"command":"fixture","args":[],"env":{},"description":""},"http":{"baseUrl":"https://fixture.invalid","headers":{},"auth":"oauth","clientName":"","oauthScope":""}}}`), nil)
	if len(report.Definitions.Connections) != 2 || !importRow(t, report, "stdio").Applicable || !importRow(t, report, "http").Applicable {
		t.Fatal(report)
	}
	s := report.Definitions.Connections["stdio"]
	h := report.Definitions.Connections["http"]
	if s.Description != "" || len(s.Transport.Stdio.Args) != 0 || len(s.Transport.Stdio.Env) != 0 || len(h.Transport.HTTP.Headers) != 0 || len(h.Auth.Scopes) != 0 || h.Auth.ClientName != "" || h.Auth.ClientID != nil || h.Auth.ClientSecret != nil {
		t.Fatal(s, h)
	}
}
