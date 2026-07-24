//go:build windows

package protocol

import (
	"os"

	"github.com/standardbeagle/go-cli-server/socket"
)

// DefaultSocketPath returns the default socket path for agnt.
// AGNT_SOCKET, when set, overrides the default (see the unix variant for the
// rationale — isolated/test daemons, second-daemon escape hatch).
func DefaultSocketPath() string {
	if p := os.Getenv("AGNT_SOCKET"); p != "" {
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
