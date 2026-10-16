package args

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSignature(t *testing.T) {
	for _, tc := range []struct{ schema, want string }{
		{``, ``},
		{`{"type":"object"}`, ``},
		{`{"type":"object","properties":{"times":{"type":"integer"},"text":{"type":"string"}},"required":["text"]}`, `text: string, times?: integer`},
		// Required arguments keep the schema's order; optional ones sort.
		{`{"properties":{"a":{"type":"string"},"z":{"type":"string"},"m":{"type":"number"},"b":{"type":"boolean"}},"required":["z","a","z"]}`, `z: string, a: string, b?: boolean, m?: number`},
		{`{"properties":{"mode":{"type":"string","enum":["fast","slow"]},"level":{"enum":[1,2,3]},"on":{"const":true}}}`, `level?: 1|2|3, mode?: fast|slow, on?: true`},
		// An enum too long for a line falls back to its type.
		{`{"properties":{"c":{"type":"string","enum":["a","b","c","d","e","f","g"]},"d":{"enum":["aaaaaaaaaaaaaaaaaaaa","bbbbbbbbbbbbbbbbbbbbbbbb"]}}}`, `c?: string, d?: string`},
		{`{"properties":{"tags":{"type":"array","items":{"type":"string"}},"rows":{"type":"array","items":{"type":"object"}},"grid":{"type":"array","items":{"type":"array","items":{"type":"integer"}}},"any":{"type":"array"},"modes":{"type":"array","items":{"enum":["a","b"]}}}}`, `any?: array, grid?: integer[][], modes?: (a|b)[], rows?: object[], tags?: string[]`},
		{`{"properties":{"filter":{"type":"object","properties":{"q":{"type":"string"}}},"loose":{"properties":{}},"free":{}}}`, `filter?: object, free?: any, loose?: object`},
		// Nullable and other unions, including anyOf and oneOf.
		{`{"properties":{"a":{"type":["string","null"]},"b":{"anyOf":[{"type":"integer"},{"type":"null"}]},"c":{"oneOf":[{"type":"string"},{"type":"object"}]},"d":{"anyOf":[{"type":"string"},{}]}}}`, `a?: string|null, b?: integer|null, c?: string|object, d?: any`},
		// Local $refs, a root $ref and one-item allOf wrappers are followed.
		{`{"$ref":"#/$defs/Args","$defs":{"Args":{"properties":{"id":{"$ref":"#/$defs/Id"}},"required":["id"]},"Id":{"allOf":[{"type":"string"}]}}}`, `id: string`},
		{`{"properties":{"x":{"$ref":"#/$defs/missing"},"y":{"$ref":"https://example.com/s"}}}`, `x?: any, y?: any`},
		// A required name the schema does not describe is still listed.
		{`{"properties":{},"required":["ghost"]}`, `ghost: any`},
		// Names and values that would not read as one token are quoted.
		{`{"properties":{"two words":{"enum":["a b","c|d"]}}}`, `"two words"?: "a b"|"c|d"`},
	} {
		got, ok := Signature(json.RawMessage(tc.schema))
		if !ok || got != tc.want {
			t.Errorf("%s: got %q %v, want %q", tc.schema, got, ok, tc.want)
		}
	}
}

func TestSignatureRejectsNonObjectSchemas(t *testing.T) {
	for _, schema := range []string{`[]`, `"x"`, `{`, `{"a":1,"a":2}`} {
		if got, ok := Signature(json.RawMessage(schema)); ok {
			t.Errorf("%s: got %q, want false", schema, got)
		}
	}
}

// Server text in a signature cannot drive the terminal.
func TestSignatureCleansTerminalControls(t *testing.T) {
	got, ok := Signature(json.RawMessage(`{"properties":{"a\u001b[31mb":{"enum":["x\u001b]0;title\u0007y","\u202ez"]}}}`))
	if !ok || strings.ContainsAny(got, "\x1b\x07\u202e") || got != `ab?: xy|z` {
		t.Fatalf("%q", got)
	}
}

// Deep nesting and cyclic refs end in "any" instead of recursing.
func TestSignatureBoundsNesting(t *testing.T) {
	deep := `{"type":"string"}`
	for range 10 {
		deep = `{"type":"array","items":` + deep + `}`
	}
	got, ok := Signature(json.RawMessage(`{"properties":{"deep":` + deep + `,"loop":{"$ref":"#/$defs/a"}},"$defs":{"a":{"$ref":"#/$defs/a"}}}`))
	if !ok || got != `deep?: any[][][][], loop?: any` {
		t.Fatalf("%q", got)
	}
}
