package config

import "golang.org/x/sys/unix"

// readOnlyMount reports whether fd's file system is mounted read-only. A
// failed statfs counts as writable.
func readOnlyMount(fd int) bool {
	var st unix.Statfs_t
	return unix.Fstatfs(fd, &st) == nil && st.Flags&unix.MNT_RDONLY != 0
}
