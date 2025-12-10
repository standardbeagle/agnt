//go:build unix

// Package daemon provides the stateful daemon process that manages
// processes, proxies, and traffic logs across client connections.
package daemon

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

// SocketConfig holds configuration for socket management.
type SocketConfig struct {
	// Path is the socket file path. If empty, uses default path.
	Path string
	// Mode is the socket file permissions (default 0600).
	Mode os.FileMode
}

// DefaultSocketConfig returns the default socket configuration.
func DefaultSocketConfig() SocketConfig {
	return SocketConfig{
		Path: DefaultSocketPath(),
		Mode: 0600,
	}
}

// DefaultSocketPath returns the default socket path for the current platform.
func DefaultSocketPath() string {
	// Try XDG_RUNTIME_DIR first (standard on Linux)
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "devtool-mcp.sock")
	}

	// Fall back to /tmp with UID for uniqueness
	return fmt.Sprintf("/tmp/devtool-mcp-%d.sock", os.Getuid())
}

// SocketManager handles Unix socket lifecycle.
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

	return &SocketManager{
		config:  config,
		pidFile: config.Path + ".pid",
	}
}

// Listen creates and binds the Unix socket.
// It handles stale socket cleanup and creates a PID file.
func (sm *SocketManager) Listen() (net.Listener, error) {
	// Check for existing daemon
	if err := sm.checkExisting(); err != nil {
		return nil, err
	}

	// Clean up any stale socket file
	if err := sm.cleanupStale(); err != nil {
		return nil, fmt.Errorf("failed to cleanup stale socket: %w", err)
	}

	// Ensure parent directory exists
	dir := filepath.Dir(sm.config.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create socket directory: %w", err)
	}

	// Create Unix socket listener
	listener, err := net.Listen("unix", sm.config.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to create socket: %w", err)
	}

	// Set socket permissions
	if err := os.Chmod(sm.config.Path, sm.config.Mode); err != nil {
		listener.Close()
		os.Remove(sm.config.Path)
		return nil, fmt.Errorf("failed to set socket permissions: %w", err)
	}

	// Write PID file
	if err := sm.writePIDFile(); err != nil {
		listener.Close()
		os.Remove(sm.config.Path)
		return nil, fmt.Errorf("failed to write PID file: %w", err)
	}

	sm.listener = listener
	return listener, nil
}

// Close closes the socket and removes the socket and PID files.
func (sm *SocketManager) Close() error {
	var errs []error

	if sm.listener != nil {
		if err := sm.listener.Close(); err != nil {
			// Ignore "use of closed network connection" - listener may have been
			// closed elsewhere (e.g., by daemon.Stop() to unblock accept loop)
			if !isClosedError(err) {
				errs = append(errs, fmt.Errorf("close listener: %w", err))
			}
		}
		sm.listener = nil
	}

	// Remove socket file
	if err := os.Remove(sm.config.Path); err != nil && !os.IsNotExist(err) {
		errs = append(errs, fmt.Errorf("remove socket: %w", err))
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
// It performs a multi-layered check:
// 1. Read PID file and check if process exists
// 2. If process exists, verify it's actually a daemon by checking cmdline
// 3. Try to connect to socket to verify daemon is responsive
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
	if !isProcessRunning(pid) {
		// Process not running, clean up stale PID file
		os.Remove(sm.pidFile)
		return nil
	}

	// Process exists - verify it's actually our daemon by checking cmdline
	if !isDaemonProcess(pid) {
		// PID was recycled by a different process, clean up
		os.Remove(sm.pidFile)
		return nil
	}

	// Process appears to be daemon - try to connect to verify it's responsive
	conn, err := net.DialTimeout("unix", sm.config.Path, 500*time.Millisecond)
	if err != nil {
		// Daemon process exists but isn't responding - it's stuck/zombie
		// Kill it aggressively
		if killErr := syscall.Kill(pid, syscall.SIGKILL); killErr == nil {
			// Wait briefly for process to die
			time.Sleep(100 * time.Millisecond)
		}
		os.Remove(sm.pidFile)
		return nil
	}
	conn.Close()

	// Daemon is running and responsive
	return ErrDaemonRunning
}

// cleanupStale removes a stale socket file if it exists.
func (sm *SocketManager) cleanupStale() error {
	// Check if socket file exists
	info, err := os.Stat(sm.config.Path)
	if os.IsNotExist(err) {
		return nil // No socket file, nothing to clean
	}
	if err != nil {
		return fmt.Errorf("failed to stat socket: %w", err)
	}

	// Verify it's a socket
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("path exists but is not a socket: %s", sm.config.Path)
	}

	// Try to connect to see if daemon is alive
	conn, err := net.DialTimeout("unix", sm.config.Path, 100*1e6) // 100ms timeout
	if err == nil {
		conn.Close()
		return ErrDaemonRunning
	}

	// Connection failed, socket is stale - remove it
	if err := os.Remove(sm.config.Path); err != nil {
		return fmt.Errorf("failed to remove stale socket: %w", err)
	}

	return nil
}

