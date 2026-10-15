//go:build !windows

package daemonclient

import (
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/standardbeagle/go-cli-server/socket"

	"github.com/standardbeagle/agnt/internal/protocol"
)

// SocketName is the socket name used for agnt daemon.
const SocketName = protocol.SocketName

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

// DefaultSocketPath returns the default socket path for agnt. Canonical
// home: internal/protocol (the daemon needs it too and must not import its
// own client package); see there for the override-precedence docs.
func DefaultSocketPath() string {
	return protocol.DefaultSocketPath()
}

// LegacySocketPath is the pre-0.13.32 default: the socket sat directly in /tmp
// rather than in a per-uid subdirectory. A daemon from an older binary is still
// listening there after an in-place upgrade, invisible to the new client, and
// still holding whatever processes and ports it managed. See migrateLegacySocket.
func LegacySocketPath() string {
	return protocol.LegacySocketPath()
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
		ProcessMatcher: agntDaemonMatcher(path),
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
	return socket.CleanupZombieDaemons(socketPath, agntDaemonMatcher(socketPath))
}

// readProcessIdentity reads a process's argv from /proc. The executable is
// argv[0] (the daemon is always spawned by absolute path); /proc/<pid>/exe is
// not used because an upgraded binary reads back as "<path> (deleted)".
func readProcessIdentity(pid int) (exe string, argv []string, ok bool) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || len(raw) == 0 {
		return "", nil, false
	}
	argv = strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	return argv[0], argv, true
}
