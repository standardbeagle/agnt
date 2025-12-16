package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/process"
)

// findAgntBinary finds the agnt binary for testing.
// Returns the path to the binary or skips the test if not found.
func findAgntBinary(t *testing.T) string {
	wd, _ := os.Getwd()
	// Navigate from internal/daemon to project root
	projectRoot := filepath.Join(wd, "..", "..")
	daemonPath := filepath.Join(projectRoot, "agnt")

	// On Windows, add .exe extension
	if _, err := os.Stat(daemonPath); os.IsNotExist(err) {
		daemonPath += ".exe"
	}

	if _, err := os.Stat(daemonPath); os.IsNotExist(err) {
		t.Skipf("agnt binary not found at %s - run 'make build' first", daemonPath)
	}

	return daemonPath
}

// TestDaemonUpgrade_FullCycle tests the complete upgrade cycle
func TestDaemonUpgrade_FullCycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Find the agnt binary for testing
	daemonPath := findAgntBinary(t)

	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Start daemon
	daemon := New(DaemonConfig{
		SocketPath: sockPath,
		ProcessConfig: process.ManagerConfig{
			DefaultTimeout:    0,
			MaxOutputBuffer:   64 * 1024,
			GracefulTimeout:   2 * time.Second,
			HealthCheckPeriod: 0, // Disable health checks for test
		},
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	if err := daemon.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}

	// Wait for daemon to be ready
	time.Sleep(500 * time.Millisecond)

	// Verify daemon is running
	if !IsRunning(sockPath) {
		t.Fatal("Daemon not running after start")
	}

	// Get daemon info
	client := NewClient(WithSocketPath(sockPath))
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect to daemon: %v", err)
	}

	info, err := client.Info()
	if err != nil {
		client.Close()
		t.Fatalf("Failed to get daemon info: %v", err)
	}
	client.Close()

	t.Logf("Initial daemon version: %s", info.Version)
	initialUptime := info.Uptime

	// Create upgrader
	upgrader := NewDaemonUpgrader(UpgradeConfig{
		SocketPath:      sockPath,
		NewBinaryPath:   daemonPath, // Use actual agnt binary
		Timeout:         10 * time.Second,
		GracefulTimeout: 2 * time.Second,
		Force:           true, // Force upgrade even though version matches
		Verbose:         testing.Verbose(),
	})

	// Run upgrade
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	t.Log("Starting upgrade...")
	if err := upgrader.Upgrade(ctx); err != nil {
		t.Fatalf("Upgrade failed: %v", err)
	}

	// Verify new daemon is running
	if !IsRunning(sockPath) {
		t.Fatal("Daemon not running after upgrade")
	}

	// Connect to new daemon
	client2 := NewClient(WithSocketPath(sockPath))
	if err := client2.Connect(); err != nil {
		t.Fatalf("Failed to connect to new daemon: %v", err)
	}
	defer client2.Close()

	// Get new daemon info
	info2, err := client2.Info()
	if err != nil {
		t.Fatalf("Failed to get new daemon info: %v", err)
	}

	t.Logf("Post-upgrade daemon version: %s", info2.Version)

	// Version should match
	if info2.Version != info.Version {
		t.Errorf("Version mismatch after upgrade: expected %s, got %s",
			info.Version, info2.Version)
	}

	// Uptime should be less (daemon was restarted)
	if info2.Uptime >= initialUptime {
		t.Errorf("Daemon was not restarted: uptime %v >= initial %v",
			info2.Uptime, initialUptime)
	}

	// Shutdown for cleanup
	if err := client2.Shutdown(); err != nil {
		t.Errorf("Failed to shutdown daemon: %v", err)
	}

	// Wait for shutdown
	time.Sleep(time.Second)

	if IsRunning(sockPath) {
		t.Error("Daemon still running after shutdown")
	}
}

