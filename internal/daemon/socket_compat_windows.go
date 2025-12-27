//go:build windows

// Package daemon provides the stateful daemon process that manages
// processes, proxies, and traffic logs across client connections.
//
// This file provides socket compatibility layer using go-cli-server/socket.
package daemon

import (
	"net"
	"os"
	"strings"

	"github.com/standardbeagle/go-cli-server/socket"
	"golang.org/x/sys/windows"
)

// SocketName is the socket name used for agnt daemon.
const SocketName = "devtool-mcp"

// Re-export error types from go-cli-server/socket for backward compatibility.
var (
	ErrSocketInUse    = socket.ErrSocketInUse
	ErrSocketNotFound = socket.ErrSocketNotFound
	ErrDaemonRunning  = socket.ErrDaemonRunning
)

// SocketConfig holds configuration for socket management.
// This is a compatibility wrapper around socket.Config.
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
func DefaultSocketPath() string {
	return socket.DefaultSocketPath(SocketName)
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
// This is a no-op on Windows.
func CleanupZombieDaemons(socketPath string) int {
	return socket.CleanupZombieDaemons(socketPath, isAgntDaemonProcess)
}

// isAgntDaemonProcess checks if the process with the given PID is an agnt daemon.
// This is used as the ProcessMatcher callback for socket.Manager.
func isAgntDaemonProcess(pid int) bool {
	// On Windows, we use the Windows API to get process information
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)

	// Get the executable path
	var exePath [windows.MAX_PATH]uint16
	size := uint32(len(exePath))
	err = windows.QueryFullProcessImageName(handle, 0, &exePath[0], &size)
	if err != nil {
		return false
	}

	path := windows.UTF16ToString(exePath[:size])
	lowerPath := strings.ToLower(path)

	// Check if it's one of our daemon processes
	return strings.Contains(lowerPath, "agnt") ||
		strings.Contains(lowerPath, "devtool-mcp") ||
		strings.Contains(lowerPath, "agnt-daemon")
}
