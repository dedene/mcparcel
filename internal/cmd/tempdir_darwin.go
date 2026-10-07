package cmd

// defaultTempDir is the parent of the default runtime directory. /private/tmp
// avoids Darwin's long per-user temporary directory and its /var symlink.
func defaultTempDir() string { return "/private/tmp" }
