package args

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// Regression: a cycle under a schema named like a data keyword, or through
// draft-07 dependencies, overflowed the stack and killed the daemon.
func TestValidateCycleThroughKeywordNamesUnchecked(t *testing.T) {
	for _, schema := range []string{
		`{"type":"object","$defs":{"default":{"$ref":"#/$defs/default"}},"properties":{"x":{"$ref":"#/$defs/default"}}}`,
		`{"definitions":{"enum":{"allOf":[{"$ref":"#/definitions/enum"}]}},"properties":{"x":{"$ref":"#/definitions/enum"}}}`,
		`{"properties":{"const":{"anyOf":[{"$ref":"#/properties/const"}]},"x":{"$ref":"#/properties/const"}}}`,
		`{"patternProperties":{"examples":{"not":{"$ref":"#/patternProperties/examples"}}},"properties":{"x":{"$ref":"#/patternProperties/examples"}}}`,
		`{"dependentSchemas":{"default":{"$ref":"#/dependentSchemas/default"}},"properties":{"x":{"$ref":"#/dependentSchemas/default"}}}`,
		`{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","definitions":{"a":{"dependencies":{"x":{"$ref":"#/definitions/a"}}}},"allOf":[{"$ref":"#/definitions/a"}]}`,
		`{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"x":{"type":"integer"}},"dependencies":{"x":{"$ref":"#"}}}`,
		`{"$schema":"http://json-schema.org/draft-07/schema#","dependencies":{"x":{"$ref":"#"}}}`,
	} {
		checked, err := validate(t, schema, map[string]any{"x": json.Number("1")})
		if checked || err != nil {
			t.Fatal(schema, checked, err)
		}
	}
	// Data keywords stay data: a $ref-shaped default or enum is no edge.
	data := `{"properties":{"x":{"type":"integer","default":{"$ref":"#"},"examples":[{"$ref":"#"}],"enum":[1,{"$ref":"http://x/y"}]}}}`
	if checked, err := validate(t, data, map[string]any{"x": json.Number("1")}); !checked || err != nil {
		t.Fatal(checked, err)
	}
	// String-array dependencies are no schemas.
	deps := `{"$schema":"http://json-schema.org/draft-07/schema#","dependencies":{"x":["y"]}}`
	if checked, err := validate(t, deps, map[string]any{"x": json.Number("1")}); !checked || !errors.Is(err, ErrInvalidArgs) {
		t.Fatal(checked, err)
	}
}

// Regression: nested arguments multiplied anyOf fan-out per level, so one
// call could pin a daemon core for minutes.
func TestValidateNestedFanOutUnchecked(t *testing.T) {
	branches := strings.Repeat(`{"type":"array","items":{"$ref":"#/$defs/n"}},`, 6)
	schema := `{"type":"object","$defs":{"n":{"anyOf":[` + branches + `{"type":"integer"}]}},"properties":{"q":{"$ref":"#/$defs/n"}}}`
	nest := func(depth int, leaf any) any {
		v := leaf
		for range depth {
			v = []any{v}
		}
		return v
	}
	start := time.Now()
	checked, err := validate(t, schema, map[string]any{"q": nest(12, "x")})
	if checked || err != nil {
		t.Fatal(checked, err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("budget check too slow", time.Since(start))
	}
	if checked, err = validate(t, schema, map[string]any{"q": nest(2, "x")}); !checked || !errors.Is(err, ErrInvalidArgs) {
		t.Fatal(checked, err)
	}
	if checked, err = validate(t, schema, map[string]any{"q": nest(3, json.Number("1"))}); !checked || err != nil {
		t.Fatal(checked, err)
	}
}

// A large failing instance under wide fan-out would make the validator print
// it once per branch.
func TestValidateLargeInstanceFanOutUnchecked(t *testing.T) {
	branches := strings.Repeat(`{"type":"string"},`, 200)
	schema := `{"properties":{"q":{"anyOf":[` + branches + `{"type":"integer"}]}}}`
	big := make([]any, 20000)
	for i := range big {
		big[i] = "sk-SENTINEL-padding"
	}
	if checked, err := validate(t, schema, map[string]any{"q": big}); checked || err != nil {
		t.Fatal(checked, err)
	}
	// The same instance under a plain schema stays well within budget.
	plainSchema := `{"properties":{"q":{"type":"array","items":{"type":"string","maxLength":3}}}}`
	if checked, err := validate(t, plainSchema, map[string]any{"q": big}); !checked || !errors.Is(err, ErrInvalidArgs) {
		t.Fatal(checked, err)
	}
}

// Regression: a schema path or $id containing ": required: " made the
// message quote the argument value.
func TestValidateMessageIgnoresPathText(t *testing.T) {
	for _, schema := range []string{
		`{"properties":{"k: required: z":{"type":"string","maxLength":3}}}`,
		`{"$id":"x: required: z","properties":{"k: required: z":{"type":"string","maxLength":3}}}`,
		`{"$id":"x: required: z","properties":{"a":{"type":"string","maxLength":3}}}`,
	} {
		values := map[string]any{"k: required: z": "SECRETVALUE", "a": "SECRETVALUE"}
		_, err := validate(t, schema, values)
		if err == nil || strings.Contains(err.Error(), "SECRET") || !strings.Contains(err.Error(), "(maxLength)") {
			t.Fatal(schema, err)
		}
	}
	_, err := validate(t, `{"$id":"x: required: z","required":["need"]}`, map[string]any{})
	if err == nil || !strings.HasSuffix(err.Error(), "(required): missing need.") {
		t.Fatal(err)
	}
}

// Regression: anyOf over shared refs made coercion quadratic in schema size.
func TestCoerceUnionBudget(t *testing.T) {
	const n = 6000
	u := `{"type":"string"}` + strings.Repeat(`,{"type":"null"}`, n)
	p := strings.TrimSuffix(strings.Repeat(`{"$ref":"#/$defs/U"},`, n), ",")
	schema := `{"$defs":{"U":{"anyOf":[` + u + `]},"V":{"type":"integer"}},
		"properties":{"p":{"anyOf":[` + p + `]},"q":{"anyOf":[{"$ref":"#/$defs/V"},{"type":"null"}]}}}`
	start := time.Now()
	got, err := coerceWith(t, schema, map[string]string{"p": "x", "q": "5"})
	if err != nil || got["p"] != "x" || got["q"] != json.Number("5") {
		t.Fatal(got, err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("coercion too slow", time.Since(start))
	}
}
