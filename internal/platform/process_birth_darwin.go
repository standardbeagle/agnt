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
	info, ok := readBSDInfo(pid)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%d.%06d", info.StartTVSec, info.StartTVUsec), true
}

// processLineage returns pid's parent PID and birth identity from one
// proc_pidinfo call, so both describe the same process. A zombie reports
// false: it has exited and cannot be killed.
func processLineage(pid int) (ppid int, birth string, ok bool) {
	info, ok := readBSDInfo(pid)
	if !ok || info.Status == darwinSZOMB {
		return 0, "", false
	}
	return int(info.PPID), fmt.Sprintf("%d.%06d", info.StartTVSec, info.StartTVUsec), true
}

func readBSDInfo(pid int) (procBSDInfo, bool) {
	var info procBSDInfo
	if pid <= 1 {
		return info, false
	}
	size := unsafe.Sizeof(info)
	n, _, errno := unix.Syscall6(unix.SYS_PROC_INFO, procPIDInfo, uintptr(pid), procPIDTBSDInfo,
		0, uintptr(unsafe.Pointer(&info)), size)
	if errno != 0 || n < size || (info.StartTVSec == 0 && info.StartTVUsec == 0) {
		return info, false
	}
	return info, true
}
