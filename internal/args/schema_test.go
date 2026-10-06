package args

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const refsSchema = `{"type":"object","$defs":{"Limit":{"type":"integer","minimum":1},"Filter":{"type":"object","additionalProperties":false,"required":["kind"],"properties":{"kind":{"enum":["open","closed"]}}}},
	"properties":{"limit":{"$ref":"#/$defs/Limit"},"filter":{"$ref":"#/$defs/Filter"},"maybe":{"anyOf":[{"type":"integer"},{"type":"null"}]}},"required":["limit"]}`

func validate(t *testing.T, schema string, values map[string]any) (bool, error) {
	t.Helper()
	return Validate(values, json.RawMessage(schema))
}

func TestValidateRejectsInvalidInput(t *testing.T) {
	for name, values := range map[string]map[string]any{
		"required":   {"filter": map[string]any{"kind": "open"}},
		"enum":       {"limit": json.Number("1"), "filter": map[string]any{"kind": "other"}},
		"additional": {"limit": json.Number("1"), "filter": map[string]any{"kind": "open", "extra": true}},
		"minimum":    {"limit": json.Number("0")},
		"type":       {"limit": "five"},
	} {
		checked, err := validate(t, refsSchema, values)
		if !checked || !errors.Is(err, ErrInvalidArgs) {
			t.Fatalf("%s: %v %v", name, checked, err)
		}
	}
	checked, err := validate(t, refsSchema, map[string]any{"limit": json.Number("3"), "filter": map[string]any{"kind": "open"}, "maybe": nil})
	if !checked || err != nil {
		t.Fatal(checked, err)
	}
}

func TestValidateLocalRefs(t *testing.T) {
	schema := `{"$ref":"#/definitions/Args","definitions":{"Args":{"type":"object","properties":{"limit":{"$ref":"#/definitions/N"}}},"N":{"type":"integer"}}}`
	if checked, err := validate(t, schema, map[string]any{"limit": json.Number("2")}); !checked || err != nil {
		t.Fatal(checked, err)
	}
	if checked, err := validate(t, schema, map[string]any{"limit": "x"}); !checked || !errors.Is(err, ErrInvalidArgs) {
		t.Fatal(checked, err)
	}
}

func TestValidateNeverFetchesRemoteRef(t *testing.T) {
	var hits atomic.Int32
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"type":"integer"}`))
	}))
	defer hs.Close()
	for _, schema := range []string{
		`{"properties":{"limit":{"$ref":"` + hs.URL + `/schema.json"}}}`,
		`{"$id":"` + hs.URL + `/root.json","properties":{"limit":{"$ref":"other.json"}}}`,
		`{"properties":{"limit":{"$dynamicRef":"#x"}}}`,
		`{"properties":{"limit":{"$recursiveRef":"#"}}}`,
	} {
		checked, err := validate(t, schema, map[string]any{"limit": "x"})
		if checked || err != nil {
			t.Fatal(schema, checked, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatal("remote schema fetched")
	}
}

func TestValidateUnsupportedDraftUnchecked(t *testing.T) {
	for _, draft := range []string{"http://json-schema.org/draft-04/schema#", "https://json-schema.org/draft/2019-09/schema", "x"} {
		checked, err := validate(t, `{"$schema":"`+draft+`","properties":{"limit":{"type":"integer"}}}`, map[string]any{"limit": "x"})
		if checked || err != nil {
			t.Fatal(draft, checked, err)
		}
	}
	if checked, _ := validate(t, `{"$schema":5}`, nil); checked {
		t.Fatal("non-string $schema checked")
	}
	for _, draft := range []string{"http://json-schema.org/draft-07/schema#", "https://json-schema.org/draft-07/schema#", "https://json-schema.org/draft/2020-12/schema"} {
		checked, err := validate(t, `{"$schema":"`+draft+`","properties":{"limit":{"type":"integer"}}}`, map[string]any{"limit": "x"})
		if !checked || !errors.Is(err, ErrInvalidArgs) {
			t.Fatal(draft, checked, err)
		}
	}
}

func TestValidateRefCycleUnchecked(t *testing.T) {
	for _, schema := range []string{
		`{"$ref":"#/$defs/a","$defs":{"a":{"$ref":"#/$defs/b"},"b":{"$ref":"#/$defs/a"}}}`,
		`{"$ref":"#"}`,
		`{"properties":{"x":{"anyOf":[{"$ref":"#/properties/x"}]}}}`,
		`{"allOf":[{"not":{"$ref":"#"}}]}`,
		`{"if":{"$ref":"#/$defs/a"},"$defs":{"a":{"then":{"$ref":"#"}}}}`,
		`{"dependentSchemas":{"x":{"$ref":"#"}}}`,
	} {
		checked, err := validate(t, schema, map[string]any{"x": json.Number("1")})
		if checked || err != nil {
			t.Fatal(schema, checked, err)
		}
	}
	// Recursion through properties consumes the instance, so it is no cycle.
	tree := `{"$defs":{"Node":{"type":"object","properties":{"child":{"$ref":"#/$defs/Node"},"v":{"type":"integer"}}}},"properties":{"root":{"$ref":"#/$defs/Node"}}}`
	checked, err := validate(t, tree, map[string]any{"root": map[string]any{"child": map[string]any{"v": "x"}}})
	if !checked || !errors.Is(err, ErrInvalidArgs) {
		t.Fatal(checked, err)
	}
}

func TestValidateApplicatorBlowupUnchecked(t *testing.T) {
	chain := func(n int) string {
		defs := `"d0":{"type":"integer"}`
		for i := 1; i <= n; i++ {
			prev := `#/$defs/d` + itoa(i-1)
			defs += `,"d` + itoa(i) + `":{"anyOf":[{"$ref":"` + prev + `"},{"$ref":"` + prev + `"}]}`
		}
		return `{"$defs":{` + defs + `},"properties":{"x":{"$ref":"#/$defs/d` + itoa(n) + `"}}}`
	}
	checked, err := validate(t, chain(30), map[string]any{"x": "no"})
	if checked || err != nil {
		t.Fatal(checked, err)
	}
	if checked, err = validate(t, chain(5), map[string]any{"x": "no"}); !checked || !errors.Is(err, ErrInvalidArgs) {
		t.Fatal(checked, err)
	}
}

