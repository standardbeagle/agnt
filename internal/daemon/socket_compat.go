//go:build !windows

// Package daemon provides the stateful daemon process that manages
// processes, proxies, and traffic logs across client connections.
//
// This file provides socket compatibility layer using go-cli-server/socket.
package daemon

import (
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/standardbeagle/go-cli-server/socket"
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
