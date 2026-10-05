package runtime

import (
	"net"
	"os"

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

func peerUID(uid uint32) error {
	if uid != uint32(os.Getuid()) {
		return config.ErrUnsafePath
	}
	return nil
}
