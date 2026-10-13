//go:build darwin

package platform

import "golang.org/x/sys/unix"

// ProcessName returns the short command name of pid (the kernel's p_comm, at
// most 16 bytes; "node", "Google Chrome He"), or "" when the process does not
// exist. It is one sysctl read: no exec, no dependence on the caller being
// allowed to read the target's argv, so it works for other users' processes,
// which is what a port-conflict report needs.
//
// macOS has no /proc, so the Linux lookup (/proc/<pid>/comm) answers "" for
// every PID here; port-conflict reports and `doctor` showed bare PIDs.
func ProcessName(pid int) string {
	if pid <= 0 {
		return ""
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp == nil || int(kp.Proc.P_pid) != pid {
		return ""
	}
	return darwinComm(kp.Proc.P_comm[:])
}

// darwinSZOMB is p_stat for a process that has exited and awaits its parent's
// wait() (SZOMB in <sys/proc.h>).
const darwinSZOMB = 5

// pidIsZombie reports whether pid has exited but not been reaped. A pure
// sysctl read; false when the process does not exist or cannot be read.
func pidIsZombie(pid int) bool {
	if pid <= 0 {
		return false
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp == nil || int(kp.Proc.P_pid) != pid {
		return false
	}
	return kp.Proc.P_stat == darwinSZOMB
}
