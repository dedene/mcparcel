package runtime

import (
	"os"

	"golang.org/x/sys/unix"
)

// cloneFile makes name in dir a copy-on-write clone of src (APFS): free in
// time and space, and later writes to src do not reach it.
func cloneFile(src *os.File, dir int, name string) error {
	return unix.Fclonefileat(int(src.Fd()), dir, name, unix.CLONE_NOFOLLOW|unix.CLONE_NOOWNERCOPY)
}
