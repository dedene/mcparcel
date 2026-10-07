package runtime

// Fallbacks when the caller's environment has no PATH or SHELL.
const (
	defaultPath  = "/usr/bin:/bin:/usr/sbin:/sbin"
	defaultShell = "/bin/zsh"
)