func TestValidateBigIntegers(t *testing.T) {
	schema := `{"properties":{"n":{"type":"integer"},"f":{"type":"number","maximum":10}}}`
	for _, n := range []string{"9007199254740993", "-9223372036854775808", "123456789012345678901234567890", "5.0"} {
		if checked, err := validate(t, schema, map[string]any{"n": json.Number(n)}); !checked || err != nil {
			t.Fatal(n, checked, err)
		}
	}
	values := map[string]any{"n": json.Number("9007199254740993"), "f": json.Number("11")}
	if checked, err := validate(t, schema, values); !checked || !errors.Is(err, ErrInvalidArgs) {
		t.Fatal(checked, err)
	}
	if values["n"] != json.Number("9007199254740993") {
		t.Fatal("Validate changed the values sent to the server")
	}
	if checked, _ := validate(t, schema, map[string]any{"n": json.Number("1e400")}); checked {
		t.Fatal("overflowing number checked")
	}
}

func TestValidateMessageOmitsValues(t *testing.T) {
	schema := `{"properties":{"p":{"type":"string","pattern":"^a$"},"e":{"enum":["a"]},"t":{"type":"integer"},"m":{"type":"string","maxLength":1},"c":{"const":"a"},"o":{"type":"object","additionalProperties":false}}}`
	for _, key := range []string{"p", "e", "t", "m", "c"} {
		_, err := validate(t, schema, map[string]any{key: "sk-SENTINEL"})
		if err == nil || strings.Contains(err.Error(), "SENTINEL") || !strings.Contains(err.Error(), "/properties/"+key) {
			t.Fatal(key, err)
		}
	}
	_, err := validate(t, schema, map[string]any{"o": map[string]any{"sk-SENTINEL": 1}})
	if err == nil || strings.Contains(err.Error(), "SENTINEL") || !strings.Contains(err.Error(), "(additionalProperties)") {
		t.Fatal(err)
	}
}

func TestValidateMessageFormat(t *testing.T) {
	for _, tc := range []struct{ values, want string }{
		{`{"filter":{"kind":"open"}}`, "invalid arguments: Arguments do not match the tool schema at root (required): missing limit."},
		{`{"limit":"x"}`, "invalid arguments: Arguments do not match the tool schema at /properties/limit (type)."},
		{`{"limit":0}`, "invalid arguments: Arguments do not match the tool schema at /properties/limit (minimum)."},
		{`{"limit":1,"filter":{"kind":"x"}}`, "invalid arguments: Arguments do not match the tool schema at /$defs/Filter/properties/kind (enum)."},
		{`{"limit":1,"filter":{}}`, "invalid arguments: Arguments do not match the tool schema at /properties/filter (required): missing kind."},
		{`{"limit":1,"filter":{"kind":"open","x":1}}`, "invalid arguments: Arguments do not match the tool schema at /properties/filter (additionalProperties)."},
		{`{"limit":1,"maybe":"x"}`, "invalid arguments: Arguments do not match the tool schema at /properties/maybe (anyOf)."},
	} {
		var values map[string]any
		d := json.NewDecoder(strings.NewReader(tc.values))
		d.UseNumber()
		if err := d.Decode(&values); err != nil {
			t.Fatal(err)
		}
		_, err := validate(t, refsSchema, values)
		if err == nil || err.Error() != tc.want {
			t.Fatalf("%s:\n got %v\nwant %s", tc.values, err, tc.want)
		}
	}
	for _, raw := range []string{"", "validating root: ", "validating /properties/x: odd: sk-SENTINEL", "type: 5"} {
		if got := schemaMessage(errors.New(raw)); got != "Arguments do not match the tool schema." {
			t.Fatalf("%q: %s", raw, got)
		}
	}
}

func TestPrepareOrder(t *testing.T) {
	raw, _ := Parse([]string{"limit=x"}, "", nil)
	_, checked, err := Prepare(raw, json.RawMessage(refsSchema))
	if checked || err == nil || err.Error() != `invalid arguments: argument "limit" must be integer` {
		t.Fatal(checked, err)
	}
	raw, _ = Parse([]string{"limit=0"}, "", nil)
	_, checked, err = Prepare(raw, json.RawMessage(refsSchema))
	if !checked || !strings.Contains(err.Error(), "(minimum)") {
		t.Fatal(checked, err)
	}
	raw, _ = Parse([]string{"limit=3", "maybe=null"}, "", nil)
	values, checked, err := Prepare(raw, json.RawMessage(refsSchema))
	if !checked || err != nil || values["limit"] != json.Number("3") || values["maybe"] != nil {
		t.Fatal(values, checked, err)
	}
	values, checked, err = Prepare(raw, nil)
	if !checked || err != nil || values["limit"] != "3" {
		t.Fatal(values, checked, err)
	}
	if _, _, err = Prepare(raw, json.RawMessage(`[]`)); !errors.Is(err, ErrInvalidSchema) {
		t.Fatal(err)
	}
}
