//go:build darwin || linux

package config

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// MaxTokenFileBytes caps a service-account token file.
const MaxTokenFileBytes = 16 * 1024

// ErrTokenEmpty is CheckTokenFile's answer for a safe but empty token file.
var ErrTokenEmpty = errors.New("token file is empty")

// OpenTokenFile opens a service-account profile's tokenFile for reading. It
// takes OpenConfigFile's walk (symbolic links resolved once, so a Kubernetes
// Secret volume's ..data layout works) and tokenFileOK's stricter file rule.
// An absent path or a dangling link is os.ErrNotExist, a path the user may not
// read os.ErrPermission, anything unsafe ErrUnsafePath.
func OpenTokenFile(path string) (*os.File, error) {
	f, err := openResolvedFile(path, tokenFileOK)
	switch {
	case errors.Is(err, errDanglingLink):
		return nil, os.ErrNotExist
	case errors.Is(err, errNoAccess):
		return nil, os.ErrPermission
	}
	return f, err
}

// CheckTokenFile is OpenTokenFile without the read: it opens, checks the
// file's size and closes, so doctor never sees the token. An empty file is
// ErrTokenEmpty.
func CheckTokenFile(path string) error {
	f, err := OpenTokenFile(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ErrUnsafePath
	}
	if info.Size() == 0 {
		return ErrTokenEmpty
	}
	return nil
}

// tokenFileOK accepts a regular file owned by uid or root that others can
// neither read nor write, on any mount: a read-only mount proves integrity,
// not confidentiality. Group bits are accepted only on a read-only mount (a
// Kubernetes Secret with defaultMode 0440 and fsGroup).
func tokenFileOK(st *unix.Stat_t, uid int, readOnly func() bool) bool {
	return st.Mode&unix.S_IFMT == unix.S_IFREG && configOwnerOK(st, uid) && st.Mode&0o007 == 0 && (st.Mode&0o060 == 0 || readOnly())
}
