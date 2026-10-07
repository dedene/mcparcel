package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/catalog"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

func metadataSeed(t *testing.T) (config.Paths, *config.Store, config.State) {
	t.Helper()
	p := metadataEnv(t)
	store := config.NewStore(p)
	state, err := store.Update(context.Background(), 0, func(s *config.State) error {
		s.Personal.Domains["docs"] = config.Domain{Label: "Documentation"}
		for _, id := range []string{"paper", "disabled", "missing", "review"} {
			s.Personal.Connections[id] = config.Connection{Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal("nonexistent-fixture")}}}
			s.Selections.Connections["local:"+id] = config.Selection{Enabled: id != "disabled", ReviewRequired: id == "review"}
		}
		c := s.Personal.Connections["paper"]
		c.Domains = []string{"docs"}
		s.Personal.Connections["paper"] = c
		s.Personal.CredentialProfiles["team"] = config.ProfileRequirement{}
		c = s.Personal.Connections["missing"]
		c.CredentialProfile = "team"
		c.Transport.Stdio.Env = map[string]config.Value{"TOKEN": {Secret: &config.SecretRef{Secret: "op://Fixture/item/token"}}}
		c.Inputs = map[string]config.Input{"required": {Kind: "string", Description: "Required"}}
		s.Personal.Connections["missing"] = c
		s.Personal.Connections["removed"] = config.Connection{Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal("fixture")}}}
		s.Selections.Connections["local:removed"] = config.Selection{Enabled: true}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = store.Update(context.Background(), state.Selections.Revision, func(s *config.State) error { delete(s.Personal.Connections, "removed"); return nil })
	if err != nil {
		t.Fatal(err)
	}
	return p, store, state
}

func TestMetadataNoAuth(t *testing.T) {
	p, _, _ := metadataSeed(t)
	t.Setenv("PATH", "")
	for _, argv := range [][]string{{"catalog"}, {"list"}, {"inspect", "paper"}, {"inspect", "removed"}} {
		code, out, stderr := run(t, append(argv, "--json", "--no-input")...)
		if code != 0 || stderr != "" || !strings.Contains(out, `"ok":true`) {
			t.Fatal(code, out, stderr)
		}
	}
	for _, path := range []string{p.SocketFile, p.LogFile, filepath.Join(p.StateDir, "fixture-auth-events")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal(path, err)
		}
	}
}

func TestCatalogDomain(t *testing.T) {
	metadataSeed(t)
	for _, tc := range []struct {
		domain string
		ids    []string
		code   int
	}{{"docs", []string{"local:paper"}, 0}, {"other", []string{"local:disabled", "local:missing", "local:removed", "local:review"}, 0}, {"unknown", nil, 2}} {
		code, out, _ := run(t, "catalog", "--domain", tc.domain, "--json")
		if code != tc.code {
			t.Fatal(code, out)
		}
		if code == 0 {
			var v struct{ Data output.MetadataData }
			if err := json.Unmarshal([]byte(out), &v); err != nil {
				t.Fatal(err)
			}
			ids := []string{}
			for _, r := range v.Data.Items {
				ids = append(ids, r.ID)
			}
			if !reflect.DeepEqual(ids, tc.ids) {
				t.Fatal(ids)
			}
		}
	}
	for _, argv := range [][]string{{"catalog", "--domain", ""}, {"catalog", "--domain", "docs", "--domain", "other"}, {"catalog", "--domain=--json"}} {
		code, out, _ := run(t, append(argv, "--json")...)
		if code != 2 || !strings.Contains(out, `"ok":false`) {
			t.Fatal(code, out)
		}
	}
}

