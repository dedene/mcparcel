//go:build !mcparceltest

package cmd

import "golang.org/x/sys/unix"

// getTermios is the ioctl that reads a terminal's settings.
const getTermios = unix.TIOCGETA

// flushInput discards input that fd's terminal received but nobody read yet.
func flushInput(fd int) error {
	return unix.IoctlSetPointerInt(fd, unix.TIOCFLUSH, unix.TCIFLUSH)
}
