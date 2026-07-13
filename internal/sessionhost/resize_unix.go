//go:build !windows

package sessionhost

import (
	"os"

	"golang.org/x/sys/unix"
)

// setPTYSize uses File.SyscallConn so Close and the ioctl share os.File's fd
// lifetime coordination without an external lock that can impede readLoop.
func setPTYSize(ptmx *os.File, cols, rows int, before func()) error {
	raw, err := ptmx.SyscallConn()
	if err != nil {
		return err
	}
	var ioctlErr error
	err = raw.Control(func(fd uintptr) {
		if before != nil {
			before()
		}
		ioctlErr = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{Col: uint16(cols), Row: uint16(rows)})
	})
	if err != nil {
		return err
	}
	return ioctlErr
}
