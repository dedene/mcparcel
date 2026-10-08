//go:build !linux

package runtime

// HardenProcess is a no-op off Linux: PR_SET_DUMPABLE is Linux only (D20).
func HardenProcess() {}
