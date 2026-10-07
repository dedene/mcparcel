package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/catalog"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// withVersion sets the build version for one test.
func withVersion(t *testing.T, v string) {
	t.Helper()
	old := version
	version = v
	t.Cleanup(func() { version = old })
}

// newerFetcher returns state's catalog for source with minVersion 9.0.0 and
// a new commit, behind the minVersion check add and sync use.
func newerFetcher(state config.State) catalog.Fetcher {
	return minVersionFetcher{syncFetcher(func(_ context.Context, source config.Source) (catalog.Snapshot, error) {
		c := state.Catalogs[source.ID]
		c.MinVersion = "9.0.0"
		source.Commit = strings.Repeat("b", 40)
		return catalog.Snapshot{Source: source, Catalog: c}, nil
	})}
}

func TestAddRefusesNewerMinVersion(t *testing.T) {
	withVersion(t, "0.1.0-rc.1+3f2a9c1d0e4b")
	metadataEnv(t)
	deps, err := catalogDependencies()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := deps.Fetcher.(minVersionFetcher); !ok {
		t.Fatalf("add and sync fetch without the minVersion check: %T", deps.Fetcher)
	}
	source := config.Source{ID: "github-1", Owner: "fixture", Repo: "repo"}
	_, err = newerFetcher(config.State{Catalogs: map[string]config.Catalog{"github-1": {SchemaVersion: 1}}}).Fetch(context.Background(), source)
	if !errors.Is(err, config.ErrCatalogNewer) {
		t.Fatal(err)
	}
	e := safeFailure(err)
	if e.Code != "catalog_requires_upgrade" || output.ExitCode(e) != 4 || e.Message != "This catalog needs MCParcel 9.0.0 or newer; this is 0.1.0-rc.1+3f2a9c1d0e4b." || !strings.Contains(e.NextAction, "Upgrade MCParcel") {
		t.Fatalf("%+v", e)
	}
}

func TestSyncRefusesNewerMinVersionKeepsSnapshot(t *testing.T) {
	withVersion(t, "0.1.0")
	service, state := syncSeed(t)
	service.Fetcher = newerFetcher(state)
	for _, apply := range []bool{false, true} {
		d, err := runSync(context.Background(), service, state, "fixture/repo1", apply, nil)
		var failure *commandFailure
		if !errors.As(err, &failure) || failure.Failure.Code != "catalog_requires_upgrade" || len(d.Results) != 1 || d.Results[0].Applied || d.Results[0].Plan != nil {
			t.Fatal(apply, d, err)
		}
		if !strings.Contains(d.Results[0].Error.Message, "9.0.0") {
			t.Fatal(d.Results[0].Error)
		}
	}
	latest, err := service.Store.Read(context.Background())
	if err != nil || latest.Local.Sources[0].Commit != strings.Repeat("a", 40) || latest.Catalogs["github-1"].MinVersion != "" {
		t.Fatal("the saved snapshot changed", err)
	}
}

func TestDevBuildIgnoresMinVersion(t *testing.T) {
	withVersion(t, "dev")
	service, state := syncSeed(t)
	service.Fetcher = newerFetcher(state)
	d, err := runSync(context.Background(), service, state, "fixture/repo1", true, nil)
	if err != nil || !d.Results[0].Applied {
		t.Fatal(d, err)
	}
}
