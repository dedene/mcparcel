package cmd

import (
	"context"

	"github.com/dedene/mcparcel/internal/catalog"
	"github.com/dedene/mcparcel/internal/config"
)

// catalogDependencies provides add's and sync's fetcher. In headless mode it
// refuses with config_read_only before the command reads state or sends any
// request: neither command could save what it fetched, and a sync preview
// would describe a change nobody can apply.
func catalogDependencies() (*CatalogDependencies, error) {
	_, rt, err := resolveCommandPaths()
	if err != nil {
		return nil, err
	}
	if rt.Mode == config.ModeHeadless {
		return nil, config.ErrConfigReadOnly
	}
	return &CatalogDependencies{Fetcher: minVersionFetcher{newCatalogFetcher()}}, nil
}

// minVersionFetcher refuses a fetched catalog whose minVersion is above this
// build (catalog_requires_upgrade), for add and for sync preview and apply;
// a sync source that fails keeps its saved snapshot. A development build,
// whose version is not semver, never refuses.
type minVersionFetcher struct{ catalog.Fetcher }

func (f minVersionFetcher) Fetch(ctx context.Context, source config.Source) (catalog.Snapshot, error) {
	snapshot, err := f.Fetcher.Fetch(ctx, source)
	if err != nil {
		return snapshot, err
	}
	if err = config.CheckMinVersion(snapshot.Catalog, version); err != nil {
		return catalog.Snapshot{}, err
	}
	return snapshot, nil
}
