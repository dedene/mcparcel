//go:build darwin || linux

package runtime

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/lockfile"
	"github.com/dedene/mcparcel/internal/output"
)

func socketStat(dir *os.File) (*unix.Stat_t, error) {
	var st unix.Stat_t
	err := unix.Fstatat(int(dir.Fd()), "daemon.sock", &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil || safeSocketStat(&st) != nil {
		return nil, config.ErrUnsafePath
	}
	return &st, nil
}

func sameSocket(a, b *unix.Stat_t) bool {
	return a != nil && b != nil && a.Dev == b.Dev && a.Ino == b.Ino
}

func StartDaemon(ctx context.Context, paths config.Paths, executable string, env []string) (bool, error) {
	return startDaemon(ctx, paths, executable, env, config.OpenPrivateFile)
}

func startDaemon(ctx context.Context, paths config.Paths, executable string, env []string, open func(*os.File, string, bool) (*os.File, error)) (bool, error) {
	dir, err := config.OpenPrivateDirUnder(paths.StateRoot, paths.RuntimeDir, true)
	if err != nil {
		return false, err
	}
	defer dir.Close()
	lock, err := lockfile.Open(ctx, true, func() (*os.File, error) {
		return open(dir, "daemon.lock", true)
	})
	if err != nil {
		return false, err
	}
	defer lock.Close() // Do NOT LOCK_UN: the child inherits this description.
	err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return false, nil
	}
	if err != nil {
		return false, config.ErrUnsafePath
	}
	st, err := socketStat(dir)
	if err != nil {
		return false, err
	}
	if st != nil {
		dialCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		conn, e := (&net.Dialer{}).DialContext(dialCtx, "unix", paths.SocketFile)
		cancel()
		if e == nil {
			_ = conn.Close()
			return false, nil
		}
		if !errors.Is(e, syscall.ECONNREFUSED) && !errors.Is(e, syscall.ENOENT) {
			return false, output.NewError("runtime_start_failed", nil)
		}
		current, e := socketStat(dir)
		if e != nil {
			return false, e
		}
		if current != nil {
			if !sameSocket(st, current) {
				return false, config.ErrUnsafePath
			}
			if e := unix.Unlinkat(int(dir.Fd()), "daemon.sock", 0); e != nil {
				return false, config.ErrUnsafePath
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return false, output.NewError("runtime_start_failed", nil)
	}
	defer null.Close()
	if !filepath.IsAbs(executable) {
		return false, output.NewError("runtime_start_failed", nil)
	}
	child := exec.Command(executable, "daemon", "--lock-fd=3")
	child.Env, child.Dir = env, paths.Home
	child.Stdin, child.Stdout, child.Stderr = null, null, null
	child.ExtraFiles = []*os.File{lock}
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		return false, output.NewError("runtime_start_failed", nil)
	}
	// The initiating CLI exits independently. The daemon owns the inherited lock.
	_ = child.Process.Release()
	return true, nil
}

func AdoptLock(paths config.Paths, fd uintptr) (*os.File, error) {
	inherited := os.NewFile(fd, "inherited-daemon-lock")
	if inherited == nil {
		return nil, config.ErrUnsafePath
	}
	fail := func() (*os.File, error) { _ = inherited.Close(); return nil, config.ErrUnsafePath }
	dir, err := config.OpenPrivateDirUnder(paths.StateRoot, paths.RuntimeDir, false)
	if err != nil {
		return fail()
	}
	defer dir.Close()
	expected, err := config.OpenPrivateFile(dir, "daemon.lock", false)
	if err != nil {
		return fail()
	}
	defer expected.Close()
	var a, b unix.Stat_t
	if unix.Fstat(int(inherited.Fd()), &a) != nil || unix.Fstat(int(expected.Fd()), &b) != nil ||
		a.Dev != b.Dev || a.Ino != b.Ino || a.Mode&unix.S_IFMT != unix.S_IFREG ||
		a.Uid != uint32(os.Getuid()) || a.Mode&0o7777 != 0o600 || a.Nlink != 1 {
		return fail()
	}
	if unix.Flock(int(inherited.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return fail()
	}
	unix.CloseOnExec(int(inherited.Fd())) // MCP children must not inherit the lifetime lock.
	return inherited, nil
}

func safeSocketStat(st *unix.Stat_t) error {
	if st.Mode&unix.S_IFMT != unix.S_IFSOCK || st.Uid != uint32(os.Getuid()) || st.Mode&0o7777 != 0o600 {
		return config.ErrUnsafePath
	}
	return nil
}

func DaemonEnvironment(p config.Paths) []string {
	env := map[string]string{"HOME": p.Home, "XDG_CONFIG_HOME": filepath.Dir(p.ConfigDir)}
	// A headless daemon derives its state directories from config.json's
	// state root, exactly as the CLI did.
	if p.StateRoot == "" {
		env["XDG_DATA_HOME"], env["XDG_CACHE_HOME"], env["XDG_STATE_HOME"], env["MCPARCEL_RUNTIME_DIR"] = filepath.Dir(p.DataDir), filepath.Dir(p.CacheDir), filepath.Dir(p.StateDir), p.RuntimeDir
	}
	for _, k := range []string{"PATH", "SHELL", "TMPDIR", "USER", "LOGNAME", "LANG", "LC_ALL", "__CF_USER_TEXT_ENCODING"} {
		if v, ok := os.LookupEnv(k); ok {
			env[k] = v
		}
	}
	if env["PATH"] == "" {
		env["PATH"] = defaultPath
	}
	if env["SHELL"] == "" {
		env["SHELL"] = defaultShell
	}
	result := make([]string, 0, len(env))
	for k, v := range env {
		result = append(result, k+"="+v)
	}
	return result
}

func dialSocket(ctx context.Context, p config.Paths) (*net.UnixConn, error) {
	dir, e := config.OpenPrivateDirUnder(p.StateRoot, p.RuntimeDir, false)
	if e != nil {
		return nil, e
	}
	defer func() { _ = dir.Close() }()
	st, e := socketStat(dir)
	if e != nil {
		return nil, e
	}
	if st == nil {
		return nil, os.ErrNotExist
	}
	conn, e := (&net.Dialer{}).DialContext(ctx, "unix", p.SocketFile)
	if e != nil {
		return nil, e
	}
	c := conn.(*net.UnixConn)
	if e = CheckPeer(c); e != nil {
		_ = c.Close()
		return nil, e
	}
	return c, nil
}

func lockHeld(p config.Paths) (bool, error) {
	dir, e := config.OpenPrivateDirUnder(p.StateRoot, p.RuntimeDir, false)
	if errors.Is(e, os.ErrNotExist) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	defer func() { _ = dir.Close() }()
	f, e := config.OpenPrivateFile(dir, "daemon.lock", false)
	if errors.Is(e, os.ErrNotExist) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	defer func() { _ = f.Close() }()
	e = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(e, unix.EWOULDBLOCK) || errors.Is(e, unix.EAGAIN) {
		return true, nil
	}
	if e != nil {
		return false, config.ErrUnsafePath
	}
	return false, nil
}

func waitLock(ctx context.Context, p config.Paths) error {
	for {
		held, e := lockHeld(p)
		if e != nil {
			return e
		}
		if !held {
			return nil
		}
		select {
		case <-ctx.Done():
			return output.NewError("runtime_start_failed", nil)
		case <-time.After(25 * time.Millisecond):
		}
	}
}
