package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/dedene/mcparcel/internal/jsonutil"
)

type validationCase struct {
	Name        string `json:"name"`
	Document    string `json:"document"`
	Raw         string `json:"raw"`
	SyntaxValid bool   `json:"syntaxValid"`
	SchemaValid bool   `json:"schemaValid"`
	GoValid     bool   `json:"goValid"`
	Path        string `json:"path,omitempty"`
}

func schemaFile(t *testing.T, document string) *jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile("../../schema/" + document + ".schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var s jsonschema.Schema
	if err = json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	if s.Schema != "https://json-schema.org/draft/2020-12/schema" || s.ID != "https://mcparcel.dev/schema/"+document+".schema.json" {
		t.Fatal("schema identifiers", s.Schema, s.ID)
	}
	return &s
}

func validateSchema(t *testing.T, document string, raw []byte) error {
	t.Helper()
	v, err := jsonutil.Decode(raw)
	if err != nil {
		return err
	}
	resolved, err := schemaFile(t, document).Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := schemaInstance(v)
	if err != nil {
		return err
	}
	return resolved.Validate(instance)
}

// v0.4.3 classifies json.Number as a string in its type check. These
// document schemas permit only bounded integer numbers, so convert them
// exactly and reject fractions/overflow without passing through float64.
func schemaInstance(v any) (any, error) {
	switch v := v.(type) {
	case json.Number:
		r, ok := new(big.Rat).SetString(string(v))
		if !ok || !r.IsInt() {
			return nil, errors.New("schema instance requires integer numbers")
		}
		if r.Num().IsInt64() {
			return r.Num().Int64(), nil
		}
		if r.Num().IsUint64() {
			return r.Num().Uint64(), nil
		}
		return nil, errors.New("schema instance integer overflow")
	case map[string]any:
		next := make(map[string]any, len(v))
		for k, x := range v {
			converted, err := schemaInstance(x)
			if err != nil {
				return nil, err
			}
			next[k] = converted
		}
		return next, nil
	case []any:
		next := make([]any, len(v))
		for i, x := range v {
			converted, err := schemaInstance(x)
			if err != nil {
				return nil, err
			}
			next[i] = converted
		}
		return next, nil
	}
	return v, nil
}

func decodeDocument(document string, raw []byte) error {
	switch document {
	case "catalog":
		_, err := DecodeCatalog(raw)
		return err
	case "config":
		_, err := DecodeLocal(raw)
		return err
	case "selections":
		_, err := DecodeSelections(raw)
		return err
	}
	return fmt.Errorf("unknown document %s", document)
}

func schemaCases(t *testing.T) []validationCase {
	t.Helper()
	data, err := os.ReadFile("../../testdata/catalogs/validation-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []validationCase
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("empty corpus")
	}
	return cases
}

func checkValidationCase(t *testing.T, c validationCase) {
	t.Helper()
	_, syntaxErr := jsonutil.Decode([]byte(c.Raw))
	schemaErr := validateSchema(t, c.Document, []byte(c.Raw))
	goErr := decodeDocument(c.Document, []byte(c.Raw))
	if (syntaxErr == nil) != c.SyntaxValid || (schemaErr == nil) != c.SchemaValid || (goErr == nil) != c.GoValid {
		t.Fatalf("syntax=%v schema=%v Go=%v; want %t/%t/%t", syntaxErr, schemaErr, goErr, c.SyntaxValid, c.SchemaValid, c.GoValid)
	}
	if goErr != nil && (!errors.Is(goErr, ErrConfig) || c.Path != "" && !strings.Contains(goErr.Error(), c.Path)) {
		t.Fatalf("Go error %v; want ErrConfig at %s", goErr, c.Path)
	}
}

