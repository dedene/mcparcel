package config

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func storeConnection() Connection {
	return Connection{Transport: Transport{Stdio: &Stdio{Command: Literal("fixture")}}}
}

func seedStore(t *testing.T, p Paths, revision uint64, legacy bool) {
	t.Helper()
	c := Catalog{SchemaVersion: 1, Connections: map[string]Connection{"paper": storeConnection()}}
	b, e := json.Marshal(c)
	if e != nil {
		t.Fatal(e)
	}
	testWrite(t, p.PersonalFile, b, 0o644)
	if !legacy {
		s := Selections{SchemaVersion: 1, Revision: revision, Connections: map[string]Selection{"local:paper": {Enabled: true}}}
		b, e = json.Marshal(s)
		if e != nil {
			t.Fatal(e)
		}
		testWrite(t, p.SelectionsFile, b, 0o644)
	}
}

func stateFiles(t *testing.T, p Paths) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, path := range []string{p.ConfigFile, p.PersonalFile, p.SelectionsFile} {
		b, e := os.ReadFile(path)
		if errors.Is(e, os.ErrNotExist) {
			out[path] = "absent"
		} else if e != nil {
			t.Fatal(e)
		} else {
			out[path] = string(b)
		}
	}
	return out
}

func TestStoreConcurrentSave(t *testing.T) {
	p := storePaths(t)
	seedStore(t, p, 4, false)
	start := make(chan struct{})
	type result struct {
		s     State
		e     error
		calls int
	}
	results := make(chan result, 2)
	for _, label := range []string{"first", "second"} {
		go func() {
			calls := 0
			<-start
			s, e := NewStore(p).Update(context.Background(), 4, func(s *State) error {
				calls++
				c := s.Personal.Connections["paper"]
				c.Label = label
				s.Personal.Connections["paper"] = c
				return nil
			})
			results <- result{s, e, calls}
		}()
	}
	close(start)
	winner, loser := <-results, <-results
	if winner.e != nil {
		winner, loser = loser, winner
	}
	if winner.e != nil || winner.s.Selections.Revision != 5 || winner.calls != 1 || !errors.Is(loser.e, ErrConfigConflict) || loser.calls != 0 || !reflect.DeepEqual(loser.s, State{}) {
		t.Fatalf("winner=%+v err=%v; loser=%+v err=%v", winner, winner.e, loser, loser.e)
	}
	s, e := NewStore(p).Read(context.Background())
	if e != nil || s.Personal.Connections["paper"].Label != winner.s.Personal.Connections["paper"].Label {
		t.Fatal(s, e)
	}
	winner.s.Personal.Connections["injected"] = storeConnection()
	s, e = NewStore(p).Read(context.Background())
	if e != nil || len(s.Personal.Connections) != 1 {
		t.Fatal(s, e)
	}
}

func TestStoreCallbackFailure(t *testing.T) {
	p := storePaths(t)
	seedStore(t, p, 0, false)
	before := stateFiles(t, p)
	sentinel := errors.New("callback")
	s, e := NewStore(p).Update(context.Background(), 0, func(s *State) error { s.Personal.Connections["other"] = storeConnection(); return sentinel })
	if !errors.Is(e, sentinel) || errors.Is(e, ErrConfigWrite) || !reflect.DeepEqual(s, State{}) || !reflect.DeepEqual(before, stateFiles(t, p)) {
		t.Fatal(s, e)
	}
}

func TestStoreRejectsInvalidPrefix(t *testing.T) {
	p := storePaths(t)
	seedStore(t, p, 0, false)
	c := Catalog{SchemaVersion: 1, Connections: map[string]Connection{"paper": {Inputs: map[string]Input{"old": {Kind: "path", Description: "Command"}}, Transport: Transport{Stdio: &Stdio{Command: Value{Input: &InputRef{Input: "old"}}}}}}}
	b, _ := json.Marshal(c)
	testWrite(t, p.PersonalFile, b, 0o644)
	sel := Selections{SchemaVersion: 1, Connections: map[string]Selection{"local:paper": {Enabled: true, Inputs: map[string]string{"old": "/fixture"}}}}
	b, _ = json.Marshal(sel)
	testWrite(t, p.SelectionsFile, b, 0o644)
	before := stateFiles(t, p)
	_, e := NewStore(p).Update(context.Background(), 0, func(s *State) error {
		c := s.Personal.Connections["paper"]
		c.Inputs["new"] = c.Inputs["old"]
		delete(c.Inputs, "old")
		c.Transport.Stdio.Command = Value{Input: &InputRef{Input: "new"}}
		s.Personal.Connections["paper"] = c
		sel := s.Selections.Connections["local:paper"]
		sel.Inputs = map[string]string{"new": "/fixture"}
		s.Selections.Connections["local:paper"] = sel
		addStoreSource(s)
		return nil
	})
	if !errors.Is(e, ErrConfig) || !reflect.DeepEqual(before, stateFiles(t, p)) {
		t.Fatal(e)
	}
	if _, e := os.Stat(p.DataDir); !errors.Is(e, os.ErrNotExist) {
		t.Fatal(e)
	}
}

