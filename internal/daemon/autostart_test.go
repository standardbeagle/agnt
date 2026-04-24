package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAutoStartDaemon(t *testing.T) {
	t.Parallel()
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

	// Ensure cleanup happens even if test fails — force kill by PID if graceful fails
	defer func() {
		_ = StopDaemon(socketPath)
		time.Sleep(200 * time.Millisecond)
		// Force kill any process still using this socket path
		out, err := exec.Command("pgrep", "-f", socketPath).Output()
		if err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				pid, err := strconv.Atoi(strings.TrimSpace(line))
				if err != nil || pid <= 0 {
					continue
				}
				if p, err := os.FindProcess(pid); err == nil {
					_ = p.Signal(syscall.SIGKILL)
					_, _ = p.Wait()
				}
			}
		}
		os.Remove(socketPath)
	}()

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
}

func TestDefaultAutoStartConfig(t *testing.T) {
	t.Parallel()
	config := DefaultAutoStartConfig()

	if config.SocketPath == "" {
		t.Error("Expected non-empty socket path")
	}
	if config.StartTimeout == 0 {
		t.Error("Expected non-zero start timeout")
	}
	if config.RetryInterval == 0 {
		t.Error("Expected non-zero retry interval")
	}
	if config.MaxRetries == 0 {
		t.Error("Expected non-zero max retries")
	}
}

func TestAutoStartConfig_toLibraryConfig(t *testing.T) {
	t.Parallel()
	config := AutoStartConfig{
		SocketPath:    "/tmp/test.sock",
		DaemonPath:    "/usr/bin/daemon",
		StartTimeout:  10 * time.Second,
		RetryInterval: 200 * time.Millisecond,
		MaxRetries:    20,
	}

	libConfig := config.toLibraryConfig()

	if libConfig.SocketPath != config.SocketPath {
		t.Errorf("Expected SocketPath %s, got %s", config.SocketPath, libConfig.SocketPath)
	}
	if libConfig.HubPath != config.DaemonPath {
		t.Errorf("Expected HubPath %s, got %s", config.DaemonPath, libConfig.HubPath)
	}
	if libConfig.StartTimeout != config.StartTimeout {
		t.Errorf("Expected StartTimeout %v, got %v", config.StartTimeout, libConfig.StartTimeout)
	}
	if libConfig.RetryInterval != config.RetryInterval {
		t.Errorf("Expected RetryInterval %v, got %v", config.RetryInterval, libConfig.RetryInterval)
	}
	if libConfig.MaxRetries != config.MaxRetries {
		t.Errorf("Expected MaxRetries %d, got %d", config.MaxRetries, libConfig.MaxRetries)
	}
	if libConfig.ProcessMatcher == nil {
		t.Error("Expected ProcessMatcher to be set")
	}
}

func TestNewAutoStartClient(t *testing.T) {
	t.Parallel()
	config := AutoStartConfig{
		SocketPath:    "/tmp/test-newclient.sock",
		DaemonPath:    "/usr/bin/daemon",
		StartTimeout:  5 * time.Second,
		RetryInterval: 100 * time.Millisecond,
		MaxRetries:    10,
	}

	client := NewAutoStartClient(config)
	if client == nil {
		t.Fatal("Expected non-nil client")
	}
	if client.Client == nil {
		t.Error("Expected non-nil embedded Client")
	}
}