// writePIDFile writes the current process PID to the PID file.
func (sm *SocketManager) writePIDFile() error {
	pid := os.Getpid()
	return os.WriteFile(sm.pidFile, []byte(strconv.Itoa(pid)), 0600)
}

// Connect attempts to connect to an existing daemon socket.
func Connect(path string) (net.Conn, error) {
	if path == "" {
		path = DefaultSocketPath()
	}

	conn, err := net.Dial("unix", path)
	if err != nil {
		if os.IsNotExist(err) || isConnectionRefused(err) || isNoSuchFile(err) {
			return nil, ErrSocketNotFound
		}
		return nil, fmt.Errorf("failed to connect: %w", err)
	}

	return conn, nil
}

// IsRunning checks if a daemon is running at the given socket path.
func IsRunning(path string) bool {
	if path == "" {
		path = DefaultSocketPath()
	}

	conn, err := net.DialTimeout("unix", path, 100*1e6) // 100ms timeout
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// isProcessRunning checks if a process with the given PID is running.
func isProcessRunning(pid int) bool {
	// Sending signal 0 checks if process exists without actually signaling
	err := syscall.Kill(pid, 0)
	return err == nil
}

// isDaemonProcess checks if the process with the given PID is actually a daemon process.
// This prevents issues when PIDs are recycled by the kernel.
func isDaemonProcess(pid int) bool {
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
		strings.Contains(cmd, "agnt-daemon") ||
		strings.Contains(cmd, "devtool-mcp-daemon")
}

// CleanupZombieDaemons finds and kills any zombie daemon processes that aren't
// responding on their socket. This is called during startup to clean up any
// leftover processes from previous runs.
func CleanupZombieDaemons(socketPath string) int {
	cleaned := 0

	// Read all /proc entries to find daemon processes
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue // Not a PID directory
		}

		// Skip our own process
		if pid == os.Getpid() {
			continue
		}

		// Check if this is a daemon process
		if !isDaemonProcess(pid) {
			continue
		}

		// Check if this daemon is for our socket path
		cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			continue
		}

		// Check if this daemon is using our socket
		if !strings.Contains(string(cmdline), socketPath) {
			continue
		}

		// Found a daemon for our socket - check if it's responsive
		conn, err := net.DialTimeout("unix", socketPath, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			continue // Daemon is responsive, leave it alone
		}

		// Daemon is not responsive - kill it
		if err := syscall.Kill(pid, syscall.SIGKILL); err == nil {
			cleaned++
		}
	}

	// Clean up stale socket and PID files if we killed anything
	if cleaned > 0 {
		time.Sleep(100 * time.Millisecond) // Give processes time to die
		os.Remove(socketPath)
		os.Remove(socketPath + ".pid")
	}

	return cleaned
}

// isConnectionRefused checks if the error is a connection refused error.
func isConnectionRefused(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		var syscallErr *os.SyscallError
		if errors.As(opErr.Err, &syscallErr) {
			return errors.Is(syscallErr.Err, syscall.ECONNREFUSED)
		}
	}
	return false
}

// isNoSuchFile checks if the error indicates the socket file doesn't exist.
func isNoSuchFile(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		var syscallErr *os.SyscallError
		if errors.As(opErr.Err, &syscallErr) {
			return errors.Is(syscallErr.Err, syscall.ENOENT)
		}
	}
	return false
}
