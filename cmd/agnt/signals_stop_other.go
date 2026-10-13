//go:build unix && !linux

package main

import "syscall"

// childStopped reports whether pid is currently stopped (Ctrl+Z), without
// reaping it. The BSD family (darwin included) accepts WNOWAIT on wait4(2);
// Linux does not and uses waitid(2) instead — see signals_stop_linux.go.
// WNOWAIT keeps the child waitable so the exit path's c.Wait() still reaps it.
func childStopped(pid int) bool {
	var ws syscall.WaitStatus
	got, err := syscall.Wait4(pid, &ws, syscall.WUNTRACED|syscall.WNOHANG|syscall.WNOWAIT, nil)
	if err != nil || got <= 0 {
		return false
	}
	return ws.Stopped()
}
