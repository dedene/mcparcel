package config

import (
	"reflect"
	"strings"
	"testing"
)

func resolveState() State {
	return State{Local: Local{SchemaVersion: 1, CredentialProfiles: map[string]Profile{}, Aliases: map[string]string{}}, Personal: Catalog{SchemaVersion: 1, Connections: map[string]Connection{}, CredentialProfiles: map[string]ProfileRequirement{}}, Selections: Selections{SchemaVersion: 1, Connections: map[string]Selection{}}, Catalogs: map[string]Catalog{}}
}

func TestResolveInputsProfiles(t *testing.T) {
	s := resolveState()
	s.Personal.CredentialProfiles["team"] = ProfileRequirement{}
	s.Local.CredentialProfiles["work"] = Profile{Mode: "desktop", Account: "fixture"}
	s.Personal.Connections["paper"] = Connection{CredentialProfile: "team", Inputs: map[string]Input{"cmd": {Kind: "path", Description: "command"}}, Transport: Transport{Stdio: &Stdio{Command: Value{Input: &InputRef{Input: "cmd"}}, Env: map[string]Value{"TOKEN": {Secret: &SecretRef{Secret: "op://v/i/f"}}}}}}
	s.Selections.Connections["local:paper"] = Selection{Enabled: true, Inputs: map[string]string{"cmd": "/fixture"}, CredentialProfile: "work"}
	e, err := Resolve(s)
	if err != nil {
		t.Fatal(err)
	}
	c := e.Connections["local:paper"]
	if c.Connection.CredentialProfile != "work" || *c.Connection.Transport.Stdio.Command.Literal != "/fixture" || c.Connection.Transport.Stdio.Env["TOKEN"].Secret.Secret != "op://v/i/f" {
		t.Fatal(c)
	}
	if s.Personal.Connections["paper"].Transport.Stdio.Command.Input == nil {
		t.Fatal("mutation")
	}
	delete(s.Selections.Connections, "local:paper")
	e, err = Resolve(s)
	if err != nil || !reflect.DeepEqual(e.Connections["local:paper"].Blockers, []string{"connection_disabled", "config_required"}) {
		t.Fatal(e, err)
	}
	s = resolveState()
	url := "https://fixture.invalid"
	s.Personal.CredentialProfiles["team"] = ProfileRequirement{}
	s.Local.CredentialProfiles["work"] = Profile{Mode: "desktop", Account: "fixture"}
	s.Personal.Connections["web"] = Connection{CredentialProfile: "team", Inputs: map[string]Input{"url": {Kind: "url", Description: "endpoint", Default: &url}}, Transport: Transport{HTTP: &HTTP{URL: Value{Input: &InputRef{Input: "url"}}, Headers: map[string]Value{"X-Token": {Secret: &SecretRef{Secret: "op://v/i/f", Prefix: "Bearer "}}}}}}
	s.Selections.Connections["local:web"] = Selection{Enabled: true, CredentialProfile: "work"}
	e, err = Resolve(s)
	if err != nil || *e.Connections["local:web"].Connection.Transport.HTTP.URL.Literal != url || e.Connections["local:web"].Connection.Transport.HTTP.Headers["X-Token"].Secret.Prefix != "Bearer " {
		t.Fatal(e, err)
	}
}

func TestResolveUnavailableAndReview(t *testing.T) {
	s := resolveState()
	s.Personal.Connections["paper"] = Connection{Transport: Transport{Stdio: &Stdio{Command: Literal("fixture")}}}
	s.Selections.Connections["local:paper"] = Selection{Enabled: true, ReviewRequired: true}
	s.Selections.Connections["local:removed"] = Selection{Enabled: true}
	e, err := Resolve(s)
	if err != nil {
		t.Fatal(err)
	}
	r := e.Connections["local:removed"]
	if !r.Enabled || r.Available || r.Connection != nil || r.Definition != nil || !reflect.DeepEqual(r.Blockers, []string{"connection_unavailable"}) {
		t.Fatal(r)
	}
	if !e.Connections["local:paper"].Enabled || !reflect.DeepEqual(e.Connections["local:paper"].Blockers, []string{"review_required"}) {
		t.Fatal(e)
	}
}

func TestResolveDomains(t *testing.T) {
	s := resolveState()
	for i, id := range []string{"github-1", "github-2"} {
		s.Local.Sources = append(s.Local.Sources, Source{ID: id, RepositoryID: uint64(i + 1), Owner: "example", Repo: id, Path: "catalog.json", Ref: "main", Commit: strings.Repeat("a", 40)})
		s.Catalogs[id] = Catalog{SchemaVersion: 1, Domains: map[string]Domain{"shared": {Label: id}}, Connections: map[string]Connection{"paper": {Transport: Transport{Stdio: &Stdio{Command: Literal("fixture")}}}}}
	}
	e, err := Resolve(s)
	if err != nil || e.Domains["shared"].Label != "github-1" || e.Domains["other"].Label != "Other" || !reflect.DeepEqual(e.Connections["github:example/github-1#paper"].Connection.Domains, []string{"other"}) {
		t.Fatal(e, err)
	}
}
