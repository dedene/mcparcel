package args

import "strconv"

const maxSchemaPaths = 1000

// maxValidationWork bounds the validator calls one Validate may cause, each
// weighed by the size of the instance it checks: a failing branch prints its
// instance, and fan-out multiplies at every level of argument nesting.
const maxValidationWork = 500_000

// dataKeyword names keywords whose values are instance data, not schemas.
var dataKeyword = map[string]bool{"const": true, "enum": true, "default": true, "examples": true}

// schemaMap names keywords whose value maps names to schemas; its keys are
// names, never keywords.
var schemaMap = map[string]bool{
	"properties": true, "patternProperties": true, "$defs": true, "definitions": true,
	"dependentSchemas": true, "dependencies": true,
}

// schemaGraph holds a schema's subschemas keyed by JSON-pointer path.
type schemaGraph struct {
	nodes map[string]map[string]any
}

// inspectSchema reports whether the validator can be given root: every $ref
// is local and resolvable, there are no dynamic or recursive refs and no
// nested $id, and the edges that apply to the same instance ($ref and the
// in-place applicators) form no cycle and fan out to at most maxSchemaPaths
// paths.
func inspectSchema(root map[string]any) (*schemaGraph, bool) {
	g := &schemaGraph{nodes: map[string]map[string]any{}}
	safe := true
	var walk func(v any, path string, names bool)
	walk = func(v any, path string, names bool) {
		switch t := v.(type) {
		case map[string]any:
			if names {
				for k, child := range t {
					walk(child, path+"/"+escapeToken(k), false)
				}
				return
			}
			if _, nested := t["$id"].(string); nested && path != "#" {
				safe = false // refs inside would resolve against another base
			}
			for _, k := range []string{"$dynamicRef", "$recursiveRef", "$dynamicAnchor", "$recursiveAnchor"} {
				if _, has := t[k]; has {
					safe = false
				}
			}
			if ref, has := t["$ref"]; has {
				text, ok := ref.(string)
				if !ok {
					safe = false
				} else if _, ok = pointer(root, text); !ok {
					safe = false
				}
			}
			g.nodes[path] = t
			for k, child := range t {
				if !dataKeyword[k] {
					walk(child, path+"/"+escapeToken(k), schemaMap[k])
				}
			}
		case []any:
			for i, child := range t {
				walk(child, path+"/"+strconv.Itoa(i), false)
			}
		}
	}
	walk(root, "#", false)
	if !safe {
		return nil, false
	}
	const visiting = -1
	paths := map[string]int{}
	var count func(path string) int
	count = func(path string) int {
		if n, seen := paths[path]; seen {
			return n // visiting reports a cycle as -1
		}
		if _, ok := g.nodes[path]; !ok {
			return 1
		}
		paths[path] = visiting
		total := 1
		for _, next := range g.edges(path) {
			n := count(next)
			if n == visiting || total+n > maxSchemaPaths {
				paths[path] = visiting
				return visiting
			}
			total += n
		}
		paths[path] = total
		return total
	}
	for path := range g.nodes {
		if count(path) == visiting {
			return nil, false
		}
	}
	return g, true
}

// edges are the subschemas that apply to the same instance as the schema at
// path: its $ref and in-place applicators, including draft-07 dependencies.
func (g *schemaGraph) edges(path string) []string {
	s := g.nodes[path]
	var out []string
	if ref, ok := s["$ref"].(string); ok {
		out = append(out, canonical(ref))
	}
	for _, k := range []string{"allOf", "anyOf", "oneOf"} {
		if list, ok := s[k].([]any); ok {
			for i := range list {
				out = append(out, path+"/"+k+"/"+strconv.Itoa(i))
			}
		}
	}
	for _, k := range []string{"not", "if", "then", "else"} {
		if _, ok := s[k].(map[string]any); ok {
			out = append(out, path+"/"+k)
		}
	}
	for _, k := range []string{"dependentSchemas", "dependencies"} {
		if deps, ok := s[k].(map[string]any); ok {
			for name, dep := range deps {
				if _, ok := dep.(map[string]any); ok {
					out = append(out, path+"/"+k+"/"+escapeToken(name))
				}
			}
		}
	}
	return out
}

// weighed is an argument value with its validation weight: one per node,
// plus one per 64 bytes of string or key text.
type weighed struct {
	weight int
	props  map[string]*weighed
	items  []*weighed
}

func weigh(v any) *weighed {
	w := &weighed{weight: 1}
	switch t := v.(type) {
	case map[string]any:
		w.props = make(map[string]*weighed, len(t))
		for k, child := range t {
			c := weigh(child)
			w.props[k] = c
			w.weight += c.weight + len(k)/64
		}
	case []any:
		w.items = make([]*weighed, len(t))
		for i, child := range t {
			c := weigh(child)
			w.items[i] = c
			w.weight += c.weight
		}
	case string:
		w.weight += len(t) / 64
	}
	return w
}

// withinBudget walks the subschemas the validator may apply to instance, an
// upper bound on its work (every branch, pattern and conditional), and
// reports whether that work stays within maxValidationWork.
func (g *schemaGraph) withinBudget(instance any) bool {
	budget := maxValidationWork
	var visit func(path string, w *weighed) bool
	visit = func(path string, w *weighed) bool {
		if budget -= w.weight; budget < 0 {
			return false
		}
		s, ok := g.nodes[path]
		if !ok {
			return true // a boolean schema applies nothing further
		}
		for _, next := range g.edges(path) {
			if !visit(next, w) {
				return false
			}
		}
		// apply visits the subschema under keyword kw, when s has one.
		apply := func(kw string, w *weighed) bool {
			_, has := s[kw]
			return !has || visit(path+"/"+kw, w)
		}
		props, _ := s["properties"].(map[string]any)
		patterns, _ := s["patternProperties"].(map[string]any)
		for k, c := range w.props {
			if _, has := props[k]; has && !visit(path+"/properties/"+escapeToken(k), c) {
				return false
			}
			for p := range patterns {
				if !visit(path+"/patternProperties/"+escapeToken(p), c) {
					return false
				}
			}
			if !apply("additionalProperties", c) || !apply("unevaluatedProperties", c) ||
				!apply("propertyNames", &weighed{weight: 1 + len(k)/64}) {
				return false
			}
		}
		tuple, _ := s["items"].([]any)
		prefix, _ := s["prefixItems"].([]any)
		for i, c := range w.items {
			index := strconv.Itoa(i)
			if i < len(tuple) && !visit(path+"/items/"+index, c) ||
				i < len(prefix) && !visit(path+"/prefixItems/"+index, c) {
				return false
			}
			if _, single := s["items"].(map[string]any); single && !visit(path+"/items", c) {
				return false
			}
			if !apply("additionalItems", c) || !apply("contains", c) || !apply("unevaluatedItems", c) {
				return false
			}
		}
		return true
	}
	return visit("#", weigh(instance))
}
