package config

import (
	"os"
	"path/filepath"
	"runtime"
)

// DefaultTempDir is the parent of the default runtime directory. On macOS it is
// /private/tmp, which avoids Darwin's long per-user $TMPDIR and the /tmp
// symlink. Elsewhere it is $TMPDIR, cleaned, or /tmp when that is unset or
// relative.
func DefaultTempDir() string { return tempDirFor(runtime.GOOS, os.Getenv("TMPDIR")) }

func tempDirFor(goos, tmpdir string) string {
	if goos == "darwin" {
		return "/private/tmp"
	}
	if tmpdir == "" || !filepath.IsAbs(tmpdir) {
		return "/tmp"
	}
	return filepath.Clean(tmpdir)
}
