//go:build darwin

package main

import "golang.org/x/sys/unix"

// procStateStopped is SSTOP from <sys/proc.h>: the process is stopped by a
// signal. x/sys/unix does not export the S* process states.
const procStateStopped = 4

// childStopped reports whether pid is currently stopped (Ctrl+Z), without
// reaping it.
//
// macOS has no usable wait-family peek for this. wait4(2) there does not
// honour WNOWAIT: on a real Mac it reaped an exited child (so the exit path's
// c.Wait() failed with "no child processes") and never reported a stopped
// one. The process state from sysctl is a pure read: it cannot consume the
// child's status, and a zombie reports SZOMB, not SSTOP, so an exited child is
// left for the main path to reap.
func childStopped(pid int) bool {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return false
	}
	return kp.Proc.P_stat == procStateStopped
}
