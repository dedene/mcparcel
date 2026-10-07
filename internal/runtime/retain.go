//go:build darwin || linux

package runtime

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"golang.org/x/sys/unix"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/lockfile"
)

// A desktop runtime runs from a retained copy of the CLI binary, so npm
// rewriting or removing its cache never touches the file a daemon executes.

var retainedVersion = regexp.MustCompile(`^[0-9A-Za-z._+-]{1,128}$`)

var errRetainVersion = errors.New("version cannot name a retained binary")

// RetainedPath is <DataDir>/runtime/<version>/mcparcel. version must match
// ^[0-9A-Za-z._+-]{1,128}$ and not be "." or "..".
func RetainedPath(paths config.Paths, version string) (string, error) {
	if !retainedVersion.MatchString(version) || version == "." || version == ".." {
		return "", errRetainVersion
	}
	return filepath.Join(paths.DataDir, "runtime", version, "mcparcel"), nil
}

// retainHook is the copy startDaemon makes under the daemon lock, or nil
// when this client does not retain (headless mode, or Retain off).
func (c *Client) retainHook() func(context.Context, string) (string, func(), error) {
	if !c.Retain || c.Paths.Headless() {
		return nil
	}
	return func(ctx context.Context, exe string) (string, func(), error) {
		return retainExecutable(ctx, c.Paths, c.Version, exe)
	}
}

// errRetainUnwritable is a data directory MCParcel cannot write (read-only,
// full, not writable by the user): the daemon starts from the CLI binary.
var errRetainUnwritable = errors.New("data directory cannot be written")

// retainExecutable makes sure an identical copy of exe (same size and SHA-256)
// exists at RetainedPath and returns that path, with release, which the
// caller calls once it has started the copy. Until then it holds
// <DataDir>/runtime/retain.lock, which every CLI sharing the data directory
// takes to copy, prune or start a retained binary, whatever its runtime
// directory. exe already at that path is not copied. The copy goes to a
// random ".mcparcel-<hex>.tmp" in the private 0700 version directory (a
// copy-on-write clone where the platform has one, else a byte copy), is
// checked and fsynced, renamed over the target, and the directory is
// fsynced. A rename never touches the inode a running daemon executes. After
// a new copy it prunes other version directories except the most recently
// modified one, the rollback target.
func retainExecutable(ctx context.Context, paths config.Paths, version, exe string) (string, func(), error) {
	target, err := RetainedPath(paths, version)
	if err != nil {
		return "", nil, err
	}
	release, err := lockRetained(ctx, paths)
	if err != nil {
		return "", nil, err
	}
	if err = copyExecutable(paths, version, exe, target); err != nil {
		release()
		return "", nil, err
	}
	return target, release, nil
}

