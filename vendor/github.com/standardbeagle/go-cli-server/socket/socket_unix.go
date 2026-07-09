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
	// Name is the socket name prefix for default path.
	Name string
	// ProcessMatcher is an optional function to detect if a PID belongs to this hub.
	// If nil, a simple cmdline check is performed.
	ProcessMatcher func(pid int) bool
}

// DefaultConfig returns the default socket configuration.
func DefaultConfig() Config {
	return Config{
		Path: DefaultSocketPath(DefaultSocketName),
		Mode: 0600,
		Name: DefaultSocketName,
	}
}

// DefaultSocketPath returns the default socket path for the given name.
func DefaultSocketPath(name string) string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, name+".sock")
	}
	// Fallback: nest the socket inside a per-uid directory rather than a bare
	// /tmp/<name>-<uid>/<name>.sock. /tmp is world-writable and sticky, so any local user
	// could pre-create that bare path and squat it (permanent DoS) or impersonate
	// the hub. A 0700 uid-owned directory — enforced at bind time by
	// secureSocketDir — closes that vector.
	return fmt.Sprintf("/tmp/%s-%d/%s.sock", name, os.Getuid(), name)
}

// secureSocketDir creates (or validates) the directory that will hold the socket
// so a local attacker cannot pre-create a world/group-accessible or
// foreign-owned directory and intercept connections to a command-executing hub.
func secureSocketDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create socket directory: %w", err)
	}

	// Lstat (not Stat) so a symlink planted at the path is caught rather than
	// silently followed to an attacker-controlled target.
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("failed to stat socket directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("socket directory %s is a symlink; refusing to use it", dir)
	}

	st, ok := info.Sys().(*syscall.Stat_t)
	if ok {
		if int(st.Uid) != os.Getuid() {
			return fmt.Errorf("socket directory %s is owned by uid %d, not %d", dir, st.Uid, os.Getuid())
		}
		// Reject group/other access. If we own it, tighten it back to 0700.
		if info.Mode().Perm()&0o077 != 0 {
			if err := os.Chmod(dir, 0o700); err != nil {
				return fmt.Errorf("socket directory %s is group/other-accessible and could not be tightened: %w", dir, err)
			}
		}
	}
	return nil
}

// Manager handles Unix socket lifecycle.
type Manager struct {
	config         Config
	listener       net.Listener
	pidFile        string
	processMatcher func(pid int) bool
	// owned is true only after this manager successfully bound the socket and
	// wrote its PID file. Close removes the socket/pid files only when owned, so a
	// second hub that got ErrDaemonRunning cannot unlink the live daemon's files.
	owned bool
}

// NewManager creates a new socket manager.
func NewManager(config Config) *Manager {
	if config.Path == "" {
		name := config.Name
		if name == "" {
			name = DefaultSocketName
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
	if err := secureSocketDir(dir); err != nil {
		return nil, err
	}

	// The socket lives inside a 0700 uid-owned directory (enforced by
	// secureSocketDir above), so no other user can reach it even for the brief
	// window between bind and Chmod. We deliberately do NOT use syscall.Umask to
	// tighten the bind: Umask is a PROCESS-GLOBAL setting, and toggling it here
	// races every other goroutine creating files/dirs in the same process — a
	// concurrent os.MkdirTemp(…, 0700) landing inside the window gets masked to
	// 0600 (no execute bit), silently making that directory unsearchable. The
	// parent-dir containment already closes the privilege window Umask was
	// guarding, without the global side effect.
	listener, err := Listen("unix", sm.config.Path)
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
	sm.owned = true
	return listener, nil
}

// Listen creates a listener without Go's default SO_REUSEADDR setup. Some
// restricted runtimes deny that setsockopt even for AF_UNIX sockets where it is
// unnecessary, causing net.Listen to fail before bind. Use this helper for
// project-owned listeners that need to run in those environments.
func Listen(network, address string) (net.Listener, error) {
	switch network {
	case "unix":
		return listenUnix(address)
	case "tcp", "tcp4":
		return listenTCP4(address)
	default:
		return net.Listen(network, address)
	}
}

func listenUnix(path string) (net.Listener, error) {
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, os.NewSyscallError("socket", err)
	}
	syscall.CloseOnExec(fd)

	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: path}); err != nil {
		_ = syscall.Close(fd)
		return nil, os.NewSyscallError("bind", err)
	}
	if err := syscall.Listen(fd, listenerBacklog()); err != nil {
		_ = syscall.Close(fd)
		return nil, os.NewSyscallError("listen", err)
	}
	return fileListener(fd, path)
}

