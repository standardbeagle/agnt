package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/standardbeagle/go-cli-server/socket"
)

var errStartupLockHeld = errors.New("startup lock held")

// AutoStartConfig holds configuration for auto-starting the hub.
type AutoStartConfig struct {
	// SocketPath is the socket path to connect to.
	SocketPath string
	// HubPath is the path to the hub executable.
	// If empty, will look for a "-daemon" variant of the current executable.
	HubPath string
	// HubArgs are the arguments to pass to the hub executable.
	// Defaults to ["--socket", socketPath].
	HubArgs []string
	// StartTimeout is how long to wait for the hub to start.
	StartTimeout time.Duration
	// RetryInterval is how long to wait between connection attempts.
	RetryInterval time.Duration
	// MaxRetries is the maximum number of connection attempts.
	MaxRetries int
	// ProcessMatcher is an optional function to detect if a PID belongs to this hub.
	// Used for zombie process cleanup.
	ProcessMatcher func(pid int) bool
}

// DefaultAutoStartConfig returns sensible defaults.
func DefaultAutoStartConfig(socketName string) AutoStartConfig {
	return AutoStartConfig{
		SocketPath:    socket.DefaultSocketPath(socketName),
		StartTimeout:  5 * time.Second,
		RetryInterval: 100 * time.Millisecond,
		MaxRetries:    50,
	}
}

// AutoStartConn wraps a Conn with auto-start capability.
type AutoStartConn struct {
	*Conn
	config AutoStartConfig
}

// NewAutoStartConn creates a new auto-start connection.
func NewAutoStartConn(config AutoStartConfig) *AutoStartConn {
	return &AutoStartConn{
		Conn: NewConn(
			WithSocketPath(config.SocketPath),
			WithTimeout(30*time.Second),
		),
		config: config,
	}
}

// Connect connects to the hub, starting it if necessary.
func (c *AutoStartConn) Connect() error {
	return c.ConnectContext(context.Background())
}

// ConnectContext is Connect, abandoning the wait for a starting hub when ctx is
// cancelled. A caller that can be shut down (a reconnect loop, say) must not be
// pinned here for the full StartTimeout after it is told to stop.
func (c *AutoStartConn) ConnectContext(ctx context.Context) error {
	// Reject an empty socket path outright: it would derive a relative
	// ".startup.lock" in the working directory and connect nowhere useful.
	if c.config.SocketPath == "" {
		return fmt.Errorf("auto-start requires a non-empty SocketPath")
	}

	// First, try to connect directly
	err := c.Conn.EnsureConnected()
	if err == nil {
		return nil
	}

	// If the socket wasn't found, try to start the hub
	if err != socket.ErrSocketNotFound {
		return err
	}

	// Start the hub
	if err := c.startHub(); err != nil {
		return fmt.Errorf("failed to start hub: %w", err)
	}

	// Wait for hub to be ready
	return c.waitForHub(ctx)
}

// startHub starts the hub process in the background.
func (c *AutoStartConn) startHub() error {
	// Acquire startup lock to prevent race conditions when multiple clients
	// try to start the hub simultaneously
	lockPath := c.config.SocketPath + ".startup.lock"
	lockFile, err := acquireStartupLock(lockPath)
	if err != nil {
		if errors.Is(err, errStartupLockHeld) {
			// Another process is starting the hub; wait for it below.
			return nil
		}
		return err
	}
	defer releaseStartupLock(lockFile, lockPath)

	// Double-check: hub might have started while we were acquiring the lock
	if socket.IsRunning(c.config.SocketPath) {
		return nil
	}

	// First, aggressively clean up any zombie hub processes
	socket.CleanupZombieDaemons(c.config.SocketPath, c.config.ProcessMatcher)

	execPath := c.config.HubPath
	if execPath == "" {
		// Look for daemon binary next to current executable
		// This avoids self-exec restrictions in sandboxed environments
		selfPath, err := os.Executable()
		if err != nil {
			return fmt.Errorf("failed to get executable path: %w", err)
		}

		// Try the dedicated daemon binary first (e.g., myapp-daemon)
		daemonPath := selfPath + "-daemon"
		if _, err := os.Stat(daemonPath); err == nil {
			execPath = daemonPath
		} else if isGoTestBinary(selfPath) {
			// Refuse to self-spawn a Go test binary. Without a dedicated
			// daemon binary next to it, falling through to selfPath would
			// invoke the test binary with hub args like "--socket /path".
			// The test binary's flag.Parse()
			// ignores positional args and runs the entire test suite
			// recursively, producing long-lived leaked processes that stay
			// alive for hours and break subsequent test runs.
			//
			// Callers in test context should set HubPath explicitly or
			// ensure an in-process hub is already listening on SocketPath
			// before calling Connect().
			return fmt.Errorf("refusing to self-spawn test binary %q: set AutoStartConfig.HubPath or ensure an in-process hub is listening on %s before calling Connect()", selfPath, c.config.SocketPath)
		} else {
			// Fall back to self-exec if daemon binary not found
			execPath = selfPath
		}
	}

	// Build command arguments
	args := c.config.HubArgs
	if len(args) == 0 {
		args = defaultHubArgs(c.config.SocketPath)
	}

	cmd := exec.Command(execPath, args...)

	// Detach from parent process
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil

	// Set process group to prevent hub from receiving signals sent to parent
	setSysProcAttr(cmd)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start hub process: %w", err)
	}

	// Don't wait for hub (it runs in background)
	go cmd.Wait() //nolint:errcheck

	return nil
}

