package runtime

// Fallbacks when the caller's environment has no PATH or SHELL.
const (
	defaultPath  = "/usr/bin:/bin:/usr/sbin:/sbin"
	defaultShell = "/bin/sh"
)

// runtimeDirAction is the next action for an unsafe desktop runtime
// directory: the default under /tmp is shared, so another user can create it
// first.
const runtimeDirAction = "Set MCPARCEL_RUNTIME_DIR to a private directory you own (for example $XDG_RUNTIME_DIR/mcparcel), then run the command again."