func TestSchemaCorpus(t *testing.T) {
	for _, document := range []string{"catalog", "config", "selections"} {
		t.Run("valid-"+document, func(t *testing.T) {
			raw, err := os.ReadFile("../../testdata/catalogs/valid-" + document + ".json")
			if err != nil {
				t.Fatal(err)
			}
			checkValidationCase(t, validationCase{Document: document, Raw: string(raw), SyntaxValid: true, SchemaValid: true, GoValid: true})
		})
	}
	t.Run("valid-state", func(t *testing.T) {
		read := func(name string) []byte {
			data, err := os.ReadFile("../../testdata/catalogs/valid-" + name + ".json")
			if err != nil {
				t.Fatal(err)
			}
			return data
		}
		personal, err := DecodeCatalog(read("catalog"))
		if err != nil {
			t.Fatal(err)
		}
		local, err := DecodeLocal(read("config"))
		if err != nil {
			t.Fatal(err)
		}
		selections, err := DecodeSelections(read("selections"))
		if err != nil {
			t.Fatal(err)
		}
		catalogs := make(map[string]Catalog)
		for _, source := range local.Sources {
			catalogs[source.ID] = personal
		}
		if err = ValidateState(State{Personal: personal, Local: local, Selections: selections, Catalogs: catalogs}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("grammars", func(t *testing.T) {
		for _, entry := range []struct{ document, name, pattern string }{
			{"catalog", "Identifier", identifier.String()},
			{"catalog", "EnvName", envName.String()},
			{"catalog", "HeaderName", headerName.String()},
			{"config", "Identifier", identifier.String()},
			{"config", "Owner", ownerGrammar.String()},
			{"config", "Repo", repoGrammar.String()},
			{"config", "Commit", commitGrammar.String()},
			{"selections", "Identifier", identifier.String()},
		} {
			s := schemaFile(t, entry.document).Defs[entry.name]
			if s == nil || s.Pattern != entry.pattern {
				t.Fatalf("%s %s grammar must equal %q", entry.document, entry.name, entry.pattern)
			}
		}
		for _, id := range []string{"local:paper", "github:owner/repo.name#paper", "github:o/r#x", "local:Upper", "github:Owner/repo#paper", "github:-owner/repo#paper", "github:owner/..#paper", "github:owner/repo#paper#more", "local:paper ", "github:owner/repo/extra#paper"} {
			data, _ := json.Marshal(map[string]any{"schemaVersion": 1, "aliases": map[string]string{"alias": id}})
			checkValidationCase(t, validationCase{Document: "config", Raw: string(data), SyntaxValid: true, SchemaValid: canonicalID(id), GoValid: canonicalID(id)})
		}
	})
	seen := map[string]bool{}
	for _, c := range schemaCases(t) {
		if c.Name == "" || seen[c.Name] {
			t.Fatal("missing or duplicate case name", c.Name)
		}
		seen[c.Name] = true
		t.Run(c.Name, func(t *testing.T) { checkValidationCase(t, c) })
	}
}

func TestSchemaRejectsUnknownAndDuplicate(t *testing.T) {
	cases := []validationCase{
		{Name: "duplicate", Document: "catalog", Raw: `{"schemaVersion":1,"connections":{"paper":{"transport":{"type":"stdio","command":"a","command":"b"}}}}`, Path: "connections.paper.transport.command"},
		{Name: "oauth", Document: "catalog", Raw: `{"schemaVersion":1,"connections":{"paper":{"transport":{"type":"http","url":"https://example.invalid"},"auth":{"type":"oauth","extra":"x"}}}}`, SyntaxValid: true, Path: "connections.paper.auth.extra"},
		{Name: "input", Document: "catalog", Raw: `{"schemaVersion":1,"connections":{"paper":{"inputs":{"x":{"kind":"string","description":"X","extra":"x"}},"transport":{"type":"stdio","command":"fixture"}}}}`, SyntaxValid: true, Path: "connections.paper.inputs.x.extra"},
		{Name: "profile", Document: "config", Raw: `{"schemaVersion":1,"credentialProfiles":{"team":{"mode":"desktop","account":"Fixture","extra":"x"}}}`, SyntaxValid: true, Path: "credentialProfiles.team.extra"},
		{Name: "selection", Document: "selections", Raw: `{"schemaVersion":1,"revision":0,"connections":{"local:paper":{"enabled":false,"extra":"x"}}}`, SyntaxValid: true, Path: "connections.local:paper.extra"},
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) { checkValidationCase(t, c) })
	}
}

func TestSchemaSemanticBoundary(t *testing.T) {
	remaining := map[string]bool{"undeclared-input": true, "invalid-duration": true, "http-consent": true}
	for _, c := range schemaCases(t) {
		if remaining[c.Name] {
			if !c.SyntaxValid || !c.SchemaValid || c.GoValid || c.Path == "" {
				t.Fatal("undocumented boundary", c)
			}
			t.Run(c.Name, func(t *testing.T) { checkValidationCase(t, c) })
			delete(remaining, c.Name)
		}
	}
	if len(remaining) != 0 {
		t.Fatal("missing boundary cases", remaining)
	}
}

func fieldCoverage(typ reflect.Type, s *jsonschema.Schema, extra, exclude []string) error {
	if s == nil {
		return errors.New("missing schema object")
	}
	var want, got []string
	for i := 0; i < typ.NumField(); i++ {
		name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" && !slices.Contains(exclude, name) {
			want = append(want, name)
		}
	}
	want = append(want, extra...)
	for name := range s.Properties {
		got = append(got, name)
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(want, got) {
		return fmt.Errorf("%s fields: Go=%v schema=%v", typ, want, got)
	}
	return nil
}

func unionArmCoverage(typ reflect.Type, want map[string]reflect.Type) error {
	if typ.NumField() != len(want) {
		return fmt.Errorf("%s has unexpected union arms", typ)
	}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if want[f.Name] != f.Type || f.Tag.Get("json") != "-" {
			return fmt.Errorf("%s.%s is an unexpected union arm", typ, f.Name)
		}
	}
	return nil
}

