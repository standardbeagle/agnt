//go:build unix

package daemonclient

import (
	"path/filepath"
	"testing"
)

func TestClient_ConnectToNonExistentDaemon(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	client := NewClient(WithSocketPath(sockPath))
	err := client.Connect()
	if err != ErrSocketNotFound {
		t.Errorf("Expected ErrSocketNotFound, got %v", err)
	}
}

func TestClient_NotConnected(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "nonexistent.sock")

	client := NewClient(WithSocketPath(sockPath))

	// Try to ping without daemon running - should get ErrSocketNotFound
	// (The client attempts lazy connection on first operation)
	err := client.Ping()
	if err != ErrSocketNotFound {
		t.Errorf("Expected ErrSocketNotFound, got %v", err)
	}
}

// TestSessionBasedCleanup verifies that when a client that registered a session
// disconnects, only resources for that session's project path are cleaned up.
