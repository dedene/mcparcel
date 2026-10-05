package config

import (
	"encoding/json"
	"testing"
)

func TestToolPolicyIntersection(t *testing.T) {
	allow := []string{"read", "write"}
	p := IntersectToolPolicy(&ToolPolicy{Allow: &allow, Deny: []string{"write"}}, []string{"read"})
	if ToolAllowed(p, "read") || ToolAllowed(p, "write") || p.Allow == nil || len(*p.Allow) != 0 {
		t.Fatal(p)
	}
	if !ToolAllowed(IntersectToolPolicy(nil, []string{"write"}), "read") {
		t.Fatal("unbounded")
	}
	empty := []string{}
	if ToolAllowed(IntersectToolPolicy(&ToolPolicy{Allow: &empty}, nil), "read") {
		t.Fatal("empty")
	}
}

func TestPolicyNeverWidens(t *testing.T) {
	names := []string{"a", "b", "c"}
	for a := -1; a < 8; a++ {
		for d := 0; d < 8; d++ {
			for x := 0; x < 8; x++ {
				subset := func(mask int) []string {
					out := []string{}
					for i, n := range names {
						if mask&(1<<i) != 0 {
							out = append(out, n)
						}
					}
					return out
				}
				p := ToolPolicy{Deny: subset(d)}
				if a >= 0 {
					v := subset(a)
					p.Allow = &v
				}
				before, _ := json.Marshal(p)
				effective := IntersectToolPolicy(&p, subset(x))
				for _, n := range names {
					if ToolAllowed(effective, n) && !ToolAllowed(p, n) {
						t.Fatal(p, effective, n)
					}
				}
				after, _ := json.Marshal(p)
				if string(before) != string(after) {
					t.Fatal("mutation")
				}
			}
		}
	}
}

func TestExecutionChanged(t *testing.T) {
	a := Connection{Transport: Transport{HTTP: &HTTP{URL: Literal("https://a.invalid")}}}
	b := cloneConnection(a)
	b.Description = "edit"
	if ExecutionChanged(a, b) {
		t.Fatal("metadata")
	}
	b.Transport.HTTP.URL = Literal("https://b.invalid")
	if !ExecutionChanged(a, b) {
		t.Fatal("endpoint")
	}
	b = cloneConnection(a)
	b.ToolPolicy = &ToolPolicy{Deny: []string{"write"}}
	if ExecutionChanged(a, b) || !ExecutionChanged(b, a) {
		t.Fatal("policy")
	}
}
