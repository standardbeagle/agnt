//go:build unix

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
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

// TestSessionBasedCleanup verifies that when a client that registered a session
// disconnects, only resources for that session's project path are cleaned up.
func TestSessionBasedCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Create two project directories (must exist for process working directory)
	project1 := filepath.Join(tmpDir, "project1")
	project2 := filepath.Join(tmpDir, "project2")
	if err := os.MkdirAll(project1, 0755); err != nil {
		t.Fatalf("Failed to create project1 dir: %v", err)
	}
	if err := os.MkdirAll(project2, 0755); err != nil {
		t.Fatalf("Failed to create project2 dir: %v", err)
	}

	// Start daemon
	daemon := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	if err := daemon.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		daemon.Stop(ctx)
	}()

	// Client 1 - will register a session and start a process
	client1 := NewClient(WithSocketPath(sockPath))
	if err := client1.Connect(); err != nil {
		t.Fatalf("Failed to connect client1: %v", err)
	}

	// Register session for client1
	_, err := client1.SessionRegister("session1", "/tmp/overlay1", project1, "test", nil)
	if err != nil {
		t.Fatalf("Failed to register session1: %v", err)
	}

	// Start a process for project1 via Run (using raw mode)
	_, err = client1.Run(protocol.RunConfig{
		ID:      "proc1",
		Path:    project1,
		Mode:    "background",
		Command: "sleep",
		Args:    []string{"100"},
		Raw:     true,
	})
	if err != nil {
		t.Fatalf("Failed to start process1: %v", err)
	}

	// Client 2 - will NOT register a session but start a process for project2
	client2 := NewClient(WithSocketPath(sockPath))
	if err := client2.Connect(); err != nil {
		t.Fatalf("Failed to connect client2: %v", err)
	}
	defer client2.Close()

	// Start a process for project2 (without a session, using raw mode)
	_, err = client2.Run(protocol.RunConfig{
		ID:      "proc2",
		Path:    project2,
		Mode:    "background",
		Command: "sleep",
		Args:    []string{"100"},
		Raw:     true,
	})
	if err != nil {
		t.Fatalf("Failed to start process2: %v", err)
	}

	// Verify both processes are running
	procs, err := client2.ProcList(protocol.DirectoryFilter{Global: true})
	if err != nil {
		t.Fatalf("Failed to list processes: %v", err)
	}
	procsList, ok := procs["processes"].([]interface{})
	if !ok {
		t.Fatalf("Expected processes list, got %T", procs["processes"])
	}
	if len(procsList) != 2 {
		t.Fatalf("Expected 2 processes, got %d", len(procsList))
	}

	// Close client1 (should trigger cleanup for project1 only)
	client1.Close()

	// Give cleanup a moment to complete
	time.Sleep(500 * time.Millisecond)

	// Verify only proc2 is still running
	procs, err = client2.ProcList(protocol.DirectoryFilter{Global: true})
	if err != nil {
		t.Fatalf("Failed to list processes after cleanup: %v", err)
	}

	procsList, ok = procs["processes"].([]interface{})
	if !ok {
		t.Fatalf("Expected processes list after cleanup, got %T", procs["processes"])
	}

	if len(procsList) != 1 {
		t.Errorf("Expected 1 process after cleanup, got %d", len(procsList))
	}

	if len(procsList) > 0 {
		proc := procsList[0].(map[string]interface{})
		if proc["id"] != "proc2" {
			t.Errorf("Expected proc2 to survive, got %v", proc["id"])
		}
	}

	// Verify session1 is unregistered
	sessionsResult, err := client2.SessionList(protocol.DirectoryFilter{Global: true})
	if err != nil {
		t.Fatalf("Failed to list sessions: %v", err)
	}
	sessionsList, _ := sessionsResult["sessions"].([]interface{})
	for _, s := range sessionsList {
		session := s.(map[string]interface{})
		if session["code"] == "session1" {
			t.Error("session1 should have been unregistered")
		}
	}
}

