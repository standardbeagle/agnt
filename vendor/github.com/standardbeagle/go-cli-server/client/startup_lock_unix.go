//go:build unix

package client

import (
	"errors"
	"os"
	"syscall"
)

func lockStartupFile(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return errStartupLockHeld
		}
		return err
	}
	return nil
}

func unlockStartupFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
