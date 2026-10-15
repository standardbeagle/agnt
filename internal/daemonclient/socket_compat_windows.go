//go:build windows

package daemonclient

import (
	"net"
	"os"
	"unsafe"

	"github.com/standardbeagle/go-cli-server/socket"
	"golang.org/x/sys/windows"

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

// DefaultSocketPath returns the default socket path for agnt.
// AGNT_SOCKET, when set, overrides the default (see the unix variant for the
// rationale — isolated/test daemons, second-daemon escape hatch).
func DefaultSocketPath() string {
	return protocol.DefaultSocketPath()
}

// migrateLegacySocket lives in internal/daemon (socket_migrate_windows.go);
// the daemon-side stub belongs to the daemon package, not the client.

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
// This is a no-op on Windows.
func CleanupZombieDaemons(socketPath string) int {
	return socket.CleanupZombieDaemons(socketPath, agntDaemonMatcher(socketPath))
}

// readProcessIdentity reads a process's image path and command line. The
// command line comes from NtQueryInformationProcess(ProcessCommandLineInformation),
// which needs only PROCESS_QUERY_LIMITED_INFORMATION (Windows 8.1+). Any failure
// reports !ok, so the matcher declines rather than guessing.
func readProcessIdentity(pid int) (exe string, argv []string, ok bool) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", nil, false
	}
	defer windows.CloseHandle(handle)

	var exePath [windows.MAX_PATH]uint16
	size := uint32(len(exePath))
	if err := windows.QueryFullProcessImageName(handle, 0, &exePath[0], &size); err != nil {
		return "", nil, false
	}
	exe = windows.UTF16ToString(exePath[:size])

	var need uint32
	_ = windows.NtQueryInformationProcess(handle, windows.ProcessCommandLineInformation, nil, 0, &need)
	if need < uint32(unsafe.Sizeof(windows.NTUnicodeString{})) || need > 1<<20 {
		return "", nil, false
	}
	buf := make([]byte, need)
	if err := windows.NtQueryInformationProcess(handle, windows.ProcessCommandLineInformation,
		unsafe.Pointer(&buf[0]), need, &need); err != nil {
		return "", nil, false
	}
	cmdline := (*windows.NTUnicodeString)(unsafe.Pointer(&buf[0])).String()
	argv, err = windows.DecomposeCommandLine(cmdline)
	if err != nil || len(argv) == 0 {
		return "", nil, false
	}
	return exe, argv, true
}
