package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/dedene/mcparcel/internal/args"
	"github.com/dedene/mcparcel/internal/catalog"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

func runSync(ctx context.Context, service *catalog.Service, state config.State, repository string, apply bool, accept []string) (output.SyncData, error) {
	data := output.SyncData{Apply: apply, Results: []output.SourceSyncResult{}}
	if !apply && len(accept) > 0 {
		return data, args.ErrInvalidArgs
	}
	sources := slices.Clone(state.Local.Sources)
	if repository != "" {
		owner, repo, err := catalog.ValidateRepositoryName(repository)
		if err != nil {
			return data, output.NewError("invalid_repository", nil)
		}
		sources = slices.DeleteFunc(sources, func(s config.Source) bool { return s.Owner != owner || s.Repo != repo })
		if len(sources) == 0 {
			return data, output.SourceNotRegisteredError()
		}
	}
	accepted := map[string][]string{}
	if len(accept) > 0 {
		ids, err := resolveSelectionIDs(state, accept)
		if err != nil {
			return data, err
		}
		effective, err := config.Resolve(state)
		if err != nil {
			return data, err
		}
		for _, id := range ids {
			row := effective.Connections[id]
			if !row.Available {
				return data, config.ErrNotFound
			}
			if !row.Enabled {
				return data, config.ErrDisabled
			}
			if !slices.ContainsFunc(sources, func(s config.Source) bool { return s.ID == row.SourceID }) {
				return data, args.ErrInvalidArgs
			}
			accepted[row.SourceID] = append(accepted[row.SourceID], id)
		}
	}
	var first *output.Error
	var stop error
	for _, source := range sources {
		result := output.SourceSyncResult{Repository: source.Owner + "/" + source.Repo, SourceID: source.ID, Accepted: []string{}, Revision: state.Selections.Revision}
		attempt := func() error {
			if stop != nil {
				return stop
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			current, err := catalog.SnapshotFromState(state, source.ID)
			if err != nil {
				return err
			}
			fetchCtx, cancel := context.WithTimeout(ctx, catalog.FetchTimeout)
			candidate, err := service.Fetcher.Fetch(fetchCtx, current.Source)
			cancel()
			if err != nil {
				return err
			}
			if err = ctx.Err(); err != nil {
				return err
			}
			plan, err := catalog.PlanUpdate(current, candidate)
			if err != nil {
				return err
			}
			result.Plan = &plan
			if !apply {
				return nil
			}
			next, err := service.ApplyUpdateState(ctx, plan, accepted[source.ID], state.Selections.Revision)
			if err != nil {
				return err
			}
			state = next
			result.Revision = next.Selections.Revision
			result.Applied = true
			if len(accepted[source.ID]) > 0 {
				result.Accepted = slices.Clone(accepted[source.ID])
			}
			return nil
		}
		if err := attempt(); err != nil {
			result.Error = safeFailure(err)
			if first == nil {
				first = result.Error
			}
			switch {
			case errors.Is(err, config.ErrConfigConflict), errors.Is(err, config.ErrConfigWrite), errors.Is(err, config.ErrDurability):
				stop = config.ErrConfigConflict
			case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
				stop = err
			}
		}
		data.Results = append(data.Results, result)
	}
	if first != nil {
		raw, err := json.Marshal(data)
		if err != nil {
			return data, err
		}
		details := output.Details{}
		if first.Details != nil {
			details = *first.Details
		}
		details.SyncReport = raw
		return data, &commandFailure{Data: data, Failure: output.NewError(first.Code, &details)}
	}
	return data, nil
}
