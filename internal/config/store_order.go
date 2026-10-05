package config

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"golang.org/x/sys/unix"
)

type pendingWrite struct {
	Path  string
	Bytes []byte
}
type preparedUpdate struct {
	Next      State
	Bridge    Selections
	Snapshots []pendingWrite
	Mutable   []pendingWrite
	Changed   bool
}

func prepareAcceptedUpdate(paths Paths, current, draft State, accept []string) (preparedUpdate, error) {
	if len(accept) == 0 {
		return prepareUpdate(paths, current, draft)
	}
	reviewed, err := cloneState(draft)
	if err != nil {
		return preparedUpdate{}, err
	}
	effective, err := Resolve(reviewed)
	if err != nil {
		return preparedUpdate{}, err
	}
	seen := map[string]bool{}
	for _, id := range accept {
		if ValidateCanonicalID(id) != nil || seen[id] {
			return preparedUpdate{}, ErrConfig
		}
		seen[id] = true
		row, exists := effective.Connections[id]
		if !exists || !row.Available {
			return preparedUpdate{}, ErrNotFound
		}
		if !row.Enabled {
			return preparedUpdate{}, ErrDisabled
		}
		if slices.Contains(row.Blockers, "config_required") {
			return preparedUpdate{}, ErrConfigRequired
		}
		selection := reviewed.Selections.Connections[id]
		selection.ReviewRequired = false
		reviewed.Selections.Connections[id] = selection
	}
	update, err := prepareUpdate(paths, current, reviewed)
	if err != nil {
		return preparedUpdate{}, err
	}
	for _, id := range accept {
		selection := update.Next.Selections.Connections[id]
		selection.ReviewRequired = false
		update.Next.Selections.Connections[id] = selection
	}
	if err := ValidateState(update.Next); err != nil {
		return preparedUpdate{}, err
	}
	if _, err := Resolve(update.Next); err != nil {
		return preparedUpdate{}, err
	}
	if _, err := marshalDocument(update.Next.Selections); err != nil {
		return preparedUpdate{}, err
	}
	return update, nil
}

func prepareUpdate(paths Paths, current, draft State) (preparedUpdate, error) {
	if draft.Selections.Revision != current.Selections.Revision || draft.Legacy != current.Legacy {
		return preparedUpdate{}, ErrConfig
	}
	if err := ValidateState(draft); err != nil {
		return preparedUpdate{}, err
	}
	next, err := cloneState(draft)
	if err != nil {
		return preparedUpdate{}, err
	}
	old, err := cloneState(current)
	if err != nil {
		return preparedUpdate{}, err
	}
	old.Legacy = false
	next.Legacy = false
	if reflect.DeepEqual(old, next) {
		return preparedUpdate{Next: next}, nil
	}
	oldEffective, err := Resolve(old)
	if err != nil {
		return preparedUpdate{}, err
	}
	newEffective, err := Resolve(next)
	if err != nil {
		return preparedUpdate{}, err
	}
	for id, selection := range next.Selections.Connections {
		row := newEffective.Connections[id]
		previous, exists := old.Selections.Connections[id]
		if selection.Enabled && !row.Available && (!exists || !previous.Enabled) {
			return preparedUpdate{}, fieldError("selections.connections."+id, "enabled connection must exist")
		}
	}
	bridge := old.Selections
	for id, selection := range bridge.Connections {
		final := next.Selections.Connections[id]
		if !selection.Enabled {
			continue
		}
		if !final.Enabled {
			selection.Enabled = false
			bridge.Connections[id] = selection
			continue
		}
		before, after := oldEffective.Connections[id], newEffective.Connections[id]
		rawChanged := before.Definition != nil && after.Definition != nil &&
			ExecutionChanged(*before.Definition, *after.Definition)
		if after.Available && (!before.Available || rawChanged || effectiveExecutionChanged(old, next, before, after)) {
			selection.ReviewRequired = true
			bridge.Connections[id] = selection
			final.ReviewRequired = true
			next.Selections.Connections[id] = final
		}
	}
	update := preparedUpdate{Next: next, Bridge: bridge, Changed: true}
	for _, source := range next.Local.Sources {
		catalog := next.Catalogs[source.ID]
		data, err := marshalDocument(catalog)
		if err != nil {
			return preparedUpdate{}, err
		}
		oldCommit := ""
		for _, previous := range old.Local.Sources {
			if previous.ID == source.ID {
				oldCommit = previous.Commit
				break
			}
		}
		if oldCommit != source.Commit || !reflect.DeepEqual(old.Catalogs[source.ID], catalog) {
			update.Snapshots = append(update.Snapshots, pendingWrite{Path: filepath.Join(paths.DataDir, "catalogs", source.ID, source.Commit+".json"), Bytes: data})
		}
	}
	for _, document := range []struct {
		path          string
		before, after any
	}{{paths.ConfigFile, old.Local, next.Local}, {paths.PersonalFile, old.Personal, next.Personal}} {
		data, err := marshalDocument(document.after)
		if err != nil {
			return preparedUpdate{}, err
		}
		if !reflect.DeepEqual(document.before, document.after) {
			update.Mutable = append(update.Mutable, pendingWrite{Path: document.path, Bytes: data})
		}
	}
	prefix := old
	prefix.Selections = bridge
	prefixes := []State{prefix}
	prefix.Local = next.Local
	prefix.Catalogs = next.Catalogs
	prefixes = append(prefixes, prefix)
	prefix.Personal = next.Personal
	prefixes = append(prefixes, prefix)
	for _, prefix := range prefixes {
		if err := ValidateState(prefix); err != nil {
			return preparedUpdate{}, err
		}
		if _, err := Resolve(prefix); err == nil {
			continue
		}
		for _, id := range sortedKeys(bridge.Connections) {
			if _, err := Resolve(connectionOnly(prefix, id)); err == nil {
				continue
			}
			selection := bridge.Connections[id]
			selection.Enabled = false
			selection.ReviewRequired = true
			selection.Inputs = nil
			bridge.Connections[id] = selection
		}
	}
	// A repaired bridge must resolve against every definition prefix, including
	// those checked before the repair. Final selections keep the reviewed bindings.
	for _, prefix := range append(prefixes, next) {
		if _, err := Resolve(prefix); err != nil {
			return preparedUpdate{}, err
		}
	}
	update.Bridge = bridge
	if _, err := marshalDocument(bridge); err != nil {
		return preparedUpdate{}, err
	}
	if _, err := marshalDocument(next.Selections); err != nil {
		return preparedUpdate{}, err
	}
	return update, nil
}

