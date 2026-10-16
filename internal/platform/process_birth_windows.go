//go:build windows

package platform

import (
	"strconv"

	"golang.org/x/sys/windows"
)

// ProcessBirthID returns the kernel creation time of a running Windows
// process, in 100ns ticks since 1601. Windows recycles PIDs aggressively, so
// a PID recorded earlier (the shim watcher's in the shim manifest) names this
// process only if the creation time still matches.
//
// A process that has exited reports false even while its kernel object, and
// with it the PID, is kept alive by an open handle: it is not running.
func ProcessBirthID(pid int) (string, bool) {
	if pid <= 0 {
		return "", false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return "", false
	}
	defer windows.CloseHandle(h)
	if ev, err := windows.WaitForSingleObject(h, 0); err != nil || ev != uint32(windows.WAIT_TIMEOUT) {
		return "", false // signalled: the process has exited
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return "", false
	}
	ticks := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	if ticks == 0 {
		return "", false
	}
	return strconv.FormatUint(ticks, 10), true
}
