package config

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

type (
	writeTarget struct {
		Dir      *os.File
		Name     string
		Mode     os.FileMode
		Original *unix.Stat_t
		Absent   bool
	}
	atomicHooks struct {
		BeforeRename func() error
		AfterRename  func() error
	}
)

var ErrDurability = errors.New("configuration write durability uncertain")

// canonicalConfigPath resolves existing ancestors without inventing missing components.
func canonicalConfigPath(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", ErrUnsafePath
	}
	var missing []string
	for {
		resolved, e := filepath.EvalSymlinks(path)
		if e == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		}
		if !errors.Is(e, os.ErrNotExist) {
			return "", ErrUnsafePath
		}
		if info, e := os.Lstat(path); e == nil {
			// A cooperating writer may have created the directory since EvalSymlinks.
			if info.IsDir() {
				continue
			}
			return "", ErrUnsafePath
		} else if !errors.Is(e, os.ErrNotExist) {
			return "", ErrUnsafePath
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", ErrUnsafePath
		}
		missing = append(missing, filepath.Base(path))
		path = parent
	}
}

func writableStat(st *unix.Stat_t) error {
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Getuid()) || st.Mode&0o22 != 0 || st.Nlink != 1 {
		return ErrUnsafePath
	}
	return nil
}

func openWriteTarget(path string) (writeTarget, error) {
	file, err := OpenConfigFile(path)
	absent := errors.Is(err, os.ErrNotExist)
	if err != nil && !absent {
		return writeTarget{}, err
	}
	var original *unix.Stat_t
	mode := os.FileMode(0o600)
	if !absent {
		defer file.Close()
		path = file.Name()
		var st unix.Stat_t
		if err := unix.Fstat(int(file.Fd()), &st); err != nil {
			return writeTarget{}, err
		}
		if err := writableStat(&st); err != nil {
			return writeTarget{}, err
		}
		original = &st
		mode = os.FileMode(st.Mode & 0o777)
	} else {
		if _, err := os.Lstat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
			return writeTarget{}, ErrUnsafePath
		}
		parent, err := canonicalConfigPath(filepath.Dir(path))
		if err != nil {
			return writeTarget{}, err
		}
		path = filepath.Join(parent, filepath.Base(path))
	}
	name := filepath.Base(path)
	if name == "." || name == ".." || name == "/" {
		return writeTarget{}, ErrUnsafePath
	}
	dir, err := openConfigDir(filepath.Dir(path), absent)
	if err != nil {
		return writeTarget{}, err
	}
	target := writeTarget{Dir: dir, Name: name, Mode: mode, Original: original, Absent: absent}
	if absent {
		var st unix.Stat_t
		if unix.Fstat(int(dir.Fd()), &st) != nil || st.Uid != uint32(os.Getuid()) {
			_ = dir.Close()
			return writeTarget{}, ErrUnsafePath
		}
	}
	if err := recheckWriteTarget(target); err != nil {
		_ = dir.Close()
		return writeTarget{}, err
	}
	return target, nil
}

func recheckWriteTarget(target writeTarget) error {
	var parent unix.Stat_t
	if unix.Fstat(int(target.Dir.Fd()), &parent) != nil || (parent.Uid != uint32(os.Getuid()) && parent.Uid != 0) || (parent.Mode&0o022 != 0 && parent.Mode&unix.S_ISVTX == 0) || (target.Absent && parent.Uid != uint32(os.Getuid())) {
		return ErrUnsafePath
	}
	var st unix.Stat_t
	err := unix.Fstatat(int(target.Dir.Fd()), target.Name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		if target.Absent {
			return nil
		}
		return ErrConfigConflict
	}
	if err != nil {
		return err
	}
	if err := writableStat(&st); err != nil {
		return err
	}
	if target.Absent || st.Dev != target.Original.Dev || st.Ino != target.Original.Ino {
		return ErrConfigConflict
	}
	return nil
}

func atomicReplace(ctx context.Context, path string, data []byte, hooks atomicHooks) (renamed bool, err error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	target, err := openWriteTarget(path)
	if err != nil {
		return false, err
	}
	defer target.Dir.Close()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return false, err
	}
	temp := ".mcparcel-" + hex.EncodeToString(nonce[:]) + ".tmp"
	dirfd := int(target.Dir.Fd())
	fd, err := unix.Openat(dirfd, temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return false, err
	}
	file := os.NewFile(uintptr(fd), temp)
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
		if !renamed {
			_ = unix.Unlinkat(dirfd, temp, 0)
		}
	}()
	if err := file.Chmod(target.Mode); err != nil {
		return false, err
	}
	remaining := data
	for len(remaining) > 0 {
		n, err := file.Write(remaining)
		if err != nil {
			return false, err
		}
		if n <= 0 {
			return false, io.ErrShortWrite
		}
		remaining = remaining[n:]
	}
	if err := file.Sync(); err != nil {
		return false, err
	}
	err = file.Close()
	closed = true
	if err != nil {
		return false, err
	}
	if hooks.BeforeRename != nil {
		if err := hooks.BeforeRename(); err != nil {
			return false, err
		}
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := recheckWriteTarget(target); err != nil {
		return false, err
	}
	if err := unix.Renameat(dirfd, temp, dirfd, target.Name); err != nil {
		return false, err
	}
	renamed = true
	if hooks.AfterRename != nil {
		if err := hooks.AfterRename(); err != nil {
			return true, errors.Join(ErrDurability, err)
		}
	}
	if err := target.Dir.Sync(); err != nil {
		return true, errors.Join(ErrDurability, err)
	}
	return true, nil
}