func TestStoreNoopAndOverflow(t *testing.T) {
	p := storePaths(t)
	seedStore(t, p, 7, false)
	before := stateFiles(t, p)
	info, _ := os.Stat(p.SelectionsFile)
	s, e := NewStore(p).Update(context.Background(), 7, func(*State) error { return nil })
	after, _ := os.Stat(p.SelectionsFile)
	if e != nil || s.Selections.Revision != 7 || !os.SameFile(info, after) || !reflect.DeepEqual(before, stateFiles(t, p)) {
		t.Fatal(s, e)
	}
	seedStore(t, p, MaxRevision, false)
	before = stateFiles(t, p)
	_, e = NewStore(p).Update(context.Background(), MaxRevision, func(s *State) error { s.Personal.Connections["other"] = storeConnection(); return nil })
	if !errors.Is(e, ErrRevisionExhausted) || !reflect.DeepEqual(before, stateFiles(t, p)) {
		t.Fatal(e)
	}
	p = storePaths(t)
	seedStore(t, p, 0, true)
	s, e = NewStore(p).Update(context.Background(), 0, func(*State) error { return nil })
	if e != nil || !s.Legacy {
		t.Fatal(s, e)
	}
	if _, e = os.Stat(p.SelectionsFile); !errors.Is(e, os.ErrNotExist) {
		t.Fatal(e)
	}
}

func TestStoreLegacyFreeze(t *testing.T) {
	p := storePaths(t)
	seedStore(t, p, 0, true)
	seen := false
	s, e := NewStore(p).update(context.Background(), 0, func(s *State) error { s.Personal.Connections["other"] = storeConnection(); return nil }, storeHooks{AfterStep: func(step string) error {
		if step == "reserve" {
			seen = true
			s, e := readStateUnlocked(p)
			if e != nil || s.Legacy || !s.Selections.Connections["local:paper"].Enabled || s.Selections.Connections["local:other"].Enabled {
				t.Fatalf("bridge: %+v %v", s, e)
			}
		}
		return nil
	}})
	if e != nil || !seen || s.Legacy || s.Selections.Revision != 1 || !s.Selections.Connections["local:paper"].Enabled || s.Selections.Connections["local:other"].Enabled {
		t.Fatal(s, e)
	}
}

func TestStoreReadDuringWrite(t *testing.T) {
	p := storePaths(t)
	seedStore(t, p, 0, false)
	reserved := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, e := NewStore(p).update(context.Background(), 0, func(s *State) error { s.Personal.Connections["other"] = storeConnection(); return nil }, storeHooks{AfterStep: func(step string) error {
			if step == "reserve" {
				close(reserved)
				<-release
			}
			return nil
		}})
		done <- e
	}()
	<-reserved
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	_, e := ReadState(ctx, p)
	cancel()
	if !errors.Is(e, context.DeadlineExceeded) {
		close(release)
		t.Fatal(e)
	}
	type result struct {
		s State
		e error
	}
	reading := make(chan struct{})
	read := make(chan result, 1)
	go func() { close(reading); s, e := NewStore(p).Read(context.Background()); read <- result{s, e} }()
	<-reading
	select {
	case r := <-read:
		close(release)
		t.Fatalf("reader escaped lock: %+v", r)
	default:
	}
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	r := <-read
	if r.e != nil || r.s.Selections.Revision != 1 || len(r.s.Personal.Connections) != 2 {
		t.Fatal(r)
	}
}