func defaultHubArgs(socketPath string) []string {
	return []string{"--socket", socketPath}
}

// isGoTestBinary returns true if the given executable path looks like a Go
// test binary. Go test binaries end in ".test" (or ".test.exe" on Windows) or
// live under a go-build temp directory. Detecting these prevents the
// self-spawn path in startHub() from invoking the test binary with subcommand
// args, which would trigger recursive test-suite execution.
func isGoTestBinary(path string) bool {
	if strings.HasSuffix(path, ".test") || strings.HasSuffix(path, ".test.exe") {
		return true
	}
	// Go test binaries are built into a temp directory named like
	// /tmp/go-build<number>/b001/<pkg>.test on unix or
	// C:\Users\...\AppData\Local\Temp\go-build<number>\b001\<pkg>.test.exe
	// on Windows. The "go-build" segment is stable.
	return strings.Contains(path, string(os.PathSeparator)+"go-build")
}

// acquireStartupLock attempts to acquire an exclusive lock for hub startup.
// Returns the lock file handle on success, or error if lock is held by another process.
func acquireStartupLock(lockPath string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := lockStartupFile(f); err != nil {
		f.Close()
		if errors.Is(err, errStartupLockHeld) {
			return nil, fmt.Errorf("startup lock held by another process")
		}
		return nil, err
	}

	// Write PID and timestamp
	if err := f.Truncate(0); err != nil {
		_ = unlockStartupFile(f)
		f.Close()
		return nil, err
	}
	if _, err := f.Seek(0, 0); err != nil {
		_ = unlockStartupFile(f)
		f.Close()
		return nil, err
	}
	fmt.Fprintf(f, "%d\n", os.Getpid())
	return f, nil
}

// releaseStartupLock releases the startup lock.
func releaseStartupLock(f *os.File, lockPath string) {
	if f != nil {
		own, statErr := f.Stat()
		if statErr == nil {
			if info, err := os.Stat(lockPath); err == nil && os.SameFile(info, own) {
				_ = os.Remove(lockPath)
			}
		}
		_ = unlockStartupFile(f)
		f.Close()
	}
}

// waitForHub waits for the hub to be ready to accept connections.
func (c *AutoStartConn) waitForHub(parent context.Context) error {
	startTimeout := c.config.StartTimeout
	if startTimeout <= 0 {
		startTimeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, startTimeout)
	defer cancel()

	retryInterval := c.config.RetryInterval
	if retryInterval <= 0 {
		retryInterval = 50 * time.Millisecond
	}
	ticker := time.NewTicker(retryInterval)
	defer ticker.Stop()

	retries := 0
	for {
		select {
		case <-ctx.Done():
			if parent.Err() != nil {
				return parent.Err()
			}
			return fmt.Errorf("timeout waiting for hub to start")
		case <-ticker.C:
			err := c.Conn.EnsureConnected()
			if err == nil {
				return nil
			}
			if err != socket.ErrSocketNotFound {
				return err
			}
			retries++
			// MaxRetries <= 0 means rely solely on the context timeout.
			if c.config.MaxRetries > 0 && retries >= c.config.MaxRetries {
				return fmt.Errorf("max retries exceeded waiting for hub")
			}
		}
	}
}

// EnsureHubRunning ensures the hub is running, starting it if needed.
// Returns a connected Conn.
func EnsureHubRunning(config AutoStartConfig) (*Conn, error) {
	return EnsureHubRunningContext(context.Background(), config)
}

// EnsureHubRunningContext is EnsureHubRunning, abandoning the wait for a
// starting hub when ctx is cancelled.
func EnsureHubRunningContext(ctx context.Context, config AutoStartConfig) (*Conn, error) {
	client := NewAutoStartConn(config)
	if err := client.ConnectContext(ctx); err != nil {
		return nil, err
	}
	return client.Conn, nil
}

// StopHub connects to a running hub and requests shutdown.
func StopHub(socketPath string) error {
	if socketPath == "" {
		socketPath = socket.DefaultSocketPath(socket.DefaultSocketName)
	}

	conn := NewConn(WithSocketPath(socketPath))
	if err := conn.EnsureConnected(); err != nil {
		if err == socket.ErrSocketNotFound {
			return nil // Hub not running, nothing to stop
		}
		return err
	}
	defer conn.Close()

	// Send SHUTDOWN command
	return conn.Request("SHUTDOWN").OK()
}

// IsHubRunning checks if the hub is running at the given socket path.
func IsHubRunning(socketPath string) bool {
	if socketPath == "" {
		socketPath = socket.DefaultSocketPath(socket.DefaultSocketName)
	}
	return socket.IsRunning(socketPath)
}
