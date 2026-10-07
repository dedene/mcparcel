//go:build !mcparceltest

package cmd

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
)

func newCredentials(_ config.Paths, version string) auth.Resolver {
	return auth.NewResolver(auth.ResolverOptions{Provider: auth.NewOnePasswordProvider(version)})
}

func newKeychain(config.Paths) func(context.Context, string) (string, error) {
	return runtimeclient.KeychainLookup
}

func newKeyring(config.Paths) auth.Keyring { return auth.SystemKeyring{} }

// newBrowser opens an https URL, or an http URL on a loopback host, in the
// default browser. The URL is one argv entry; no shell runs.
func newBrowser(config.Paths) func(context.Context, string) error {
	return func(ctx context.Context, raw string) error {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.Scheme != "https" && (u.Scheme != "http" || !loopbackHost(u.Hostname())) {
			return errors.New("unsupported sign-in URL")
		}
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, "/usr/bin/open", raw).Run()
	}
}

func loopbackHost(host string) bool {
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}

// newTerminal returns the reader for one prompt's answer, or nil unless in and
// errOut are terminals and this process is in the foreground of in's terminal
// (a background read would stop it with SIGTTIN).
func newTerminal(_ config.Paths, in, errOut *os.File) func(context.Context) io.Reader {
	fd, ok := fileFD(in)
	errFD, errOK := fileFD(errOut)
	if !ok || !errOK || !isTerminal(fd) || !isTerminal(errFD) {
		return nil
	}
	if group, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP); err != nil || group != unix.Getpgrp() {
		return nil
	}
	return func(ctx context.Context) io.Reader { return promptReader(ctx, fd) }
}

// newSetupTerminal reports whether setup may take over the terminal, with the
// same checks as newTerminal: in and errOut are terminals and this process is
// in the foreground of in's terminal.
func newSetupTerminal(in, errOut *os.File) bool {
	return newTerminal(config.Paths{}, in, errOut) != nil
}

// promptReader first discards input typed before the prompt, so a line typed
// ahead cannot answer a prompt not yet shown; if that fails it reads as EOF.
func promptReader(ctx context.Context, fd int) io.Reader {
	if unix.IoctlSetPointerInt(fd, unix.TIOCFLUSH, unix.TCIFLUSH) != nil {
		return strings.NewReader("")
	}
	return &pollReader{ctx: ctx, fd: fd}
}

// fileFD returns f's descriptor without File.Fd, which can change its flags.
func fileFD(f *os.File) (int, bool) {
	if f == nil {
		return -1, false
	}
	raw, err := f.SyscallConn()
	if err != nil {
		return -1, false
	}
	fd := -1
	if raw.Control(func(u uintptr) { fd = int(u) }) != nil {
		return -1, false
	}
	return fd, fd >= 0
}

func isTerminal(fd int) bool {
	_, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	return err == nil
}

// pollReader reads fd in short polls so the end of ctx stops it. It never
// changes the descriptor's flags: a terminal shares them with stdout and stderr.
type pollReader struct {
	ctx context.Context
	fd  int
}

func (r *pollReader) Read(p []byte) (int, error) {
	for {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
		ready, err := unix.Poll([]unix.PollFd{{Fd: int32(r.fd), Events: unix.POLLIN}}, 100)
		if ready == 0 || errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, err
		}
		n, err := unix.Read(r.fd, p)
		switch {
		case errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN):
			continue
		case err != nil:
			return 0, err
		case n == 0:
			return 0, io.EOF
		}
		return n, nil
	}
}

// dialogScript shows argv as title, text, timeout in seconds and buttons;
// server text only ever arrives as argv, never as script source.
var dialogScript = []string{
	"on run argv",
	"set r to display dialog (item 2 of argv) with title (item 1 of argv) buttons (items 4 thru -1 of argv) default button 1 giving up after ((item 3 of argv) as integer) with icon caution",
	"if gave up of r then return \"\"",
	"return button returned of r",
	"end run",
}

func newDialog(config.Paths) func(context.Context, []string) (string, error) {
	return func(ctx context.Context, argv []string) (string, error) {
		out, err := dialogCommand(ctx, argv).Output()
		return string(out), err
	}
}

func dialogCommand(ctx context.Context, argv []string) *exec.Cmd {
	args := make([]string, 0, 2*len(dialogScript)+len(argv))
	for _, line := range dialogScript {
		args = append(args, "-e", line)
	}
	return exec.CommandContext(ctx, "/usr/bin/osascript", append(args, argv...)...)
}
