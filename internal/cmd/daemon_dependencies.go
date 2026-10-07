//go:build !mcparceltest

package cmd

import (
	"time"

	"github.com/dedene/mcparcel/internal/config"
)

// daemonIdleTimeout is the runtime's idle exit delay; zero keeps the default.
func daemonIdleTimeout(config.Paths) time.Duration { return 0 }
