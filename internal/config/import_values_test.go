package config

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestImportReferenceAffixes(t *testing.T) {
	b := map[string]CredentialBinding{"TREG_TOKEN": {Profile: "work", Secret: "op://Fixture/import/TREG_TOKEN"}}
	v, p, i := importValue("Bearer ${TREG_TOKEN}!", "headers.Authorization", b)
	if i != nil || p != "work" || v.Secret == nil || v.Secret.Prefix != "Bearer " || v.Secret.Suffix != "!" || v.Secret.Secret != b["TREG_TOKEN"].Secret {
		t.Fatal(v, p, i)
	}
	for text, code := range map[string]string{"${UNKNOWN}": "unresolved_credential", "${TREG_TOKEN}${UNKNOWN}": "multiple_references", "${bad-name}": "invalid_reference", "${TREG_TOKEN}${oops": "invalid_reference"} {
		_, _, i := importValue(text, "test", b)
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
