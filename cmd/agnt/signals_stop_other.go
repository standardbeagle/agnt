//go:build unix && !linux && !darwin

package main

import "syscall"

// childStopped reports whether pid is currently stopped (Ctrl+Z), without
// reaping it. The remaining BSDs are documented to accept WNOWAIT on wait4(2),
// which keeps the child waitable so the exit path's c.Wait() still reaps it.
// This path is not executed by any CI job. Linux rejects the flag and uses
// waitid(2) (signals_stop_linux.go); macOS ignores it and uses the process
// state from sysctl (signals_stop_darwin.go).
func childStopped(pid int) bool {
	var ws syscall.WaitStatus
	got, err := syscall.Wait4(pid, &ws, syscall.WUNTRACED|syscall.WNOHANG|syscall.WNOWAIT, nil)
	if err != nil || got <= 0 {
		return false
	}
	return ws.Stopped()
}
