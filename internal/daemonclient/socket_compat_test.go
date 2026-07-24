package daemonclient

import (
	"os"
	"testing"
)

func TestDefaultSocketConfig(t *testing.T) {
	t.Parallel()
	config := DefaultSocketConfig()

	if config.Path == "" {
		t.Error("Expected non-empty socket path")
	}

	if config.Mode != 0600 {
		t.Errorf("Expected mode 0600, got %o", config.Mode)
	}
}

func TestDefaultSocketPath_SocketCompat(t *testing.T) {
	t.Parallel()
	path := DefaultSocketPath()
	if path == "" {
		t.Error("Expected non-empty socket path")
	}
	t.Logf("Default socket path: %s", path)
}

func TestDefaultSocketPath_AGNTDaemonSocketOverride(t *testing.T) {
	t.Setenv("AGNT_SOCKET", "")
	t.Setenv("AGNT_DAEMON_SOCKET", "/tmp/agnt-daemon-socket-override-test.sock")
	if got := DefaultSocketPath(); got != "/tmp/agnt-daemon-socket-override-test.sock" {
		t.Errorf("DefaultSocketPath() = %q, want AGNT_DAEMON_SOCKET value", got)
	}
}

func TestDefaultSocketPath_AGNTSocketWinsOverAGNTDaemonSocket(t *testing.T) {
	t.Setenv("AGNT_SOCKET", "/tmp/agnt-socket-wins-test.sock")
	t.Setenv("AGNT_DAEMON_SOCKET", "/tmp/agnt-daemon-socket-loses-test.sock")
	if got := DefaultSocketPath(); got != "/tmp/agnt-socket-wins-test.sock" {
		t.Errorf("DefaultSocketPath() = %q, want AGNT_SOCKET to win when both are set", got)
	}
}

func TestNewSocketManager(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	sockPath := tmpDir + "/test.sock"

	config := SocketConfig{
		Path: sockPath,
		Mode: 0600,
	}

	sm := NewSocketManager(config)
	if sm == nil {
		t.Fatal("Expected non-nil SocketManager")
	}

	// Check path accessor
	if sm.Path() != sockPath {
		t.Errorf("Expected path %s, got %s", sockPath, sm.Path())
	}
}

func TestNewSocketManager_DefaultPath(t *testing.T) {
	t.Parallel()
	// Test with empty path uses default
	config := SocketConfig{
		Path: "",
		Mode: 0600,
	}

	sm := NewSocketManager(config)
	if sm == nil {
		t.Fatal("Expected non-nil SocketManager")
	}

	// Path should be the default
	if sm.Path() == "" {
		t.Error("Expected non-empty default path")
	}
}

func TestSocketManager_ListenAndClose(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	sockPath := tmpDir + "/listen-test.sock"

	config := SocketConfig{
		Path: sockPath,
		Mode: 0600,
	}

	sm := NewSocketManager(config)

	// Listen
	listener, err := sm.Listen()
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}

	// Verify socket exists
	if _, err := os.Stat(sockPath); os.IsNotExist(err) {
		t.Error("Socket file should exist after listen")
	}

	// Close
	listener.Close()
	sm.Close()
}
