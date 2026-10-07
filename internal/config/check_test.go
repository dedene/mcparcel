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

const (
	checkPersonal   = `{"schemaVersion":1,"connections":{"paper":{"transport":{"type":"stdio","command":"paper"}}}}`
	checkSelections = `{"schemaVersion":1,"revision":3,"connections":{"local:paper":{"enabled":true}}}`
)

func fileStatus(t *testing.T, r FileReport, name string) FileStatus {
	t.Helper()
	for _, f := range r.Files {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("no %s in %+v", name, r.Files)
	return FileStatus{}
}

func TestCheckFilesIsolatesErrors(t *testing.T) {
	p := storePaths(t)
	testWrite(t, p.ConfigFile, []byte(`{"schemaVersion":1,"runtime":{"mode":"server"}}`), 0o600)
	testWrite(t, p.PersonalFile, []byte(checkPersonal), 0o600)
	testWrite(t, p.SelectionsFile, []byte(`{"schemaVersion":1,"revision":1,"connections":{},"x":1}`), 0o600)
	r, err := CheckFiles(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if f := fileStatus(t, r, "config.json"); !f.Present || FieldReason(f.Err) != "runtime.mode: must be desktop or headless" {
		t.Fatal(f, FieldReason(f.Err))
	}
	if f := fileStatus(t, r, "personal.json"); !f.Present || f.Err != nil {
		t.Fatal(f)
	}
	if f := fileStatus(t, r, "selections.json"); !errors.Is(f.Err, ErrConfig) || FieldReason(f.Err) == "" {
		t.Fatal(f)
	}
	if r.State != nil || r.StateErr != nil {
		t.Fatal("state assembled from invalid documents")
	}
	// An unsafe file is reported as such; the others still decode.
	if err := os.Chmod(p.PersonalFile, 0o666); err != nil {
		t.Fatal(err)
	}
	testWrite(t, p.ConfigFile, []byte(`{"schemaVersion":1}`), 0o600)
	r, _ = CheckFiles(context.Background(), p)
	if f := fileStatus(t, r, "personal.json"); !errors.Is(f.Err, ErrUnsafePath) {
		t.Fatal(f)
	}
	if f := fileStatus(t, r, "config.json"); f.Err != nil {
		t.Fatal(f)
	}
	// All valid: the State is the one ReadState returns.
	if err := os.Chmod(p.PersonalFile, 0o600); err != nil {
		t.Fatal(err)
	}
	testWrite(t, p.SelectionsFile, []byte(checkSelections), 0o600)
	r, err = CheckFiles(context.Background(), p)
	if err != nil || r.State == nil || r.StateErr != nil || len(r.Files) != 3 {
		t.Fatal(r, err)
	}
	read, err := ReadState(context.Background(), p)
	if err != nil || !reflect.DeepEqual(read, *r.State) {
		t.Fatal(err)
	}
}

func TestCheckFilesStateError(t *testing.T) {
	p := storePaths(t)
	testWrite(t, p.PersonalFile, []byte(checkPersonal), 0o600)
	testWrite(t, p.SelectionsFile, []byte(`{"schemaVersion":1,"revision":1,"connections":{"local:paper":{"enabled":true,"inputs":{"nope":"x"}}}}`), 0o600)
	r, err := CheckFiles(context.Background(), p)
	if err != nil || r.State != nil || FieldReason(r.StateErr) == "" {
		t.Fatal(r, err)
	}
	for _, f := range r.Files {
		if f.Err != nil {
			t.Fatal(f)
		}
	}
}

func TestCheckFilesMissingSnapshot(t *testing.T) {
	p := storePaths(t)
	commit := strings.Repeat("a", 40)
	testWrite(t, p.ConfigFile, []byte(`{"schemaVersion":1,"sources":[{"id":"github-1","repositoryId":1,"owner":"example","repo":"tools","path":"catalog.json","ref":"main","commit":"`+commit+`","pinned":false}]}`), 0o600)
	r, err := CheckFiles(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	f := fileStatus(t, r, "catalog github-1")
	if f.Present || FieldReason(f.Err) != "catalogs.github-1: active source snapshot required" || r.State != nil {
		t.Fatal(f, r)
	}
	testWrite(t, filepath.Join(p.DataDir, "catalogs", "github-1", commit+".json"), []byte(checkPersonal), 0o600)
	r, _ = CheckFiles(context.Background(), p)
	if f = fileStatus(t, r, "catalog github-1"); !f.Present || f.Err != nil || r.State == nil || len(r.State.Catalogs) != 1 {
		t.Fatal(f, r)
	}
}

func TestCheckFilesWaitsForWriterLock(t *testing.T) {
	p := storePaths(t)
	testWrite(t, p.PersonalFile, []byte(checkPersonal), 0o600)
	writer, err := acquireConfigLock(context.Background(), p, true, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err = CheckFiles(ctx, p); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	done := make(chan FileReport, 1)
	go func() {
		r, e := CheckFiles(context.Background(), p)
		if e != nil {
			t.Error(e)
		}
		done <- r
	}()
	// The writer saves selections.json while it holds the lock; doctor can
	// only read after the release, so it sees the result.
	testWrite(t, p.SelectionsFile, []byte(checkSelections), 0o600)
	if err = releaseConfigLock(writer); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.State == nil || r.State.Selections.Revision != 3 || !fileStatus(t, r, "selections.json").Present {
		t.Fatal(r)
	}
}

func TestCheckFilesCreatesNoLock(t *testing.T) {
	p := storePaths(t)
	if _, err := CheckFiles(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(p.ConfigDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created the configuration directory", err)
	}
	testWrite(t, p.PersonalFile, []byte(checkPersonal), 0o600)
	if _, err := CheckFiles(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(p.ConfigDir, ".mcparcel.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created the lock file", err)
	}
	if err := os.Chmod(p.ConfigDir, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckFiles(context.Background(), p); !errors.Is(err, ErrUnsafePath) {
		t.Fatal(err)
	}
}

func TestFieldReason(t *testing.T) {
	if got := FieldReason(fieldError("a.b", "bad thing")); got != "a.b: bad thing" {
		t.Fatal(got)
	}
	if got := FieldReason(errors.Join(errors.New("context"), fieldError("x", "y"))); got != "x: y" {
		t.Fatal(got)
	}
	for _, err := range []error{nil, ErrUnsafePath, ErrConfig, errors.New("invalid configuration: x: y")} {
		if got := FieldReason(err); got != "" {
			t.Fatal(err, got)
		}
	}
}

func TestOnePasswordRefPartsValid(t *testing.T) {
	for ref, ok := range map[string]bool{
		"op://vault/item/field":                 true,
		"op://v-1/item.name_2/section/field":    true,
		"op://abcd1234efgh5678/ijkl9012/field":  true,
		"op://Private Vault/item/field":         false,
		"op://vault/item:1/field":               false,
		"op://vault/item/fi%eld":                false,
		"op://vault//field":                     false,
		"env:NAME":                              false,
		"op://vault/item/fieldé":                false,
		"op://vault/item/field/with/extra/part": true,
	} {
		if OnePasswordRefPartsValid(ref) != ok {
			t.Fatal(ref)
		}
	}
}

func TestNewSnapshotMatchesLoad(t *testing.T) {
	p := storePaths(t)
	testWrite(t, p.PersonalFile, []byte(checkPersonal), 0o600)
	testWrite(t, p.SelectionsFile, []byte(checkSelections), 0o600)
	loaded, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	state, err := ReadState(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	built, err := NewSnapshot(state)
	if err != nil || !reflect.DeepEqual(loaded, built) {
		t.Fatal(err)
	}
	// Unlike Load, an empty configuration is a snapshot, not ErrConfigRequired.
	empty := storePaths(t)
	if _, err = Load(empty); !errors.Is(err, ErrConfigRequired) {
		t.Fatal(err)
	}
	state, _ = ReadState(context.Background(), empty)
	if s, err := NewSnapshot(state); err != nil || len(s.Effective.Connections) != 0 {
		t.Fatal(s, err)
	}
}

func TestMissingConfig(t *testing.T) {
	personal := `{"schemaVersion":1,"credentialProfiles":{"team":{}},"connections":{"x":{"credentialProfile":"team","inputs":{"b":{"kind":"string","description":"b"},"a":{"kind":"string","description":"a"},"c":{"kind":"string","description":"c","default":"z"}},"transport":{"type":"stdio","command":"x"}}}}`
	p := storePaths(t)
	testWrite(t, p.PersonalFile, []byte(personal), 0o600)
	testWrite(t, p.SelectionsFile, []byte(`{"schemaVersion":1,"revision":1,"connections":{"local:x":{"enabled":true,"inputs":{"b":"set"},"credentialProfile":"gone"}}}`), 0o600)
	state, err := ReadState(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	effective, err := Resolve(state)
	if err != nil {
		t.Fatal(err)
	}
	inputs, profile := MissingConfig(state, effective.Connections["local:x"])
	if !reflect.DeepEqual(inputs, []string{"a"}) || !profile {
		t.Fatal(inputs, profile)
	}
	if inputs, profile = MissingConfig(state, EffectiveConnection{ID: "local:gone"}); inputs != nil || profile {
		t.Fatal(inputs, profile)
	}
}
