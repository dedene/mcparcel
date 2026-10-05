package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func OpenPrivateDir(path string, create bool) (*os.File, error) {
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
		if errors.Is(e, unix.ENOENT) && create {
			e = unix.Mkdirat(fd, part, 0o700)
			if e == nil {
				if syncErr := syncCreatedParents(fd); syncErr != nil {
					_ = unix.Close(fd)
					return nil, syncErr
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
		if unix.Fstat(fd, &st) != nil {
			_ = unix.Close(fd)
			return nil, ErrUnsafePath
		}
		owner := st.Uid == uint32(os.Getuid())
		safeAncestor := (owner || st.Uid == 0) && (st.Mode&0o022 == 0 || st.Mode&unix.S_ISVTX != 0)
		if !safeAncestor || (last && (!owner || st.Mode&0o7777 != 0o700)) {
			_ = unix.Close(fd)
			return nil, ErrUnsafePath
		}
	}
	return os.NewFile(uintptr(fd), path), nil
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

func OpenConfigFile(path string) (*os.File, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, ErrUnsafePath
	}
	resolved, err := filepath.EvalSymlinks(path)
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
	if e != nil {
		return nil, ErrUnsafePath
	}
	var st unix.Stat_t
	if unix.Fstat(next, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Getuid()) || st.Mode&0o022 != 0 {
		_ = unix.Close(next)
		return nil, ErrUnsafePath
	}
	return os.NewFile(uintptr(next), resolved), nil
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
		if unix.Fstat(fd, &st) != nil || (st.Uid != uint32(os.Getuid()) && st.Uid != 0) || (st.Mode&0o22 != 0 && st.Mode&unix.S_ISVTX == 0) {
			_ = unix.Close(fd)
			return nil, ErrUnsafePath
		}
	}
	return os.NewFile(uintptr(fd), path), nil
}
func syncCreatedParents(fd int) error { return unix.Fsync(fd) }
