package runtime

import (
	"net"

	"golang.org/x/sys/unix"

	"github.com/dedene/mcparcel/internal/config"
)

func CheckPeer(conn *net.UnixConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return config.ErrUnsafePath
	}
	var check error
	err = raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
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
