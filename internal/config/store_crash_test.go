package config

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func addStoreSource(s *State) {
	s.Local.Sources = []Source{{ID: "github-1", RepositoryID: 1, Owner: "example", Repo: "tools", Path: "catalog.json", Ref: "main", Commit: strings.Repeat("a", 40)}}
	s.Catalogs["github-1"] = Catalog{SchemaVersion: 1, Connections: map[string]Connection{"remote": storeConnection()}}
}

func TestSyncAtomicApply(t *testing.T) {
	for _, step := range []string{"snapshot", "reserve", "config", "selections"} {
		t.Run(step, func(t *testing.T) {
			p := storePaths(t)
			seedAcceptedStore(t, p)
			failure := errors.New("injected acceptance failure")
			_, err := NewStore(p).updateAccepted(context.Background(), 4, []string{acceptedPaper}, acceptedExecutionChange, storeHooks{AfterStep: func(label string) error {
				prefix, err := readStateUnlocked(p)
				if err != nil {
					t.Fatal(label, err)
				}
				if _, err := Resolve(prefix); err != nil {
					t.Fatal(label, err)
				}
				if label == step {
					return failure
				}
				return nil
			}})
			if !errors.Is(err, failure) || !errors.Is(err, ErrConfigWrite) {
				t.Fatal(err)
			}
			state, err := NewStore(p).Read(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Resolve(state); err != nil {
				t.Fatal(err)
			}
			wantCommit, wantRevision, wantReview := strings.Repeat("a", 40), uint64(5), true
			switch step {
			case "snapshot":
				wantRevision, wantReview = 4, false
			case "config":
				wantCommit = strings.Repeat("b", 40)
			case "selections":
				wantCommit, wantReview = strings.Repeat("b", 40), false
			}
			selection := state.Selections.Connections[acceptedPaper]
			if state.Local.Sources[0].Commit != wantCommit || state.Selections.Revision != wantRevision || !selection.Enabled || selection.ReviewRequired != wantReview {
				t.Fatal(step, state)
			}
			_, err = DecodeCatalog(testBytes(t, filepath.Join(p.DataDir, "catalogs", "github-1", strings.Repeat("b", 40)+".json")))
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func crashMutation(s *State) error {
	addStoreSource(s)
	s.Local.Aliases["paper"] = "local:paper"
	s.Personal.Connections["other"] = storeConnection()
	s.Selections.Connections["local:other"] = Selection{Enabled: true}
	return nil
}

func storeChildEnv(p Paths) []string {
	return []string{"HOME=" + p.Home, "TMPDIR=" + filepath.Dir(filepath.Dir(p.ConfigDir)), "SHELL=/bin/sh", "XDG_CONFIG_HOME=" + filepath.Dir(p.ConfigDir), "XDG_DATA_HOME=" + filepath.Dir(p.DataDir), "XDG_STATE_HOME=" + filepath.Dir(p.StateDir), "XDG_CACHE_HOME=" + filepath.Dir(p.CacheDir), "MCPARCEL_RUNTIME_DIR=" + p.RuntimeDir}
}

func childPaths(t *testing.T) Paths {
	t.Helper()
	p, e := ResolvePaths(os.Getenv, os.Getenv("HOME"), os.Getenv("TMPDIR"), os.Getuid())
	if e != nil {
		t.Fatal(e)
	}
	return p
}

func TestStoreConcurrentSaveProcesses(t *testing.T) {
	if label := os.Getenv("MCP_STORE_PROCESS"); label != "" {
		p := childPaths(t)
		fmt.Println("ready")
		var token [1]byte
		if _, e := io.ReadFull(os.Stdin, token[:]); e != nil {
			t.Fatal(e)
		}
		calls := 0
		s, e := NewStore(p).Update(context.Background(), 0, func(s *State) error {
			calls++
			c := s.Personal.Connections["paper"]
			c.Label = label
			s.Personal.Connections["paper"] = c
			return nil
		})
		if errors.Is(e, ErrConfigConflict) && calls == 0 {
			fmt.Println("conflict")
			return
		}
		if e != nil || calls != 1 || s.Selections.Revision != 1 {
			t.Fatal(s, e, calls)
		}
		fmt.Println("winner " + label)
		return
	}
	p := storePaths(t)
	seedStore(t, p, 0, false)
	type child struct {
		cmd    *exec.Cmd
		in     io.WriteCloser
		out    *bufio.Reader
		stderr bytes.Buffer
	}
	children := make([]*child, 0, 2)
	for _, label := range []string{"first", "second"} {
		c := &child{cmd: exec.Command(os.Args[0], "-test.run=^TestStoreConcurrentSaveProcesses$")}
		c.cmd.Env = append(storeChildEnv(p), "MCP_STORE_PROCESS="+label)
		c.cmd.Stderr = &c.stderr
		in, e := c.cmd.StdinPipe()
		if e != nil {
			t.Fatal(e)
		}
		c.in = in
		out, e := c.cmd.StdoutPipe()
		if e != nil {
			t.Fatal(e)
		}
		c.out = bufio.NewReader(out)
		if e = c.cmd.Start(); e != nil {
			t.Fatal(e)
		}
		children = append(children, c)
	}
	for _, c := range children {
		line, e := c.out.ReadString('\n')
		if e != nil || line != "ready\n" {
			t.Fatal(line, e)
		}
	}
	for _, c := range children {
		if _, e := c.in.Write([]byte("x")); e != nil {
			t.Fatal(e)
		}
		_ = c.in.Close()
	}
	winners, conflicts := 0, 0
	winner := ""
	for _, c := range children {
		b, e := io.ReadAll(c.out)
		if e != nil {
			t.Fatal(e)
		}
		if e := c.cmd.Wait(); e != nil {
			t.Fatalf("%v\n%s\n%s", e, b, c.stderr.String())
		}
		if strings.Contains(string(b), "winner ") {
			winners++
			winner = strings.TrimPrefix(strings.Split(string(b), "\n")[0], "winner ")
		}
		if strings.Contains(string(b), "conflict") {
			conflicts++
		}
	}
	s, e := ReadState(context.Background(), p)
	if winners != 1 || conflicts != 1 || e != nil || s.Selections.Revision != 1 || s.Personal.Connections["paper"].Label != winner {
		t.Fatal(winners, conflicts, s, e)
	}
}

func TestStoreCrashPrefixes(t *testing.T) {
	if step := os.Getenv("MCP_STORE_CRASH"); step != "" {
		p := childPaths(t)
		_, e := NewStore(p).update(context.Background(), 0, crashMutation, storeHooks{AfterStep: func(label string) error {
			if label == step {
				os.Exit(23)
			}
			return nil
		}})
		t.Fatal("missed crash", step, e)
		return
	}
	for _, step := range []string{"snapshot", "reserve", "config", "personal", "selections"} {
		t.Run(step, func(t *testing.T) {
			p := storePaths(t)
			seedStore(t, p, 0, true)
			cmd := exec.Command(os.Args[0], "-test.run=^TestStoreCrashPrefixes$")
			cmd.Env = append(storeChildEnv(p), "MCP_STORE_CRASH="+step)
			out, e := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(e, &exit) || exit.ExitCode() != 23 {
				t.Fatalf("%v\n%s", e, out)
			}
			s, e := ReadState(context.Background(), p)
			if e != nil {
				t.Fatal(e)
			}
			want := uint64(1)
			if step == "snapshot" {
				want = 0
			}
			if s.Selections.Revision != want {
				t.Fatal(s)
			}
			if !s.Selections.Connections["local:paper"].Enabled {
				t.Fatal("legacy paper lost")
			}
			_, exists := s.Personal.Connections["other"]
			enabled := s.Selections.Connections["local:other"].Enabled
			if enabled && !exists || enabled != (step == "selections") {
				t.Fatal(step, s)
			}
			if step != "snapshot" {
				calls := 0
				_, e := NewStore(p).Update(context.Background(), 0, func(*State) error { calls++; return nil })
				if !errors.Is(e, ErrConfigConflict) || calls != 0 {
					t.Fatal(e, calls)
				}
			}
		})
	}
}

func TestSnapshotBeforePointer(t *testing.T) {
	p := storePaths(t)
	seedStore(t, p, 0, false)
	s, e := NewStore(p).Update(context.Background(), 0, func(s *State) error { addStoreSource(s); return nil })
	if e != nil {
		t.Fatal(e)
	}
	old := s.Local.Sources[0].Commit
	sentinel := errors.New("after snapshot")
	_, e = NewStore(p).update(context.Background(), 1, func(s *State) error {
		s.Local.Sources[0].Commit = strings.Repeat("b", 40)
		cat := s.Catalogs["github-1"]
		cat.Connections["other"] = storeConnection()
		s.Catalogs["github-1"] = cat
		return nil
	}, storeHooks{AfterStep: func(step string) error {
		if step == "snapshot" {
			return sentinel
		}
		return nil
	}})
	if !errors.Is(e, sentinel) {
		t.Fatal(e)
	}
	s, e = ReadState(context.Background(), p)
	if e != nil || s.Local.Sources[0].Commit != old || s.Selections.Revision != 1 || len(s.Catalogs["github-1"].Connections) != 1 {
		t.Fatal(s, e)
	}
	path := filepath.Join(p.DataDir, "catalogs", "github-1", strings.Repeat("b", 40)+".json")
	if _, e := DecodeCatalog(testBytes(t, path)); e != nil {
		t.Fatal(e)
	}
}

func TestImmutableSnapshotCollision(t *testing.T) {
	p := storePaths(t)
	seedStore(t, p, 0, false)
	s, e := NewStore(p).Update(context.Background(), 0, func(s *State) error { addStoreSource(s); return nil })
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(p.DataDir, "catalogs", "github-1", s.Local.Sources[0].Commit+".json")
	before := testBytes(t, path)
	files := stateFiles(t, p)
	_, e = NewStore(p).Update(context.Background(), 1, func(s *State) error {
		cat := s.Catalogs["github-1"]
		cat.Connections["other"] = storeConnection()
		s.Catalogs["github-1"] = cat
		return nil
	})
	if !errors.Is(e, ErrConfig) || !bytes.Equal(before, testBytes(t, path)) || !reflect.DeepEqual(files, stateFiles(t, p)) {
		t.Fatal(e)
	}
	b, e := marshalDocument(s.Catalogs["github-1"])
	if e != nil {
		t.Fatal(e)
	}
	st, _ := os.Stat(path)
	if e := writeImmutable(context.Background(), pendingWrite{Path: path, Bytes: b}); e != nil {
		t.Fatal(e)
	}
	after, _ := os.Stat(path)
	if !os.SameFile(st, after) {
		t.Fatal("replaced immutable snapshot")
	}
}

func TestAllCrashPrefixesValidate(t *testing.T) {
	for _, step := range []string{"snapshot", "reserve", "config", "personal", "selections"} {
		t.Run(step, func(t *testing.T) {
			p := storePaths(t)
			seedStore(t, p, 0, true)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStoreCrashPrefixes$")
			cmd.Env = append(storeChildEnv(p), "MCP_STORE_CRASH="+step)
			out, e := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(e, &exit) || exit.ExitCode() != 23 {
				t.Fatalf("%v %s", e, out)
			}
			for path, document := range map[string]string{p.ConfigFile: "config", p.PersonalFile: "catalog", p.SelectionsFile: "selections", filepath.Join(p.DataDir, "catalogs", "github-1", strings.Repeat("a", 40)+".json"): "catalog"} {
				raw, e := os.ReadFile(path)
				if errors.Is(e, os.ErrNotExist) {
					continue
				}
				if e != nil {
					t.Fatal(e)
				}
				if !json.Valid(raw) {
					t.Fatal("incomplete JSON", path)
				}
				if e = validateSchema(t, document, raw); e != nil {
					t.Fatal(path, e)
				}
				if e = decodeDocument(document, raw); e != nil {
					t.Fatal(path, e)
				}
			}
			state, e := ReadState(ctx, p)
			if e != nil {
				t.Fatal(e)
			}
			if _, err := Load(p); err != nil {
				t.Fatal("crash prefix Load", err)
			}
			effective, e := Resolve(state)
			if e != nil {
				t.Fatal(e)
			}
			for id, row := range effective.Connections {
				if row.Enabled && !row.Available {
					t.Fatal("enabled missing definition", id)
				}
			}
			if step != "snapshot" {
				_, e = NewStore(p).Update(ctx, 0, func(*State) error { t.Fatal("stale callback ran"); return nil })
				if !errors.Is(e, ErrConfigConflict) {
					t.Fatal(e)
				}
			}
		})
	}
}

func TestStoreRepeatedReadsDuringUpdates(t *testing.T) {
	p := storePaths(t)
	seedStore(t, p, 0, false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := make(chan struct{})
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(ready)
		<-start
		reader := NewStore(p)
		for range 64 {
			state, e := reader.Read(ctx)
			if e != nil {
				done <- e
				return
			}
			if e = ValidateState(state); e != nil {
				done <- e
				return
			}
			for _, path := range []string{p.PersonalFile, p.SelectionsFile} {
				raw, e := readConfig(path)
				if e != nil {
					done <- e
					return
				}
				if !json.Valid(raw) {
					done <- errors.New("incomplete JSON")
					return
				}
			}
		}
		done <- nil
	}()
	<-ready
	close(start)
	writer := NewStore(p)
	for revision := range uint64(32) {
		_, e := writer.Update(ctx, revision, func(s *State) error {
			c := s.Personal.Connections["paper"]
			c.Label = fmt.Sprintf("revision-%d", revision)
			s.Personal.Connections["paper"] = c
			return nil
		})
		if e != nil {
			cancel()
			<-done
			t.Fatal(e)
		}
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	state, e := NewStore(p).Read(ctx)
	if e != nil || state.Selections.Revision != 32 {
		t.Fatal(state, e)
	}
}

func TestStoreCrashPrefixesInputKindChange(t *testing.T) {
	for _, step := range []string{"snapshot", "reserve", "config", "personal", "selections"} {
		t.Run(step, func(t *testing.T) {
			p := storePaths(t)
			seedStore(t, p, 0, false)
			initial := Catalog{SchemaVersion: 1, Connections: map[string]Connection{
				"paper":     {Inputs: map[string]Input{"arg": {Kind: "string", Description: "Argument"}}, Transport: Transport{Stdio: &Stdio{Command: Literal("fixture"), Args: []Value{{Input: &InputRef{Input: "arg"}}}}}},
				"unrelated": storeConnection(),
			}}
			b, err := json.Marshal(initial)
			if err != nil {
				t.Fatal(err)
			}
			testWrite(t, p.PersonalFile, b, 0o644)
			b, err = json.Marshal(Selections{SchemaVersion: 1, Connections: map[string]Selection{"local:paper": {Enabled: true, Inputs: map[string]string{"arg": "relative"}}, "local:unrelated": {Enabled: true}}})
			if err != nil {
				t.Fatal(err)
			}
			testWrite(t, p.SelectionsFile, b, 0o644)
			crash := errors.New("simulated crash")
			_, err = NewStore(p).update(context.Background(), 0, func(s *State) error {
				addStoreSource(s)
				s.Local.Aliases["paper"] = "local:paper"
				c := s.Personal.Connections["paper"]
				c.Inputs["arg"] = Input{Kind: "path", Description: "Argument"}
				s.Personal.Connections["paper"] = c
				sel := s.Selections.Connections["local:paper"]
				sel.Inputs["arg"] = "/absolute"
				s.Selections.Connections["local:paper"] = sel
				return nil
			}, storeHooks{AfterStep: func(label string) error {
				if label == step {
					return crash
				}
				return nil
			}})
			if !errors.Is(err, crash) {
				t.Fatalf("missed failure hook: %v", err)
			}
			snap, err := Load(p)
			if err != nil {
				t.Fatalf("Load after %s: %v", step, err)
			}
			if _, _, err := snap.Connection("unrelated"); err != nil {
				t.Fatalf("unrelated connection blocked: %v", err)
			}
			if step == "reserve" || step == "config" || step == "personal" {
				state, err := ReadState(context.Background(), p)
				if err != nil {
					t.Fatal(err)
				}
				sel := state.Selections.Connections["local:paper"]
				if sel.Enabled || !sel.ReviewRequired {
					t.Fatal("unsafe intermediate selection", sel)
				}
			}
			if step == "selections" {
				if _, _, err := snap.Connection("paper"); !errors.Is(err, ErrReviewRequired) {
					t.Fatal("final review lost", err)
				}
			}
		})
	}
}
