//go:build windows

package daemon

import (
	"fmt"
	"os"
)

// DefaultSocketPath returns the default socket path for Windows.
// On Windows, we use named pipes instead of Unix sockets.
func DefaultSocketPath() string {
	username := os.Getenv("USERNAME")
	if username == "" {
		username = "default"
	}
	return fmt.Sprintf(`\\.\pipe\devtool-mcp-%s`, username)
}

// Note: The Listen and Connect functions in socket.go will work
// on Windows because Go's net package supports named pipes via
// the "unix" network type on Windows since Go 1.12.
//
// For full Windows named pipe support with security descriptors,
// we would need to use golang.org/x/sys/windows.CreateNamedPipe.
// This is deferred to Task 010.