// lockRetained takes retain.lock, waiting for another holder until ctx ends.
func lockRetained(ctx context.Context, paths config.Paths) (func(), error) {
	root, err := openRetainDir(paths, filepath.Join(paths.DataDir, "runtime"))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	lock, err := lockfile.Open(ctx, true, func() (*os.File, error) {
		return config.OpenPrivateFile(root, "retain.lock", true)
	})
	if errors.Is(err, config.ErrNotCreated) {
		return nil, errRetainUnwritable
	}
	if err != nil {
		return nil, err
	}
	for {
		err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { _ = lock.Close() }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			_ = lock.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = lock.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// openRetainDir opens (creating) a private directory for retained copies. A
// directory that cannot be created is errRetainUnwritable, not unsafe; so is
// a retain.lock that cannot be created (lockRetained).
func openRetainDir(paths config.Paths, dir string) (*os.File, error) {
	f, err := config.OpenPrivateDirUnder(paths.StateRoot, dir, true)
	if errors.Is(err, config.ErrNotCreated) {
		return nil, errRetainUnwritable
	}
	return f, err
}

// copyExecutable is retainExecutable's work under retain.lock.
func copyExecutable(paths config.Paths, version, exe, target string) error {
	if filepath.Clean(exe) == target {
		return nil
	}
	src, err := os.Open(exe)
	if err != nil {
		return err
	}
	defer src.Close()
	sum, err := digest(src)
	if err != nil {
		return err
	}
	dir, err := openRetainDir(paths, filepath.Dir(target))
	if err != nil {
		return err
	}
	defer dir.Close()
	same, err := retainedMatches(int(dir.Fd()), sum)
	if err != nil || same {
		return err
	}
	if err = replaceRetained(int(dir.Fd()), src, sum); err != nil {
		return err
	}
	pruneRetained(paths, version)
	return nil
}

// digest hashes f from its start.
func digest(f *os.File) ([]byte, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// retainedMatches reports whether dir already holds an identical private
// copy. A target that is not a regular file of this user with one link
// (a symlink, say) is unsafe; a different mode or content, or a file this
// user cannot read, is replaced.
func retainedMatches(dir int, sum []byte) (bool, error) {
	fd, err := unix.Openat(dir, "mcparcel", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	switch {
	case errors.Is(err, unix.ENOENT), errors.Is(err, unix.EACCES), errors.Is(err, unix.EPERM):
		return false, nil
	case errors.Is(err, unix.ELOOP):
		return false, config.ErrUnsafePath
	case err != nil:
		return false, err
	}
	f := os.NewFile(uintptr(fd), "mcparcel")
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Getuid()) || st.Nlink != 1 {
		return false, config.ErrUnsafePath
	}
	if st.Mode&0o7777 != 0o700 {
		return false, nil
	}
	got, err := digest(f)
	return err == nil && bytes.Equal(got, sum), err
}

// replaceRetained writes src to a temporary file in dir, checks it against
// sum (src may change while it is copied), and renames it over the target.
func replaceRetained(dir int, src *os.File, sum []byte) (err error) {
	var random [8]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	tmp := ".mcparcel-" + hex.EncodeToString(random[:]) + ".tmp"
	defer func() {
		if err != nil {
			_ = unix.Unlinkat(dir, tmp, 0)
		}
	}()
	f, err := copyRetained(dir, tmp, src)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = unix.Fchmod(int(f.Fd()), 0o700); err != nil {
		return err
	}
	got, err := digest(f)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, sum) {
		return errors.New("executable changed while it was copied")
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = unix.Renameat(dir, tmp, dir, "mcparcel"); err != nil {
		return err
	}
	return unix.Fsync(dir)
}

// copyRetained creates tmp in dir as a copy of src and returns it open for
// reading: a clone where the platform supports one, else a byte copy.
func copyRetained(dir int, tmp string, src *os.File) (*os.File, error) {
	if cloneFile(src, dir, tmp) == nil {
		fd, err := unix.Openat(dir, tmp, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		f := os.NewFile(uintptr(fd), tmp)
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Getuid()) {
			_ = f.Close()
			return nil, config.ErrUnsafePath
		}
		return f, nil
	}
	fd, err := unix.Openat(dir, tmp, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o700)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), tmp)
	if _, err = src.Seek(0, io.SeekStart); err == nil {
		_, err = io.Copy(f, src)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// pruneRetained removes version directories other than keep and the most
// recently modified other one. It acts only on directories owned by this
// user, through the private runtime directory's descriptor, never follows a
// symlink, and ignores errors: a stale copy costs disk space, nothing else.
func pruneRetained(paths config.Paths, keep string) {
	root, err := config.OpenPrivateDirUnder(paths.StateRoot, filepath.Join(paths.DataDir, "runtime"), false)
	if err != nil {
		return
	}
	defer root.Close()
	names, err := root.Readdirnames(-1)
	if err != nil {
		return
	}
	type version struct {
		name  string
		mtime int64
	}
	var others []version
	for _, name := range names {
		var st unix.Stat_t
		if name == keep || unix.Fstatat(int(root.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW) != nil {
			continue
		}
		if st.Mode&unix.S_IFMT == unix.S_IFDIR && st.Uid == uint32(os.Getuid()) {
			others = append(others, version{name, st.Mtim.Nano()})
		}
	}
	slices.SortFunc(others, func(a, b version) int { return cmp.Compare(b.mtime, a.mtime) })
	for _, v := range others[min(1, len(others)):] {
		removeVersionDir(int(root.Fd()), v.name)
	}
}

// removeVersionDir unlinks the files directly in parent/name, then the
// directory. It does not descend further; a directory it cannot empty stays.
func removeVersionDir(parent int, name string) {
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return
	}
	dir := os.NewFile(uintptr(fd), name)
	defer dir.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != uint32(os.Getuid()) {
		return
	}
	entries, err := dir.Readdirnames(-1)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if unix.Fstatat(fd, entry, &st, unix.AT_SYMLINK_NOFOLLOW) == nil && st.Mode&unix.S_IFMT != unix.S_IFDIR {
			_ = unix.Unlinkat(fd, entry, 0)
		}
	}
	_ = unix.Unlinkat(parent, name, unix.AT_REMOVEDIR)
}
