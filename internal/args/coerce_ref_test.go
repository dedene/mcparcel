package args

import (
	"encoding/json"
	"errors"
	"testing"
)

func coerceWith(t *testing.T, schema string, values map[string]string) (map[string]any, error) {
	t.Helper()
	raw := Raw{Values: map[string]Value{}}
	for k, v := range values {
		raw.Values[k] = Value{Text: v}
	}
	return Coerce(raw, json.RawMessage(schema))
}

func TestCoerceLocalRef(t *testing.T) {
	schema := `{"$defs":{"Limit":{"type":"integer"},"Esc/a~b":{"type":"boolean"},"Sp ace":{"type":"number"}},"definitions":{"Old":{"type":"integer"}},"properties":{
		"limit":{"$ref":"#/$defs/Limit"},"flag":{"$ref":"#/$defs/Esc~1a~0b"},"n":{"$ref":"#/$defs/Sp%20ace"},"old":{"$ref":"#/definitions/Old"}}}`
	got, err := coerceWith(t, schema, map[string]string{"limit": "5", "flag": "true", "n": "1.5", "old": "7"})
	if err != nil || got["limit"] != json.Number("5") || got["flag"] != true || got["n"] != json.Number("1.5") || got["old"] != json.Number("7") {
		t.Fatal(got, err)
	}
	if _, err = coerceWith(t, schema, map[string]string{"limit": "x"}); !errors.Is(err, ErrInvalidArgs) {
		t.Fatal(err)
	}
}

func TestCoerceRootRef(t *testing.T) {
	for _, schema := range []string{
		`{"$ref":"#/$defs/Args","$defs":{"Args":{"type":"object","properties":{"limit":{"type":"integer"}}}}}`,
		`{"$ref":"#/definitions/Args","definitions":{"Args":{"$ref":"#/definitions/Inner"},"Inner":{"properties":{"limit":{"type":"integer"}}}}}`,
		`{"allOf":[{"$ref":"#/$defs/Args"}],"$defs":{"Args":{"properties":{"limit":{"type":"integer"}}}}}`,
	} {
		got, err := coerceWith(t, schema, map[string]string{"limit": "5"})
		if err != nil || got["limit"] != json.Number("5") {
			t.Fatal(schema, got, err)
		}
	}
	if _, err := coerceWith(t, `{"$ref":"#/$defs/Args","$defs":{"Args":{"properties":[]}}}`, nil); !errors.Is(err, ErrInvalidSchema) {
		t.Fatal(err)
	}
}

func TestCoerceSingleAllOf(t *testing.T) {
	schema := `{"$defs":{"Limit":{"type":"integer"}},"properties":{"a":{"allOf":[{"$ref":"#/$defs/Limit"}],"description":"x"},"b":{"allOf":[{"type":"integer"},{"minimum":1}]}}}`
	got, err := coerceWith(t, schema, map[string]string{"a": "5", "b": "5"})
	if err != nil || got["a"] != json.Number("5") || got["b"] != "5" {
		t.Fatal(got, err)
	}
}

func TestCoerceRefChainAndCycle(t *testing.T) {
	schema := `{"$defs":{"x":{"$ref":"#/$defs/y"},"y":{"$ref":"#/$defs/x"},"s":{"$ref":"#"}},"properties":{"loop":{"$ref":"#/$defs/x"},"self":{"$ref":"#/$defs/s"}}}`
	got, err := coerceWith(t, schema, map[string]string{"loop": "5", "self": "5"})
	if err != nil || got["loop"] != "5" || got["self"] != "5" {
		t.Fatal(got, err)
	}
	long := `{"$defs":{`
	for i := range 40 {
		long += `"d` + itoa(i) + `":{"$ref":"#/$defs/d` + itoa(i+1) + `"},`
	}
	long += `"d40":{"type":"integer"}},"properties":{"long":{"$ref":"#/$defs/d0"},"short":{"$ref":"#/$defs/d10"}}}`
	got, err = coerceWith(t, long, map[string]string{"long": "5", "short": "5"})
	if err != nil || got["long"] != "5" || got["short"] != json.Number("5") {
		t.Fatal(got, err)
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func TestCoerceRemoteRefStaysString(t *testing.T) {
	schema := `{"properties":{"a":{"$ref":"https://example.invalid/schema.json"},"b":{"$ref":"other.json#/x"},"c":{"$ref":"#anchor"},"d":{"$ref":"#/missing"},"e":{"$ref":5}}}`
	got, err := coerceWith(t, schema, map[string]string{"a": "5", "b": "5", "c": "5", "d": "5", "e": "5"})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		if got[k] != "5" {
			t.Fatal(k, got)
		}
	}
}

func TestCoerceNullableUnion(t *testing.T) {
	schema := `{"$defs":{"Int":{"type":"integer"},"Null":{"type":"null"}},"properties":{
		"arr":{"type":["integer","null"]},"any":{"anyOf":[{"type":"integer"},{"type":"null"}]},"one":{"oneOf":[{"type":"null"},{"type":"boolean"}]},
		"ref":{"anyOf":[{"$ref":"#/$defs/Int"},{"$ref":"#/$defs/Null"}]},"str":{"type":["string","null"]},"strany":{"anyOf":[{"type":"string"},{"type":"null"}]},
		"two":{"anyOf":[{"type":"integer"},{"type":"string"}]},"three":{"type":["integer","boolean","null"]},"onlynull":{"anyOf":[{"type":"null"}]}}}`
	got, err := coerceWith(t, schema, map[string]string{"arr": "5", "any": "6", "one": "true", "ref": "7", "str": "8", "strany": "null", "two": "9", "three": "1", "onlynull": "null"})
	want := map[string]any{"arr": json.Number("5"), "any": json.Number("6"), "one": true, "ref": json.Number("7"), "str": "8", "strany": "null", "two": "9", "three": "1", "onlynull": "null"}
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: %#v want %#v", k, got[k], v)
		}
	}
	got, err = coerceWith(t, schema, map[string]string{"arr": "null", "any": "null", "ref": "null", "str": "null"})
	if err != nil || got["arr"] != nil || got["any"] != nil || got["ref"] != nil || got["str"] != "null" {
		t.Fatal(got, err)
	}
	for _, k := range []string{"arr", "any", "ref"} {
		if _, ok := got[k]; !ok {
			t.Fatal("null dropped", k)
		}
	}
	if _, err = coerceWith(t, schema, map[string]string{"any": "x"}); !errors.Is(err, ErrInvalidArgs) || err.Error() != `invalid arguments: argument "any" must be integer` {
		t.Fatal(err)
	}
}
