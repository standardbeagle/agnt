//go:build windows

package protocol

import "github.com/standardbeagle/go-cli-server/socket"

// DefaultSocketPath returns the default socket path for agnt. The
// environment overrides (socketPathOverride) win over the platform default.
func DefaultSocketPath() string {
	if p := socketPathOverride(); p != "" {
		return p
	}
	return socket.DefaultSocketPath(SocketName)
}

// LegacySocketPath returns "" on Windows: the pre-0.13.32 legacy socket
// layout was a Unix /tmp construct and never existed on Windows, so there is
// nothing to migrate.
func LegacySocketPath() string {
	return ""
}