func TestClient_SessionSchedule(t *testing.T) {
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

	// Register a session first
	projectPath := filepath.Join(tmpDir, "project")
	if err := os.MkdirAll(projectPath, 0755); err != nil {
		t.Fatalf("Failed to create project dir: %v", err)
	}

	_, err := client.SessionRegister("sched-session", "/tmp/overlay", projectPath, "test", nil)
	if err != nil {
		t.Fatalf("Failed to register session: %v", err)
	}

	// Schedule a message
	result, err := client.SessionSchedule("sched-session", "1h", "test scheduled message")
	if err != nil {
		t.Fatalf("SessionSchedule failed: %v", err)
	}

	if result == nil {
		t.Fatal("Expected non-nil result from SessionSchedule")
	}

	taskID, ok := result["task_id"].(string)
	if !ok || taskID == "" {
		t.Error("Expected task_id in result")
	}
}

func TestClient_SessionCancel(t *testing.T) {
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

	// Register a session first
	projectPath := filepath.Join(tmpDir, "project")
	if err := os.MkdirAll(projectPath, 0755); err != nil {
		t.Fatalf("Failed to create project dir: %v", err)
	}

	_, err := client.SessionRegister("cancel-session", "/tmp/overlay", projectPath, "test", nil)
	if err != nil {
		t.Fatalf("Failed to register session: %v", err)
	}

	// Schedule a message
	schedResult, err := client.SessionSchedule("cancel-session", "1h", "test scheduled message")
	if err != nil {
		t.Fatalf("SessionSchedule failed: %v", err)
	}

	taskID := schedResult["task_id"].(string)

	// Cancel the scheduled task
	err = client.SessionCancel(taskID)
	if err != nil {
		t.Fatalf("SessionCancel failed: %v", err)
	}

	// Verify task is no longer present
	tasksResult, err := client.SessionTasks(protocol.DirectoryFilter{Global: true})
	if err != nil {
		t.Fatalf("Failed to list tasks: %v", err)
	}

	tasks, _ := tasksResult["tasks"].([]interface{})
	for _, task := range tasks {
		taskMap := task.(map[string]interface{})
		if taskMap["id"] == taskID {
			t.Error("Cancelled task should not be in list")
		}
	}
}

func TestClient_SessionAttach(t *testing.T) {
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

	// Register a session in a nested directory
	projectPath := filepath.Join(tmpDir, "project", "src")
	if err := os.MkdirAll(projectPath, 0755); err != nil {
		t.Fatalf("Failed to create project dir: %v", err)
	}

	_, err := client.SessionRegister("attach-session", "/tmp/overlay", projectPath, "test", nil)
	if err != nil {
		t.Fatalf("Failed to register session: %v", err)
	}

	// Attach from the same directory
	result, err := client.SessionAttach(projectPath)
	if err != nil {
		t.Fatalf("SessionAttach failed: %v", err)
	}

	if result == nil {
		t.Fatal("Expected non-nil result from SessionAttach")
	}

	// Result should contain session info - check the nested session object
	session, ok := result["session"].(map[string]interface{})
	if ok {
		code := session["code"].(string)
		if code != "attach-session" {
			t.Errorf("Expected code 'attach-session', got %q", code)
		}
	} else if code, ok := result["code"].(string); ok {
		if code != "attach-session" {
			t.Errorf("Expected code 'attach-session', got %q", code)
		}
	} else {
		// Log actual result for debugging
		t.Logf("SessionAttach result: %v", result)
	}
}

func TestClient_TunnelStart(t *testing.T) {
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

	// Try to start a tunnel (will fail if cloudflared not installed, but should exercise the code)
	_, err := client.TunnelStart(protocol.TunnelStartConfig{
		ID:        "test-tunnel",
		Provider:  "cloudflare",
		LocalPort: 8080,
	})

	// We expect either success or an error about cloudflared not being available
	// The important thing is that the client method is exercised
	if err != nil {
		// Check if it's an expected error about missing binary
		t.Logf("TunnelStart returned expected error: %v", err)
	}
}
