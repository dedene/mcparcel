package cmd

import (
	"context"
	"path/filepath"
	"slices"
	"time"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

func sourceMetadata(paths config.Paths, state config.State, now time.Time) ([]output.SourceMetadata, error) {
	sources := make([]output.SourceMetadata, 0, len(state.Local.Sources))
	for _, source := range state.Local.Sources {
		file, err := config.OpenConfigFile(filepath.Join(paths.DataDir, "catalogs", source.ID, source.Commit+".json"))
		if err != nil {
			return nil, err
		}
		info, err := file.Stat()
		closeErr := file.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		age := max(0, now.Sub(info.ModTime()).Seconds())
		sources = append(sources, output.SourceMetadata{Source: source, CacheAgeSeconds: age})
	}
	return sources, nil
}

func metadataItems(effective config.EffectiveConfig, enabledOnly bool, domain string) ([]config.EffectiveConnection, error) {
	if domain != "" {
		if _, ok := effective.Domains[domain]; !ok {
			return nil, config.ErrConfig
		}
	}
	items := []config.EffectiveConnection{}
	for _, row := range effective.Connections {
		if enabledOnly && !row.Enabled {
			continue
		}
		if domain != "" {
			domains := []string{"other"}
			if row.Definition != nil {
				domains = row.Definition.Domains
			}
			if !slices.Contains(domains, domain) {
				continue
			}
		}
		items = append(items, row)
	}
	slices.SortFunc(items, func(a, b config.EffectiveConnection) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return items, nil
}

func readMetadata(ctx context.Context) (config.State, config.EffectiveConfig, []output.SourceMetadata, error) {
	paths, err := commandPaths()
	if err != nil {
		return config.State{}, config.EffectiveConfig{}, nil, err
	}
	state, err := config.NewStore(paths).Read(ctx)
	if err != nil {
		return config.State{}, config.EffectiveConfig{}, nil, err
	}
	effective, err := config.Resolve(state)
	if err != nil {
		return config.State{}, config.EffectiveConfig{}, nil, err
	}
	sources, err := sourceMetadata(paths, state, time.Now())
	return state, effective, sources, err
}

func writeMetadata(ctx context.Context, s *Streams, opts *CommandOptions, enabledOnly bool, domain string) error {
	_, effective, sources, err := readMetadata(ctx)
	if err != nil {
		return err
	}
	items, err := metadataItems(effective, enabledOnly, domain)
	if err != nil {
		return err
	}
	return writeSuccess(s, opts, output.MetadataData{Revision: effective.Revision, Items: items, Domains: effective.Domains, SourceRevisions: effective.SourceRevisions, Sources: sources})
}

func inspectMetadata(state config.State, effective config.EffectiveConfig, sources []output.SourceMetadata, id string) output.InspectData {
	data := output.InspectData{Revision: effective.Revision, Item: effective.Connections[id], Selection: state.Selections.Connections[id], SourceRevisions: effective.SourceRevisions}
	for _, source := range sources {
		if source.Source.ID == data.Item.SourceID {
			data.Source = &source
			break
		}
	}
	return data
}
