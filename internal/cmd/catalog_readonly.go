package cmd

import "github.com/dedene/mcparcel/internal/config"

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
	return &CatalogDependencies{Fetcher: newCatalogFetcher()}, nil
}
