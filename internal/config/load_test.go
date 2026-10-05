package config_test

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

const (
	protectedPersonal = `{"schemaVersion":1,"credentialProfiles":{"team":{}},"connections":{"protected":{"credentialProfile":"team","transport":{"type":"stdio","command":"fixture","env":{"X":{"secret":"op://v/i/f"},"Y":{"secret":"op://v/i/f"},"Z":{"secret":"op://a/b/c"}}}},"paper":{"transport":{"type":"stdio","command":"paper"}}}}`
	localTeam         = `{"schemaVersion":1,"credentialProfiles":{"team":{"mode":"desktop-service-account","account":"Fixture","bootstrapRef":"op://v/i/token"}}}`
)

func TestProfileMapping(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	writeFile(t, p.PersonalFile, protectedPersonal, 0o644)
	writeFile(t, p.ConfigFile, localTeam, 0o644)
	s, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	canonical, c, err := s.Connection("protected")
	if err != nil || canonical != "local:protected" || c.CredentialProfile != "team" {
		t.Fatal(canonical, c, err)
	}
	refs := config.SecretRefs(c)
	if !reflect.DeepEqual(refs, []string{"op://a/b/c", "op://v/i/f"}) {
		t.Fatal(refs)
	}
	writeFile(t, p.ConfigFile, `{"schemaVersion":1}`, 0o644)
	s, err = config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Connection("protected"); !errors.Is(err, config.ErrConfigRequired) {
		t.Fatal(err)
	}
	if _, err = config.DecodePersonal([]byte(strings.Replace(protectedPersonal, `"credentialProfiles":{"team":{}},`, "", 1))); !errors.Is(err, config.ErrConfig) {
		t.Fatal(err)
	}
}

func TestUnrelatedMissingProfile(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	writeFile(t, p.PersonalFile, protectedPersonal, 0o644)
	s, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Connection("paper"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Connection("protected"); !errors.Is(err, config.ErrConfigRequired) {
		t.Fatal(err)
	}
}

func TestLoadIsolation(t *testing.T) {
	p, env := testutil.IsolatedPaths(t)
	// No process environment is read: a sentinel supplied Home cannot influence explicit paths.
	p.Home = "/unusable-fixture-home"
	writeFile(t, p.PersonalFile, `{"schemaVersion":1,"connections":{"paper":{"transport":{"type":"stdio","command":"fixture"}}}}`, 0o644)
	s, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Local.SchemaVersion != 1 || s.Local.CredentialProfiles == nil {
		t.Fatal(s.Local)
	}
	for _, path := range []string{p.DataDir, p.CacheDir, p.StateDir, p.RuntimeDir} {
		entries, err := os.ReadDir(path)
		if err != nil || len(entries) != 0 {
			t.Fatal(path, entries, err)
		}
	}
	for _, name := range []string{"HOME=", "TMPDIR=", "SHELL=/bin/sh", "XDG_CONFIG_HOME=", "XDG_DATA_HOME=", "XDG_CACHE_HOME=", "XDG_STATE_HOME=", "MCPARCEL_RUNTIME_DIR=", "PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LC_ALL=C"} {
		found := false
		for _, e := range env {
			if strings.HasPrefix(e, name) {
				found = true
			}
		}
		if !found {
			t.Fatal(name, env)
		}
	}
	if len(env) != 11 {
		t.Fatal(env)
	}
}

func TestHashAndIdentity(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	writeFile(t, p.PersonalFile, protectedPersonal, 0o644)
	writeFile(t, p.ConfigFile, localTeam, 0o644)
	first, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	own, err := first.ConnectionHash("protected")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Hash) != 64 || len(own) != 64 {
		t.Fatal(first.Hash, own)
	}
	reordered := strings.Replace(protectedPersonal, `"schemaVersion":1,"credentialProfiles":{"team":{}},`, `"credentialProfiles":{"team":{}},"schemaVersion":1,`, 1)
	writeFile(t, p.PersonalFile, reordered, 0o644)
	second, err := config.Load(p)
	if err != nil || first.Hash != second.Hash {
		t.Fatal(second, err)
	}
	writeFile(t, p.PersonalFile, strings.Replace(reordered, `"command":"paper"`, `"command":"paper-new"`, 1), 0o644)
	third, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	thirdOwn, err := third.ConnectionHash("local:protected")
	if err != nil || thirdOwn != own || third.Hash == first.Hash {
		t.Fatal(thirdOwn, err)
	}
	writeFile(t, p.PersonalFile, strings.Replace(reordered, "op://v/i/f", "op://v/i/new", 1), 0o644)
	fourth, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	fourthOwn, err := fourth.ConnectionHash("protected")
	if err != nil || fourthOwn == own {
		t.Fatal(fourthOwn, err)
	}
	for _, id := range []string{"missing", "github:owner/repo#paper"} {
		if _, _, err := fourth.Connection(id); !errors.Is(err, config.ErrNotFound) {
			t.Fatal(err)
		}
	}
	// A lookup gives its caller an independent copy, including nested maps.
	_, c, err := first.Connection("protected")
	if err != nil {
		t.Fatal(err)
	}
	delete(c.Transport.Stdio.Env, "X")
	still, err := first.ConnectionHash("protected")
	if err != nil || still != own {
		t.Fatal(still, err)
	}
}

func TestHashHeaderAndProfile(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	personal := `{"schemaVersion":1,"credentialProfiles":{"team":{}},"connections":{"fixture":{"credentialProfile":"team","transport":{"type":"http","url":"https://fixture.invalid","headers":{"Authorization":{"secret":"op://v/i/f"}}}}}}`
	writeFile(t, p.PersonalFile, personal, 0o644)
	writeFile(t, p.ConfigFile, localTeam, 0o644)
	first, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := first.ConnectionHash("fixture")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p.PersonalFile, strings.Replace(personal, "op://v/i/f", "op://v/i/new", 1), 0o644)
	second, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	next, err := second.ConnectionHash("fixture")
	if err != nil || next == hash {
		t.Fatal(next, err)
	}
	writeFile(t, p.PersonalFile, personal, 0o644)
	writeFile(t, p.ConfigFile, strings.Replace(localTeam, "Fixture", "Changed account", 1), 0o644)
	third, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	next, err = third.ConnectionHash("fixture")
	if err != nil || next == hash {
		t.Fatal(next, err)
	}
}

func TestLoadLimit(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	writeFile(t, p.PersonalFile, `{"schemaVersion":1,"connections":{},"name":"`+strings.Repeat("x", 2*1024*1024)+`"}`, 0o644)
	if _, err := config.Load(p); !errors.Is(err, config.ErrConfig) {
		t.Fatal(err)
	}
}
