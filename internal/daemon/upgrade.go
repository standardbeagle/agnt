package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

// UpgradeConfig holds configuration for daemon upgrades.
type UpgradeConfig struct {
	// SocketPath is the socket path to connect to the daemon.
	// If empty, uses DefaultSocketPath().
	SocketPath string

	// NewBinaryPath is the path to the new daemon binary.
	// If empty, uses the current executable.
	NewBinaryPath string

	// Timeout is the maximum time for the entire upgrade process.
	// Default: 30 seconds.
	Timeout time.Duration

	// GracefulTimeout is the timeout for graceful daemon shutdown.
	// Default: 5 seconds.
	GracefulTimeout time.Duration

	// Force bypasses version checks and performs upgrade even if versions match.
	Force bool

	// Verbose enables detailed logging during upgrade.
	Verbose bool
}

// DefaultUpgradeConfig returns sensible defaults.
func DefaultUpgradeConfig() UpgradeConfig {
	return UpgradeConfig{
		SocketPath:      DefaultSocketPath(),
		Timeout:         30 * time.Second,
		GracefulTimeout: 5 * time.Second,
		Force:           false,
		Verbose:         false,
	}
}

// DaemonUpgrader handles atomic daemon upgrades with locking.
type DaemonUpgrader struct {
	config   UpgradeConfig
	lockPath string   // Path to upgrade lock file
	lockFile *os.File // Active lock file handle
}

// NewDaemonUpgrader creates a new daemon upgrader.
func NewDaemonUpgrader(config UpgradeConfig) *DaemonUpgrader {
	if config.SocketPath == "" {
		config.SocketPath = DefaultSocketPath()
	}
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}
	if config.GracefulTimeout == 0 {
		config.GracefulTimeout = 5 * time.Second
	}

	lockPath := config.SocketPath + ".upgrade.lock"

	return &DaemonUpgrader{
		config:   config,
		lockPath: lockPath,
	}
}

// Upgrade performs an atomic daemon upgrade with the following steps:
//  1. Acquire upgrade lock (prevents concurrent upgrades)
//  2. Connect to running daemon and get current version
//  3. Request graceful shutdown (stops all processes)
//  4. Wait for daemon to exit
//  5. Clean up stale socket/PID files
//  6. Start new daemon binary
//  7. Wait for new daemon to be ready
//  8. Verify new daemon version
//  9. Release upgrade lock
func (u *DaemonUpgrader) Upgrade(ctx context.Context) error {
	// Step 1: Acquire upgrade lock
	if err := u.acquireLock(); err != nil {
		return fmt.Errorf("failed to acquire upgrade lock: %w", err)
	}
	defer u.releaseLock()

	u.log("Starting daemon upgrade...")

	// Step 2: Connect to running daemon and get version
	client := NewClient(WithSocketPath(u.config.SocketPath))
	err := client.Connect()
	if err != nil {
		if errors.Is(err, ErrSocketNotFound) {
			// No daemon running - just start new one
			u.log("No daemon running, starting new daemon...")
			return u.startNewDaemon(ctx)
		}
		return fmt.Errorf("failed to connect to daemon: %w", err)
	}

	// Get current daemon info
	info, err := client.Info()
	if err != nil {
		client.Close()
		return fmt.Errorf("failed to get daemon info: %w", err)
	}

	u.log("Current daemon version: %s", info.Version)

	// Check if upgrade is needed
	newVersion := u.getNewVersion()
	if !u.config.Force && VersionsMatch(info.Version, newVersion) {
		client.Close()
		u.log("Daemon already at version %s, no upgrade needed", newVersion)
		return nil
	}

	u.log("Upgrading daemon from %s to %s...", info.Version, newVersion)

	// Step 3: Request graceful shutdown
	u.log("Requesting graceful shutdown...")
	if err := client.Shutdown(); err != nil {
		client.Close()
		return fmt.Errorf("failed to request shutdown: %w", err)
	}
	client.Close()

	// Step 4: Wait for daemon to exit
	u.log("Waiting for daemon to exit...")
	if err := u.waitForDaemonExit(ctx); err != nil {
		return fmt.Errorf("daemon did not exit: %w", err)
	}

	// Step 5: Clean up stale files
	u.log("Cleaning up stale files...")
	u.cleanupStaleFiles()

	// Step 6: Start new daemon
	u.log("Starting new daemon...")
	if err := u.startNewDaemon(ctx); err != nil {
		return fmt.Errorf("failed to start new daemon: %w", err)
	}

	// Step 7: Wait for new daemon to be ready
	u.log("Waiting for new daemon to be ready...")
	if err := u.waitForDaemonReady(ctx); err != nil {
		return fmt.Errorf("new daemon failed to start: %w", err)
	}

	// Step 8: Verify new daemon version
	u.log("Verifying new daemon version...")
	if err := u.verifyNewVersion(newVersion); err != nil {
		return fmt.Errorf("version verification failed: %w", err)
	}

	u.log("✓ Upgrade complete! Daemon now at version %s", newVersion)
	return nil
}

