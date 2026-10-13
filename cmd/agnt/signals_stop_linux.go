//go:build linux

package main

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// cldStopped is si_code CLD_STOPPED: the child was stopped by a signal.
// x/sys/unix does not export the CLD_* codes.
const cldStopped = 5

// childStopped reports whether pid is currently stopped (Ctrl+Z), without
// reaping it. It is a non-destructive peek: WNOWAIT leaves the child waitable
// so the exit path's c.Wait() can still collect its status.
//
// Linux needs waitid(2) for this. wait4(2) rejects WNOWAIT with EINVAL, so the
// earlier wait4-based peek failed on every call and the suspend path never
// ran. Only WSTOPPED is requested (no WEXITED), so a child that has exited is
// simply not reported and stays for the main path to reap.
func childStopped(pid int) bool {
	var info unix.Siginfo
	if err := unix.Waitid(unix.P_PID, pid, &info, unix.WSTOPPED|unix.WNOHANG|unix.WNOWAIT, nil); err != nil {
		return false
	}
	// With WNOHANG and nothing to report, waitid succeeds and leaves the
	// siginfo zeroed.
	return syscall.Signal(info.Signo) == syscall.SIGCHLD && info.Code == cldStopped
}
