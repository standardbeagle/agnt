//go:build windows

// Package daemon provides the stateful daemon process that manages
// processes, proxies, and traffic logs across client connections.
package daemon

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"syscall"
	"time"
)

var (
	// ErrSocketInUse is returned when the socket is already in use.
	ErrSocketInUse = errors.New("socket already in use")
	// ErrSocketNotFound is returned when the socket doesn't exist.
	ErrSocketNotFound = errors.New("socket not found")
	// ErrDaemonRunning is returned when another daemon is already running.
	ErrDaemonRunning = errors.New("daemon already running")
)

// SocketConfig holds configuration for socket management.
type SocketConfig struct {
	// Path is the socket/pipe path. If empty, uses default path.
	Path string
	// Mode is the socket file permissions (not applicable on Windows).
	Mode os.FileMode
}

// DefaultSocketConfig returns the default socket configuration.
func DefaultSocketConfig() SocketConfig {
	return SocketConfig{
		Path: DefaultSocketPath(),
		Mode: 0600,
	}
}

// DefaultSocketPath returns the default socket path for Windows.
// On Windows, we use named pipes instead of Unix sockets.
func DefaultSocketPath() string {
	username := os.Getenv("USERNAME")
	if username == "" {
		username = "default"
	}
	return fmt.Sprintf(`\\.\pipe\devtool-mcp-%s`, username)
}

// SocketManager handles named pipe lifecycle on Windows.
type SocketManager struct {
	config   SocketConfig
	listener net.Listener
	pidFile  string
}

// NewSocketManager creates a new socket manager.
func NewSocketManager(config SocketConfig) *SocketManager {
	if config.Path == "" {
		config.Path = DefaultSocketPath()
	}
	if config.Mode == 0 {
		config.Mode = 0600
	}

	// On Windows, use a temp file for PID tracking
	pidFile := os.TempDir() + "\\devtool-mcp.pid"

	return &SocketManager{
		config:  config,
		pidFile: pidFile,
	}
}

// Listen creates and binds the named pipe.
// It handles stale pipe cleanup and creates a PID file.
func (sm *SocketManager) Listen() (net.Listener, error) {
	// Check for existing daemon
	if err := sm.checkExisting(); err != nil {
		return nil, err
	}

	// Create named pipe listener
	// Go's net package supports named pipes on Windows via "unix" network
	listener, err := net.Listen("unix", sm.config.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to create pipe: %w", err)
	}

	// Write PID file
	if err := sm.writePIDFile(); err != nil {
		listener.Close()
		return nil, fmt.Errorf("failed to write PID file: %w", err)
	}

	sm.listener = listener
	return listener, nil
}

// Close closes the pipe and removes the PID file.
func (sm *SocketManager) Close() error {
	var errs []error

	if sm.listener != nil {
		if err := sm.listener.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close listener: %w", err))
		}
		sm.listener = nil
	}

	// Remove PID file
	if err := os.Remove(sm.pidFile); err != nil && !os.IsNotExist(err) {
		errs = append(errs, fmt.Errorf("remove PID file: %w", err))
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// Path returns the socket path.
func (sm *SocketManager) Path() string {
	return sm.config.Path
}

// checkExisting checks if another daemon is already running.
func (sm *SocketManager) checkExisting() error {
	// Read PID file
	data, err := os.ReadFile(sm.pidFile)
	if os.IsNotExist(err) {
		return nil // No PID file, no daemon running
	}
	if err != nil {
		return fmt.Errorf("failed to read PID file: %w", err)
	}

	pid, err := strconv.Atoi(string(data))
	if err != nil {
		// Invalid PID file, remove it
		os.Remove(sm.pidFile)
		return nil
	}

	// Check if process is running
	if isProcessRunning(pid) {
		// Try to connect to verify it's actually our daemon
		conn, err := net.DialTimeout("unix", sm.config.Path, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return ErrDaemonRunning
		}
		// Process exists but not responding on pipe - stale
	}

	// Process not running, clean up stale PID file
	os.Remove(sm.pidFile)
	return nil
}

// writePIDFile writes the current process PID to the PID file.
func (sm *SocketManager) writePIDFile() error {
	pid := os.Getpid()
	return os.WriteFile(sm.pidFile, []byte(strconv.Itoa(pid)), 0600)
}

// Connect attempts to connect to an existing daemon pipe.
func Connect(path string) (net.Conn, error) {
	if path == "" {
		path = DefaultSocketPath()
	}

	conn, err := net.Dial("unix", path)
	if err != nil {
		// On Windows, named pipe errors are different
		if isPipeNotFound(err) {
			return nil, ErrSocketNotFound
		}
		return nil, fmt.Errorf("failed to connect: %w", err)
	}

	return conn, nil
}

// IsRunning checks if a daemon is running at the given pipe path.
func IsRunning(path string) bool {
	if path == "" {
		path = DefaultSocketPath()
	}

	conn, err := net.DialTimeout("unix", path, 100*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// isProcessRunning checks if a process with the given PID is running.
func isProcessRunning(pid int) bool {
	// On Windows, we can't use syscall.Kill with signal 0
	// Instead, try to open the process
	handle, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	syscall.CloseHandle(handle)
	return true
}

// isPipeNotFound checks if the error indicates the pipe doesn't exist.
func isPipeNotFound(err error) bool {
	if err == nil {
		return false
	}
	// Check for typical Windows pipe not found errors
	errStr := err.Error()
	return errStr == "The system cannot find the file specified." ||
		errStr == "The system cannot find the path specified." ||
		os.IsNotExist(err)
}
