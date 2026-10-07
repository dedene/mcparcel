package cli_test

import (
	"fmt"
	"os/exec"
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
	fail := func(err error) (int, string, error) { _ = unix.Close(master); return -1, "", err }
	if err = unix.IoctlSetInt(master, unix.TIOCPTYGRANT, 0); err != nil {
		return fail(err)
	}
	if err = unix.IoctlSetInt(master, unix.TIOCPTYUNLK, 0); err != nil {
		return fail(err)
	}
	// ptsname(3) names the slave /dev/ttys%03d after the master's minor number.
	var st unix.Stat_t
	if err = unix.Fstat(master, &st); err != nil {
		return fail(err)
	}
	minor := unix.Minor(uint64(st.Rdev))
	name := fmt.Sprintf("/dev/ttys%03d", minor)
	var slave unix.Stat_t
	if err = unix.Stat(name, &slave); err != nil {
		return fail(err)
	}
	if unix.Minor(uint64(slave.Rdev)) != minor {
		return fail(fmt.Errorf("%s does not match the pty master", name))
	}
	return master, name, nil
}

// sessionAndGroup returns pid's session from getsid(2), because ps shows no
// session ID on macOS, and its process group from ps.
func sessionAndGroup(pid int) (session, group int, err error) {
	if session, err = unix.Getsid(pid); err != nil {
		return 0, 0, err
	}
	out, err := exec.Command("/bin/ps", "-o", "pgid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, 0, err
	}
	group, err = strconv.Atoi(strings.TrimSpace(string(out)))
	return session, group, err
}

// stdioTargets returns what pid's descriptors 0, 1 and 2 refer to, from lsof.
func stdioTargets(pid int) ([]string, error) {
	out, err := exec.Command("/usr/sbin/lsof", "-a", "-p", strconv.Itoa(pid), "-d", "0,1,2", "-Fn").Output()
	if err != nil {
		return nil, err
	}
	targets := make([]string, 3)
	fd := -1
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "f"):
			if fd, err = strconv.Atoi(line[1:]); err != nil || fd < 0 || fd > 2 {
				return nil, fmt.Errorf("unexpected lsof line %q", line)
			}
		case strings.HasPrefix(line, "n") && fd >= 0:
			targets[fd] = line[1:]
		}
	}
	return targets, nil
}
