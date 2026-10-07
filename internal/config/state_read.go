package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

func legacyState(personal Personal, local Local) State {
	if personal.SchemaVersion == 0 {
		personal.SchemaVersion = 1
	}
	if personal.Connections == nil {
		personal.Connections = map[string]Connection{}
	}
	if local.SchemaVersion == 0 {
		local.SchemaVersion = 1
	}
	s := State{Personal: personal, Local: local, Selections: Selections{SchemaVersion: 1, Connections: map[string]Selection{}}, Catalogs: map[string]Catalog{}, Legacy: true}
	for id, c := range personal.Connections {
		s.Selections.Connections["local:"+id] = Selection{Enabled: true, CredentialProfile: c.CredentialProfile, Inputs: map[string]string{}}
	}
	return s
}

func ReadState(ctx context.Context, paths Paths) (State, error) {
	var state State
	err := withReadLock(ctx, paths, func() error {
		var readErr error
		state, readErr = readStateUnlocked(paths)
		return readErr
	})
	if err != nil {
		return State{}, err
	}
	return state, nil
}

// withReadLock runs read under the shared configuration lock. It never
// creates the lock file: when it is absent, read runs unlocked, then once more
// under the lock if a writer created it meanwhile. It waits for a writer's
// exclusive lock until ctx ends. The error is the lock's, the context's or the
// last read's.
func withReadLock(ctx context.Context, paths Paths, read func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	lock, err := acquireConfigLock(ctx, paths, false, false)
	if err == nil {
		defer releaseConfigLock(lock)
		readErr := read()
		if canceled := ctx.Err(); canceled != nil {
			return canceled
		}
		return readErr
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	readErr := read()
	lock, err = acquireConfigLock(ctx, paths, false, false)
	if err == nil {
		defer releaseConfigLock(lock)
		readErr = read()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return readErr
}

func readStateUnlocked(paths Paths) (State, error) {
	ctx := context.Background()
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	personal := Catalog{SchemaVersion: 1, Connections: map[string]Connection{}}
	local := Local{SchemaVersion: 1, CredentialProfiles: map[string]Profile{}, Aliases: map[string]string{}}
	data, err := readConfig(paths.PersonalFile)
	if err == nil {
		personal, err = DecodeCatalog(data)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return State{}, err
	}
	data, err = readConfig(paths.ConfigFile)
	if err == nil {
		local, err = DecodeLocal(data)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return State{}, err
	}
	// Normalize the same way whether the files were present or absent.
	if personal.Domains == nil {
		personal.Domains = map[string]Domain{}
	}
	if personal.CredentialProfiles == nil {
		personal.CredentialProfiles = map[string]ProfileRequirement{}
	}
	s := legacyState(personal, local)
	selectionPath := paths.SelectionsFile
	if selectionPath == "" {
		selectionPath = filepath.Join(paths.ConfigDir, "selections.json")
	}
	data, err = readConfig(selectionPath)
	if err == nil {
		s.Selections, err = DecodeSelections(data)
		s.Legacy = false
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return State{}, err
	}
	for _, source := range local.Sources {
		if err := ctx.Err(); err != nil {
			return State{}, err
		}
		data, err = readConfig(filepath.Join(paths.DataDir, "catalogs", source.ID, source.Commit+".json"))
		if err != nil {
			if errors.Is(err, ErrUnsafePath) {
				return State{}, err
			}
			return State{}, fieldError("catalogs."+source.ID, "active source snapshot required")
		}
		catalog, e := DecodeCatalog(data)
		if e != nil {
			return State{}, e
		}
		s.Catalogs[source.ID] = catalog
	}
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	if err := ValidateState(s); err != nil {
		return State{}, err
	}
	return s, nil
}
