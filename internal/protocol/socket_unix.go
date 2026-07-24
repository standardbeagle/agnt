//go:build !windows

package protocol

import (
	"fmt"
	"os"
)

// DefaultSocketPath returns the default socket path for agnt.
//
// Override precedence (first match wins):
//  1. AGNT_SOCKET — the original override. Pins the daemon to an isolated
//     socket (tests use this to avoid touching the host's real daemon, and
//     it's a useful escape hatch for running a second daemon).
//  2. AGNT_DAEMON_SOCKET — a second, equally-valid override recognized by
//     every CLI socket-resolution path (agnt monitor, agnt doctor, the MCP
//     daemon client). It exists so `agnt ssh` can point local tooling at a
//     forwarded remote daemon socket (see internal/sshclient/forward.go)
//     without disturbing AGNT_SOCKET, which some scripts already set for
//     other reasons. If both are set, AGNT_SOCKET wins, since it is the
//     older and more specific name.
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
	if p := os.Getenv("AGNT_SOCKET"); p != "" {
		return p
	}
	if p := os.Getenv("AGNT_DAEMON_SOCKET"); p != "" {
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
