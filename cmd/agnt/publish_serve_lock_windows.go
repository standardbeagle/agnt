//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// tryLockFile takes a non-blocking exclusive lock on the first byte of f. The
// lock is released by the kernel when the handle closes, including on process
// exit, so a crashed serve leaves no stale lock.
func tryLockFile(f *os.File) error {
	var ol windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ol)
}

func unlockFile(f *os.File) error {
	var ol windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ol)
}
