//go:build !mcparceltest

package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/elicit"
)

// The production binary answers a prompt only from a real terminal: fixture
// stand-in files in StateDir change nothing.
func TestNewTerminalNeedsTerminal(t *testing.T) {
	state := t.TempDir()
	for _, name := range []string{"fixture-terminal", "fixture-dialog-answer"} {
		if err := os.WriteFile(filepath.Join(state, name), []byte("4\nAlways allow\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	paths := config.Paths{StateDir: state}
	for _, files := range [][2]*os.File{{r, w}, {nil, w}, {r, nil}, {nil, nil}} {
		if newTerminal(paths, files[0], files[1]) != nil {
			t.Fatal("terminal without a tty")
		}
	}
}

func TestPollReader(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	fd, ok := fileFD(r)
	if !ok {
		t.Fatal("no fd")
	}
	ctx, cancel := context.WithCancel(context.Background())
	if _, err = w.WriteString("4\n"); err != nil {
		t.Fatal(err)
	}
	p := elicit.Prompt{Message: "Allow?", Persist: []string{"session", "always"}}
	if a := elicit.Ask(ctx, &pollReader{ctx: ctx, fd: fd}, io.Discard, "fixture", p); a.Action != "accept" || a.Persist != "always" {
		t.Fatal(a)
	}
	done := make(chan error)
	go func() {
		_, err := (&pollReader{ctx: ctx, fd: fd}).Read(make([]byte, 1))
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err = <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read ignored the end of ctx")
	}
	_ = w.Close()
	if _, err = (&pollReader{ctx: context.Background(), fd: fd}).Read(make([]byte, 1)); err != io.EOF {
		t.Fatal(err)
	}
}

// A line typed before a prompt opens cannot answer it: only input after the
// prompt's reader is created counts.
func TestPromptReaderDropsTypeAhead(t *testing.T) {
	master, slave := openPTY(t)
	if _, err := master.WriteString("4\n"); err != nil {
		t.Fatal(err)
	}
	fd, ok := fileFD(slave)
	if !ok {
		t.Fatal("no fd")
	}
	ctx := context.Background()
	r := promptReader(ctx, fd)
	if _, err := master.WriteString("2\n"); err != nil {
		t.Fatal(err)
	}
	p := elicit.Prompt{Message: "Allow?", Persist: []string{"session", "always"}}
	if a := elicit.Ask(ctx, r, io.Discard, "fixture", p); a.Action != "accept" || a.Persist != "" {
		t.Fatal(a)
	}
	if _, err := master.WriteString("4\n"); err != nil {
		t.Fatal(err)
	}
	if a := elicit.Ask(ctx, promptReader(ctx, -1), io.Discard, "fixture", p); a.Action != "cancel" {
		t.Fatal("unflushable input answered", a)
	}
}

func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = master.Close() })
	fd, _ := fileFD(master)
	name := make([]byte, 128)
	if err = unix.IoctlSetInt(fd, unix.TIOCPTYGRANT, 0); err == nil {
		err = unix.IoctlSetInt(fd, unix.TIOCPTYUNLK, 0)
	}
	if err == nil {
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), unix.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))); errno != 0 {
			err = errno
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	slave, err = os.OpenFile(string(name[:bytes.IndexByte(name, 0)]), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	return master, slave
}

// The dialog runs only /usr/bin/osascript with a fixed script; server text is
// argv after it, never script source. The command is built, never run.
func TestDialogCommand(t *testing.T) {
	text := `"; do shell script "touch /tmp/x" --`
	argv := elicit.DialogArgs("fixture", elicit.Prompt{Message: text, Persist: []string{"always"}})
	cmd := dialogCommand(context.Background(), argv)
	if cmd.Path != "/usr/bin/osascript" || cmd.Args[0] != "/usr/bin/osascript" {
		t.Fatal(cmd.Path, cmd.Args)
	}
	script := cmd.Args[1 : len(cmd.Args)-len(argv)]
	if !slices.Equal(cmd.Args[len(cmd.Args)-len(argv):], argv) || len(script) != 2*len(dialogScript) {
		t.Fatal(cmd.Args)
	}
	for i, line := range dialogScript {
		if script[2*i] != "-e" || script[2*i+1] != line || strings.Contains(line, "touch") {
			t.Fatal(script)
		}
	}
	if !strings.HasPrefix(argv[0], "MCParcel") || !slices.Equal(argv[3:], []string{"Decline", "Allow once", "Always allow"}) {
		t.Fatal(argv)
	}
}
