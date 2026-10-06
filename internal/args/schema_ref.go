package args

import (
	"net/url"
	"strconv"
	"strings"
)

const maxRefHops = 32

// resolveLocal follows a local JSON-pointer $ref ("#" or "#/...") from root,
// through chains of at most maxRefHops refs; a remote, anchor, missing or
// cyclic ref reports false.
func resolveLocal(root map[string]any, ref string) (map[string]any, bool) {
	seen := map[string]bool{}
	for range maxRefHops {
		if seen[ref] {
			return nil, false
		}
		seen[ref] = true
		target, ok := pointer(root, ref)
		if !ok {
			return nil, false
		}
		next, has := target["$ref"]
		if !has {
			return target, true
		}
		if ref, ok = next.(string); !ok {
			return nil, false
		}
	}
	return nil, false
}

// pointer resolves one local ref to the schema object it names.
func pointer(root map[string]any, ref string) (map[string]any, bool) {
	if ref != "#" && !strings.HasPrefix(ref, "#/") {
		return nil, false
	}
	fragment, err := url.PathUnescape(ref[1:])
	if err != nil {
		return nil, false
	}
	var node any = root
	if fragment != "" {
		for _, token := range strings.Split(fragment[1:], "/") {
			token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
			switch v := node.(type) {
			case map[string]any:
				child, ok := v[token]
				if !ok {
					return nil, false
				}
				node = child
			case []any:
				i, e := strconv.Atoi(token)
				if e != nil || i < 0 || i >= len(v) || token != strconv.Itoa(i) {
					return nil, false
				}
				node = v[i]
			default:
				return nil, false
			}
		}
	}
	target, ok := node.(map[string]any)
	return target, ok
}

// deref follows local $refs and one-item allOf wrappers, as generators emit
// them, sharing one hop budget; false when a ref cannot be followed.
func deref(root, schema map[string]any) (map[string]any, bool) {
	for range maxRefHops {
		if ref, has := schema["$ref"]; has {
			text, ok := ref.(string)
			if !ok {
				return nil, false
			}
			if schema, ok = resolveLocal(root, text); !ok {
				return nil, false
			}
			continue
		}
		if all, ok := schema["allOf"].([]any); ok && len(all) == 1 {
			if schema, ok = all[0].(map[string]any); !ok {
				return nil, false
			}
			continue
		}
		return schema, true
	}
	return nil, false
}

// maxTypeNodes bounds the schemas propertyType inspects for one argument, so
// unions that share large definitions cannot make coercion quadratic.
const maxTypeNodes = 1024

// propertyType is the type a text argument coerces to and whether null is
// also allowed: a direct type, or exactly one non-null type T in a union with
// null (a type array, or anyOf/oneOf branches). Anything else returns "",
// which sends the text as a string.
func propertyType(root map[string]any, property any) (string, bool) {
	schema, ok := property.(map[string]any)
	if !ok {
		return "", false
	}
	budget := maxTypeNodes
	return typeOf(root, schema, 0, &budget)
}

func typeOf(root, schema map[string]any, depth int, budget *int) (string, bool) {
	if *budget--; depth > 2 || *budget < 0 {
		return "", false
	}
	schema, ok := deref(root, schema)
	if !ok {
		return "", false
	}
	if _, has := schema["allOf"]; has {
		return "", false
	}
	anyOf, hasAny := schema["anyOf"]
	oneOf, hasOne := schema["oneOf"]
	if hasAny || hasOne {
		if hasAny && hasOne {
			return "", false
		}
		branches, ok := anyOf.([]any)
		if hasOne {
			branches, ok = oneOf.([]any)
		}
		if !ok {
			return "", false
		}
		var types []string
		for _, b := range branches {
			branch, ok := b.(map[string]any)
			if !ok {
				return "", false
			}
			t, nullable := typeOf(root, branch, depth+1, budget)
			if t == "" {
				return "", false
			}
			types = append(types, t)
			if nullable {
				types = append(types, "null")
			}
		}
		return nullableUnion(types)
	}
	switch t := schema["type"].(type) {
	case string:
		return t, false
	case []any:
		types := make([]string, 0, len(t))
		for _, v := range t {
			s, ok := v.(string)
			if !ok {
				return "", false
			}
			types = append(types, s)
		}
		return nullableUnion(types)
	}
	return "", false
}

// nullableUnion returns T when types hold exactly one non-null type T and at
// least one null.
func nullableUnion(types []string) (string, bool) {
	single, nulls := "", 0
	for _, t := range types {
		switch {
		case t == "null":
			nulls++
		case single == "" || single == t:
			single = t
		default:
			return "", false
		}
	}
	if single == "" || nulls == 0 {
		return "", false
	}
	return single, true
}

// rootProperties returns the root's properties, following a root-level $ref
// or one-item allOf when the root has none of its own.
func rootProperties(root map[string]any) (map[string]any, error) {
	schema := root
	if _, has := root["properties"]; !has {
		var ok bool
		if schema, ok = deref(root, root); !ok {
			return nil, nil
		}
	}
	p, exists := schema["properties"]
	if !exists {
		return nil, nil
	}
	properties, ok := p.(map[string]any)
	if !ok {
		return nil, ErrInvalidSchema
	}
	return properties, nil
}

// canonical rewrites a local ref into the walk's path form: decoded, then
// re-escaped per token, so equal targets share one key.
func canonical(ref string) string {
	fragment, err := url.PathUnescape(ref[1:])
	if err != nil || fragment == "" {
		return "#"
	}
	tokens := strings.Split(fragment[1:], "/")
	for i, token := range tokens {
		tokens[i] = escapeToken(strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~"))
	}
	return "#/" + strings.Join(tokens, "/")
}

func escapeToken(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}
