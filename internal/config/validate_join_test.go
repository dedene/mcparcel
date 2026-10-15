package config_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

// joinCatalog is a catalog that uses both join fields; edit changes its
// decoded JSON before it is encoded again.
func joinCatalog(t *testing.T, edit func(map[string]any)) string {
	t.Helper()
	var doc map[string]any
	raw := `{"schemaVersion":1,"minVersion":"0.2.0",
		"credentialProfiles":{"team":{"description":"Team vault","suggest":{"mode":"desktop","account":"example.1password.com"}}},
		"presets":[{"id":"developer","label":"Developer","description":"Code and docs","connections":["paper","docs"]},{"id":"support","label":"Support","connections":["docs"]}],
		"connections":{"paper":{"transport":{"type":"stdio","command":"fixture"}},"docs":{"credentialProfile":"team","transport":{"type":"http","url":"https://docs.example.invalid/mcp"}}}}`
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(doc)
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func preset(doc map[string]any, i int) map[string]any {
	return doc["presets"].([]any)[i].(map[string]any)
}

func suggestion(doc map[string]any) map[string]any {
	return doc["credentialProfiles"].(map[string]any)["team"].(map[string]any)["suggest"].(map[string]any)
}

func TestJoinFieldsValidation(t *testing.T) {
	for _, c := range []struct {
		name          string
		edit          func(map[string]any)
		schema, valid bool
		path          string
	}{
		{"valid", nil, true, true, ""},
		{"prerelease floor", func(d map[string]any) { d["minVersion"] = "0.2.0-rc.1" }, true, true, ""},
		{"later major", func(d map[string]any) { d["minVersion"] = "1.0.0" }, true, true, ""},
		{"later minor", func(d map[string]any) { d["minVersion"] = "0.10.0" }, true, true, ""},
		{"presets without minVersion", func(d map[string]any) {
			delete(d, "minVersion")
			delete(d["credentialProfiles"].(map[string]any)["team"].(map[string]any), "suggest")
		}, false, false, "minVersion: 0.2.0 or newer required by presets"},
		{"suggest without minVersion", func(d map[string]any) { delete(d, "minVersion"); delete(d, "presets") }, false, false, "minVersion: 0.2.0 or newer required by credentialProfiles.team.suggest"},
		{"minVersion too old", func(d map[string]any) { d["minVersion"] = "0.1.9" }, false, false, "minVersion: 0.2.0 or newer"},
		{"minVersion 0.1 prerelease", func(d map[string]any) { d["minVersion"] = "0.1.0-rc.1" }, false, false, "minVersion"},
		{"suggest service account", func(d map[string]any) { suggestion(d)["mode"] = "service-account" }, false, false, "suggest.mode"},
		{"suggest desktop service account", func(d map[string]any) { suggestion(d)["mode"] = "desktop-service-account" }, false, false, "suggest.mode"},
		{"suggest token", func(d map[string]any) { suggestion(d)["token"] = "ops_secret" }, false, false, "suggest.token"},
		{"suggest bootstrapRef", func(d map[string]any) { suggestion(d)["bootstrapRef"] = "op://Vault/item/token" }, false, false, "suggest.bootstrapRef"},
		{"suggest tokenEnv", func(d map[string]any) { suggestion(d)["tokenEnv"] = "OP_SERVICE_ACCOUNT_TOKEN" }, false, false, "suggest.tokenEnv"},
		{"suggest without account", func(d map[string]any) { delete(suggestion(d), "account") }, false, false, "suggest.account"},
		{"suggest without mode", func(d map[string]any) { delete(suggestion(d), "mode") }, false, false, "suggest.mode"},
		{"account uppercase", func(d map[string]any) { suggestion(d)["account"] = "Example.1password.com" }, false, false, "suggest.account"},
		{"account single label", func(d map[string]any) { suggestion(d)["account"] = "example" }, false, false, "suggest.account"},
		{"account email", func(d map[string]any) { suggestion(d)["account"] = "me@example.1password.com" }, false, false, "suggest.account"},
		{"account empty label", func(d map[string]any) { suggestion(d)["account"] = "example..com" }, false, false, "suggest.account"},
		{"account hyphen edge", func(d map[string]any) { suggestion(d)["account"] = "-example.1password.com" }, false, false, "suggest.account"},
		{"account secret reference", func(d map[string]any) { suggestion(d)["account"] = "op://Vault/item/field" }, false, false, "suggest.account"},
		{"account too long", func(d map[string]any) {
			suggestion(d)["account"] = strings.Repeat(strings.Repeat("a", 63)+".", 4) + "com"
		}, false, false, "suggest.account"},
		{"account digits", func(d map[string]any) { suggestion(d)["account"] = "team-1.eu.1password.eu" }, true, true, ""},
		{"empty presets", func(d map[string]any) { d["presets"] = []any{} }, false, false, "presets"},
		{"duplicate preset ID", func(d map[string]any) { preset(d, 1)["id"] = "developer" }, true, false, "presets[1].id"},
		{"invalid preset ID", func(d map[string]any) { preset(d, 0)["id"] = "Dev" }, false, false, "presets[0].id"},
		{"missing preset ID", func(d map[string]any) { delete(preset(d, 0), "id") }, false, false, "presets[0].id"},
		{"empty label", func(d map[string]any) { preset(d, 0)["label"] = "" }, false, false, "presets[0].label"},
		{"missing label", func(d map[string]any) { delete(preset(d, 0), "label") }, false, false, "presets[0].label"},
		{"multi-line label", func(d map[string]any) { preset(d, 0)["label"] = "Dev\nOps" }, false, false, "presets[0].label"},
		{"escape in description", func(d map[string]any) { preset(d, 0)["description"] = "\x1b[31mred" }, false, false, "presets[0].description"},
		{"long label", func(d map[string]any) { preset(d, 0)["label"] = strings.Repeat("a", 65) }, false, false, "presets[0].label"},
		{"label of 64 characters", func(d map[string]any) { preset(d, 0)["label"] = strings.Repeat("é", 64) }, true, true, ""},
		{"long description", func(d map[string]any) { preset(d, 0)["description"] = strings.Repeat("a", 201) }, false, false, "presets[0].description"},
		{"no connections", func(d map[string]any) { preset(d, 0)["connections"] = []any{} }, false, false, "presets[0].connections"},
		{"missing connections", func(d map[string]any) { delete(preset(d, 0), "connections") }, false, false, "presets[0].connections"},
		{"undeclared connection", func(d map[string]any) { preset(d, 0)["connections"] = []any{"paper", "slack"} }, true, false, "presets[0].connections[1]"},
		{"duplicate connection", func(d map[string]any) { preset(d, 0)["connections"] = []any{"paper", "paper"} }, false, false, "presets[0].connections[1]"},
		{"unknown preset field", func(d map[string]any) { preset(d, 0)["default"] = true }, false, false, "presets[0].default"},
		{"too many presets", func(d map[string]any) {
			var list []any
			for i := range 33 {
				list = append(list, map[string]any{"id": fmt.Sprintf("p%d", i), "label": "P", "connections": []any{"paper"}})
			}
			d["presets"] = list
		}, false, false, "presets"},
	} {
		t.Run(c.name, func(t *testing.T) {
			raw := joinCatalog(t, c.edit)
			config.CheckCatalogCase(t, raw, c.schema, c.valid, c.path)
		})
	}
}

func TestJoinFieldsKeepOrderAndRoundTrip(t *testing.T) {
	cat, err := config.DecodeCatalog([]byte(joinCatalog(t, nil)))
	if err != nil {
		t.Fatal(err)
	}
	want := []config.Preset{
		{ID: "developer", Label: "Developer", Description: "Code and docs", Connections: []string{"paper", "docs"}},
		{ID: "support", Label: "Support", Connections: []string{"docs"}},
	}
	if !reflect.DeepEqual(cat.Presets, want) {
		t.Fatal(cat.Presets)
	}
	s := cat.CredentialProfiles["team"].Suggest
	if s == nil || s.Profile() != (config.Profile{Mode: config.ProfileModeDesktop, Account: "example.1password.com"}) {
		t.Fatal(s)
	}
	raw, err := json.Marshal(cat)
	if err != nil {
		t.Fatal(err)
	}
	again, err := config.DecodeCatalog(raw)
	if err != nil || !reflect.DeepEqual(again, cat) {
		t.Fatal(again, err)
	}
}

func TestPersonalRejectsJoinFields(t *testing.T) {
	for _, c := range []struct {
		name, reason string
		edit         func(map[string]any)
	}{
		{"presets", "personal.presets: allowed only in a GitHub catalog", func(d map[string]any) {
			delete(d, "minVersion")
			delete(d["credentialProfiles"].(map[string]any)["team"].(map[string]any), "suggest")
		}},
		{"suggest", "personal.credentialProfiles.team.suggest: allowed only in a GitHub catalog", func(d map[string]any) { delete(d, "minVersion"); delete(d, "presets") }},
		{"minVersion", "personal.minVersion: allowed only in a GitHub catalog", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			raw := []byte(joinCatalog(t, c.edit))
			if _, err := config.DecodePersonal(raw); !errors.Is(err, config.ErrConfig) || config.FieldReason(err) != c.reason {
				t.Fatal(err)
			}
		})
	}
	personal, err := config.DecodeCatalog([]byte(joinCatalog(t, nil)))
	if err != nil {
		t.Fatal(err)
	}
	personal.MinVersion = ""
	state := config.State{Personal: personal, Local: config.Local{SchemaVersion: 1}, Selections: config.Selections{SchemaVersion: 1, Connections: map[string]config.Selection{}}, Catalogs: map[string]config.Catalog{}}
	if err = config.ValidateState(state); config.FieldReason(err) != "personal.presets: allowed only in a GitHub catalog" {
		t.Fatal(err)
	}
}