func TestSnapshotAge(t *testing.T) {
	p := metadataEnv(t)
	store := config.NewStore(p)
	source := config.Source{ID: "github-42", RepositoryID: 42, Owner: "fixture-owner", Repo: "fixture.repo", Path: "mcparcel.json", Ref: "main", Commit: strings.Repeat("a", 40)}
	state, err := store.Update(context.Background(), 0, func(s *config.State) error {
		s.Local.Sources = []config.Source{source}
		s.Catalogs[source.ID] = config.Catalog{SchemaVersion: 1, Connections: map[string]config.Connection{}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(p.DataDir, "catalogs", source.ID, source.Commit+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1800000000, 0)
	for _, age := range []float64{60, 0} {
		mtime := now.Add(-60 * time.Second)
		if age == 0 {
			mtime = now.Add(time.Hour)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		rows, err := sourceMetadata(p, state, now)
		if err != nil || len(rows) != 1 || rows[0].CacheAgeSeconds != age {
			t.Fatal(rows, err)
		}
		info, _ := os.Stat(path)
		after, _ := os.ReadFile(path)
		if !info.ModTime().Equal(mtime) || !bytes.Equal(before, after) {
			t.Fatal("snapshot changed")
		}
	}
}

func TestMetadataEnvelopeAndWriter(t *testing.T) {
	metadataEnv(t)
	code, out, stderr := run(t, "catalog", "--json")
	if code != 0 || stderr != "" || strings.Count(out, "\n") != 1 || !strings.Contains(out, `"items":[]`) || !strings.Contains(out, `"sources":[]`) {
		t.Fatal(code, out, stderr)
	}
	w := &failingProductWriter{}
	var errOut bytes.Buffer
	if code := Run(context.Background(), []string{"catalog", "--json"}, strings.NewReader(""), w, &errOut); code != 1 || w.calls != 1 || errOut.Len() != 0 {
		t.Fatal(code, w.calls, errOut.String())
	}
}

type syncFetcher func(context.Context, config.Source) (catalog.Snapshot, error)

func (f syncFetcher) Fetch(ctx context.Context, s config.Source) (catalog.Snapshot, error) {
	return f(ctx, s)
}

func syncSeed(t *testing.T) (*catalog.Service, config.State) {
	t.Helper()
	store := config.NewStore(metadataEnv(t))
	state, err := store.Update(context.Background(), 0, func(s *config.State) error {
		for i := 1; i <= 3; i++ {
			source := config.Source{ID: fmt.Sprintf("github-%d", i), RepositoryID: uint64(i), Owner: "fixture", Repo: fmt.Sprintf("repo%d", i), Path: "mcparcel.json", Ref: "main", Commit: strings.Repeat("a", 40)}
			s.Local.Sources = append(s.Local.Sources, source)
			s.Catalogs[source.ID] = config.Catalog{SchemaVersion: 1, Connections: map[string]config.Connection{"paper": {Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal("fixture")}}}}}
		}
		s.Selections.Connections["github:fixture/repo1#paper"] = config.Selection{Enabled: true}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	service := &catalog.Service{Store: store}
	service.Fetcher = syncFetcher(func(_ context.Context, source config.Source) (catalog.Snapshot, error) {
		source.Commit = strings.Repeat("b", 40)
		return catalog.Snapshot{Source: source, Catalog: state.Catalogs[source.ID]}, nil
	})
	return service, state
}

func TestPartialMultiSourceSync(t *testing.T) {
	service, state := syncSeed(t)
	f := service.Fetcher
	service.Fetcher = syncFetcher(func(ctx context.Context, s config.Source) (catalog.Snapshot, error) {
		if s.ID == "github-2" {
			return catalog.Snapshot{}, catalog.ErrRepositoryMissing
		}
		return f.Fetch(ctx, s)
	})
	d, err := runSync(context.Background(), service, state, "", true, nil)
	var failure *commandFailure
	if !errors.As(err, &failure) || failure.Failure.Code != "catalog_unavailable" || len(d.Results) != 3 || !d.Results[0].Applied || d.Results[1].Applied || !d.Results[2].Applied || d.Results[2].Revision != 3 {
		t.Fatal(d, err)
	}
	latest, e := service.Store.Read(context.Background())
	if e != nil || latest.Local.Sources[1].Commit != state.Local.Sources[1].Commit || latest.Local.Sources[2].Commit != strings.Repeat("b", 40) {
		t.Fatal(latest, e)
	}
}

func TestPartialMultiSourceConflict(t *testing.T) {
	service, state := syncSeed(t)
	f := service.Fetcher
	calls := 0
	service.Fetcher = syncFetcher(func(ctx context.Context, s config.Source) (catalog.Snapshot, error) {
		calls++
		if calls == 2 {
			_, e := service.Store.Update(ctx, 2, func(s *config.State) error { s.Local.Aliases["saved"] = "github:fixture/repo1#paper"; return nil })
			if e != nil {
				t.Fatal(e)
			}
		}
		return f.Fetch(ctx, s)
	})
	d, err := runSync(context.Background(), service, state, "", true, nil)
	if safeFailure(err).Code != "config_conflict" || calls != 2 || !d.Results[0].Applied || d.Results[1].Error.Code != "config_conflict" || d.Results[2].Error.Code != "config_conflict" {
		t.Fatal(d, err, calls)
	}
	latest, e := service.Store.Read(context.Background())
	if e != nil || latest.Local.Aliases["saved"] == "" || latest.Local.Sources[1].Commit != strings.Repeat("a", 40) {
		t.Fatal(latest, e)
	}
}

func TestSyncAcceptScope(t *testing.T) {
	for _, tc := range []struct {
		repo   string
		apply  bool
		accept []string
		code   string
	}{{"", false, []string{"paper"}, "invalid_arguments"}, {"", true, []string{"*"}, "connection_unavailable"}, {"", true, []string{"unknown"}, "connection_unavailable"}, {"", true, []string{"github:fixture/repo2#paper"}, "connection_disabled"}, {"fixture/repo2", true, []string{"github:fixture/repo1#paper"}, "invalid_arguments"}, {"", true, []string{"paper"}, "ambiguous_id"}} {
		t.Run(tc.code+tc.repo, func(t *testing.T) {
			service, state := syncSeed(t)
			service.Fetcher = syncFetcher(func(context.Context, config.Source) (catalog.Snapshot, error) {
				t.Fatal("preflight fetched")
				return catalog.Snapshot{}, nil
			})
			_, err := runSync(context.Background(), service, state, tc.repo, tc.apply, tc.accept)
			if safeFailure(err).Code != tc.code {
				t.Fatal(err, safeFailure(err))
			}
		})
	}
}

func TestCatalogParserSafety(t *testing.T) {
	for _, argv := range [][]string{{"add", "fixture/repo", "--path", "--json"}, {"add", "fixture/repo", "--ref", "main", "--ref", "other", "--json"}, {"sync", "--apply", "--accept=", "--json"}, {"catalog", "--domain", "--json"}, {"add", "fixture/repo", "--path=", "--json"}} {
		metadataEnv(t)
		code, out, _ := run(t, argv...)
		if code != 2 {
			t.Fatal(code, out)
		}
		intent := scanIntent(argv)
		if intent.json && !strings.Contains(out, `"ok":false`) {
			t.Fatal(out)
		}
		if !intent.json && strings.Contains(out, `"schemaVersion"`) {
			t.Fatal("opaque flag toggled JSON", out)
		}
	}
}

func TestSyncFailureEnvelope(t *testing.T) {
	metadataEnv(t)
	service, state := syncSeed(t)
	fetcher := service.Fetcher
	service.Fetcher = syncFetcher(func(ctx context.Context, source config.Source) (catalog.Snapshot, error) {
		if source.ID == "github-2" {
			return catalog.Snapshot{}, catalog.ErrOffline
		}
		return fetcher.Fetch(ctx, source)
	})
	d, err := runSync(context.Background(), service, state, "", true, nil)
	if len(d.Results) != 3 || !d.Results[0].Applied || d.Results[1].Error.Code != "catalog_offline" || !d.Results[2].Applied {
		t.Fatal(d, err)
	}
	for _, result := range d.Results {
		if result.Plan != nil {
			result.Plan.Current = catalog.Snapshot{}
			result.Plan.Candidate = catalog.Snapshot{}
		}
	}
	var out, stderr bytes.Buffer
	exit := writeFailure(&out, &stderr, true, err)
	var envelope struct {
		Data  json.RawMessage
		Error *output.Error
	}
	if e := json.Unmarshal(out.Bytes(), &envelope); e != nil {
		t.Fatal(e)
	}
	if exit != 6 || string(envelope.Data) != "null" || strings.Count(out.String(), "\n") != 1 || stderr.Len() != 0 {
		t.Fatal(out.String(), exit)
	}
	var report output.SyncData
	if e := json.Unmarshal(envelope.Error.Details.SyncReport, &report); e != nil || !reflect.DeepEqual(d, report) {
		t.Fatal(report, e)
	}
	w := &failingProductWriter{}
	if code := writeFailure(w, &stderr, true, err); code != 1 || w.calls != 1 {
		t.Fatal(code, w.calls)
	}
}

func TestSyncCancellationStopsFetch(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			service, state := syncSeed(t)
			calls := 0
			service.Fetcher = syncFetcher(func(context.Context, config.Source) (catalog.Snapshot, error) {
				calls++
				return catalog.Snapshot{}, cause
			})
			d, err := runSync(context.Background(), service, state, "", true, nil)
			if calls != 1 || len(d.Results) != 3 || safeFailure(err).Code != safeFailure(cause).Code {
				t.Fatal(calls, d, err)
			}
			for _, row := range d.Results {
				if row.Error.Code != safeFailure(cause).Code || row.Plan != nil || row.Revision != state.Selections.Revision {
					t.Fatal(row)
				}
			}
		})
	}
}

func TestHeadlessCatalogFetchRefused(t *testing.T) {
	p, _ := headlessEnv(t)
	if deps, err := catalogDependencies(); deps != nil || !errors.Is(err, config.ErrConfigReadOnly) {
		t.Fatal(deps, err)
	}
	if err := os.Chmod(p.ConfigDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p.ConfigDir, 0o700) })
	before, err := os.ReadDir(p.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{{"add", "fixture-owner/fixture.repo"}, {"sync"}, {"sync", "--apply"}, {"sync", "fixture-owner/fixture.repo"}} {
		code, stdout, stderr := run(t, append(argv, "--json")...)
		if e := envelopeError(t, stdout); code != 2 || e.Code != "config_read_only" || stderr != "" {
			t.Fatal(argv, code, stdout, stderr)
		}
	}
	after, err := os.ReadDir(p.ConfigDir)
	if err != nil || len(after) != len(before) {
		t.Fatal(before, after, err)
	}
	if err = os.Chmod(p.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(p.ConfigFile); err != nil {
		t.Fatal(err)
	}
	if deps, err := catalogDependencies(); err != nil || deps == nil || deps.Fetcher == nil {
		t.Fatal("desktop mode lost its fetcher", deps, err)
	}
}