// TestUpgradeLock_ConcurrentAttempts tests that concurrent upgrade attempts are blocked
func TestUpgradeLock_ConcurrentAttempts(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Find the agnt binary for testing
	daemonPath := findAgntBinary(t)

	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Start daemon
	daemon := New(DaemonConfig{
		SocketPath: sockPath,
		ProcessConfig: process.ManagerConfig{
			DefaultTimeout:    0,
			MaxOutputBuffer:   64 * 1024,
			GracefulTimeout:   2 * time.Second,
			HealthCheckPeriod: 0,
		},
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	if err := daemon.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer func() {
		client := NewClient(WithSocketPath(sockPath))
		if err := client.Connect(); err == nil {
			client.Shutdown()
			client.Close()
		}
	}()

	time.Sleep(500 * time.Millisecond)

	// Create two upgraders
	upgrader1 := NewDaemonUpgrader(UpgradeConfig{
		SocketPath:      sockPath,
		NewBinaryPath:   daemonPath, // Use actual agnt binary
		Timeout:         10 * time.Second,
		GracefulTimeout: 2 * time.Second,
		Force:           true,
		Verbose:         false,
	})

	upgrader2 := NewDaemonUpgrader(UpgradeConfig{
		SocketPath:      sockPath,
		NewBinaryPath:   daemonPath, // Use actual agnt binary
		Timeout:         10 * time.Second,
		GracefulTimeout: 2 * time.Second,
		Force:           true,
		Verbose:         false,
	})

	// Start first upgrade in goroutine
	errCh1 := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		errCh1 <- upgrader1.Upgrade(ctx)
	}()

	// Give first upgrade time to acquire lock
	time.Sleep(100 * time.Millisecond)

	// Try second upgrade (should fail with lock error)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()

	err2 := upgrader2.Upgrade(ctx2)
	if err2 == nil {
		t.Fatal("Second upgrade should have failed with lock error, but succeeded")
	}

	if !stringContains(err2.Error(), "lock") && !stringContains(err2.Error(), "progress") {
		t.Errorf("Expected lock error, got: %v", err2)
	}

	t.Logf("Second upgrade correctly blocked: %v", err2)

	// Wait for first upgrade to complete
	err1 := <-errCh1
	if err1 != nil {
		t.Errorf("First upgrade failed: %v", err1)
	}
}

// TestUpgradeStaleSocket tests upgrade when socket exists but daemon is not running
func TestUpgradeStaleSocket(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Find the agnt binary for testing
	daemonPath := findAgntBinary(t)

	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Create a stale socket file
	f, err := os.Create(sockPath)
	if err != nil {
		t.Fatalf("Failed to create stale socket: %v", err)
	}
	f.Close()

	// Verify daemon is not running
	if IsRunning(sockPath) {
		t.Fatal("Daemon should not be running with stale socket")
	}

	// Create upgrader
	upgrader := NewDaemonUpgrader(UpgradeConfig{
		SocketPath:      sockPath,
		NewBinaryPath:   daemonPath, // Use actual agnt binary
		Timeout:         10 * time.Second,
		GracefulTimeout: 2 * time.Second,
		Force:           false,
		Verbose:         testing.Verbose(),
	})

	// Run upgrade (should start new daemon since old one isn't running)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	t.Log("Starting upgrade with stale socket...")
	if err := upgrader.Upgrade(ctx); err != nil {
		t.Fatalf("Upgrade failed: %v", err)
	}

	// Verify new daemon is running
	if !IsRunning(sockPath) {
		t.Fatal("Daemon not running after upgrade")
	}

	// Cleanup
	client := NewClient(WithSocketPath(sockPath))
	if err := client.Connect(); err == nil {
		client.Shutdown()
		client.Close()
	}
}

// TestUpgradeVersionCheck tests that upgrade respects version matching
func TestUpgradeVersionCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Find the agnt binary for testing
	daemonPath := findAgntBinary(t)

	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Start daemon
	daemon := New(DaemonConfig{
		SocketPath: sockPath,
		ProcessConfig: process.ManagerConfig{
			DefaultTimeout:    0,
			MaxOutputBuffer:   64 * 1024,
			GracefulTimeout:   2 * time.Second,
			HealthCheckPeriod: 0,
		},
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	if err := daemon.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer func() {
		client := NewClient(WithSocketPath(sockPath))
		if err := client.Connect(); err == nil {
			client.Shutdown()
			client.Close()
		}
	}()

	time.Sleep(500 * time.Millisecond)

	// Create upgrader WITHOUT force flag
	upgrader := NewDaemonUpgrader(UpgradeConfig{
		SocketPath:      sockPath,
		NewBinaryPath:   daemonPath, // Use actual agnt binary
		Timeout:         10 * time.Second,
		GracefulTimeout: 2 * time.Second,
		Force:           false, // Don't force upgrade
		Verbose:         testing.Verbose(),
	})

	// Run upgrade (should be no-op since versions match)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	t.Log("Starting upgrade without force flag (versions match)...")
	if err := upgrader.Upgrade(ctx); err != nil {
		t.Fatalf("Upgrade failed: %v", err)
	}

	// Daemon should still be running
	if !IsRunning(sockPath) {
		t.Fatal("Daemon not running after upgrade")
	}

	t.Log("Upgrade correctly detected matching versions (no-op)")
}

// Helper function to check if a string contains a substring
func stringContains(s, substr string) bool {
	return strings.Contains(s, substr)
}
