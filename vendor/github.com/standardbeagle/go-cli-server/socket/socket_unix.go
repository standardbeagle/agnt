//go:build unix

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
	// Path is the socket file path. If empty, uses default path.
	Path string
	// Mode is the socket file permissions (default 0600).
	Mode os.FileMode
	// Name is the socket name prefix for default path (default "mcp-hub").
	Name string
	// ProcessMatcher is an optional function to detect if a PID belongs to this hub.
	// If nil, a simple cmdline check is performed.
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

// DefaultSocketPath returns the default socket path for the given name.
func DefaultSocketPath(name string) string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, name+".sock")
	}
	return fmt.Sprintf("/tmp/%s-%d.sock", name, os.Getuid())
}

// Manager handles Unix socket lifecycle.
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

	return &Manager{
		config:         config,
		pidFile:        config.Path + ".pid",
		processMatcher: config.ProcessMatcher,
	}
}

// Listen creates and binds the Unix socket.
func (sm *Manager) Listen() (net.Listener, error) {
	if err := sm.checkExisting(); err != nil {
		return nil, err
	}

	if err := sm.cleanupStale(); err != nil {
		return nil, fmt.Errorf("failed to cleanup stale socket: %w", err)
	}

	dir := filepath.Dir(sm.config.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create socket directory: %w", err)
	}

	listener, err := net.Listen("unix", sm.config.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to create socket: %w", err)
	}

	if err := os.Chmod(sm.config.Path, sm.config.Mode); err != nil {
		listener.Close()
		os.Remove(sm.config.Path)
		return nil, fmt.Errorf("failed to set socket permissions: %w", err)
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
			if !IsClosedError(err) {
				errs = append(errs, fmt.Errorf("close listener: %w", err))
			}
		}
		sm.listener = nil
	}

	if err := os.Remove(sm.config.Path); err != nil && !os.IsNotExist(err) {
		errs = append(errs, fmt.Errorf("remove socket: %w", err))
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

	pid, err := strconv.Atoi(string(data))
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
		if killErr := syscall.Kill(pid, syscall.SIGKILL); killErr == nil {
			time.Sleep(100 * time.Millisecond)
		}
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
	return isDaemonProcess(pid)
}

// cleanupStale removes a stale socket file.
func (sm *Manager) cleanupStale() error {
	info, err := os.Stat(sm.config.Path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to stat socket: %w", err)
	}

	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("path exists but is not a socket: %s", sm.config.Path)
	}

	conn, err := net.DialTimeout("unix", sm.config.Path, 100*1e6)
	if err == nil {
		conn.Close()
		return ErrDaemonRunning
	}

	if err := os.Remove(sm.config.Path); err != nil {
		return fmt.Errorf("failed to remove stale socket: %w", err)
	}

	return nil
}

// writePIDFile writes the current process PID.
func (sm *Manager) writePIDFile() error {
	pid := os.Getpid()
	return os.WriteFile(sm.pidFile, []byte(strconv.Itoa(pid)), 0600)
}

// Connect attempts to connect to an existing socket.
func Connect(path string) (net.Conn, error) {
	if path == "" {
		path = DefaultSocketPath("mcp-hub")
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

// IsRunning checks if a daemon is running at the given path.
func IsRunning(path string) bool {
	if path == "" {
		path = DefaultSocketPath("mcp-hub")
	}

	conn, err := net.DialTimeout("unix", path, 100*1e6)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// isProcessRunning checks if a process exists.
func isProcessRunning(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil
}

// isDaemonProcess checks if the process appears to be a hub daemon.
func isDaemonProcess(pid int) bool {
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}

	cmd := string(cmdline)
	return strings.Contains(cmd, "daemon") || strings.Contains(cmd, "mcp") || strings.Contains(cmd, "hub")
}

// CleanupZombieDaemons finds and kills zombie daemon processes.
func CleanupZombieDaemons(socketPath string, processMatcher func(pid int) bool) int {
	cleaned := 0

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
			continue
		}

		if pid == os.Getpid() {
			continue
		}

		isOurs := false
		if processMatcher != nil {
			isOurs = processMatcher(pid)
		} else {
			isOurs = isDaemonProcess(pid)
		}
		if !isOurs {
			continue
		}

		cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			continue
		}

		if !strings.Contains(string(cmdline), socketPath) {
			continue
		}

		conn, err := net.DialTimeout("unix", socketPath, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			continue
		}

		if err := syscall.Kill(pid, syscall.SIGKILL); err == nil {
			cleaned++
		}
	}

	if cleaned > 0 {
		time.Sleep(100 * time.Millisecond)
		os.Remove(socketPath)
		os.Remove(socketPath + ".pid")
	}

	return cleaned
}

// isConnectionRefused checks for connection refused errors.
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

// isNoSuchFile checks for file not found errors.
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

// IsClosedError returns true if err indicates a closed connection.
func IsClosedError(err error) bool {
	if err == nil {
		return false
	}
	return err == net.ErrClosed ||
		strings.Contains(err.Error(), "use of closed network connection")
}
