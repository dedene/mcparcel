package config

import "golang.org/x/sys/unix"

// readOnlyMount reports whether fd's mount is read-only (a ConfigMap volume).
// A failed statfs counts as writable.
func readOnlyMount(fd int) bool {
	var st unix.Statfs_t
	return unix.Fstatfs(fd, &st) == nil && uint64(st.Flags)&unix.ST_RDONLY != 0
}
