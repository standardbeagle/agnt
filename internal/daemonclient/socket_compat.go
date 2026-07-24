//go:build !windows

package daemonclient

import (
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/standardbeagle/go-cli-server/socket"
)

// SocketName is the socket name used for agnt daemon.
const SocketName = "devtool-mcp"

// Socket errors re-exported from go-cli-server/socket for convenience.
// These are used by cmd/agnt when checking daemon status before startup.
var (
	ErrSocketInUse    = socket.ErrSocketInUse
	ErrSocketNotFound = socket.ErrSocketNotFound
	ErrDaemonRunning  = socket.ErrDaemonRunning
)

// SocketConfig holds configuration for socket management.
// Wraps go-cli-server/socket.Config with agnt-specific defaults (socket name, process matcher).
type SocketConfig struct {
	Path string
	Mode os.FileMode
}

// DefaultSocketConfig returns the default socket configuration.
func DefaultSocketConfig() SocketConfig {
	return SocketConfig{
		Path: DefaultSocketPath(),
		Mode: 0600,
	}
}

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

// SocketManager handles socket lifecycle.
// This is a compatibility wrapper around socket.Manager.
type SocketManager struct {
	manager *socket.Manager
}

// NewSocketManager creates a new socket manager.
func NewSocketManager(config SocketConfig) *SocketManager {
	path := config.Path
	if path == "" {
		path = DefaultSocketPath()
	}

	sockConfig := socket.Config{
		Path:           path,
		Mode:           config.Mode,
		Name:           SocketName,
		ProcessMatcher: isAgntDaemonProcess,
	}

	return &SocketManager{
		manager: socket.NewManager(sockConfig),
	}
}

// Listen creates and binds the socket.
func (sm *SocketManager) Listen() (net.Listener, error) {
	return sm.manager.Listen()
}

// Close closes the socket and removes files.
func (sm *SocketManager) Close() error {
	return sm.manager.Close()
}

// Path returns the socket path.
func (sm *SocketManager) Path() string {
	return sm.manager.Path()
}

// Connect attempts to connect to an existing daemon socket.
func Connect(path string) (net.Conn, error) {
	if path == "" {
		path = DefaultSocketPath()
	}
	return socket.Connect(path)
}

// IsRunning checks if a daemon is running at the given socket path.
func IsRunning(path string) bool {
	if path == "" {
		path = DefaultSocketPath()
	}
	return socket.IsRunning(path)
}

// CleanupZombieDaemons finds and kills zombie daemon processes.
func CleanupZombieDaemons(socketPath string) int {
	return socket.CleanupZombieDaemons(socketPath, isAgntDaemonProcess)
}

// isAgntDaemonProcess checks if the process with the given PID is an agnt daemon.
// This is used as the ProcessMatcher callback for socket.Manager.
func isAgntDaemonProcess(pid int) bool {
	// Read the process cmdline from /proc
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}

	// cmdline is null-separated, convert to string
	cmd := string(cmdline)

	// Check if it's one of our daemon processes
	// Look for "daemon" and "start" or "agnt" or "devtool-mcp" in cmdline
	return (strings.Contains(cmd, "daemon") && strings.Contains(cmd, "start")) ||
		strings.Contains(cmd, "agnt") ||
		strings.Contains(cmd, "devtool-mcp")
}
