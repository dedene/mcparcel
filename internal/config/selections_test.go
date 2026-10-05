package config_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

func TestLocalProfileUnion(t *testing.T) {
	for _, tt := range []struct {
		name, profile string
		valid         bool
	}{
		{"serviceAccount", `{"mode":"desktop-service-account","account":"Fixture","bootstrapRef":"op://v/i/token"}`, true},
		{"desktop", `{"mode":"desktop","account":"Fixture"}`, true},
		{"desktopBootstrap", `{"mode":"desktop","account":"Fixture","bootstrapRef":""}`, false},
		{"environment", `{"mode":"environment","account":"Fixture"}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			l, err := config.DecodeLocal([]byte(`{"schemaVersion":1,"credentialProfiles":{"team":` + tt.profile + `}}`))
			if !tt.valid {
				if !errors.Is(err, config.ErrConfig) {
					t.Fatal(err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if l.CredentialProfiles["team"].SessionDuration != "24h" {
				t.Fatal(l)
			}
		})
	}
}

func TestSelectionsStrict(t *testing.T) {
	raw := `{"schemaVersion":1,"revision":0,"connections":{"local:paper":{"enabled":false}}}`
	s, err := config.DecodeSelections([]byte(raw))
	if err != nil || s.Revision != 0 || s.Connections["local:paper"].Enabled {
		t.Fatal(s, err)
	}
	cases := []string{
		strings.Replace(raw, `"enabled":false`, ``, 1),
		strings.Replace(raw, `"revision":0,`, ``, 1),
		strings.Replace(raw, `"revision":0`, `"revision":-1`, 1),
		strings.Replace(raw, `"revision":0`, `"revision":0.5`, 1),
		strings.Replace(raw, `"revision":0`, `"revision":9007199254740992`, 1),
	}
	for _, raw := range cases {
		if _, err := config.DecodeSelections([]byte(raw)); !errors.Is(err, config.ErrConfig) {
			t.Fatal(raw, err)
		}
	}
}

func TestIntegralJSONNumbers(t *testing.T) {
	c, err := config.DecodeCatalog([]byte(`{"schemaVersion":1.0,"connections":{}}`))
	if err != nil || c.SchemaVersion != 1 {
		t.Fatal(c, err)
	}
	s, err := config.DecodeSelections([]byte(`{"schemaVersion":1,"revision":1e3,"connections":{}}`))
	if err != nil || s.Revision != 1000 {
		t.Fatal(s, err)
	}
	l, err := config.DecodeLocal([]byte(`{"schemaVersion":1,"sources":[{"id":"github-9007199254740991","repositoryId":9007199254740991,"owner":"owner","repo":"repo","path":"catalog.json","ref":"main","commit":"0123456789abcdef0123456789abcdef01234567","pinned":false}]}`))
	if err != nil || l.Sources[0].RepositoryID != config.MaxRevision {
		t.Fatal(l, err)
	}
}

func TestValidateStateProgrammatic(t *testing.T) {
	c, err := config.DecodeCatalog([]byte(fullCatalog))
	if err != nil {
		t.Fatal(err)
	}
	state := config.State{Local: config.Local{SchemaVersion: 1}, Personal: c, Selections: config.Selections{SchemaVersion: 1, Connections: map[string]config.Selection{"local:paper": {Enabled: true}}}}
	if err := config.ValidateState(state); err != nil {
		t.Fatal("missing bindings are entry blockers", err)
	}
	state.Selections.Connections["local:paper"] = config.Selection{Inputs: map[string]string{"unknown": "value"}}
	if err := config.ValidateState(state); !errors.Is(err, config.ErrConfig) {
		t.Fatal(err)
	}
	state.Selections.Connections = map[string]config.Selection{"local:removed": {Inputs: map[string]string{"unknown": "value"}, CredentialProfile: "missing"}}
	if err := config.ValidateState(state); err != nil {
		t.Fatal("unavailable entry", err)
	}
	state.Personal.Connections["paper"] = config.Connection{}
	if err := config.ValidateState(state); !errors.Is(err, config.ErrConfig) {
		t.Fatal("invalid programmatic definition", err)
	}
}

func TestLocalSourcesAndAliases(t *testing.T) {
	raw := `{"schemaVersion":1,"sources":[{"id":"github-42","repositoryId":42,"owner":"owner","repo":"repo.name","path":"config/catalog.json","ref":"main","commit":"0123456789abcdef0123456789abcdef01234567","pinned":false}],"aliases":{"paper":"github:owner/repo.name#paper"},"runtime":{"keepAlive":true}}`
	if _, err := config.DecodeLocal([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ old, replacement string }{
		{`"id":"github-42"`, `"id":"github-43"`},
		{`"repositoryId":42`, `"repositoryId":0`},
		{`"owner":"owner"`, `"owner":"Owner"`},
		{`"repo":"repo.name"`, `"repo":".."`},
		{`"path":"config/catalog.json"`, `"path":"config/../catalog.json"`},
		{`"ref":"main"`, `"ref":""`},
		{`"commit":"0123456789abcdef0123456789abcdef01234567"`, `"commit":"0123456789ABCDEF0123456789abcdef01234567"`},
		{`"paper":"github:owner/repo.name#paper"`, `"paper":"github:Owner/repo.name#paper"`},
	} {
		if _, err := config.DecodeLocal([]byte(strings.Replace(raw, tt.old, tt.replacement, 1))); !errors.Is(err, config.ErrConfig) {
			t.Fatal(tt, err)
		}
	}
}

func TestLocalRuntimeApprovalDialog(t *testing.T) {
	local, err := config.DecodeLocal([]byte(`{"schemaVersion":1,"runtime":{"keepAlive":true}}`))
	if err != nil || local.Runtime == nil || local.Runtime.ApprovalDialog {
		t.Fatal("default", local.Runtime, err)
	}
	local, err = config.DecodeLocal([]byte(`{"schemaVersion":1,"runtime":{"approvalDialog":true}}`))
	if err != nil || local.Runtime == nil || !local.Runtime.ApprovalDialog {
		t.Fatal("set", local.Runtime, err)
	}
	b, err := json.Marshal(local.Runtime)
	if err != nil || string(b) != `{"approvalDialog":true}` {
		t.Fatal(string(b), err)
	}
	if _, err = config.DecodeLocal([]byte(`{"schemaVersion":1,"runtime":{"approvalDialog":"yes"}}`)); !errors.Is(err, config.ErrConfig) {
		t.Fatal("string", err)
	}
}

func TestValidateStateSourceCorrespondence(t *testing.T) {
	personal, err := config.DecodeCatalog([]byte(`{"schemaVersion":1,"connections":{"paper":{"transport":{"type":"stdio","command":"fixture"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	local, err := config.DecodeLocal([]byte(`{"schemaVersion":1,"sources":[{"id":"github-42","repositoryId":42,"owner":"owner","repo":"repo","path":"catalog.json","ref":"main","commit":"0123456789abcdef0123456789abcdef01234567","pinned":false}]}`))
	if err != nil {
		t.Fatal(err)
	}
	state := config.State{Local: local, Personal: personal, Selections: config.Selections{SchemaVersion: 1, Connections: map[string]config.Selection{}}}
	if err := config.ValidateState(state); !errors.Is(err, config.ErrConfig) {
		t.Fatal("missing source", err)
	}
	state.Catalogs = map[string]config.Catalog{"github-42": personal}
	if err := config.ValidateState(state); err != nil {
		t.Fatal(err)
	}
	state.Catalogs["github-43"] = personal
	if err := config.ValidateState(state); !errors.Is(err, config.ErrConfig) {
		t.Fatal("extra source", err)
	}
	delete(state.Catalogs, "github-43")
	state.Selections.Connections["local:paper"] = config.Selection{CredentialProfile: "team"}
	if err := config.ValidateState(state); !errors.Is(err, config.ErrConfig) {
		t.Fatal("unexpected profile binding", err)
	}
}
