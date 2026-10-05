package config

import (
	"path/filepath"
	"strconv"
)

func ResolvePaths(getenv func(string) string, home, osTemp string, uid int) (Paths, error) {
	if !filepath.IsAbs(home) || !filepath.IsAbs(osTemp) {
		return Paths{}, ErrUnsafePath
	}
	root := func(name, defaultPath string) (string, error) {
		path := getenv(name)
		if path == "" {
			path = filepath.Join(home, defaultPath)
		}
		if !filepath.IsAbs(path) {
			return "", ErrUnsafePath
		}
		return filepath.Join(path, "mcparcel"), nil
	}
	p := Paths{Home: home}
	var err error
	if p.ConfigDir, err = root("XDG_CONFIG_HOME", ".config"); err != nil {
		return Paths{}, err
	}
	if p.DataDir, err = root("XDG_DATA_HOME", ".local/share"); err != nil {
		return Paths{}, err
	}
	if p.CacheDir, err = root("XDG_CACHE_HOME", ".cache"); err != nil {
		return Paths{}, err
	}
	if p.StateDir, err = root("XDG_STATE_HOME", ".local/state"); err != nil {
		return Paths{}, err
	}
	p.RuntimeDir = getenv("MCPARCEL_RUNTIME_DIR")
	if p.RuntimeDir == "" {
		p.RuntimeDir = filepath.Join(osTemp, "mcp-"+strconv.Itoa(uid))
	}
	if !filepath.IsAbs(p.RuntimeDir) || filepath.Clean(p.RuntimeDir) != p.RuntimeDir {
		return Paths{}, ErrUnsafePath
	}
	p.PersonalFile = filepath.Join(p.ConfigDir, "personal.json")
	p.ConfigFile = filepath.Join(p.ConfigDir, "config.json")
	p.SelectionsFile = filepath.Join(p.ConfigDir, "selections.json")
	p.SocketFile = filepath.Join(p.RuntimeDir, "daemon.sock")
	p.LockFile = filepath.Join(p.RuntimeDir, "daemon.lock")
	p.LogFile = filepath.Join(p.StateDir, "daemon.log")
	if len(p.SocketFile) > 100 {
		return Paths{}, ErrUnsafePath
	}
	return p, nil
}
