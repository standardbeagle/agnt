package daemon

import (
	"os"
	"testing"
)

func TestDefaultSocketConfig(t *testing.T) {
	config := DefaultSocketConfig()

	if config.Path == "" {
		t.Error("Expected non-empty socket path")
	}

	if config.Mode != 0600 {
		t.Errorf("Expected mode 0600, got %o", config.Mode)
	}
}

func TestDefaultSocketPath_SocketCompat(t *testing.T) {
	path := DefaultSocketPath()
	if path == "" {
		t.Error("Expected non-empty socket path")
	}
	t.Logf("Default socket path: %s", path)
}

func TestNewSocketManager(t *testing.T) {
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