// Isolate resolution failures without parsing diagnostic strings or changing
// the source definitions. Other selections become unavailable in this probe.
func connectionOnly(state State, canonical string) State {
	filter := func(catalog Catalog, prefix string) Catalog {
		connections := map[string]Connection{}
		for id, connection := range catalog.Connections {
			if prefix+id == canonical {
				connections[id] = connection
			}
		}
		catalog.Connections = connections
		return catalog
	}
	state.Personal = filter(state.Personal, "local:")
	catalogs := map[string]Catalog{}
	for _, source := range state.Local.Sources {
		catalogs[source.ID] = filter(state.Catalogs[source.ID], "github:"+source.Owner+"/"+source.Repo+"#")
	}
	state.Catalogs = catalogs
	return state
}

func effectiveExecutionChanged(old, next State, before, after EffectiveConnection) bool {
	a, b := cloneConnection(*before.Connection), cloneConnection(*after.Connection)
	if ExecutionChanged(a, b) {
		return true
	}
	if a.CredentialProfile != "" || b.CredentialProfile != "" {
		return !reflect.DeepEqual(old.Local.CredentialProfiles[a.CredentialProfile], next.Local.CredentialProfiles[b.CredentialProfile])
	}
	return false
}

func writeImmutable(ctx context.Context, write pendingWrite) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	wanted, err := DecodeCatalog(write.Bytes)
	if err != nil {
		return err
	}
	reuse := func() error {
		data, err := readConfig(write.Path)
		if err != nil {
			return err
		}
		existing, err := DecodeCatalog(data)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(existing, wanted) {
			return ErrConfig
		}
		return nil
	}
	if err := reuse(); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	target, err := openWriteTarget(write.Path)
	if err != nil {
		return err
	}
	defer target.Dir.Close()
	if !target.Absent {
		return reuse()
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temp := ".mcparcel-" + hex.EncodeToString(nonce[:]) + ".tmp"
	dirfd := int(target.Dir.Fd())
	fd, err := unix.Openat(dirfd, temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), temp)
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
		_ = unix.Unlinkat(dirfd, temp, 0)
	}()
	remaining := write.Bytes
	for len(remaining) > 0 {
		n, err := file.Write(remaining)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		remaining = remaining[n:]
	}
	if err := file.Sync(); err != nil {
		return err
	}
	err = file.Close()
	closed = true
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.Linkat(dirfd, temp, dirfd, target.Name, 0); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return reuse()
		}
		return err
	}
	if err := target.Dir.Sync(); err != nil {
		return errors.Join(ErrDurability, err)
	}
	if err := unix.Unlinkat(dirfd, temp, 0); err != nil {
		return errors.Join(ErrDurability, err)
	}
	if err := target.Dir.Sync(); err != nil {
		return errors.Join(ErrDurability, err)
	}
	return nil
}
