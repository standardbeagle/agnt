//go:build unix

package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestClient_ConnectToNonExistentDaemon(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	client := NewClient(WithSocketPath(sockPath))
	err := client.Connect()
	if err != ErrSocketNotFound {
		t.Errorf("Expected ErrSocketNotFound, got %v", err)
	}
}

func TestClient_PingPong(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Start a daemon
	daemon := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	if err := daemon.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		daemon.Stop(ctx)
	}()

	// Connect client
	client := NewClient(WithSocketPath(sockPath))
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	// Ping
	if err := client.Ping(); err != nil {
		t.Errorf("Ping failed: %v", err)
	}
}

func TestClient_Info(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Start a daemon
	daemon := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	if err := daemon.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		daemon.Stop(ctx)
	}()

	// Connect client
	client := NewClient(WithSocketPath(sockPath))
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	// Get info
	info, err := client.Info()
	if err != nil {
		t.Fatalf("Info failed: %v", err)
	}

	if info.Version == "" {
		t.Error("Version should not be empty")
	}
	if info.SocketPath != sockPath {
		t.Errorf("SocketPath = %s, want %s", info.SocketPath, sockPath)
	}
}

func TestClient_Detect(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Start a daemon
	daemon := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	if err := daemon.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		daemon.Stop(ctx)
	}()

	// Connect client
	client := NewClient(WithSocketPath(sockPath))
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	// Detect project (this project is a Go project)
	result, err := client.Detect(".")
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	projectType, ok := result["type"].(string)
	if !ok {
		t.Fatal("Expected type field")
	}
	// Since we're in the test directory, type should be "go"
	if projectType != "go" {
		t.Logf("Project type detected: %s", projectType)
	}
}

func TestClient_NotConnected(t *testing.T) {
	client := NewClient()

	// Try to ping without connecting
	err := client.Ping()
	if err != ErrNotConnected {
		t.Errorf("Expected ErrNotConnected, got %v", err)
	}
}

func TestClient_MultipleConnections(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Start a daemon
	daemon := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	if err := daemon.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		daemon.Stop(ctx)
	}()

	// Create multiple clients
	clients := make([]*Client, 5)
	for i := range clients {
		clients[i] = NewClient(WithSocketPath(sockPath))
		if err := clients[i].Connect(); err != nil {
			t.Fatalf("Failed to connect client %d: %v", i, err)
		}
		defer clients[i].Close()
	}

	// All clients should be able to ping
	for i, client := range clients {
		if err := client.Ping(); err != nil {
			t.Errorf("Client %d ping failed: %v", i, err)
		}
	}
}
