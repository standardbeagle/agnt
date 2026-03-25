//go:build !windows

package daemon

import "syscall"

// pidAlive checks if a process is alive using signal 0.
func pidAlive(pid int) bool {
	return syscall.Kill(pid, syscall.Signal(0)) == nil
}
