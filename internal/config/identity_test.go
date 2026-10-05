package config

import (
	"errors"
	"reflect"
	"testing"
)

func TestCanonicalIDs(t *testing.T) {
	a, e := CanonicalLocal("paper")
	if e != nil || a != "local:paper" {
		t.Fatal(a, e)
	}
	a, e = CanonicalGitHub("Example", "tools.v2", "paper")
	if e != nil || a != "github:example/tools.v2#paper" {
		t.Fatal(a, e)
	}
	for _, s := range []string{"github:Example/tools#paper", "local:paper.x", "github:a/../#x", "github:a/b#x#y", "local: x"} {
		if ValidateCanonicalID(s) == nil {
			t.Fatal(s)
		}
	}
}

func TestAliasCollision(t *testing.T) {
	a := map[string]string{}
	for range 2 {
		if e := AddAlias(a, "paper", "local:paper"); e != nil {
			t.Fatal(e)
		}
	}
	if e := AddAlias(a, "paper", "github:example/tools#paper"); !errors.Is(e, ErrAliasCollision) || a["paper"] != "local:paper" {
		t.Fatal(a, e)
	}
}

func TestDanglingAliasNeverFallsBack(t *testing.T) {
	_, e := ResolveID("paper", map[string]string{"paper": "local:paper"}, []string{"github:example/tools#paper"})
	if !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
}

func TestAmbiguousID(t *testing.T) {
	_, e := ResolveID("paper", nil, []string{"local:paper", "github:example/tools#paper"})
	var a *AmbiguousIDError
	if !errors.Is(e, ErrAmbiguousID) || !errors.As(e, &a) || !reflect.DeepEqual(a.Candidates, []string{"github:example/tools#paper", "local:paper"}) {
		t.Fatal(e)
	}
}
