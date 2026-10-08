package runtime

import "golang.org/x/sys/unix"

// HardenProcess makes the daemon non-dumpable (D20), so a same-uid stdio
// child can read neither /proc/<daemon>/environ (a headless service-account
// token, a forwarded client secret) nor, where Yama allows ptrace, the
// daemon's memory. The kernel resets the flag on execve, so children are
// unaffected. Call it first, before any secret is read.
func HardenProcess() { _ = unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0) }
