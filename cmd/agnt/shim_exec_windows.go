//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
)

// execRealBinary runs the real command as a child and proxies stdio and
// the exit code (Windows has no execve).
func execRealBinary(path, command string, args []string) int {
	c := exec.Command(path, args...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "agnt shim: exec %s: %v\n", path, err)
		return 126
	}
	return 0
}
