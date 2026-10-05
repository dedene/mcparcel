package config

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestImportReferenceAffixes(t *testing.T) {
	b := map[string]CredentialBinding{"TREG_TOKEN": {Profile: "work", Secret: "op://Fixture/import/TREG_TOKEN"}}
	v, p, i := importValue("Bearer ${TREG_TOKEN}!", "headers.Authorization", b, false)
	if i != nil || p != "work" || v.Secret == nil || v.Secret.Prefix != "Bearer " || v.Secret.Suffix != "!" || v.Secret.Secret != b["TREG_TOKEN"].Secret {
		t.Fatal(v, p, i)
	}
	for text, code := range map[string]string{"${UNKNOWN}": "unresolved_credential", "${TREG_TOKEN}${UNKNOWN}": "multiple_references", "${bad-name}": "invalid_reference", "${TREG_TOKEN}${oops": "invalid_reference"} {
		_, _, i := importValue(text, "test", b, false)
		if i == nil || i.Code != code {
			t.Fatal(text, i)
		}
	}
}

func TestDecodeBindings(t *testing.T) {
	valid := `{"API_KEY":{"profile":"work","secret":"op://Fixture/service/key"}}`
	b, e := DecodeBindings([]byte(valid))
	if e != nil || b["API_KEY"].Profile != "work" {
		t.Fatal(b, e)
	}
	for _, raw := range []string{`null`, `{"API_KEY":null}`, `{"BAD-NAME":{"profile":"work","secret":"op://v/i/f"}}`, `{"A":{"profile":"Work","secret":"op://v/i/f"}}`, `{"A":{"profile":"work","secret":"canary"}}`, `{"A":{"profile":"work"}}`, `{"A":{"secret":"op://v/i/f"}}`, `{"A":{"profile":"work","secret":"op://v/i/f","extra":true}}`, `{"A":{"profile":"work","profile":"work","secret":"op://v/i/f"}}`} {
		_, e := DecodeBindings([]byte(raw))
		if !errors.Is(e, ErrConfig) || strings.Contains(e.Error(), "canary") {
			t.Fatal(raw, e)
		}
	}
	_, e = ImportMcporter([]byte(`{"mcpServers":{}}`), map[string]CredentialBinding{"A": {Profile: "work", Secret: "bad"}})
	if !errors.Is(e, ErrConfig) {
		t.Fatal(e)
	}
}

