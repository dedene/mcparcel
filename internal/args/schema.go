package args

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/dedene/mcparcel/internal/elicit"
	"github.com/dedene/mcparcel/internal/jsonutil"
)

// supportedDrafts are the $schema values the validator handles; jsonschema-go
// would silently treat any other value as 2020-12.
var supportedDrafts = map[string]bool{
	"http://json-schema.org/draft-07/schema#":      true,
	"https://json-schema.org/draft-07/schema#":     true,
	"https://json-schema.org/draft/2020-12/schema": true,
}

// Prepare coerces raw against the tool's input schema, then validates the
// result; checked is false when the schema could not be checked locally.
func Prepare(raw Raw, inputSchema json.RawMessage) (map[string]any, bool, error) {
	values, err := Coerce(raw, inputSchema)
	if err != nil {
		return nil, false, err
	}
	checked, err := Validate(values, inputSchema)
	if err != nil {
		return nil, checked, err
	}
	return values, checked, nil
}

// Validate checks values against inputSchema without fetching anything. It
// reports checked=false, and no error, for a schema it cannot check safely:
// undecodable, cyclic or oversized, with remote or dynamic refs, of an
// unsupported draft, one the validator rejects or panics on, or one whose
// validation of these values would exceed the work budget.
func Validate(values map[string]any, inputSchema json.RawMessage) (checked bool, err error) {
	if len(inputSchema) == 0 {
		return true, nil
	}
	v, e := jsonutil.Decode(inputSchema)
	root, ok := v.(map[string]any)
	if e != nil || !ok {
		return false, nil
	}
	graph, ok := inspectSchema(root)
	if !ok {
		return false, nil
	}
	if draft, has := root["$schema"]; has {
		if s, ok := draft.(string); !ok || !supportedDrafts[s] {
			return false, nil
		}
	}
	instance, ok := plain(values, 0)
	if !ok || !graph.withinBudget(instance) {
		return false, nil
	}
	defer func() {
		if recover() != nil {
			checked, err = false, nil
		}
	}()
	var schema jsonschema.Schema
	if json.Unmarshal(inputSchema, &schema) != nil {
		return false, nil
	}
	resolved, e := schema.Resolve(&jsonschema.ResolveOptions{})
	if e != nil {
		return false, nil
	}
	if e = resolved.Validate(instance); e != nil {
		return true, fmt.Errorf("%w: %s", ErrInvalidArgs, schemaMessage(e))
	}
	return true, nil
}

// plain deep-copies v for the validator, which treats json.Number as a
// string: an integer that fits becomes int64, any other number float64.
func plain(v any, depth int) (any, bool) {
	if depth > 128 {
		return nil, false
	}
	switch t := v.(type) {
	case json.Number:
		if n, err := strconv.ParseInt(t.String(), 10, 64); err == nil {
			return n, true
		}
		f, err := strconv.ParseFloat(t.String(), 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, false
		}
		return f, true
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, child := range t {
			c, ok := plain(child, depth+1)
			if !ok {
				return nil, false
			}
			out[k] = c
		}
		return out, true
	case []any:
		out := make([]any, len(t))
		for i, child := range t {
			c, ok := plain(child, depth+1)
			if !ok {
				return nil, false
			}
			out[i] = c
		}
		return out, true
	}
	return v, true
}

// keywords are the jsonschema-go v0.4.3 failure prefixes schemaMessage names.
var keywords = map[string]bool{
	"type": true, "enum": true, "const": true, "multipleOf": true, "minimum": true, "maximum": true,
	"exclusiveMinimum": true, "exclusiveMaximum": true, "minLength": true, "maxLength": true, "pattern": true,
	"anyOf": true, "oneOf": true, "not": true, "contains": true, "minContains": true, "maxContains": true,
	"minItems": true, "maxItems": true, "uniqueItems": true, "minProperties": true, "maxProperties": true,
	"required": true, "dependentRequired": true, "additionalProperties": true, "propertyNames": true,
	"items": true, "prefixItems": true, "additionalItems": true, "unevaluatedItems": true, "unevaluatedProperties": true,
	"format": true, "if": true, "then": true, "else": true, "false": true,
}

const genericSchemaMessage = "Arguments do not match the tool schema."

// schemaMessage names the failing schema path and keyword of a validator
// error, never the argument values the raw message quotes. The validator
// wraps each level as "validating <path>: <inner>", so the levels are split
// by unwrapping, never by searching text a schema path could contain.
func schemaMessage(err error) string {
	var segments []string
	for {
		inner := errors.Unwrap(err)
		if inner == nil {
			break
		}
		prefix, ok := strings.CutSuffix(err.Error(), ": "+inner.Error())
		path, wrapped := strings.CutPrefix(prefix, "validating ")
		if !ok || !wrapped {
			return genericSchemaMessage
		}
		segments = append(segments, path)
		err = inner
	}
	if len(segments) == 0 {
		return genericSchemaMessage
	}
	path := segments[len(segments)-1]
	for _, s := range segments {
		if strings.Contains(s, "/properties/") {
			path = s
		}
	}
	text := err.Error()
	keyword, detail := "", ""
	switch {
	case strings.HasPrefix(text, "unexpected additional properties"):
		keyword = "additionalProperties"
	default:
		name, rest, ok := strings.Cut(text, ":")
		if !ok {
			return genericSchemaMessage
		}
		if i := strings.IndexByte(name, '['); i > 0 {
			name = name[:i]
		}
		keyword, detail = name, rest
	}
	if !keywords[keyword] {
		return genericSchemaMessage
	}
	message := "Arguments do not match the tool schema at " + elicit.Clean(path, 200) + " (" + keyword + ")"
	if keyword == "required" {
		// The validator's own text: required: missing properties: ["a" "b"].
		if list, ok := strings.CutPrefix(detail, " missing properties: "); ok {
			if names := quoted(list); len(names) > 0 {
				message += ": missing " + elicit.Clean(strings.Join(names, ", "), 200)
			}
		}
	}
	return message + "."
}

// quoted extracts the Go-quoted names of a required failure, which come from
// the schema's required list.
func quoted(text string) []string {
	var names []string
	for {
		start := strings.IndexByte(text, '"')
		if start < 0 {
			return names
		}
		prefix, err := strconv.QuotedPrefix(text[start:])
		if err != nil {
			return names
		}
		name, _ := strconv.Unquote(prefix)
		names = append(names, name)
		text = text[start+len(prefix):]
	}
}
