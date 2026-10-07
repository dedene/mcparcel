package cli_test

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// openPTY opens a pseudo-terminal master and returns it with its slave path.
func openPTY() (int, string, error) {
	master, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, "", err
	}
	if err = unix.IoctlSetPointerInt(master, unix.TIOCSPTLCK, 0); err != nil {
		_ = unix.Close(master)
		return -1, "", err
	}
	n, err := unix.IoctlGetUint32(master, unix.TIOCGPTN)
	if err != nil {
		_ = unix.Close(master)
		return -1, "", err
	}
	return master, fmt.Sprintf("/dev/pts/%d", n), nil
}

// sessionAndGroup reads pid's session and process group from /proc/<pid>/stat.
func sessionAndGroup(pid int) (session, group int, err error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, 0, err
	}
	// Fields after the command name: state ppid pgrp session ...
	fields := strings.Fields(string(b[bytes.LastIndexByte(b, ')')+1:]))
	if len(fields) < 4 {
		return 0, 0, fmt.Errorf("short stat: %q", b)
	}
	if group, err = strconv.Atoi(fields[2]); err != nil {
		return 0, 0, err
	}
	session, err = strconv.Atoi(fields[3])
	return session, group, err
}

// stdioTargets returns what pid's descriptors 0, 1 and 2 refer to.
func stdioTargets(pid int) ([]string, error) {
	targets := make([]string, 3)
	for fd := range targets {
		target, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%d", pid, fd))
		if err != nil {
			return nil, err
		}
		targets[fd] = target
	}
	return targets, nil
}
