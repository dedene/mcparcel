//go:build !mcparceltest

package cmd

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
)

// desktopSupported reports whether this platform runs desktop mode; Linux runs
// headless mode only.
func desktopSupported() bool { return config.DesktopSupported(runtime.GOOS) }

func newCredentials(_ config.Paths, version string) auth.Resolver {
	return auth.NewResolver(auth.ResolverOptions{Provider: auth.NewOnePasswordProvider(version)})
}

func newKeychain(config.Paths) func(context.Context, string) (string, error) {
	return runtimeclient.KeychainLookup
}

func newKeyring(config.Paths) auth.Keyring { return auth.SystemKeyring{} }

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
	if flushInput(fd) != nil {
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
	_, err := unix.IoctlGetTermios(fd, getTermios)
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
