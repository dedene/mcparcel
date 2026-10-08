package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	ModeDesktop  = "desktop"
	ModeHeadless = "headless"
)

// ErrHeadlessOnly is returned for a runtime command in desktop mode on a
// platform without desktop support (Linux).
var ErrHeadlessOnly = errors.New("linux supports headless mode only")

// maxSocketPath keeps sockaddr_un.sun_path within its smallest platform size.
const maxSocketPath = 100

// Headless reports whether config.json selects headless mode.
func (l Local) Headless() bool { return l.Runtime != nil && l.Runtime.Mode == ModeHeadless }

// Headless reports whether these paths come from a headless state root.
func (p Paths) Headless() bool { return p.StateRoot != "" }

// ErrHeadlessOnePassword is config_required for a connection that needs a
// 1Password reference in headless mode through a profile other than a
// service-account one.
var ErrHeadlessOnePassword = fmt.Errorf("%w: 1Password desktop-app profiles are unavailable in headless mode", ErrConfigRequired)

// DesktopSupported reports whether desktop mode can run on goos.
func DesktopSupported(goos string) bool { return goos == "darwin" }

// CheckMode refuses desktop mode where only headless mode is supported.
func CheckMode(rt RuntimeDefaults, desktopSupported bool) error {
	if rt.Mode != ModeHeadless && !desktopSupported {
		return ErrHeadlessOnly
	}
	return nil
}

func validateRuntime(rt *RuntimeDefaults) error {
	if rt == nil {
		return nil
	}
	switch rt.Mode {
	case "", ModeDesktop:
		if rt.StateRoot != "" {
			return fieldError("runtime.stateRoot", "allowed only in headless mode")
		}
		if rt.Supervised {
			return fieldError("runtime.supervised", "allowed only in headless mode")
		}
	case ModeHeadless:
		if !cleanAbsolute(rt.StateRoot) {
			return fieldError("runtime.stateRoot", "clean absolute path required in headless mode")
		}
	default:
		return fieldError("runtime.mode", "must be desktop or headless")
	}
	return nil
}

func cleanAbsolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/" && !strings.ContainsRune(path, 0)
}

// ReadRuntime reads config.json only (strictly), without lock, and returns its
// runtime block; an absent file or block is desktop mode. It never creates
// anything, so it works on a read-only configuration directory.
func ReadRuntime(ctx context.Context, p Paths) (RuntimeDefaults, error) {
	if err := ctx.Err(); err != nil {
		return RuntimeDefaults{}, err
	}
	path := p.ConfigFile
	if path == "" {
		path = filepath.Join(p.ConfigDir, "config.json")
	}
	data, err := readConfig(path)
	if errors.Is(err, os.ErrNotExist) {
		return RuntimeDefaults{}, nil
	}
	if err != nil {
		return RuntimeDefaults{}, err
	}
	local, err := DecodeLocal(data)
	if err != nil {
		return RuntimeDefaults{}, err
	}
	if local.Runtime == nil {
		return RuntimeDefaults{}, nil
	}
	return *local.Runtime, nil
}

// ApplyStateRoot derives StateDir, DataDir, CacheDir and RuntimeDir from root
// (<root>/state, /data, /cache, /run); ConfigDir is unchanged. Symlinks in the
// root's ancestors are resolved once, the root itself is not, as in
// ResolvePaths. It creates nothing.
func ApplyStateRoot(p Paths, root string) (Paths, error) {
	if !cleanAbsolute(root) {
		return Paths{}, ErrUnsafePath
	}
	parent, err := canonicalConfigPath(filepath.Dir(root))
	if err != nil {
		return Paths{}, err
	}
	root = filepath.Join(parent, filepath.Base(root))
	p.StateRoot = root
	p.StateDir = filepath.Join(root, "state")
	p.DataDir = filepath.Join(root, "data")
	p.CacheDir = filepath.Join(root, "cache")
	p.RuntimeDir = filepath.Join(root, "run")
	p.SocketFile = filepath.Join(p.RuntimeDir, "daemon.sock")
	p.LockFile = filepath.Join(p.RuntimeDir, "daemon.lock")
	p.LogFile = filepath.Join(p.StateDir, "daemon.log")
	if len(p.SocketFile) > maxSocketPath {
		return Paths{}, ErrUnsafePath
	}
	return p, nil
}
