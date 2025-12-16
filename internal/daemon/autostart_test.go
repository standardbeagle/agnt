package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAutoStartDaemon(t *testing.T) {
	// Use a unique socket for this test (platform-appropriate path)
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "test-autostart.sock")

	// Ensure any existing daemon is stopped
	StopDaemon(socketPath)
	os.Remove(socketPath)

	// Verify daemon is not running
	if IsDaemonRunning(socketPath) {
		t.Fatal("Daemon should not be running at start of test")
	}

	// Find the agnt binary - when running tests, os.Executable() returns the test binary,
	// so we need to explicitly set the daemon path
	wd, _ := os.Getwd()
	// Navigate from internal/daemon to project root
	projectRoot := filepath.Join(wd, "..", "..")
	daemonPath := filepath.Join(projectRoot, "agnt")

	// Check if binary exists
	if _, err := os.Stat(daemonPath); os.IsNotExist(err) {
		t.Skipf("agnt binary not found at %s - run 'make build' first", daemonPath)
	}

	// Create autostart client with test config
	config := AutoStartConfig{
		SocketPath:    socketPath,
		DaemonPath:    daemonPath,
		StartTimeout:  10 * time.Second,
		RetryInterval: 100 * time.Millisecond,
		MaxRetries:    100,
	}

	t.Logf("Using daemon path: %s", daemonPath)
	t.Logf("Config: %+v", config)

	client := NewAutoStartClient(config)

	t.Log("Attempting to connect (should autostart daemon)...")
	err := client.Connect()
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	t.Log("Connected successfully!")

	// Verify daemon is running
	if !IsDaemonRunning(socketPath) {
		t.Error("Daemon should be running after autostart")
	}

	// Cleanup
	StopDaemon(socketPath)
	os.Remove(socketPath)
}