// acquireLock acquires the upgrade lock file.
// Returns error if another upgrade is in progress or lock acquisition fails.
func (u *DaemonUpgrader) acquireLock() error {
	// Try to create lock file exclusively
	f, err := os.OpenFile(u.lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		// Lock file exists - check if stale
		if !os.IsExist(err) {
			return err
		}

		// Check if lock is stale (>5 minutes old)
		info, statErr := os.Stat(u.lockPath)
		if statErr == nil && time.Since(info.ModTime()) > 5*time.Minute {
			u.log("Removing stale upgrade lock (>5 minutes old)")
			os.Remove(u.lockPath)
			// Retry
			return u.acquireLock()
		}

		return errors.New("upgrade already in progress (lock file exists)")
	}

	// Write PID to lock file
	fmt.Fprintf(f, "%d\n", os.Getpid())
	u.lockFile = f
	return nil
}

// releaseLock releases the upgrade lock.
func (u *DaemonUpgrader) releaseLock() {
	if u.lockFile != nil {
		u.lockFile.Close()
		os.Remove(u.lockPath)
		u.lockFile = nil
	}
}

// waitForDaemonExit waits for the daemon to exit.
func (u *DaemonUpgrader) waitForDaemonExit(ctx context.Context) error {
	deadline := time.Now().Add(u.config.GracefulTimeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return errors.New("timeout waiting for daemon to exit")
			}

			// Check if daemon is still running
			if !IsRunning(u.config.SocketPath) {
				return nil
			}
		}
	}
}

// cleanupStaleFiles removes stale socket and PID files.
func (u *DaemonUpgrader) cleanupStaleFiles() {
	// Remove socket file
	if err := os.Remove(u.config.SocketPath); err != nil && !os.IsNotExist(err) {
		u.log("Warning: failed to remove socket file: %v", err)
	}

	// Remove PID file (platform-specific location)
	pidFile := u.config.SocketPath + ".pid"
	if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
		u.log("Warning: failed to remove PID file: %v", err)
	}
}

// startNewDaemon starts the new daemon binary.
func (u *DaemonUpgrader) startNewDaemon(ctx context.Context) error {
	binaryPath := u.config.NewBinaryPath
	if binaryPath == "" {
		// Use current executable
		var err error
		binaryPath, err = os.Executable()
		if err != nil {
			return fmt.Errorf("failed to get executable path: %w", err)
		}

		// Try to use dedicated daemon binary if it exists
		daemonPath := binaryPath + "-daemon"
		if _, err := os.Stat(daemonPath); err == nil {
			binaryPath = daemonPath
		}
	}

	// Use AutoStartClient to start daemon
	config := AutoStartConfig{
		SocketPath:    u.config.SocketPath,
		DaemonPath:    binaryPath,
		StartTimeout:  u.config.Timeout,
		RetryInterval: 100 * time.Millisecond,
		MaxRetries:    50,
	}

	client := NewAutoStartClient(config)
	if err := client.Connect(); err != nil {
		return err
	}
	client.Close()

	return nil
}

// waitForDaemonReady waits for the new daemon to be ready and accepting connections.
func (u *DaemonUpgrader) waitForDaemonReady(ctx context.Context) error {
	deadline := time.Now().Add(u.config.Timeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return errors.New("timeout waiting for daemon to be ready")
			}

			// Try to ping daemon
			client := NewClient(WithSocketPath(u.config.SocketPath))
			if err := client.Connect(); err == nil {
				if err := client.Ping(); err == nil {
					client.Close()
					return nil
				}
				client.Close()
			}
		}
	}
}

// verifyNewVersion verifies that the new daemon is running the expected version.
func (u *DaemonUpgrader) verifyNewVersion(expectedVersion string) error {
	client := NewClient(WithSocketPath(u.config.SocketPath))
	if err := client.Connect(); err != nil {
		return err
	}
	defer client.Close()

	info, err := client.Info()
	if err != nil {
		return err
	}

	if !VersionsMatch(info.Version, expectedVersion) {
		return fmt.Errorf("version mismatch after upgrade: expected %s, got %s",
			expectedVersion, info.Version)
	}

	return nil
}

// getNewVersion returns the version of the new daemon binary.
func (u *DaemonUpgrader) getNewVersion() string {
	binaryPath := u.config.NewBinaryPath
	if binaryPath == "" {
		// Use current executable version (from build-time injection)
		return Version
	}

	// Exec the binary with --version to get its actual version
	cmd := exec.Command(binaryPath, "--version")
	output, err := cmd.Output()
	if err != nil {
		u.log("Warning: could not get version from %s: %v", binaryPath, err)
		return Version
	}

	// Parse "agnt vX.Y.Z" format
	version := strings.TrimSpace(string(output))
	version = strings.TrimPrefix(version, "agnt ")
	version = strings.TrimPrefix(version, "v")
	return version
}

// log prints a message if verbose logging is enabled.
func (u *DaemonUpgrader) log(format string, args ...interface{}) {
	if u.config.Verbose {
		log.Printf("[Upgrade] "+format, args...)
	}
}

// UpgradeDaemon is a convenience function for upgrading the daemon.
// It creates a DaemonUpgrader with the given config and runs the upgrade.
func UpgradeDaemon(ctx context.Context, config UpgradeConfig) error {
	upgrader := NewDaemonUpgrader(config)
	return upgrader.Upgrade(ctx)
}