func TestImportSecretInAssignments(t *testing.T) {
	for _, tc := range []struct {
		name, entry string
		blocked     bool
	}{
		{"endpoint", `{"command":"fixture","args":["--endpoint=https://x.invalid?token=IMPORT_SECRET_CANARY"]}`, true},
		{"service", `{"command":"fixture","args":["SERVICE_URL=https://user:IMPORT_SECRET_CANARY@x.invalid"]}`, true},
		{"docker assignment", `{"command":"fixture","args":["--env=API_KEY=IMPORT_SECRET_CANARY"]}`, true},
		{"docker single argument", `{"command":"fixture","args":["-e API_KEY=IMPORT_SECRET_CANARY"]}`, true},
		{"docker two arguments", `{"command":"fixture","args":["-e","API_KEY=IMPORT_SECRET_CANARY"]}`, true},
		{"database url", `{"command":"fixture","env":{"DATABASE_URL":"https://user:IMPORT_SECRET_CANARY@x.invalid"}}`, true},
		{"env query", `{"command":"fixture","env":{"ENDPOINT":"https://x.invalid?auth=IMPORT_SECRET_CANARY"}}`, true},
		{"header query", `{"baseUrl":"https://x.invalid","headers":{"X-Endpoint":"https://x.invalid?client_secret=IMPORT_SECRET_CANARY"}}`, true},
		{"base query", `{"baseUrl":"https://x.invalid?auth=IMPORT_SECRET_CANARY"}`, true},
		{"ordinary assignment", `{"command":"fixture","args":["--flag=value","NAME=value","--env=NAME=value","-e NAME=value"]}`, false},
		{"reference", `{"command":"fixture","env":{"API_KEY":"${NAME}"}}`, false},
		{"plain url", `{"command":"fixture","args":["--endpoint=https://x.invalid?mode=ordinary"],"env":{"DATABASE_URL":"https://x.invalid"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := ImportMcporter([]byte(`{"mcpServers":{"test":`+tc.entry+`}}`), map[string]CredentialBinding{"NAME": {Profile: "work", Secret: "op://Fixture/service/key"}})
			if err != nil {
				t.Fatal(err)
			}
			if r.Entries[0].Applicable == tc.blocked {
				t.Fatalf("applicable=%v, want blocked=%v", r.Entries[0].Applicable, tc.blocked)
			}
			if tc.blocked {
				if _, ok := r.Definitions.Connections["test"]; ok {
					t.Fatal("blocked definition retained")
				}
				if !hasImportIssue(r.Entries[0].Unresolved, r.Entries[0].Unresolved[0].Path, "secret_in_args") {
					t.Fatal(r.Entries[0].Unresolved)
				}
			}
			data, _ := json.Marshal(r)
			if strings.Contains(string(data), "IMPORT_SECRET_CANARY") {
				t.Fatal("report leaked credential")
			}
		})
	}
}

func TestImportEnvReference(t *testing.T) {
	b := map[string]CredentialBinding{"TREG_TOKEN": {Profile: "work", Secret: "op://Fixture/import/TREG_TOKEN"}}
	v, p, i := importValue("Bearer ${UNKNOWN}!", "headers.Authorization", b, true)
	if i != nil || p != "" || v.Secret == nil || *v.Secret != (SecretRef{Secret: "env:UNKNOWN", Prefix: "Bearer ", Suffix: "!"}) {
		t.Fatal(v, p, i)
	}
	for text, env := range map[string]bool{"Bearer ${UNKNOWN}!": false, "${GH_TOKEN}": true} {
		if _, _, i = importValue(text, "test", b, env); i == nil || i.Code != "unresolved_credential" {
			t.Fatal(text, i)
		}
	}
	v, p, i = importValue("${TREG_TOKEN}", "test", b, true)
	if i != nil || p != "work" || v.Secret == nil || v.Secret.Secret != b["TREG_TOKEN"].Secret {
		t.Fatal(v, p, i)
	}
}

func TestImportEnvBridgeAll32(t *testing.T) {
	raw, b := importFixture(t)
	for name := range b {
		t.Setenv(name, "credential-canary")
	}
	r := converted(t, raw, nil)
	applicable, blocked, warnings := 0, []string{}, 0
	unresolved := []ImportIssue{}
	for _, row := range r.Entries {
		if row.Applicable {
			applicable++
		} else {
			blocked = append(blocked, row.ID)
		}
		for _, i := range row.Unresolved {
			if i.Code == "unresolved_credential" {
				unresolved = append(unresolved, i)
			}
		}
		for _, w := range row.Warnings {
			if w.Code == "environment_reference" {
				if w.Variable == "" {
					t.Fatal(w)
				}
				warnings++
			}
		}
	}
	want := []ImportIssue{{Path: "mcpServers.front-mcp.oauthClientId", Code: "unresolved_credential", Variable: "FRONT_MCP_CLIENT_ID"}, {Path: "mcpServers.front-mcp.oauthClientSecret", Code: "unresolved_credential", Variable: "FRONT_MCP_CLIENT_SECRET"}}
	if applicable != 31 || len(blocked) != 1 || blocked[0] != "front-mcp" || warnings != 12 || len(unresolved) != 2 || unresolved[0] != want[0] || unresolved[1] != want[1] {
		t.Fatal(applicable, blocked, warnings, unresolved)
	}
	defs := r.Definitions.Connections
	exa, proxmox, treg := defs["exa"], defs["proxmox-mcp-plus"], defs["treg"]
	if s := exa.Transport.Stdio.Env["EXA_API_KEY"].Secret; s == nil || *s != (SecretRef{Secret: "env:EXA_API_KEY"}) {
		t.Fatal(s)
	}
	if s := proxmox.Transport.Stdio.Env["PROXMOX_TOKEN_VALUE"].Secret; s == nil || s.Secret != "env:PROXMOX_TOKEN" {
		t.Fatal(s)
	}
	if s := treg.Transport.HTTP.Headers["Authorization"].Secret; s == nil || *s != (SecretRef{Secret: "env:TREG_TOKEN", Prefix: "Bearer "}) {
		t.Fatal(s)
	}
	for _, id := range []string{"exa", "proxmox-mcp-plus", "treg"} {
		if defs[id].CredentialProfile != "" || importRow(t, r, id).CredentialProfile != "" {
			t.Fatal(id)
		}
	}
	data, e := json.Marshal(r.Definitions)
	if e != nil {
		t.Fatal(e)
	}
	if e = validateSchema(t, "catalog", data); e != nil {
		t.Fatal(e)
	}
	if _, e = DecodeCatalog(data); e != nil {
		t.Fatal(e)
	}
	store, p := importStore(t, false)
	out, e := ApplyImport(context.Background(), store, 3, r, []string{"exa"})
	if e != nil {
		t.Fatal(e)
	}
	s, e := store.Read(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	stored := s.Personal.Connections["exa"]
	if v := stored.Transport.Stdio.Env["EXA_API_KEY"].Secret; v == nil || v.Secret != "env:EXA_API_KEY" || stored.CredentialProfile != "" || len(s.Local.CredentialProfiles) != 0 {
		t.Fatal(stored, s.Local.CredentialProfiles)
	}
	for _, content := range stateFiles(t, p) {
		if strings.Contains(content, "credential-canary") {
			t.Fatal("state leaked credential")
		}
	}
	for _, report := range []ImportReport{r, out} {
		data, _ = json.Marshal(report)
		if strings.Contains(string(data), "credential-canary") {
			t.Fatal("report leaked credential")
		}
	}
}
