//go:build darwin || linux

package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func OpenPrivateDir(path string, create bool) (*os.File, error) {
	return openPrivateDir("", path, create)
}

// OpenPrivateDirUnder is OpenPrivateDir for a directory below a headless state
// root. The walk accepts trustedRoot itself when it is owned by the user or
// root, whatever its mode (kubelet creates an emptyDir 0777 or 2777); its
// ancestors and every component below it get OpenPrivateDir's checks. An empty
// trustedRoot is OpenPrivateDir; path must lie strictly below trustedRoot.
func OpenPrivateDirUnder(trustedRoot, path string, create bool) (*os.File, error) {
	if trustedRoot != "" && (!cleanAbsolute(trustedRoot) || !strings.HasPrefix(path, trustedRoot+"/")) {
		return nil, ErrUnsafePath
	}
	return openPrivateDir(trustedRoot, path, create)
}

func openPrivateDir(trustedRoot, path string, create bool) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, ErrUnsafePath
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrUnsafePath
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, part := range parts {
		last := i == len(parts)-1
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		created := false
		if errors.Is(e, unix.ENOENT) && create {
			e = unix.Mkdirat(fd, part, 0o700)
			if e == nil {
				created = true
				if syncErr := syncCreatedParents(fd); syncErr != nil {
					_ = unix.Close(fd)
					return nil, syncErr
				}
			}
			if e == nil || errors.Is(e, unix.EEXIST) {
				next, e = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			} else if notCreatable(e) {
				_ = unix.Close(fd)
				return nil, notCreatedError{}
			}
		}
		_ = unix.Close(fd)
		if errors.Is(e, unix.ENOENT) {
			return nil, os.ErrNotExist
		}
		if e != nil {
			return nil, ErrUnsafePath
		}
		fd = next
		// Linux gives a directory made inside a setgid one (an fsGroup emptyDir
		// is 2777) S_ISGID whatever mode mkdir got; clear it on what we made.
		if created && unix.Fchmod(fd, 0o700) != nil {
			_ = unix.Close(fd)
			return nil, ErrUnsafePath
		}
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil {
			_ = unix.Close(fd)
			return nil, ErrUnsafePath
		}
		owner := st.Uid == uint32(os.Getuid())
		safeAncestor := (owner || st.Uid == 0) && (st.Mode&0o022 == 0 || st.Mode&unix.S_ISVTX != 0)
		if !last && trustedRoot != "" && "/"+strings.Join(parts[:i+1], "/") == trustedRoot {
			safeAncestor = owner || st.Uid == 0
		}
		// Below a state root, S_ISGID on an existing directory is tolerated:
		// it only sets the group of new entries, and those are checked 0600.
		mode := uint32(st.Mode) & 0o7777
		if trustedRoot != "" {
			mode &^= unix.S_ISGID
		}
		if !safeAncestor || (last && (!owner || mode != 0o700)) {
			_ = unix.Close(fd)
			return nil, ErrUnsafePath
		}
	}
	return os.NewFile(uintptr(fd), path), nil
}

// notCreatable reports whether a mkdir or a creating open failed for want of
// space or write access rather than over what is already on the path.
func notCreatable(err error) bool {
	for _, errno := range []unix.Errno{unix.EACCES, unix.EPERM, unix.EROFS, unix.ENOSPC, unix.EDQUOT} {
		if errors.Is(err, errno) {
			return true
		}
	}
	return false
}

func OpenPrivateFile(dir *os.File, name string, create bool) (*os.File, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return nil, ErrUnsafePath
	}
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if create {
		flags = unix.O_RDWR | unix.O_CREAT | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	}
	fd, err := unix.Openat(int(dir.Fd()), name, flags, 0o600)
	if errors.Is(err, unix.ENOENT) {
		return nil, os.ErrNotExist
	}
	if create && notCreatable(err) {
		return nil, notCreatedError{}
	}
	if err != nil {
		return nil, ErrUnsafePath
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || privateFileStat(&st, os.Getuid()) != nil {
		_ = unix.Close(fd)
		return nil, ErrUnsafePath
	}
	return os.NewFile(uintptr(fd), name), nil
}

func privateFileStat(st *unix.Stat_t, uid int) error {
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(uid) || st.Mode&0o7777 != 0o600 || st.Nlink != 1 {
		return ErrUnsafePath
	}
	return nil
}

