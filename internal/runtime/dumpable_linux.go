package runtime

import "golang.org/x/sys/unix"

// HardenProcess makes this process non-dumpable (D20), so a same-uid stdio
// server can read neither its /proc/<pid>/environ (a headless service-account
// token, a forwarded client secret) nor, where Yama allows ptrace, its
// memory. main calls it for every mcparcel process; the daemon and runtime
// serve repeat it. The kernel resets the flag on execve, so children are
// unaffected. Call it first, before any secret is read.
func HardenProcess() { _ = unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0) }
