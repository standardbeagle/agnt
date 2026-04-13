//go:build windows

// Package socket provides Unix domain socket management for the hub.
package socket

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// Config holds configuration for socket management.
type Config struct {
	Path           string
	Mode           os.FileMode
	Name           string
	ProcessMatcher func(pid int) bool
}

// DefaultConfig returns the default socket configuration.
func DefaultConfig() Config {
	return Config{
		Path: DefaultSocketPath("mcp-hub"),
		Mode: 0600,
		Name: "mcp-hub",
	}
}

// DefaultSocketPath returns the default socket path for Windows.
func DefaultSocketPath(name string) string {
	tempDir := os.TempDir()
	return filepath.Join(tempDir, name+".sock")
}

// Manager handles Unix domain socket lifecycle on Windows.
type Manager struct {
	config         Config
	listener       net.Listener
	pidFile        string
	processMatcher func(pid int) bool
}

// NewManager creates a new socket manager.
func NewManager(config Config) *Manager {
	if config.Path == "" {
		name := config.Name
		if name == "" {
			name = "mcp-hub"
		}
		config.Path = DefaultSocketPath(name)
	}
	if config.Mode == 0 {
		config.Mode = 0600
	}

	pidFile := filepath.Join(os.TempDir(), config.Name+".pid")

	return &Manager{
		config:         config,
		pidFile:        pidFile,
		processMatcher: config.ProcessMatcher,
	}
}

// Listen creates and binds the Unix domain socket.
func (sm *Manager) Listen() (net.Listener, error) {
	if err := sm.checkExisting(); err != nil {
		return nil, err
	}

	if err := sm.cleanupStale(); err != nil {
		return nil, fmt.Errorf("failed to cleanup stale socket: %w", err)
	}

	listener, err := net.Listen("unix", sm.config.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to create socket: %w", err)
	}

	if err := sm.writePIDFile(); err != nil {
		listener.Close()
		os.Remove(sm.config.Path)
		return nil, fmt.Errorf("failed to write PID file: %w", err)
	}

	sm.listener = listener
	return listener, nil
}

// Close closes the socket and removes files.
func (sm *Manager) Close() error {
	var errs []error

	if sm.listener != nil {
		if err := sm.listener.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close listener: %w", err))
		}
		sm.listener = nil
	}

	if err := os.Remove(sm.config.Path); err != nil && !os.IsNotExist(err) {
		errs = append(errs, fmt.Errorf("remove socket file: %w", err))
	}

	if err := os.Remove(sm.pidFile); err != nil && !os.IsNotExist(err) {
		errs = append(errs, fmt.Errorf("remove PID file: %w", err))
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// Path returns the socket path.
func (sm *Manager) Path() string {
	return sm.config.Path
}

// checkExisting checks if another daemon is already running.
func (sm *Manager) checkExisting() error {
	data, err := os.ReadFile(sm.pidFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to read PID file: %w", err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		os.Remove(sm.pidFile)
		return nil
	}

	if !isProcessRunning(pid) {
		os.Remove(sm.pidFile)
		return nil
	}

	if !sm.isOurDaemonProcess(pid) {
		os.Remove(sm.pidFile)
		return nil
	}

	conn, err := net.DialTimeout("unix", sm.config.Path, 500*time.Millisecond)
	if err != nil {
		// Process exists but socket unresponsive - terminate the zombie
		terminateProcess(pid)
		time.Sleep(100 * time.Millisecond)
		os.Remove(sm.pidFile)
		return nil
	}
	conn.Close()

	return ErrDaemonRunning
}

// isOurDaemonProcess checks if the PID belongs to this hub.
func (sm *Manager) isOurDaemonProcess(pid int) bool {
	if sm.processMatcher != nil {
		return sm.processMatcher(pid)
	}
	// On Windows without a matcher, assume any running process with our PID file is ours
	return true
}

// cleanupStale removes a stale socket file that no daemon is listening on.
func (sm *Manager) cleanupStale() error {
	if _, err := os.Stat(sm.config.Path); os.IsNotExist(err) {
		return nil
	}

	conn, err := net.DialTimeout("unix", sm.config.Path, 100*time.Millisecond)
	if err == nil {
		conn.Close()
		return ErrDaemonRunning
	}

	if err := os.Remove(sm.config.Path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove stale socket: %w", err)
	}

	return nil
}

// writePIDFile writes the current process PID.
func (sm *Manager) writePIDFile() error {
	pid := os.Getpid()
	return os.WriteFile(sm.pidFile, []byte(strconv.Itoa(pid)), 0600)
}

// terminateProcess forcibly terminates a process by PID on Windows.
func terminateProcess(pid int) {
	handle, err := syscall.OpenProcess(syscall.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return
	}
	defer syscall.CloseHandle(handle)
	syscall.TerminateProcess(handle, 1)
}

// Connect attempts to connect to an existing socket.
func Connect(path string) (net.Conn, error) {
	if path == "" {
		path = DefaultSocketPath("mcp-hub")
	}

	conn, err := net.Dial("unix", path)
	if err != nil {
		if isPipeNotFound(err) {
			return nil, ErrSocketNotFound
		}
		return nil, fmt.Errorf("failed to connect: %w", err)
	}

	return conn, nil
}

// IsRunning checks if a daemon is running at the given path.
func IsRunning(path string) bool {
	if path == "" {
		path = DefaultSocketPath("mcp-hub")
	}

	conn, err := net.DialTimeout("unix", path, 100*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// isProcessRunning checks if a process exists.
func isProcessRunning(pid int) bool {
	handle, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	syscall.CloseHandle(handle)
	return true
}

// isPipeNotFound checks for socket not found errors.
func isPipeNotFound(err error) bool {
	if err == nil {
		return false
	}
	if os.IsNotExist(err) {
		return true
	}
	errLower := strings.ToLower(err.Error())
	return strings.Contains(errLower, "cannot find the file") ||
		strings.Contains(errLower, "cannot find the path") ||
		strings.Contains(errLower, "file not found") ||
		strings.Contains(errLower, "no such file") ||
		strings.Contains(errLower, "connection refused") ||
		strings.Contains(errLower, "target machine actively refused")
}

// CleanupZombieDaemons is a no-op on Windows.
func CleanupZombieDaemons(pipePath string, processMatcher func(pid int) bool) int {
	return 0
}

// IsClosedError returns true if err indicates a closed connection.
func IsClosedError(err error) bool {
	if err == nil {
		return false
	}
	return err == net.ErrClosed ||
		strings.Contains(err.Error(), "use of closed network connection")
}