func TestStoreReviewMarkers(t *testing.T) {
	for _, kind := range []string{"endpoint", "profile", "binding", "description", "narrow", "widen", "disable", "review", "timeout", "unused-input"} {
		t.Run(kind, func(t *testing.T) {
			p := storePaths(t)
			seedStore(t, p, 0, false)
			_, e := NewStore(p).Update(context.Background(), 0, func(s *State) error {
				c := s.Personal.Connections["paper"]
				c.Label = "Initial"
				switch kind {
				case "profile":
					s.Personal.CredentialProfiles = map[string]ProfileRequirement{"team": {}}
					s.Local.CredentialProfiles = map[string]Profile{"team": {Mode: "desktop-service-account", Account: "Fixture", BootstrapRef: "op://v/i/f"}}
					c.CredentialProfile = "team"
					sel := s.Selections.Connections["local:paper"]
					sel.CredentialProfile = "team"
					s.Selections.Connections["local:paper"] = sel
				case "unused-input":
					c.Inputs = map[string]Input{"extra": {Kind: "string", Description: "Extra requirement"}}
				case "binding":
					c.Inputs = map[string]Input{"cmd": {Kind: "path", Description: "Command"}}
					c.Transport.Stdio.Command = Value{Input: &InputRef{Input: "cmd"}}
					sel := s.Selections.Connections["local:paper"]
					sel.Inputs = map[string]string{"cmd": "/fixture"}
					s.Selections.Connections["local:paper"] = sel
				case "narrow", "widen":
					c.ToolPolicy = &ToolPolicy{Deny: []string{"write"}}
				case "review":
					sel := s.Selections.Connections["local:paper"]
					sel.ReviewRequired = true
					s.Selections.Connections["local:paper"] = sel
				}
				s.Personal.Connections["paper"] = c
				return nil
			})
			if e != nil {
				t.Fatal(e)
			}
			s, e := NewStore(p).Update(context.Background(), 1, func(s *State) error {
				if kind != "review" {
					sel := s.Selections.Connections["local:paper"]
					sel.ReviewRequired = false
					s.Selections.Connections["local:paper"] = sel
				}
				c := s.Personal.Connections["paper"]
				switch kind {
				case "endpoint", "disable":
					c.Transport.Stdio.Command = Literal("changed")
				case "profile":
					profile := s.Local.CredentialProfiles["team"]
					profile.Account = "Changed"
					s.Local.CredentialProfiles["team"] = profile
				case "binding":
					sel := s.Selections.Connections["local:paper"]
					sel.Inputs["cmd"] = "/changed"
					s.Selections.Connections["local:paper"] = sel
				case "description":
					c.Description = "Metadata"
				case "narrow":
					c.ToolPolicy.Deny = append(c.ToolPolicy.Deny, "read")
				case "widen":
					c.ToolPolicy = nil
				case "unused-input":
					input := c.Inputs["extra"]
					text := "ready"
					input.Default = &text
					c.Inputs["extra"] = input
				case "timeout":
					c.CallTimeout = "2s"
				}
				s.Personal.Connections["paper"] = c
				if kind == "disable" {
					sel := s.Selections.Connections["local:paper"]
					sel.Enabled = false
					s.Selections.Connections["local:paper"] = sel
				}
				return nil
			})
			want := kind == "endpoint" || kind == "profile" || kind == "binding" || kind == "widen" || kind == "review" || kind == "unused-input"
			if e != nil || s.Selections.Connections["local:paper"].ReviewRequired != want {
				t.Fatal(s, e, want)
			}
		})
	}
}

func TestStoreMutationGuards(t *testing.T) {
	for _, kind := range []string{"revision", "legacy", "unknown-enable", "nil", "invalid", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			p := storePaths(t)
			seedStore(t, p, 0, false)
			before := stateFiles(t, p)
			mutate := func(s *State) error {
				switch kind {
				case "revision":
					s.Selections.Revision++
				case "legacy":
					s.Legacy = true
				case "unknown-enable":
					s.Selections.Connections["local:unknown"] = Selection{Enabled: true}
				case "invalid":
					s.Personal.Connections["bad"] = Connection{}
				case "oversize":
					c := s.Personal.Connections["paper"]
					c.Description = strings.Repeat("a", maxConfigBytes)
					s.Personal.Connections["paper"] = c
				}
				return nil
			}
			if kind == "nil" {
				mutate = nil
			}
			_, e := NewStore(p).Update(context.Background(), 0, mutate)
			if !errors.Is(e, ErrConfig) || !reflect.DeepEqual(before, stateFiles(t, p)) {
				t.Fatal(e)
			}
		})
	}
	p := storePaths(t)
	seedStore(t, p, 0, false)
	var calls atomic.Int32
	_, e := NewStore(p).Update(context.Background(), 1, func(*State) error { calls.Add(1); return nil })
	if !errors.Is(e, ErrConfigConflict) || calls.Load() != 0 {
		t.Fatal(e)
	}
}

