//go:build !mcparceltest

package cmd

import (
	"os"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"
)

func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = master.Close() })
	fd, _ := fileFD(master)
	n := 0
	if err = unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err == nil {
		n, err = unix.IoctlGetInt(fd, unix.TIOCGPTN)
	}
	if err != nil {
		t.Fatal(err)
	}
	slave, err = os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	return master, slave
}
