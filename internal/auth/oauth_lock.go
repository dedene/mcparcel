package auth

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"

	"github.com/dedene/mcparcel/internal/config"
)

// oauthLockFile holds the connections auth lock barred from reusing their
// stored OAuth sessions. It is a set, not a timestamp, so a clock moved back
// cannot unlock anything; only a completed sign-in clears an entry. The daemon
// is its only writer and keeps the set in memory; auth status reads it offline.
const (
	oauthLockFile     = "auth-lock.json"
	oauthLockMaxBytes = 1 << 20
)

type oauthLockDoc struct {
	V           int      `json:"v"`
	Connections []string `json:"connections"`
}

// ReadOAuthLock returns the locked connection IDs; a missing file is an empty
// set.
func ReadOAuthLock(stateDir string) (map[string]bool, error) {
	set := map[string]bool{}
	dir, err := config.OpenPrivateDir(stateDir, false)
	if errors.Is(err, os.ErrNotExist) {
		return set, nil
	}
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	f, err := config.OpenPrivateFile(dir, oauthLockFile, false)
	if errors.Is(err, os.ErrNotExist) {
		return set, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, oauthLockMaxBytes+1))
	if err != nil {
		return nil, err
	}
	var doc oauthLockDoc
	if len(b) > oauthLockMaxBytes || json.Unmarshal(b, &doc) != nil || doc.V != 1 {
		return nil, errors.New("invalid auth lock file")
	}
	for _, id := range doc.Connections {
		set[id] = true
	}
	return set, nil
}

// WriteOAuthLock replaces the file with ids, sorted and without duplicates.
func WriteOAuthLock(stateDir string, ids []string) error {
	ids = slices.Clone(ids)
	slices.Sort(ids)
	b, err := json.Marshal(oauthLockDoc{V: 1, Connections: slices.Compact(ids)})
	if err != nil {
		return err
	}
	return replaceStateFile(stateDir, oauthLockFile, b)
}
