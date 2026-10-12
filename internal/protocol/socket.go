package protocol

import "os"

// SocketName is the socket name used for the agnt daemon.
const SocketName = "devtool-mcp"

// socketPathOverride returns the daemon socket path forced by the
// environment, or "" when none is set. It is the single definition of the
// override chain for every platform — the per-OS DefaultSocketPath variants
// differ only in the default they fall back to.
//
// Precedence (first match wins):
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
func socketPathOverride() string {
	if p := os.Getenv("AGNT_SOCKET"); p != "" {
		return p
	}
	return os.Getenv("AGNT_DAEMON_SOCKET")
}
