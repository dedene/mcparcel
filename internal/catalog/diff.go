package catalog

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/jsonutil"
)

func sameRevision(current, candidate Snapshot) (bool, error) {
	if current.Source.Commit != candidate.Source.Commit {
		return false, nil
	}
	if !reflect.DeepEqual(current.Catalog, candidate.Catalog) {
		return false, ErrContentInvalid
	}
	return true, nil
}

func connectionChange(id string, before, after config.Connection) (ConnectionChange, error) {
	encode := func(c config.Connection) (any, error) {
		raw, err := json.Marshal(c)
		if err != nil {
			return nil, ErrContentInvalid
		}
		v, err := jsonutil.Decode(raw)
		if err != nil {
			return nil, ErrContentInvalid
		}
		return v, nil
	}
	a, err := encode(before)
	if err != nil {
		return ConnectionChange{}, err
	}
	b, err := encode(after)
	if err != nil {
		return ConnectionChange{}, err
	}
	return ConnectionChange{
		ID: id, Fields: changedPaths(a, b, ""),
		ExecutionOrAuth: config.ExecutionChanged(before, after),
	}, nil
}

func changedPaths(before, after any, prefix string) []string {
	if reflect.DeepEqual(before, after) {
		return []string{}
	}
	a, aok := before.(map[string]any)
	b, bok := after.(map[string]any)
	if !aok || !bok {
		return []string{prefix}
	}
	keys := make([]string, 0, len(a)+len(b))
	for k := range a {
		keys = append(keys, k)
	}
	for k := range b {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	keys = slices.Compact(keys)
	out := []string{}
	for _, key := range keys {
		escaped := strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
		path := prefix + "/" + escaped
		av, ae := a[key]
		bv, be := b[key]
		if !ae || !be {
			out = append(out, path)
			continue
		}
		out = append(out, changedPaths(av, bv, path)...)
	}
	slices.Sort(out)
	return out
}
