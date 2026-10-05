package config

import (
	"reflect"
	"slices"
)

func sortSet(values []string) []string {
	out := append([]string{}, values...)
	slices.Sort(out)
	return slices.Compact(out)
}

func IntersectToolPolicy(source *ToolPolicy, disabled []string) ToolPolicy {
	out := ToolPolicy{Deny: sortSet(disabled)}
	if source == nil {
		return out
	}
	out.Deny = sortSet(append(out.Deny, source.Deny...))
	if source.Allow != nil {
		allow := []string{}
		for _, name := range *source.Allow {
			if !slices.Contains(out.Deny, name) {
				allow = append(allow, name)
			}
		}
		allow = sortSet(allow)
		out.Allow = &allow
	}
	return out
}

func ToolAllowed(policy ToolPolicy, name string) bool {
	if slices.Contains(policy.Deny, name) {
		return false
	}
	return policy.Allow == nil || slices.Contains(*policy.Allow, name)
}

func PolicyWidens(oldPolicy, newPolicy *ToolPolicy) bool {
	old := IntersectToolPolicy(oldPolicy, nil)
	next := IntersectToolPolicy(newPolicy, nil)
	if next.Allow != nil {
		for _, name := range *next.Allow {
			if ToolAllowed(next, name) && !ToolAllowed(old, name) {
				return true
			}
		}
		return false
	}
	if old.Allow != nil {
		return true
	} // Finite allow -> unbounded allow.
	for _, name := range old.Deny {
		if !slices.Contains(next.Deny, name) {
			return true
		}
	}
	return false
}

func ExecutionChanged(a, b Connection) bool {
	normalize := func(c Connection) Connection {
		c = cloneConnection(c)
		if h := c.Transport.HTTP; h != nil {
			if h.Mode == "" {
				h.Mode = "auto"
			}
			if h.AllowInsecureHTTP == "" {
				h.AllowInsecureHTTP = "never"
			}
		}
		return c
	}
	a, b = normalize(a), normalize(b)
	return !reflect.DeepEqual(a.Transport, b.Transport) || !reflect.DeepEqual(a.Auth, b.Auth) || a.CredentialProfile != b.CredentialProfile || !reflect.DeepEqual(a.Inputs, b.Inputs) || PolicyWidens(a.ToolPolicy, b.ToolPolicy)
}
