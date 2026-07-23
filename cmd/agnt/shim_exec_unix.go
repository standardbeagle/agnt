//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

// execRealBinary replaces the shim process with the real command. argv[0]
// is the invoked name so binaries that inspect it (busybox-style) behave.
func execRealBinary(path, command string, args []string) int {
	argv := append([]string{command}, args...)
	if err := syscall.Exec(path, argv, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "agnt shim: exec %s: %v\n", path, err)
		return 126
	}
	return 0 // unreachable
}
