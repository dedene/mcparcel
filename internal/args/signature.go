package args

import (
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/dedene/mcparcel/internal/elicit"
	"github.com/dedene/mcparcel/internal/jsonutil"
)

// A signature keeps each argument's type short: an enum or union too long
// for that falls back to its base type ("any" for a union), nesting is cut
// short, and a node budget bounds the work for one tool.
const (
	maxEnumValues     = 6
	maxLabelRunes     = 40
	maxLabelDepth     = 3
	maxSignatureNodes = 1024
)

var bareWord = regexp.MustCompile(`^[A-Za-z0-9_.:/-]+$`)

// Signature is a tool's argument list as human output shows it, without the
// parentheses: required arguments first in the schema's order, then optional
// ones by name with "?", each with a short type: "text: string, times?:
// integer". Enums read "a|b|c", arrays "string[]", nested objects "object"
// and anything it cannot name "any". Names and enum values are cleaned of
// terminal controls. False when the schema is not a JSON object; a missing
// schema has no arguments.
func Signature(inputSchema json.RawMessage) (string, bool) {
	if len(inputSchema) == 0 {
		return "", true
	}
	v, err := jsonutil.Decode(inputSchema)
	root, ok := v.(map[string]any)
	if err != nil || !ok {
		return "", false
	}
	schema := root
	if _, has := root["properties"]; !has {
		if schema, ok = deref(root, root); !ok {
			return "", true
		}
	}
	properties, _ := schema["properties"].(map[string]any)
	budget := maxSignatureNodes
	var params []string
	seen := map[string]bool{}
	required, _ := schema["required"].([]any)
	for _, r := range required {
		name, ok := r.(string)
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		params = append(params, argName(name)+": "+propertyLabel(root, properties[name], &budget))
	}
	var optional []string
	for name := range properties {
		if !seen[name] {
			optional = append(optional, name)
		}
	}
	sort.Strings(optional)
	for _, name := range optional {
		params = append(params, argName(name)+"?: "+propertyLabel(root, properties[name], &budget))
	}
	return strings.Join(params, ", "), true
}

func argName(name string) string { return word(elicit.Clean(name, 64)) }

// word quotes text that would not read as one token in a signature.
func word(text string) string {
	if bareWord.MatchString(text) {
		return text
	}
	return strconv.Quote(text)
}

func propertyLabel(root map[string]any, property any, budget *int) string {
	schema, ok := property.(map[string]any)
	if !ok {
		return "any"
	}
	return label(root, schema, 0, budget)
}

func label(root, schema map[string]any, depth int, budget *int) string {
	if *budget--; depth > maxLabelDepth || *budget < 0 {
		return "any"
	}
	schema, ok := deref(root, schema)
	if !ok {
		return "any"
	}
	if values, has := enumValues(schema); has {
		if text, base := enumLabel(values); text != "" {
			return text
		} else if _, typed := schema["type"]; !typed {
			return base
		}
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		if branches, ok := schema[key].([]any); ok {
			labels := make([]string, 0, len(branches))
			for _, b := range branches {
				branch, ok := b.(map[string]any)
				if !ok {
					return "any"
				}
				labels = append(labels, label(root, branch, depth+1, budget))
			}
			return union(labels)
		}
	}
	switch t := schema["type"].(type) {
	case string:
		return typeLabel(root, schema, t, depth, budget)
	case []any:
		labels := make([]string, 0, len(t))
		for _, v := range t {
			s, _ := v.(string)
			labels = append(labels, typeLabel(root, schema, s, depth, budget))
		}
		return union(labels)
	}
	if _, has := schema["properties"]; has {
		return "object"
	}
	if _, has := schema["items"]; has {
		return typeLabel(root, schema, "array", depth, budget)
	}
	return "any"
}

func typeLabel(root, schema map[string]any, t string, depth int, budget *int) string {
	switch t {
	case "string", "integer", "number", "boolean", "object", "null":
		return t
	case "array":
		items, ok := schema["items"].(map[string]any)
		if !ok {
			return "array"
		}
		item := label(root, items, depth+1, budget)
		if strings.Contains(item, "|") {
			item = "(" + item + ")"
		}
		return item + "[]"
	}
	return "any"
}

// union joins distinct labels with "|"; one it cannot name, or a union too
// long for a line, is "any".
func union(labels []string) string {
	var distinct []string
	for _, l := range labels {
		if l == "any" {
			return "any"
		}
		if !slices.Contains(distinct, l) {
			distinct = append(distinct, l)
		}
	}
	text := strings.Join(distinct, "|")
	if len(distinct) == 0 || len([]rune(text)) > maxLabelRunes {
		return "any"
	}
	return text
}

// enumValues is an enum's values, or a const as a one-value enum.
func enumValues(schema map[string]any) ([]any, bool) {
	if values, ok := schema["enum"].([]any); ok && len(values) > 0 {
		return values, true
	}
	if value, ok := schema["const"]; ok {
		return []any{value}, true
	}
	return nil, false
}

// enumLabel is "a|b|c" for a short enum of scalars, else "" with the base
// type its values share ("string", "number" or "any").
func enumLabel(values []any) (text, base string) {
	labels := make([]string, 0, len(values))
	strs, nums := 0, 0
	for _, v := range values {
		switch value := v.(type) {
		case string:
			strs++
			labels = append(labels, word(elicit.Clean(value, maxLabelRunes)))
		case json.Number:
			nums++
			labels = append(labels, value.String())
		case bool:
			labels = append(labels, strconv.FormatBool(value))
		case nil:
			labels = append(labels, "null")
		default:
			labels = append(labels, "")
		}
	}
	switch {
	case strs == len(values):
		base = "string"
	case nums == len(values):
		base = "number"
	default:
		base = "any"
	}
	if len(values) > maxEnumValues || slices.Contains(labels, "") {
		return "", base
	}
	text = strings.Join(labels, "|")
	if len([]rune(text)) > maxLabelRunes {
		return "", base
	}
	return text, base
}
