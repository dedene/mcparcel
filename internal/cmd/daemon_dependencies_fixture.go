//go:build mcparceltest

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dedene/mcparcel/internal/config"
)

// daemonIdleTimeout reads StateDir/fixture-idle-timeout (a Go duration);
// absent or invalid keeps the default.
func daemonIdleTimeout(paths config.Paths) time.Duration {
	b, err := os.ReadFile(filepath.Join(paths.StateDir, "fixture-idle-timeout"))
	if err != nil {
		return 0
	}
	d, err := time.ParseDuration(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return d
}