func TestSchemaFieldCoverage(t *testing.T) {
	valueArms := map[string]reflect.Type{"Literal": reflect.TypeFor[*string](), "Input": reflect.TypeFor[*InputRef](), "Secret": reflect.TypeFor[*SecretRef]()}
	transportArms := map[string]reflect.Type{"Stdio": reflect.TypeFor[*Stdio](), "HTTP": reflect.TypeFor[*HTTP]()}
	for _, c := range []struct {
		typ  reflect.Type
		arms map[string]reflect.Type
	}{{reflect.TypeFor[Value](), valueArms}, {reflect.TypeFor[Transport](), transportArms}} {
		if err := unionArmCoverage(c.typ, c.arms); err != nil {
			t.Fatal(err)
		}
		fields := make([]reflect.StructField, c.typ.NumField())
		for i := range fields {
			fields[i] = c.typ.Field(i)
		}
		fields = append(fields, reflect.StructField{Name: "Future", Type: reflect.TypeFor[*string](), Tag: `json:"-"`})
		if unionArmCoverage(reflect.StructOf(fields), c.arms) == nil {
			t.Fatal("new Go union arm escaped coverage")
		}
	}
	catalog := schemaFile(t, "catalog")
	local := schemaFile(t, "config")
	selections := schemaFile(t, "selections")
	cases := []struct {
		typ            reflect.Type
		s              *jsonschema.Schema
		extra, exclude []string
	}{
		{reflect.TypeFor[Catalog](), catalog, nil, nil},
		{reflect.TypeFor[Local](), local, nil, nil},
		{reflect.TypeFor[Selections](), selections, nil, nil},
		{reflect.TypeFor[Connection](), catalog.Defs["Connection"], nil, nil},
		{reflect.TypeFor[Domain](), catalog.Defs["Domain"], nil, nil},
		{reflect.TypeFor[Input](), catalog.Defs["Input"], nil, nil},
		{reflect.TypeFor[ProfileRequirement](), catalog.Defs["ProfileRequirement"], nil, nil},
		{reflect.TypeFor[ToolPolicy](), catalog.Defs["ToolPolicy"], nil, nil},
		{reflect.TypeFor[Lifecycle](), catalog.Defs["Lifecycle"], nil, nil},
		{reflect.TypeFor[OAuth](), catalog.Defs["OAuth"], nil, nil},
		{reflect.TypeFor[InputRef](), catalog.Defs["InputRef"], nil, nil},
		{reflect.TypeFor[SecretRef](), catalog.Defs["SecretRef"], nil, nil},
		{reflect.TypeFor[SecretRef](), catalog.Defs["HeaderSecretRef"], nil, nil},
		{reflect.TypeFor[Stdio](), catalog.Defs["Stdio"], []string{"type"}, nil},
		{reflect.TypeFor[HTTP](), catalog.Defs["HTTP"], []string{"type"}, nil},
		{reflect.TypeFor[Profile](), local.Defs["ProfileServiceAccount"], nil, nil},
		{reflect.TypeFor[Profile](), local.Defs["ProfileDesktop"], nil, []string{"bootstrapRef"}},
		{reflect.TypeFor[Source](), local.Defs["Source"], nil, nil},
		{reflect.TypeFor[RuntimeDefaults](), local.Defs["RuntimeDefaults"], nil, nil},
		{reflect.TypeFor[Selection](), selections.Defs["Selection"], nil, nil},
	}
	for _, c := range cases {
		if err := fieldCoverage(c.typ, c.s, c.extra, c.exclude); err != nil {
			t.Error(err)
		}
	}
	// Flattened transports and union arms are deliberate custom JSON contracts.
	for name, want := range map[string][]string{"Value": {"string", "#/$defs/InputRef", "#/$defs/SecretRef"}, "NonsecretValue": {"string", "#/$defs/InputRef"}, "Transport": {"#/$defs/Stdio", "#/$defs/HTTP"}} {
		def := catalog.Defs[name]
		if def == nil {
			t.Fatal("missing union", name)
		}
		var got []string
		for _, branch := range def.OneOf {
			if branch.Ref != "" {
				got = append(got, branch.Ref)
			} else {
				got = append(got, branch.Type)
			}
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatal(name, got, want)
		}
	}
	raw, err := json.Marshal(catalog.Defs["Connection"])
	if err != nil {
		t.Fatal(err)
	}
	var clone jsonschema.Schema
	if err = json.Unmarshal(raw, &clone); err != nil {
		t.Fatal(err)
	}
	clone.Properties["future"] = &jsonschema.Schema{Type: "string"}
	if fieldCoverage(reflect.TypeFor[Connection](), &clone, nil, nil) == nil {
		t.Fatal("extra property escaped")
	}
	delete(clone.Properties, "future")
	delete(clone.Properties, "transport")
	if fieldCoverage(reflect.TypeFor[Connection](), &clone, nil, nil) == nil {
		t.Fatal("missing property escaped")
	}
}