func TestPeekMinVersion(t *testing.T) {
	for raw, want := range map[string]string{
		`{"schemaVersion":1,"minVersion":"9.0.0","futureField":{"x":[1]},"connections":{"a":{"transport":{"type":"grpc"}}}}`: "9.0.0",
		`{"minVersion":"0.3.0-rc.1"}`:                 "0.3.0-rc.1",
		`{"schemaVersion":1,"connections":{}}`:        "",
		`{"minVersion":"v9"}`:                         "",
		`{"minVersion":"9.0.0+build"}`:                "",
		`{"minVersion":9}`:                            "",
		`{"minVersion":"9.0.0","minVersion":"0.1.0"}`: "",
		`["9.0.0"]`:             "",
		`{"minVersion":"9.0.0"`: "",
		`{"nested":{"minVersion":"9.0.0"},"other":true}`: "",
	} {
		if got := config.PeekMinVersion([]byte(raw)); got != want {
			t.Errorf("%s: got %q, want %q", raw, got, want)
		}
	}
}

func TestResolveExposesJoinOffer(t *testing.T) {
	cat, err := config.DecodeCatalog([]byte(joinCatalog(t, nil)))
	if err != nil {
		t.Fatal(err)
	}
	cat.Name = "Fixture team"
	source := config.Source{ID: "github-7", RepositoryID: 7, Owner: "fixture", Repo: "team", Path: "mcparcel.json", Ref: "main", Commit: strings.Repeat("a", 40)}
	state := config.State{
		Personal:   config.Personal{SchemaVersion: 1, Connections: map[string]config.Connection{}},
		Local:      config.Local{SchemaVersion: 1, Sources: []config.Source{source}},
		Selections: config.Selections{SchemaVersion: 1, Connections: map[string]config.Selection{}},
		Catalogs:   map[string]config.Catalog{source.ID: cat},
	}
	effective, err := config.Resolve(state)
	if err != nil {
		t.Fatal(err)
	}
	want := config.EffectiveCatalog{
		Name: "Fixture team",
		Presets: []config.EffectivePreset{
			{ID: "developer", Label: "Developer", Description: "Code and docs", Connections: []string{"github:fixture/team#paper", "github:fixture/team#docs"}},
			{ID: "support", Label: "Support", Connections: []string{"github:fixture/team#docs"}},
		},
		Requirements: []config.EffectiveRequirement{{ID: "team", Description: "Team vault", Suggest: &config.ProfileSuggestion{Mode: "desktop", Account: "example.1password.com"}, Connections: []string{"github:fixture/team#docs"}}},
	}
	if len(effective.Catalogs) != 1 || !reflect.DeepEqual(effective.Catalogs[source.ID], want) {
		t.Fatalf("%+v", effective.Catalogs)
	}
	// The offer is a copy: changing it leaves the state alone.
	effective.Catalogs[source.ID].Requirements[0].Suggest.Account = "changed.example.com"
	effective.Catalogs[source.ID].Presets[0].Connections[0] = "changed"
	if state.Catalogs[source.ID].CredentialProfiles["team"].Suggest.Account != "example.1password.com" || state.Catalogs[source.ID].Presets[0].Connections[0] != "paper" {
		t.Fatal("the offer aliases the catalog")
	}
}
