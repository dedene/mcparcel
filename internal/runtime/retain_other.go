//go:build !darwin

package runtime

import (
	"errors"
	"os"
)

// cloneFile is unsupported here: retainExecutable falls back to a byte copy.
func cloneFile(*os.File, int, string) error { return errors.ErrUnsupported }