func TestSchemaNoRemoteResolution(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	for _, document := range []string{"catalog", "config", "selections"} {
		s := schemaFile(t, document)
		s.Ref = server.URL + "/remote.schema.json"
		if _, err := s.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true}); err == nil {
			t.Fatal("external reference resolved")
		}
	}
	if requests.Load() != 0 {
		t.Fatal("remote HTTP request")
	}
}

func TestSchemaExactInteger(t *testing.T) {
	for _, tc := range []struct {
		lexeme string
		valid  bool
		want   uint64
	}{{"9007199254740991", true, MaxRevision}, {"9007199254740992", false, 0}, {"1e3", true, 1000}} {
		raw := `{"schemaVersion":1,"revision":` + tc.lexeme + `,"connections":{}}`
		checkValidationCase(t, validationCase{Document: "selections", Raw: raw, SyntaxValid: true, SchemaValid: tc.valid, GoValid: tc.valid})
		if tc.valid {
			s, err := DecodeSelections([]byte(raw))
			if err != nil || s.Revision != tc.want {
				t.Fatal(s, err)
			}
		}
	}
}

func TestImportSchemaRoundTripAll32(t *testing.T) {
	raw, bindings := importFixture(t)
	report := converted(t, raw, bindings)
	proposed, e := json.Marshal(report.Definitions)
	if e != nil {
		t.Fatal(e)
	}
	if e = validateSchema(t, "catalog", proposed); e != nil {
		t.Fatal(e)
	}
	if _, e = DecodeCatalog(proposed); e != nil {
		t.Fatal(e)
	}
	store, p := importStore(t, true)
	applied, e := ApplyImport(context.Background(), store, 3, report, nil)
	if e != nil || !applied.Applied || applied.Revision == nil || *applied.Revision != 4 {
		t.Fatal(applied, e)
	}
	state, e := ReadState(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	effective, e := Resolve(state)
	if e != nil || len(effective.Connections) != 32 {
		t.Fatal(effective, e)
	}
	counts := map[string]int{}
	oauth := 0
	for _, row := range report.Entries {
		id, e := ResolveID(row.ID, effective.Aliases, sortedKeys(effective.Connections))
		if e != nil || id != row.CanonicalID {
			t.Fatal(id, e)
		}
		c := effective.Connections[id]
		if !c.Available || !c.Enabled || c.ReviewRequired || !reflect.DeepEqual(c.Connection.Transport, state.Personal.Connections[row.ID].Transport) || !reflect.DeepEqual(c.Connection.Auth, state.Personal.Connections[row.ID].Auth) || c.Connection.CredentialProfile != state.Personal.Connections[row.ID].CredentialProfile {
			t.Fatal(c)
		}
		counts[row.Transport]++
		if row.OAuth {
			oauth++
		}
	}
	if counts["stdio"] != 17 || counts["http"] != 15 || oauth != 10 || !reflect.DeepEqual(state.Personal.Connections, report.Definitions.Connections) {
		t.Fatal(counts, oauth)
	}
	for path, document := range map[string]string{p.ConfigFile: "config", p.PersonalFile: "catalog", p.SelectionsFile: "selections"} {
		b, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		if e = validateSchema(t, document, b); e != nil {
			t.Fatal(path, e)
		}
		if e = decodeDocument(document, b); e != nil {
			t.Fatal(path, e)
		}
	}
}
