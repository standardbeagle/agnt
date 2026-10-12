//go:build !windows

package protocol

import (
	"fmt"
	"os"
)

// DefaultSocketPath returns the default socket path for agnt.
//
// The environment overrides (socketPathOverride) win over the default.
//
// Otherwise: deliberately ignores XDG_RUNTIME_DIR: agnt daemon is a long-running
// background service that outlives login sessions. XDG_RUNTIME_DIR is
// cleaned up by pam_systemd on logout, which would silently delete our
// socket mid-session. The /tmp/<name>-<uid> form persists across sessions
// and is deterministic regardless of shell environment.
//
// The socket is nested in a per-uid subdirectory (/tmp/<name>-<uid>/<name>.sock)
// rather than placed directly in /tmp. The hardened bind (socket.secureSocketDir)
// requires the socket's parent directory to be uid-owned and 0700; /tmp itself
// is root-owned on every normal system, so the old bare /tmp/<name>-<uid>.sock
// form is rejected at bind time ("socket directory /tmp is owned by uid 0") and
// the daemon never starts. The subdirectory is created 0700/uid-owned by the
// bind and still persists across login sessions (unlike XDG_RUNTIME_DIR). This
// matches go-cli-server's own non-XDG default form.
func DefaultSocketPath() string {
	if p := socketPathOverride(); p != "" {
		return p
	}
	return fmt.Sprintf("/tmp/%s-%d/%s.sock", SocketName, os.Getuid(), SocketName)
}

// LegacySocketPath is the pre-0.13.32 default: the socket sat directly in /tmp
// rather than in a per-uid subdirectory. A daemon from an older binary is still
// listening there after an in-place upgrade, invisible to the new client, and
// still holding whatever processes and ports it managed. See migrateLegacySocket.
func LegacySocketPath() string {
	return fmt.Sprintf("/tmp/%s-%d.sock", SocketName, os.Getuid())
}