func listenTCP4(address string) (net.Listener, error) {
	addr, err := net.ResolveTCPAddr("tcp4", address)
	if err != nil {
		return nil, err
	}
	ip := addr.IP.To4()
	if ip == nil {
		if addr.IP == nil || addr.IP.IsUnspecified() {
			ip = net.IPv4zero.To4()
		} else {
			return nil, fmt.Errorf("address %q is not IPv4", address)
		}
	}

	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, os.NewSyscallError("socket", err)
	}
	syscall.CloseOnExec(fd)

	sa := &syscall.SockaddrInet4{Port: addr.Port}
	copy(sa.Addr[:], ip)
	if err := syscall.Bind(fd, sa); err != nil {
		_ = syscall.Close(fd)
		return nil, os.NewSyscallError("bind", err)
	}
	if err := syscall.Listen(fd, listenerBacklog()); err != nil {
		_ = syscall.Close(fd)
		return nil, os.NewSyscallError("listen", err)
	}
	return fileListener(fd, address)
}

func fileListener(fd int, name string) (net.Listener, error) {
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("failed to wrap listener fd")
	}
	defer file.Close()

	listener, err := net.FileListener(file)
	if err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}
	return listener, nil
}

func listenerBacklog() int {
	return syscall.SOMAXCONN
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

	// Only unlink the socket/pid files if we actually own them. A manager that
	// never bound (e.g. lost the race and got ErrDaemonRunning) must not delete
	// the live daemon's files.
	if !sm.owned {
		if len(errs) > 0 {
			return errors.Join(errs...)
		}
		return nil
	}
	sm.owned = false

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
		// A live PID that does not match our identity predicate may be PID reuse
		// or an overly strict matcher. If the socket is accepting connections,
		// preserve the PID file and report the daemon as running; deleting it here
		// would disable future stale-daemon detection for a live hub.
		conn, err := net.DialTimeout("unix", sm.config.Path, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return ErrDaemonRunning
		}
		os.Remove(sm.pidFile)
		return nil
	}

	conn, err := net.DialTimeout("unix", sm.config.Path, 500*time.Millisecond)
	if err != nil {
		// Process is alive but the socket is unresponsive. Only terminate it if we
		// can positively tie the PID to THIS socket path — a bare "hub"/"mcp"/
		// "daemon" cmdline match also catches the hub CLI, github-desktop, etc.
		// Then SIGTERM first and escalate to SIGKILL only if it lingers.
		if sm.processReferencesSocket(pid) {
			_ = syscall.Kill(pid, syscall.SIGTERM)
			if !waitForExit(pid, 2*time.Second) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				time.Sleep(100 * time.Millisecond)
			}
		}
		os.Remove(sm.pidFile)
		return nil
	}
	conn.Close()

	return ErrDaemonRunning
}

// processReferencesSocket reports whether pid can be positively identified as
// our daemon. A caller-supplied matcher is authoritative; otherwise we require
// this socket path to appear in the process command line. Never guesses "true".
func (sm *Manager) processReferencesSocket(pid int) bool {
	if sm.processMatcher != nil {
		return sm.processMatcher(pid)
	}
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	return strings.Contains(string(cmdline), sm.config.Path)
}

// waitForExit polls until pid is gone or the timeout elapses.
func waitForExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !isProcessRunning(pid) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return !isProcessRunning(pid)
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
		path = DefaultSocketPath(DefaultSocketName)
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
		path = DefaultSocketPath(DefaultSocketName)
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

	// The pid file is the authoritative record of which process is the daemon.
	// When it exists, restrict cleanup to exactly that PID so a hubctl client whose
	// cmdline merely contains the socket path (and matches the loose "hub"/"mcp"
	// name heuristic) is never mistaken for a zombie daemon and killed.
	daemonPID := -1
	if data, err := os.ReadFile(socketPath + ".pid"); err == nil {
		if p, perr := strconv.Atoi(strings.TrimSpace(string(data))); perr == nil {
			daemonPID = p
		}
	} else if processMatcher == nil {
		// Without a PID file and without a caller-supplied identity predicate,
		// scanning /proc with a name substring heuristic is not safe enough to
		// kill anything. Leave stale socket-file cleanup to Manager.cleanupStale.
		return 0
	}

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

		// If we know the authoritative daemon PID, only that process is eligible.
		if daemonPID != -1 && pid != daemonPID {
			continue
		}

		isOurs := false
		if processMatcher != nil {
			isOurs = processMatcher(pid)
		} else {
			isOurs = pid == daemonPID && isDaemonProcess(pid)
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

		// This PID is confirmed to reference our socket path. SIGTERM first, then
		// SIGKILL only if it does not exit.
		_ = syscall.Kill(pid, syscall.SIGTERM)
		if waitForExit(pid, 1*time.Second) {
			cleaned++
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
