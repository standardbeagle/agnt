//go:build darwin

package platform

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	procPIDInfo     = 2
	procPIDTBSDInfo = 3
)

// procBSDInfo mirrors Darwin's proc_bsdinfo through the process start fields.
type procBSDInfo struct {
	Flags, Status, Xstatus, PID, PPID        uint32
	UID, GID, RUID, RGID, SVUID, SVGID, RFU1 uint32
	Comm                                     [16]byte
	Name                                     [32]byte
	Nfiles, PGID, Pjobc, Tdev, Tpgid         uint32
	Nice                                     int32
	StartTVSec, StartTVUsec                  uint64
}

// ProcessBirthID returns Darwin's kernel-recorded process start timeval.
func ProcessBirthID(pid int) (string, bool) {
	if pid <= 1 {
		return "", false
	}
	var info procBSDInfo
	size := unsafe.Sizeof(info)
	n, _, errno := unix.Syscall6(unix.SYS_PROC_INFO, procPIDInfo, uintptr(pid), procPIDTBSDInfo,
		0, uintptr(unsafe.Pointer(&info)), size)
	if errno != 0 || n < size || (info.StartTVSec == 0 && info.StartTVUsec == 0) {
		return "", false
	}
	return fmt.Sprintf("%d.%06d", info.StartTVSec, info.StartTVUsec), true
}
