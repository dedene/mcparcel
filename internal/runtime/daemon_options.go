package runtime

import (
	"io"
	"os"
	"time"

	"github.com/dedene/mcparcel/internal/config"
)

type DaemonOptions struct {
	Paths   config.Paths
	Version string
	// Executable is the daemon's own file (os.Executable), reported by status.
	Executable      string
	Lock            *os.File
	LoginEnv        map[string]string
	EnvFallback     bool
	Handler         Handler
	Log             io.Writer
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
	// PromptTimeout backs up the CLI's own elicit.PromptTimeout; set only in tests.
	PromptTimeout time.Duration
	// NoIdleExit keeps a supervised runtime (runtime serve) running when idle.
	NoIdleExit bool
	// Supervised refuses restart requests: only the supervisor restarts the
	// runtime, so a CLI cannot replace it with an auto-started daemon.
	Supervised bool
}
