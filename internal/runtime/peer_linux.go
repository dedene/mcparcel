package runtime

import (
	"net"

	"golang.org/x/sys/unix"

	"github.com/dedene/mcparcel/internal/config"
)

// CheckPeer accepts only a peer running as this process's UID (SO_PEERCRED).
func CheckPeer(conn *net.UnixConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return config.ErrUnsafePath
	}
	var check error
	err = raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if e != nil {
			check = config.ErrUnsafePath
		} else {
			check = peerUID(cred.Uid)
		}
	})
	if err != nil {
		return config.ErrUnsafePath
	}
	return check
}
