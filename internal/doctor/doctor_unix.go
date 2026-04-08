//go:build !windows

package doctor

import "syscall"

// PIDAlive checks if a process is alive using signal 0.
func PIDAlive(pid int) bool {
	return syscall.Kill(pid, syscall.Signal(0)) == nil
}
