//go:build !mcparceltest

package cmd

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
)

// desktopSupported reports whether this platform runs desktop mode (macOS
// and Linux).
func desktopSupported() bool { return config.DesktopSupported(runtime.GOOS) }

// desktopOnePassword reports whether desktop mode here can use the 1Password
// desktop app; on Linux only service-account profiles read 1Password.
func desktopOnePassword(config.Paths) bool { return config.DesktopOnePasswordSupported(runtime.GOOS) }

// keyringReachable reports whether this process reaches the system keyring:
// on Linux, whether a usable D-Bus session bus exists.
func keyringReachable(config.Paths) bool { return auth.KeyringReachable() }

// desktopSession reports whether this process runs in a desktop session. Off
// Linux it always does; on Linux it needs a display or a usable session bus.
func desktopSession(config.Paths) bool {
	if runtime.GOOS != "linux" {
		return true
	}
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "" || auth.KeyringReachable()
}

// newCredentials is the 1Password resolver. env is the daemon's login
// environment, which a service-account profile's tokenEnv is read from;
// headless mode, and desktop mode without the app, never wire the desktop app.
func newCredentials(paths config.Paths, version string, env map[string]string) auth.Resolver {
	desktop := !paths.Headless() && desktopOnePassword(paths)
	return auth.NewResolver(auth.ResolverOptions{Provider: auth.NewOnePasswordProvider(version, auth.OnePasswordOptions{DesktopApp: desktop, Env: env})})
}

// newKeychain is the env: fallback to a Keychain generic password; macOS only.
func newKeychain(config.Paths) func(context.Context, string) (string, error) {
	if runtime.GOOS != "darwin" {
		return nil
	}
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

// onePasswordAppDirs are where doctor looks for 1Password.app (stat only);
// none off macOS.
func onePasswordAppDirs(paths config.Paths) []string {
	if runtime.GOOS != "darwin" {
		return nil
	}
	return []string{"/Applications", filepath.Join(paths.Home, "Applications")}
}

// retainEnabled reports whether a desktop runtime starts from a retained copy
// of this binary; release builds always do.
func retainEnabled(config.Paths) bool { return true }
