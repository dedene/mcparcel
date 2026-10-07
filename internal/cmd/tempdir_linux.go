package cmd

import (
	"os"
	"path/filepath"
)

// defaultTempDir is the parent of the default runtime directory: $TMPDIR or
// /tmp, cleaned. config.ResolvePaths rejects it when it is not absolute.
func defaultTempDir() string { return filepath.Clean(os.TempDir()) }
