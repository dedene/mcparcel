package runtime

import (
	"os"

	"github.com/dedene/mcparcel/internal/config"
)

func peerUID(uid uint32) error {
	if uid != uint32(os.Getuid()) {
		return config.ErrUnsafePath
	}
	return nil
}
