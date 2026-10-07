package testutil

import "github.com/dedene/mcparcel/internal/config"

// tempRoot is read once, before any test sets TMPDIR: IsolatedPaths points
// TMPDIR into its own root, and a later root must not nest inside it.
var tempRoot = config.DefaultTempDir()

// TempRoot is the parent for test roots that hold sockets: short enough for
// the 100-byte socket path limit and free of symlinked ancestors. It follows
// the runtime's own default (/private/tmp on macOS, $TMPDIR or /tmp on Linux).
func TempRoot() string { return tempRoot }
