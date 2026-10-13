//go:build !windows

package main

import (
	"os"
	"syscall"
)

// tryLockFile takes a non-blocking exclusive advisory lock on f. The lock
// belongs to the open file description, so the kernel drops it when the process
// exits for any reason — there is no stale lock to clean up after a crash.
func tryLockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
