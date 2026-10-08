package runtime

// Fallbacks when the caller's environment has no PATH or SHELL.
const (
	defaultPath  = "/usr/bin:/bin:/usr/sbin:/sbin"
	defaultShell = "/bin/zsh"
)

// runtimeDirAction is empty: the registry's unsafe_local_path text applies.
const runtimeDirAction = ""
