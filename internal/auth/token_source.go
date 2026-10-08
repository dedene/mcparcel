package auth

import (
	"bytes"
	"errors"
	"io"
	"strings"

	"github.com/dedene/mcparcel/internal/config"
)

// openTokenFile opens a token file under the config package's rules; tests
// replace it.
var openTokenFile = config.OpenTokenFile

// ServiceAccountToken returns the token of a service-account profile, read
// from env[p.TokenEnv] or from p.TokenFile. The file is read again on every
// call, so a rotated file needs no restart. Errors are the bare sentinels
// ErrTokenUnavailable and ErrTokenUnsafe; they never carry the value or the
// file's content.
func ServiceAccountToken(p config.Profile, env map[string]string) (string, error) {
	switch {
	case p.TokenEnv != "":
		// Surrounding whitespace is dropped, as for a file: a Kubernetes
		// Secret made from a file often ends in a newline.
		token := strings.TrimSpace(env[p.TokenEnv])
		if token == "" {
			return "", ErrTokenUnavailable
		}
		return token, nil
	case p.TokenFile != "":
		return readTokenFile(p.TokenFile)
	}
	return "", ErrTokenUnavailable
}

func readTokenFile(path string) (string, error) {
	f, err := openTokenFile(path)
	if errors.Is(err, config.ErrUnsafePath) {
		return "", ErrTokenUnsafe
	}
	if err != nil {
		// os.ErrNotExist, os.ErrPermission and anything else unexpected.
		return "", ErrTokenUnavailable
	}
	// One fixed buffer, cleared afterwards, so no copy of the token is left
	// behind by a growing read.
	buf := make([]byte, config.MaxTokenFileBytes+1)
	defer clear(buf)
	n, err := io.ReadFull(f, buf)
	_ = f.Close()
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) || n > config.MaxTokenFileBytes {
		return "", ErrTokenUnavailable
	}
	token := bytes.TrimSpace(buf[:n])
	if len(token) == 0 {
		return "", ErrTokenUnavailable
	}
	for _, b := range token {
		if b < 0x21 || b > 0x7e {
			return "", ErrTokenUnavailable
		}
	}
	return string(token), nil
}
