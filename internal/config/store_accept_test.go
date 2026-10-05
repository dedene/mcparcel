package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const acceptedPaper = "github:example/tools#paper"

func seedAcceptedStore(t *testing.T, p Paths) State {
	t.Helper()
	seedStore(t, p, 3, false)
	s, err := NewStore(p).Update(context.Background(), 3, func(s *State) error {
		addStoreSource(s)
		s.Catalogs["github-1"] = Catalog{SchemaVersion: 1, Connections: map[string]Connection{
			"paper":  {Transport: Transport{HTTP: &HTTP{URL: Literal("https://paper.example.invalid/mcp")}}},
			"second": storeConnection(),
		}}
		s.Selections.Connections[acceptedPaper] = Selection{Enabled: true}
		s.Selections.Connections["github:example/tools#second"] = Selection{Enabled: true}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func acceptedExecutionChange(s *State) error {
	s.Local.Sources[0].Commit = strings.Repeat("b", 40)
	c := s.Catalogs["github-1"].Connections["paper"]
	c.Transport.HTTP.URL = Literal("https://changed.example.invalid/mcp")
	s.Catalogs["github-1"].Connections["paper"] = c
	return nil
}

func TestStoreAcceptChangedExecution(t *testing.T) {
	p := storePaths(t)
	seedAcceptedStore(t, p)
	bridgeSeen := false
	s, err := NewStore(p).updateAccepted(context.Background(), 4, []string{acceptedPaper}, acceptedExecutionChange, storeHooks{AfterStep: func(step string) error {
		if step == "reserve" {
			bridgeSeen = true
			bridge, e := readStateUnlocked(p)
			if e != nil || bridge.Selections.Revision != 5 || !bridge.Selections.Connections[acceptedPaper].Enabled || !bridge.Selections.Connections[acceptedPaper].ReviewRequired {
				t.Fatal(bridge, e)
			}
		}
		return nil
	}})
	if err != nil || !bridgeSeen || s.Selections.Revision != 5 || !s.Selections.Connections[acceptedPaper].Enabled || s.Selections.Connections[acceptedPaper].ReviewRequired {
		t.Fatal(s, err)
	}
	loaded, err := NewStore(p).Read(context.Background())
	if err != nil || !reflect.DeepEqual(s, loaded) {
		t.Fatal(loaded, err)
	}
}

func TestStoreAcceptExistingMarker(t *testing.T) {
	p := storePaths(t)
	seedAcceptedStore(t, p)
	s, err := NewStore(p).Update(context.Background(), 4, func(s *State) error {
		selection := s.Selections.Connections[acceptedPaper]
		selection.ReviewRequired = true
		s.Selections.Connections[acceptedPaper] = selection
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err = NewStore(p).UpdateAccepted(context.Background(), s.Selections.Revision, []string{acceptedPaper}, func(*State) error { return nil })
	if err != nil || s.Selections.Revision != 6 || s.Selections.Connections[acceptedPaper].ReviewRequired {
		t.Fatal(s, err)
	}
	before := stateFiles(t, p)
	info, err := os.Stat(p.SelectionsFile)
	if err != nil {
		t.Fatal(err)
	}
	s, err = NewStore(p).UpdateAccepted(context.Background(), 6, []string{acceptedPaper}, func(*State) error { return nil })
	after, statErr := os.Stat(p.SelectionsFile)
	if err != nil || statErr != nil || s.Selections.Revision != 6 || !os.SameFile(info, after) || !reflect.DeepEqual(before, stateFiles(t, p)) {
		t.Fatal(s, err, statErr)
	}
}

func TestStoreRawAuthBridge(t *testing.T) {
	for _, accept := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "accepted"}[accept], func(t *testing.T) {
			p := storePaths(t)
			seedAcceptedStore(t, p)
			_, err := NewStore(p).Update(context.Background(), 4, func(s *State) error {
				cat := s.Catalogs["github-1"]
				cat.CredentialProfiles = map[string]ProfileRequirement{"team": {}, "shared": {}}
				c := cat.Connections["paper"]
				c.CredentialProfile = "team"
				cat.Connections["paper"] = c
				s.Local.Sources[0].Commit = strings.Repeat("c", 40)
				s.Catalogs["github-1"] = cat
				s.Local.CredentialProfiles["work"] = Profile{Mode: "desktop", Account: "Fixture"}
				selection := s.Selections.Connections[acceptedPaper]
				selection.CredentialProfile = "work"
				s.Selections.Connections[acceptedPaper] = selection
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = NewStore(p).UpdateAccepted(context.Background(), 5, []string{acceptedPaper}, func(*State) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			ids := []string(nil)
			if accept {
				ids = []string{acceptedPaper}
			}
			crash := errors.New("after config")
			_, err = NewStore(p).updateAccepted(context.Background(), 6, ids, func(s *State) error {
				s.Local.Sources[0].Commit = strings.Repeat("d", 40)
				c := s.Catalogs["github-1"].Connections["paper"]
				c.CredentialProfile = "shared"
				s.Catalogs["github-1"].Connections["paper"] = c
				return nil
			}, storeHooks{AfterStep: func(step string) error {
				if step == "config" {
					return crash
				}
				return nil
			}})
			if !errors.Is(err, crash) {
				t.Fatal(err)
			}
			s, err := NewStore(p).Read(context.Background())
			if err != nil || s.Local.Sources[0].Commit != strings.Repeat("d", 40) || !s.Selections.Connections[acceptedPaper].Enabled || !s.Selections.Connections[acceptedPaper].ReviewRequired {
				t.Fatal(s, err)
			}
			s, err = NewStore(p).UpdateAccepted(context.Background(), 7, []string{acceptedPaper}, func(*State) error { return nil })
			if err != nil || s.Selections.Connections[acceptedPaper].ReviewRequired || s.Selections.Connections[acceptedPaper].CredentialProfile != "work" {
				t.Fatal(s, err)
			}
		})
	}
}

func TestStoreAcceptScoped(t *testing.T) {
	p := storePaths(t)
	seedAcceptedStore(t, p)
	s, err := NewStore(p).UpdateAccepted(context.Background(), 4, []string{acceptedPaper}, func(s *State) error {
		if err := acceptedExecutionChange(s); err != nil {
			return err
		}
		c := s.Catalogs["github-1"].Connections["second"]
		c.Transport.Stdio.Command = Literal("changed")
		s.Catalogs["github-1"].Connections["second"] = c
		return nil
	})
	if err != nil || s.Selections.Connections[acceptedPaper].ReviewRequired || !s.Selections.Connections["github:example/tools#second"].Enabled || !s.Selections.Connections["github:example/tools#second"].ReviewRequired {
		t.Fatal(s, err)
	}
}

func TestStoreAcceptCopiesIDs(t *testing.T) {
	p := storePaths(t)
	seedAcceptedStore(t, p)
	ids := []string{acceptedPaper}
	s, err := NewStore(p).UpdateAccepted(context.Background(), 4, ids, func(s *State) error {
		ids[0] = "local:missing"
		return acceptedExecutionChange(s)
	})
	if err != nil || s.Selections.Connections[acceptedPaper].ReviewRequired {
		t.Fatal(s, err)
	}
}

func TestStoreAcceptOAuthMetadata(t *testing.T) {
	p := storePaths(t)
	seedAcceptedStore(t, p)
	s, err := NewStore(p).UpdateAccepted(context.Background(), 4, []string{acceptedPaper}, func(s *State) error {
		s.Local.Sources[0].Commit = strings.Repeat("b", 40)
		c := s.Catalogs["github-1"].Connections["paper"]
		c.Auth = &OAuth{Type: "oauth"}
		s.Catalogs["github-1"].Connections["paper"] = c
		return nil
	})
	if err != nil || s.Selections.Connections[acceptedPaper].ReviewRequired {
		t.Fatal(s, err)
	}
	loaded, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := loaded.RuntimeConnection(acceptedPaper); !errors.Is(err, ErrRuntimeUnsupported) {
		t.Fatal(err)
	}
}

func TestStoreAcceptInvalid(t *testing.T) {
	for _, tc := range []struct {
		name string
		ids  []string
		want error
	}{
		{"duplicate", []string{acceptedPaper, acceptedPaper}, ErrConfig},
		{"alias", []string{"paper"}, ErrConfig},
		{"absent", []string{"local:missing"}, ErrNotFound},
		{"unavailable", []string{"local:gone"}, ErrNotFound},
		{"disabled", []string{acceptedPaper}, ErrDisabled},
		{"input", []string{acceptedPaper}, ErrConfigRequired},
		{"profile", []string{acceptedPaper}, ErrConfigRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := storePaths(t)
			seedAcceptedStore(t, p)
			before := stateFiles(t, p)
			_, err := NewStore(p).UpdateAccepted(context.Background(), 4, tc.ids, func(s *State) error {
				if err := acceptedExecutionChange(s); err != nil {
					return err
				}
				selection := s.Selections.Connections[acceptedPaper]
				c := s.Catalogs["github-1"].Connections["paper"]
				switch tc.name {
				case "disabled":
					selection.Enabled = false
				case "unavailable":
					s.Selections.Connections["local:gone"] = Selection{Enabled: true}
				case "input":
					c.Inputs = map[string]Input{"required": {Kind: "string", Description: "Required"}}
				case "profile":
					c.CredentialProfile = "team"
					cat := s.Catalogs["github-1"]
					cat.CredentialProfiles = map[string]ProfileRequirement{"team": {}}
					s.Catalogs["github-1"] = cat
				}
				s.Catalogs["github-1"].Connections["paper"] = c
				s.Selections.Connections[acceptedPaper] = selection
				return nil
			})
			if !errors.Is(err, tc.want) || !reflect.DeepEqual(before, stateFiles(t, p)) {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(p.DataDir, "catalogs", "github-1", strings.Repeat("b", 40)+".json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("snapshot written", err)
			}
		})
	}
}

func TestStoreOrdinaryUpdateCannotAccept(t *testing.T) {
	p := storePaths(t)
	seedAcceptedStore(t, p)
	s, err := NewStore(p).Update(context.Background(), 4, func(s *State) error {
		if err := acceptedExecutionChange(s); err != nil {
			return err
		}
		selection := s.Selections.Connections[acceptedPaper]
		selection.ReviewRequired = false
		s.Selections.Connections[acceptedPaper] = selection
		return nil
	})
	if err != nil || !s.Selections.Connections[acceptedPaper].ReviewRequired {
		t.Fatal(s, err)
	}
}

func acceptanceCAS(t *testing.T) {
	t.Helper()
	p := storePaths(t)
	seedStore(t, p, 5, false)
	initial := Catalog{SchemaVersion: 1, Connections: map[string]Connection{"paper": {
		Inputs:    map[string]Input{"arg": {Kind: "string", Description: "Argument"}},
		Transport: Transport{Stdio: &Stdio{Command: Literal("fixture"), Args: []Value{{Input: &InputRef{Input: "arg"}}}}},
	}}}
	b, err := marshalDocument(initial)
	if err != nil {
		t.Fatal(err)
	}
	testWrite(t, p.PersonalFile, b, 0o644)
	b, err = marshalDocument(Selections{SchemaVersion: 1, Revision: 5, Connections: map[string]Selection{"local:paper": {Enabled: true, ReviewRequired: true, Inputs: map[string]string{"arg": "before"}}}})
	if err != nil {
		t.Fatal(err)
	}
	testWrite(t, p.SelectionsFile, b, 0o644)
	type result struct {
		state State
		err   error
		calls int
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for i, paths := range []Paths{p, p} {
		go func() {
			<-start
			calls := 0
			mutate := func(s *State) error {
				calls++
				if i == 1 {
					selection := s.Selections.Connections["local:paper"]
					selection.Inputs["arg"] = "after"
					s.Selections.Connections["local:paper"] = selection
				}
				return nil
			}
			ids := []string(nil)
			if i == 0 {
				ids = []string{"local:paper"}
			}
			s, err := NewStore(paths).UpdateAccepted(context.Background(), 5, ids, mutate)
			results <- result{s, err, calls}
		}()
	}
	close(start)
	winner, loser := <-results, <-results
	if winner.err != nil {
		winner, loser = loser, winner
	}
	if winner.err != nil || winner.calls != 1 || winner.state.Selections.Revision != 6 || !errors.Is(loser.err, ErrConfigConflict) || loser.calls != 0 {
		t.Fatal(winner, loser)
	}
	loaded, err := NewStore(p).Read(context.Background())
	if err != nil || !reflect.DeepEqual(loaded, winner.state) {
		t.Fatal(loaded, err)
	}
	if _, err := os.Stat(filepath.Join(p.ConfigDir, ".mcparcel.lock")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{p.StateDir} {
		if _, err := os.Stat(filepath.Join(path, ".mcparcel.lock")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal(path, err)
		}
	}
}

func TestStoreAcceptanceCAS(t *testing.T) { acceptanceCAS(t) }

func TestStoreAcceptanceConfigLock(t *testing.T) {
	p := storePaths(t)
	seedStore(t, p, 5, false)
	q := p
	q.StateDir = filepath.Join(filepath.Dir(p.StateDir), "other-state")
	entered, release := make(chan struct{}), make(chan struct{})
	type result struct {
		state State
		err   error
	}
	done := make(chan result, 1)
	go func() {
		state, err := NewStore(p).UpdateAccepted(context.Background(), 5, []string{"local:paper"}, func(s *State) error {
			close(entered)
			<-release
			c := s.Personal.Connections["paper"]
			c.Label = "Winner"
			s.Personal.Connections["paper"] = c
			return nil
		})
		done <- result{state, err}
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	calls := 0
	_, blockedErr := NewStore(q).UpdateAccepted(ctx, 5, []string{"local:paper"}, func(*State) error { calls++; return nil })
	cancel()
	close(release)
	winner := <-done
	if !errors.Is(blockedErr, context.DeadlineExceeded) || calls != 0 || winner.err != nil || winner.state.Selections.Revision != 6 {
		t.Fatal(blockedErr, calls, winner)
	}
	_, err := NewStore(q).UpdateAccepted(context.Background(), 5, []string{"local:paper"}, func(*State) error { calls++; return nil })
	if !errors.Is(err, ErrConfigConflict) || calls != 0 {
		t.Fatal(err, calls)
	}
	if _, err := os.Stat(filepath.Join(p.ConfigDir, ".mcparcel.lock")); err != nil {
		t.Fatal(err)
	}
	for _, stateDir := range []string{p.StateDir, q.StateDir} {
		if _, err := os.Stat(filepath.Join(stateDir, ".mcparcel.lock")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
}

func TestCatalogSnapshotPermissionsCrashPrefixes(t *testing.T) {
	for _, stop := range []string{"snapshot", "reserve", "config", "selections"} {
		t.Run(stop, func(t *testing.T) {
			p := storePaths(t)
			seedAcceptedStore(t, p)
			target := filepath.Join(p.ConfigDir, "owned-config.json")
			if err := os.Rename(p.ConfigFile, target); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(target, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("owned-config.json", p.ConfigFile); err != nil {
				t.Fatal(err)
			}
			oldPath := filepath.Join(p.DataDir, "catalogs", "github-1", strings.Repeat("a", 40)+".json")
			oldBytes := testBytes(t, oldPath)
			oldInfo, err := os.Stat(oldPath)
			if err != nil {
				t.Fatal(err)
			}
			failure := errors.New("prefix failure")
			steps := []string{}
			_, err = NewStore(p).updateAccepted(t.Context(), 4, []string{acceptedPaper}, acceptedExecutionChange, storeHooks{AfterStep: func(step string) error {
				steps = append(steps, step)
				state, err := readStateUnlocked(p)
				if err != nil {
					t.Fatal(step, err)
				}
				effective, err := Resolve(state)
				if err != nil {
					t.Fatal(step, err)
				}
				active := filepath.Join(p.DataDir, "catalogs", "github-1", state.Local.Sources[0].Commit+".json")
				if _, err := DecodeCatalog(testBytes(t, active)); err != nil {
					t.Fatal("incomplete active snapshot", step, err)
				}
				for _, path := range []string{oldPath, filepath.Join(p.DataDir, "catalogs", "github-1", strings.Repeat("b", 40)+".json"), filepath.Join(p.ConfigDir, ".mcparcel.lock")} {
					info, err := os.Stat(path)
					if err != nil || info.Mode().Perm() != 0o600 {
						t.Fatal("new file permissions", path, info, err)
					}
				}
				for _, path := range []string{p.DataDir, filepath.Join(p.DataDir, "catalogs"), filepath.Join(p.DataDir, "catalogs", "github-1")} {
					info, err := os.Stat(path)
					if err != nil || info.Mode().Perm() != 0o700 {
						t.Fatal("directory permissions", path, info, err)
					}
				}
				info, err := os.Stat(target)
				if err != nil || info.Mode().Perm() != 0o644 {
					t.Fatal("safe config mode lost", info, err)
				}
				if link, err := os.Readlink(p.ConfigFile); err != nil || link != "owned-config.json" {
					t.Fatal("safe symlink lost", link, err)
				}
				local := effective.Connections["local:paper"]
				if !local.Available || !local.Enabled || local.ReviewRequired || len(local.Blockers) != 0 {
					t.Fatal("unrelated local blocked", step, local)
				}
				want := strings.Repeat("a", 40)
				if step == "config" || step == "selections" {
					want = strings.Repeat("b", 40)
				}
				if state.Local.Sources[0].Commit != want {
					t.Fatal("incorrect pointer prefix", step, state.Local.Sources[0])
				}
				sel := state.Selections.Connections[acceptedPaper]
				if sel.ReviewRequired != (step == "reserve" || step == "config") {
					t.Fatal("acceptance escaped conservative bridge", step, sel)
				}
				if step == stop {
					return failure
				}
				return nil
			}})
			if !errors.Is(err, failure) || len(steps) == 0 || steps[len(steps)-1] != stop {
				t.Fatal(steps, err)
			}
			loaded, err := Load(p)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := loaded.Connection("local:paper"); err != nil {
				t.Fatal("unrelated local blocked after crash", err)
			}
			after, err := os.Stat(oldPath)
			if err != nil || !os.SameFile(oldInfo, after) || !oldInfo.ModTime().Equal(after.ModTime()) || string(oldBytes) != string(testBytes(t, oldPath)) {
				t.Fatal("old immutable snapshot changed", err)
			}
			for _, path := range []string{p.StateDir, p.RuntimeDir} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("runtime or state created", path, err)
				}
			}
		})
	}
}