// errDanglingLink and errNoAccess say why openResolvedFile could not open a
// path: a symbolic link without a target, or a component the user may not
// search or read. OpenConfigFile reports both as ErrUnsafePath.
var (
	errDanglingLink = errors.New("dangling symbolic link")
	errNoAccess     = errors.New("permission denied")
)

func OpenConfigFile(path string) (*os.File, error) {
	f, err := openResolvedFile(path, configFileOK)
	if errors.Is(err, errDanglingLink) || errors.Is(err, errNoAccess) {
		return nil, ErrUnsafePath
	}
	return f, err
}

// openResolvedFile resolves path's symbolic links once (a Kubernetes volume's
// ..data layout), walks the resolved directory with openConfigDir's checks and
// opens the file without following links or blocking; ok decides on its
// stat. An absent path is os.ErrNotExist, any other failure ErrUnsafePath,
// errDanglingLink or errNoAccess.
func openResolvedFile(path string, ok func(st *unix.Stat_t, uid int, readOnly func() bool) bool) (*os.File, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, os.ErrNotExist
	}
	if errors.Is(err, os.ErrPermission) {
		return nil, errNoAccess
	}
	if err != nil {
		return nil, ErrUnsafePath
	}
	resolved, err := filepath.EvalSymlinks(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errDanglingLink
	}
	if errors.Is(err, os.ErrPermission) {
		return nil, errNoAccess
	}
	if err != nil {
		return nil, ErrUnsafePath
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return nil, ErrUnsafePath
	}
	dir, err := openConfigDir(filepath.Dir(resolved), false)
	if err != nil {
		return nil, ErrUnsafePath
	}
	fd := int(dir.Fd())
	parts := strings.Split(resolved, "/")
	next, e := unix.Openat(fd, parts[len(parts)-1], unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	_ = dir.Close()
	if errors.Is(e, unix.EACCES) {
		return nil, errNoAccess
	}
	if e != nil {
		return nil, ErrUnsafePath
	}
	var st unix.Stat_t
	if unix.Fstat(next, &st) != nil || !ok(&st, os.Getuid(), func() bool { return isReadOnlyMount(next) }) {
		_ = unix.Close(next)
		return nil, ErrUnsafePath
	}
	return os.NewFile(uintptr(next), resolved), nil
}

// isReadOnlyMount reports whether fd lies on a read-only mount; tests replace it.
var isReadOnlyMount = readOnlyMount

func configOwnerOK(st *unix.Stat_t, uid int) bool { return st.Uid == uint32(uid) || st.Uid == 0 }

// configFileOK accepts a regular file owned by uid or root that is not group-
// or other-writable; a file on a read-only mount cannot be changed and counts
// as not writable (a Kubernetes ConfigMap).
func configFileOK(st *unix.Stat_t, uid int, readOnly func() bool) bool {
	return st.Mode&unix.S_IFMT == unix.S_IFREG && configOwnerOK(st, uid) && (st.Mode&0o022 == 0 || readOnly())
}

// configDirOK is configFileOK's rule for a directory on the walk, where the
// sticky bit also keeps others from replacing entries.
func configDirOK(st *unix.Stat_t, uid int, readOnly func() bool) bool {
	return configOwnerOK(st, uid) && (st.Mode&0o022 == 0 || st.Mode&unix.S_ISVTX != 0 || readOnly())
}

// openConfigDir walks a canonical path while retaining descriptors for safety checks.
func openConfigDir(path string, create bool) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrUnsafePath
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if path == "/" {
		return os.NewFile(uintptr(fd), path), nil
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(e, unix.ENOENT) && create {
			e = unix.Mkdirat(fd, part, 0o700)
			if e == nil {
				if err := syncCreatedParents(fd); err != nil {
					_ = unix.Close(fd)
					return nil, err
				}
			}
			if e == nil || errors.Is(e, unix.EEXIST) {
				next, e = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			}
		}
		_ = unix.Close(fd)
		if errors.Is(e, unix.ENOENT) {
			return nil, os.ErrNotExist
		}
		if e != nil {
			return nil, ErrUnsafePath
		}
		fd = next
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil || !configDirOK(&st, os.Getuid(), func() bool { return isReadOnlyMount(fd) }) {
			_ = unix.Close(fd)
			return nil, ErrUnsafePath
		}
	}
	return os.NewFile(uintptr(fd), path), nil
}
func syncCreatedParents(fd int) error { return unix.Fsync(fd) }
