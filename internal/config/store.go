package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
)

var (
	ErrConfigConflict    = errors.New("configuration conflict")
	ErrConfigWrite       = errors.New("configuration write failed")
	ErrRevisionExhausted = errors.New("configuration revision exhausted")
)

type (
	Store      struct{ paths Paths }
	storeHooks struct{ AfterStep func(string) error }
)

func NewStore(paths Paths) *Store                        { return &Store{paths: paths} }
func (s *Store) Read(ctx context.Context) (State, error) { return ReadState(ctx, s.paths) }
func (s *Store) Update(ctx context.Context, expectedRevision uint64, mutate func(*State) error) (State, error) {
	return s.update(ctx, expectedRevision, mutate, storeHooks{})
}

func (s *Store) UpdateAccepted(ctx context.Context, expectedRevision uint64, accept []string, mutate func(*State) error) (State, error) {
	return s.updateAccepted(ctx, expectedRevision, accept, mutate, storeHooks{})
}

func marshalDocument(value any) ([]byte, error) {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, ErrConfig
	}
	b = append(b, '\n')
	if len(b) > maxConfigBytes {
		return nil, fieldError("file", "configuration exceeds 2097152 bytes")
	}
	return b, nil
}

func cloneState(state State) (State, error) {
	var out State
	b, e := marshalDocument(state.Local)
	if e != nil {
		return State{}, e
	}
	out.Local, e = DecodeLocal(b)
	if e != nil {
		return State{}, e
	}
	b, e = marshalDocument(state.Personal)
	if e != nil {
		return State{}, e
	}
	out.Personal, e = DecodeCatalog(b)
	if e != nil {
		return State{}, e
	}
	b, e = marshalDocument(state.Selections)
	if e != nil {
		return State{}, e
	}
	out.Selections, e = DecodeSelections(b)
	if e != nil {
		return State{}, e
	}
	out.Catalogs = make(map[string]Catalog, len(state.Catalogs))
	for _, id := range sortedKeys(state.Catalogs) {
		b, e = marshalDocument(state.Catalogs[id])
		if e != nil {
			return State{}, e
		}
		cat, e := DecodeCatalog(b)
		if e != nil {
			return State{}, e
		}
		out.Catalogs[id] = cat
	}
	out.Legacy = state.Legacy
	return out, nil
}

func storeWriteError(err error) error {
	if err == nil {
		return nil
	}
	for _, sentinel := range []error{ErrUnsafePath, ErrConfigConflict, ErrConfig, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, sentinel) {
			return err
		}
	}
	return errors.Join(ErrConfigWrite, err)
}

func (s *Store) update(ctx context.Context, expectedRevision uint64, mutate func(*State) error, hooks storeHooks) (State, error) {
	return s.updateAccepted(ctx, expectedRevision, nil, mutate, hooks)
}

func (s *Store) updateAccepted(ctx context.Context, expectedRevision uint64, accept []string, mutate func(*State) error, hooks storeHooks) (State, error) {
	accept = slices.Clone(accept)
	lock, err := acquireConfigLock(ctx, s.paths, true, true)
	if err != nil {
		return State{}, err
	}
	defer releaseConfigLock(lock)
	current, err := readStateUnlocked(s.paths)
	if err != nil {
		return State{}, err
	}
	if current.Selections.Revision != expectedRevision {
		return State{}, ErrConfigConflict
	}
	if mutate == nil {
		return State{}, ErrConfig
	}
	draft, err := cloneState(current)
	if err != nil {
		return State{}, err
	}
	if err := mutate(&draft); err != nil {
		return State{}, err
	}
	update, err := prepareAcceptedUpdate(s.paths, current, draft, accept)
	if err != nil {
		return State{}, err
	}
	if !update.Changed {
		return cloneState(current)
	}
	if expectedRevision >= MaxRevision {
		return State{}, ErrRevisionExhausted
	}
	revision := expectedRevision + 1
	update.Bridge.Revision = revision
	update.Next.Selections.Revision = revision
	bridge, err := marshalDocument(update.Bridge)
	if err != nil {
		return State{}, err
	}
	final, err := marshalDocument(update.Next.Selections)
	if err != nil {
		return State{}, err
	}
	afterStep := func(label string) error {
		if hooks.AfterStep != nil {
			return storeWriteError(hooks.AfterStep(label))
		}
		return nil
	}
	for _, write := range update.Snapshots {
		if err := writeImmutable(ctx, write); err != nil {
			return State{}, storeWriteError(err)
		}
		if err := afterStep("snapshot"); err != nil {
			return State{}, err
		}
	}
	if _, err := atomicReplace(ctx, s.paths.SelectionsFile, bridge, atomicHooks{}); err != nil {
		return State{}, storeWriteError(err)
	}
	if err := afterStep("reserve"); err != nil {
		return State{}, err
	}
	for _, write := range update.Mutable {
		if _, err := atomicReplace(ctx, write.Path, write.Bytes, atomicHooks{}); err != nil {
			return State{}, storeWriteError(err)
		}
		label := "personal"
		if write.Path == s.paths.ConfigFile {
			label = "config"
		}
		if err := afterStep(label); err != nil {
			return State{}, err
		}
	}
	if !bytes.Equal(bridge, final) {
		if _, err := atomicReplace(ctx, s.paths.SelectionsFile, final, atomicHooks{}); err != nil {
			return State{}, storeWriteError(err)
		}
		if err := afterStep("selections"); err != nil {
			return State{}, err
		}
	}
	return cloneState(update.Next)
}
