package config

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// FileStatus is one configuration document as doctor sees it.
type FileStatus struct {
	Name    string // "config.json" | "personal.json" | "selections.json" | "catalog <source-id>"
	Present bool
	Err     error // nil, ErrUnsafePath, or a field error
}

// FileReport is every document read under one shared configuration lock.
type FileReport struct {
	Files    []FileStatus
	State    *State // set when every document decoded and ValidateState passed
	StateErr error  // cross-document error when every document is valid on its own
}

// CheckFiles reads every document under one shared config lock (the lock of
// ReadState), decodes each on its own so one bad file does not hide the
// others, then assembles State with readStateUnlocked. It never creates the
// lock file. Its error is only the lock wait (ctx) or an unsafe config dir.
func CheckFiles(ctx context.Context, paths Paths) (FileReport, error) {
	var report FileReport
	err := withReadLock(ctx, paths, func() error {
		report = checkFilesUnlocked(paths)
		return nil
	})
	if err != nil {
		return FileReport{}, err
	}
	return report, nil
}

func checkFilesUnlocked(paths Paths) FileReport {
	var report FileReport
	check := func(name, path string, decode func([]byte) error) bool {
		status := FileStatus{Name: name}
		data, err := readConfig(path)
		if err == nil {
			status.Present = true
			err = decode(data)
		}
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
		status.Err = err
		report.Files = append(report.Files, status)
		return err == nil
	}
	var local Local
	ok := check("config.json", configPath(paths.ConfigFile, paths.ConfigDir, "config.json"), func(data []byte) (err error) {
		local, err = DecodeLocal(data)
		return err
	})
	ok = check("personal.json", configPath(paths.PersonalFile, paths.ConfigDir, "personal.json"), func(data []byte) error {
		_, err := DecodeCatalog(data)
		return err
	}) && ok
	ok = check("selections.json", configPath(paths.SelectionsFile, paths.ConfigDir, "selections.json"), func(data []byte) error {
		_, err := DecodeSelections(data)
		return err
	}) && ok
	for _, source := range local.Sources {
		name := "catalog " + source.ID
		path := filepath.Join(paths.DataDir, "catalogs", source.ID, source.Commit+".json")
		data, err := readConfig(path)
		switch {
		case err == nil:
			_, err = DecodeCatalog(data)
		case !errors.Is(err, ErrUnsafePath):
			err = fieldError("catalogs."+source.ID, "active source snapshot required")
		}
		report.Files = append(report.Files, FileStatus{Name: name, Present: data != nil, Err: err})
		ok = ok && err == nil
	}
	if !ok {
		return report
	}
	state, err := readStateUnlocked(paths)
	if err != nil {
		report.StateErr = err
		return report
	}
	report.State = &state
	return report
}

func configPath(path, dir, name string) string {
	if path != "" {
		return path
	}
	return filepath.Join(dir, name)
}

// FieldReason returns "<field path>: <reason>" for an ErrConfig field error,
// else "". Field errors carry field names and fixed reasons, never values.
func FieldReason(err error) string {
	if err == nil || !errors.Is(err, ErrConfig) {
		return ""
	}
	text := err.Error()
	prefix := ErrConfig.Error() + ": "
	i := strings.Index(text, prefix)
	if i < 0 {
		return ""
	}
	return text[i+len(prefix):]
}

var onePasswordRefPart = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// OnePasswordRefPartsValid applies the catalog.md character rule to each part
// of an already well-formed op:// reference: ^[A-Za-z0-9_.-]+$.
func OnePasswordRefPartsValid(ref string) bool {
	rest, ok := strings.CutPrefix(ref, "op://")
	if !ok {
		return false
	}
	for part := range strings.SplitSeq(rest, "/") {
		if !onePasswordRefPart.MatchString(part) {
			return false
		}
	}
	return true
}

// MissingConfig names each declared input without a value or default, and
// reports whether the declared credential profile is unbound or gone.
func MissingConfig(state State, row EffectiveConnection) (inputs []string, profile bool) {
	if row.Definition == nil {
		return nil, false
	}
	sel := state.Selections.Connections[row.ID]
	for _, name := range slices.Sorted(maps.Keys(row.Definition.Inputs)) {
		if _, ok := sel.Inputs[name]; !ok && row.Definition.Inputs[name].Default == nil {
			inputs = append(inputs, name)
		}
	}
	if row.Definition.CredentialProfile != "" {
		if _, ok := state.Local.CredentialProfiles[sel.CredentialProfile]; sel.CredentialProfile == "" || !ok {
			profile = true
		}
	}
	return inputs, profile
}