func TestStoreUnavailableAndDisableBridge(t *testing.T) {
	p := storePaths(t)
	seedStore(t, p, 0, false)
	b, _ := json.Marshal(Selections{SchemaVersion: 1, Connections: map[string]Selection{"local:paper": {Enabled: true}, "local:missing": {Enabled: true}}})
	testWrite(t, p.SelectionsFile, b, 0o644)
	s, e := NewStore(p).update(context.Background(), 0, func(s *State) error {
		s.Personal.Connections["missing"] = storeConnection()
		c := s.Personal.Connections["paper"]
		c.Transport.Stdio.Command = Literal("changed")
		s.Personal.Connections["paper"] = c
		sel := s.Selections.Connections["local:paper"]
		sel.Enabled = false
		s.Selections.Connections["local:paper"] = sel
		return nil
	}, storeHooks{AfterStep: func(step string) error {
		if step == "reserve" {
			bridge, e := readStateUnlocked(p)
			if e != nil || bridge.Selections.Connections["local:paper"].Enabled || !bridge.Selections.Connections["local:missing"].ReviewRequired {
				t.Fatal(bridge, e)
			}
		}
		return nil
	}})
	if e != nil || !s.Selections.Connections["local:missing"].ReviewRequired || s.Selections.Connections["local:paper"].ReviewRequired {
		t.Fatal(s, e)
	}
	s, e = NewStore(p).Update(context.Background(), 1, func(s *State) error { delete(s.Personal.Connections, "missing"); return nil })
	if e != nil || !s.Selections.Connections["local:missing"].Enabled {
		t.Fatal(s, e)
	}
}

func TestStoreWriteErrorClassification(t *testing.T) {
	for _, e := range []error{ErrConfig, ErrUnsafePath, ErrConfigConflict, context.Canceled, context.DeadlineExceeded} {
		if storeWriteError(e) != e {
			t.Fatal(e)
		}
	}
	for _, e := range []error{ErrDurability, errors.New("I/O")} {
		wrapped := storeWriteError(e)
		if !errors.Is(wrapped, ErrConfigWrite) || !errors.Is(wrapped, e) {
			t.Fatal(wrapped)
		}
	}
	p := storePaths(t)
	seedStore(t, p, 0, false)
	s, e := NewStore(p).update(context.Background(), 0, func(s *State) error { s.Personal.Connections["other"] = storeConnection(); return nil }, storeHooks{AfterStep: func(step string) error {
		if step == "reserve" {
			return ErrDurability
		}
		return nil
	}})
	if !errors.Is(e, ErrConfigWrite) || !errors.Is(e, ErrDurability) || !reflect.DeepEqual(s, State{}) {
		t.Fatal(s, e)
	}
	reloaded, e := ReadState(context.Background(), p)
	if e != nil || reloaded.Selections.Revision != 1 || len(reloaded.Personal.Connections) != 1 {
		t.Fatal(reloaded, e)
	}
}

func TestStoreConcurrentFirstWrite(t *testing.T) {
	p := storePaths(t)
	start := make(chan struct{})
	results := make(chan error, 8)
	var calls atomic.Int32
	for range 8 {
		go func() {
			<-start
			_, e := NewStore(p).Update(context.Background(), 0, func(s *State) error { calls.Add(1); s.Personal.Connections["paper"] = storeConnection(); return nil })
			results <- e
		}()
	}
	close(start)
	winners, conflicts := 0, 0
	for range 8 {
		e := <-results
		switch {
		case e == nil:
			winners++
		case errors.Is(e, ErrConfigConflict):
			conflicts++
		default:
			t.Fatal(e)
		}
	}
	s, e := ReadState(context.Background(), p)
	if e != nil || winners != 1 || conflicts != 7 || calls.Load() != 1 || s.Selections.Revision != 1 || s.Legacy || s.Selections.Connections["local:paper"].Enabled {
		t.Fatal(s, e, winners, conflicts, calls.Load())
	}
}
