package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// AutoStartConfig holds configuration for auto-starting the daemon.
type AutoStartConfig struct {
	// SocketPath is the socket path to connect to.
	SocketPath string
	// DaemonPath is the path to the daemon executable.
	DaemonPath string
	// StartTimeout is how long to wait for the daemon to start.
	StartTimeout time.Duration
	// RetryInterval is how long to wait between connection attempts.
	RetryInterval time.Duration
	// MaxRetries is the maximum number of connection attempts.
	MaxRetries int
}

// DefaultAutoStartConfig returns sensible defaults.
func DefaultAutoStartConfig() AutoStartConfig {
	return AutoStartConfig{
		SocketPath:    DefaultSocketPath(),
		DaemonPath:    "", // Will use current executable
		StartTimeout:  5 * time.Second,
		RetryInterval: 100 * time.Millisecond,
		MaxRetries:    50,
	}
}

// AutoStartClient creates a client that auto-starts the daemon if needed.
type AutoStartClient struct {
	*Client
	config AutoStartConfig
}

// NewAutoStartClient creates a new auto-start client.
func NewAutoStartClient(config AutoStartConfig) *AutoStartClient {
	return &AutoStartClient{
		Client: NewClient(
			WithSocketPath(config.SocketPath),
			WithTimeout(30*time.Second),
		),
		config: config,
	}
}

// Connect connects to the daemon, starting it if necessary.
func (c *AutoStartClient) Connect() error {
	// First, try to connect directly
	err := c.Client.Connect()
	if err == nil {
		return nil
	}

	// If the socket wasn't found, try to start the daemon
	if err != ErrSocketNotFound {
		return err
	}

	// Start the daemon
	if err := c.startDaemon(); err != nil {
		return fmt.Errorf("failed to start daemon: %w", err)
	}

	// Wait for daemon to be ready
	return c.waitForDaemon()
}

// startDaemon starts the daemon process in the background.
func (c *AutoStartClient) startDaemon() error {
	// Acquire startup lock to prevent race conditions when multiple clients
	// try to start the daemon simultaneously
	lockPath := c.config.SocketPath + ".startup.lock"
	lockFile, err := acquireStartupLock(lockPath)
	if err != nil {
		// Another process is starting the daemon, just wait for it
		return nil
	}
	defer releaseStartupLock(lockFile, lockPath)

	// Double-check: daemon might have started while we were acquiring the lock
	if IsRunning(c.config.SocketPath) {
		return nil
	}

	// First, aggressively clean up any zombie daemon processes
	CleanupZombieDaemons(c.config.SocketPath)

	execPath := c.config.DaemonPath
	if execPath == "" {
		// Look for daemon binary next to current executable
		// This avoids self-exec restrictions in sandboxed environments
		selfPath, err := os.Executable()
		if err != nil {
			return fmt.Errorf("failed to get executable path: %w", err)
		}

		// Try the dedicated daemon binary first (e.g., devtool-mcp-daemon)
		daemonPath := selfPath + "-daemon"
		if _, err := os.Stat(daemonPath); err == nil {
			execPath = daemonPath
		} else {
			// Fall back to self-exec if daemon binary not found
			execPath = selfPath
		}
	}

	// Start daemon with "daemon start" subcommand
	cmd := exec.Command(execPath, "daemon", "start", "--socket", c.config.SocketPath)

	// Detach from parent process
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil

	// Set process group to prevent daemon from receiving signals sent to parent
	setSysProcAttr(cmd)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start daemon process: %w", err)
	}

	// Don't wait for daemon (it runs in background)
	go cmd.Wait() //nolint:errcheck

	return nil
}

// acquireStartupLock attempts to acquire an exclusive lock for daemon startup.
// Returns the lock file handle on success, or error if lock is held by another process.
func acquireStartupLock(lockPath string) (*os.File, error) {
	// Try to create lock file exclusively
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if os.IsExist(err) {
			// Lock file exists - check if it's stale (> 30 seconds old)
			info, statErr := os.Stat(lockPath)
			if statErr == nil && time.Since(info.ModTime()) > 30*time.Second {
				// Stale lock, remove and retry
				os.Remove(lockPath)
				return acquireStartupLock(lockPath)
			}
			return nil, fmt.Errorf("startup lock held by another process")
		}
		return nil, err
	}

	// Write PID and timestamp
	fmt.Fprintf(f, "%d\n", os.Getpid())
	return f, nil
}

// releaseStartupLock releases the startup lock.
func releaseStartupLock(f *os.File, lockPath string) {
	if f != nil {
		f.Close()
		os.Remove(lockPath)
	}
}

// waitForDaemon waits for the daemon to be ready to accept connections.
func (c *AutoStartClient) waitForDaemon() error {
	ctx, cancel := context.WithTimeout(context.Background(), c.config.StartTimeout)
	defer cancel()

	ticker := time.NewTicker(c.config.RetryInterval)
	defer ticker.Stop()

	retries := 0
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for daemon to start")
		case <-ticker.C:
			err := c.Client.Connect()
			if err == nil {
				return nil
			}
			if err != ErrSocketNotFound {
				return err
			}
			retries++
			if retries >= c.config.MaxRetries {
				return fmt.Errorf("max retries exceeded waiting for daemon")
			}
		}
	}
}

// EnsureDaemonRunning ensures the daemon is running, starting it if needed.
// Returns a connected client.
func EnsureDaemonRunning(config AutoStartConfig) (*Client, error) {
	client := NewAutoStartClient(config)
	if err := client.Connect(); err != nil {
		return nil, err
	}
	return client.Client, nil
}

// StopDaemon connects to a running daemon and requests shutdown.
func StopDaemon(socketPath string) error {
	if socketPath == "" {
		socketPath = DefaultSocketPath()
	}

	client := NewClient(WithSocketPath(socketPath))
	if err := client.Connect(); err != nil {
		if err == ErrSocketNotFound {
			return nil // Daemon not running, nothing to stop
		}
		return err
	}
	defer client.Close()

	return client.Shutdown()
}

// IsDaemonRunning checks if the daemon is running at the given socket path.
func IsDaemonRunning(socketPath string) bool {
	if socketPath == "" {
		socketPath = DefaultSocketPath()
	}
	return IsRunning(socketPath)
}
