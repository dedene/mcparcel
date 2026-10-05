package args

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func coerceOne(t *testing.T, text, property string) (any, error) {
	t.Helper()
	got, err := Coerce(Raw{Values: map[string]Value{"x": {Text: text}}}, json.RawMessage(`{"properties":{"x":`+property+`}}`))
	return got["x"], err
}

func TestCoerceIntegerVersusString(t *testing.T) {
	for typ, want := range map[string]any{"integer": json.Number("5"), "string": "5"} {
		raw, _ := Parse([]string{"limit=5"}, "", nil)
		got, err := Coerce(raw, json.RawMessage(`{"properties":{"limit":{"type":"`+typ+`"}}}`))
		if err != nil || got["limit"] != want {
			t.Fatalf("%v %v", got, err)
		}
	}
}

func TestCoerceLeadingZeros(t *testing.T) {
	raw, _ := Parse([]string{"id=007"}, "", nil)
	got, err := Coerce(raw, json.RawMessage(`{"properties":{"id":{"type":"string"}}}`))
	if err != nil || got["id"] != "007" {
		t.Fatal(got, err)
	}
	_, err = Coerce(raw, json.RawMessage(`{"properties":{"id":{"type":"integer"}}}`))
	if !errors.Is(err, ErrInvalidArgs) || err.Error() != `invalid arguments: argument "id" must be integer` {
		t.Fatal(err)
	}
}

func TestCoerceArrayObjectBoolean(t *testing.T) {
	raw, _ := Parse([]string{`queries=[{"a":1}]`, `options={"x":false}`, "enabled=true"}, "", nil)
	got, err := Coerce(raw, json.RawMessage(`{"properties":{"queries":{"type":"array"},"options":{"type":"object"},"enabled":{"type":"boolean"}}}`))
	want := map[string]any{"queries": []any{map[string]any{"a": json.Number("1")}}, "options": map[string]any{"x": false}, "enabled": true}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(got, err)
	}
}

func TestCoerceExactNumber(t *testing.T) {
	for text, typ := range map[string]string{"9007199254740993": "integer", "1.25e2": "number"} {
		got, err := coerceOne(t, text, `{"type":"`+typ+`"}`)
		encoded, _ := json.Marshal(got)
		if err != nil || got != json.Number(text) || string(encoded) != text {
			t.Fatal(got, err, string(encoded))
		}
	}
}

func TestCoerceIntegralJSONNumber(t *testing.T) {
	for _, s := range []string{"5.0", "5e0"} {
		got, err := coerceOne(t, s, `{"type":"integer"}`)
		if err != nil || got != json.Number(s) {
			t.Fatal(got, err)
		}
	}
	_, err := coerceOne(t, "5.1", `{"type":"integer"}`)
	if !errors.Is(err, ErrInvalidArgs) || err.Error() != `invalid arguments: argument "x" must be integer` {
		t.Fatal(err)
	}
}

func TestCoerceWrongTypes(t *testing.T) {
	for typ, text := range map[string]string{"integer": `"5"`, "boolean": "TRUE", "array": "{}", "object": "[]", "number": "NaN"} {
		_, err := coerceOne(t, text, `{"type":"`+typ+`"}`)
		if !errors.Is(err, ErrInvalidArgs) || err.Error() != `invalid arguments: argument "x" must be `+typ {
			t.Fatal(err)
		}
	}
}

func TestCoerceUnknownAndUnion(t *testing.T) {
	for _, p := range []string{`{}`, `{"type":["integer","null"]}`, `{"$ref":"#/x","type":"integer"}`, `{"anyOf":[],"type":"integer"}`, `{"oneOf":[],"type":"integer"}`, `{"allOf":[],"type":"integer"}`, `true`} {
		got, err := coerceOne(t, "5", p)
		if err != nil || got != "5" {
			t.Fatal(got, err)
		}
	}
	for _, schema := range []json.RawMessage{nil, {}, json.RawMessage(`{}`)} {
		got, err := Coerce(Raw{Values: map[string]Value{"x": {Text: "5"}}}, schema)
		if err != nil || got["x"] != "5" {
			t.Fatal(got, err)
		}
	}
}

func TestCoerceForcedAndPayload(t *testing.T) {
	a, _ := Parse([]string{"x:=5"}, "", nil)
	b, _ := Parse(nil, `{"x":5}`, nil)
	for _, raw := range []Raw{a, b} {
		got, err := Coerce(raw, json.RawMessage(`{"properties":{"x":{"type":"string"}}}`))
		if err != nil || got["x"] != json.Number("5") {
			t.Fatal(got, err)
		}
	}
}

func TestCoerceNull(t *testing.T) {
	for typ, want := range map[string]any{"null": nil, "string": "null"} {
		got, err := coerceOne(t, "null", `{"type":"`+typ+`"}`)
		if err != nil || got != want {
			t.Fatal(got, err)
		}
	}
}

func TestCoerceLiteralString(t *testing.T) {
	for _, s := range []string{`"a"`, " true "} {
		got, err := coerceOne(t, s, `{"type":"string"}`)
		if err != nil || got != s {
			t.Fatal(got, err)
		}
	}
}

func TestCoerceMalformedSchema(t *testing.T) {
	for _, s := range []string{`[]`, `{"secret":`, `{"properties":[]}`, `{"properties":null}`} {
		_, err := Coerce(Raw{}, json.RawMessage(s))
		if !errors.Is(err, ErrInvalidSchema) || strings.Contains(err.Error(), "secret") {
			t.Fatal(err)
		}
	}
}

func TestCoerceDoesNotValidateBeyondTypes(t *testing.T) {
	raw, _ := Parse([]string{"x=1", "unknown=5"}, "", nil)
	got, err := Coerce(raw, json.RawMessage(`{"required":["missing"],"additionalProperties":false,"properties":{"x":{"type":"integer","minimum":10}}}`))
	if err != nil || got["x"] != json.Number("1") || got["unknown"] != "5" {
		t.Fatal(got, err)
	}
}

func TestCoerceWireAndOrder(t *testing.T) {
	_, err := Coerce(Raw{Values: map[string]Value{"x": {Text: "5", JSON: json.RawMessage(`5`)}}}, nil)
	if !errors.Is(err, ErrInvalidArgs) {
		t.Fatal(err)
	}
	_, err = Coerce(Raw{Values: map[string]Value{"b": {Text: "NaN"}, "a": {Text: "NaN"}}}, json.RawMessage(`{"properties":{"a":{"type":"number"},"b":{"type":"number"}}}`))
	if err == nil || err.Error() != `invalid arguments: argument "a" must be number` {
		t.Fatal(err)
	}
	raw := Raw{Values: map[string]Value{"x": {Text: "5"}}}
	got, err := Coerce(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	got["x"] = "changed"
	if raw.Values["x"].Text != "5" {
		t.Fatal(raw)
	}
}
