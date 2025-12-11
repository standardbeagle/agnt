package process

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestProcessManager_StartCommand(t *testing.T) {
	pm := NewProcessManager(DefaultManagerConfig())
	defer pm.Shutdown(context.Background())

	ctx := context.Background()

	proc, err := pm.StartCommand(ctx, ProcessConfig{
		ID:          "test-echo",
		ProjectPath: "/tmp",
		Command:     "echo",
		Args:        []string{"hello", "world"},
	})
	if err != nil {
		t.Fatalf("StartCommand failed: %v", err)
	}

	// Wait for process to complete
	select {
	case <-proc.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("process did not complete in time")
	}

	// Check state
	if proc.State() != StateStopped {
		t.Errorf("expected state=stopped, got %s", proc.State())
	}

	// Check exit code
	if proc.ExitCode() != 0 {
		t.Errorf("expected exit_code=0, got %d", proc.ExitCode())
	}

	// Check output
	stdout, truncated := proc.Stdout()
	if truncated {
		t.Error("unexpected truncation")
	}
	expected := "hello world\n"
	if string(stdout) != expected {
		t.Errorf("expected stdout=%q, got %q", expected, string(stdout))
	}
}

func TestProcessManager_Stop(t *testing.T) {
	pm := NewProcessManager(ManagerConfig{
		MaxOutputBuffer: DefaultBufferSize,
		GracefulTimeout: 1 * time.Second,
	})
	defer pm.Shutdown(context.Background())

	ctx := context.Background()

	// Start a long-running process
	proc, err := pm.StartCommand(ctx, ProcessConfig{
		ID:          "test-sleep",
		ProjectPath: "/tmp",
		Command:     "sleep",
		Args:        []string{"60"},
	})
	if err != nil {
		t.Fatalf("StartCommand failed: %v", err)
	}

	// Give it time to start
	time.Sleep(100 * time.Millisecond)

	// Verify it's running
	if proc.State() != StateRunning {
		t.Errorf("expected state=running, got %s", proc.State())
	}

	// Stop the process
	if err := pm.Stop(ctx, proc.ID); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	// Verify it stopped
	if proc.State() != StateStopped && proc.State() != StateFailed {
		t.Errorf("expected state=stopped or failed, got %s", proc.State())
	}
}

func TestProcessManager_RunSync(t *testing.T) {
	pm := NewProcessManager(DefaultManagerConfig())
	defer pm.Shutdown(context.Background())

	ctx := context.Background()

	// Run a command synchronously
	exitCode, err := pm.RunSync(ctx, ProcessConfig{
		ID:          "test-sync",
		ProjectPath: "/tmp",
		Command:     "sh",
		Args:        []string{"-c", "exit 42"},
	})
	if err != nil {
		t.Fatalf("RunSync failed: %v", err)
	}

	if exitCode != 42 {
		t.Errorf("expected exit_code=42, got %d", exitCode)
	}
}

func TestProcessManager_Restart(t *testing.T) {
	pm := NewProcessManager(ManagerConfig{
		MaxOutputBuffer: DefaultBufferSize,
		GracefulTimeout: 1 * time.Second,
	})
	defer pm.Shutdown(context.Background())

	ctx := context.Background()

	// Start a process
	proc1, err := pm.StartCommand(ctx, ProcessConfig{
		ID:          "test-restart",
		ProjectPath: "/tmp",
		Command:     "sleep",
		Args:        []string{"60"},
	})
	if err != nil {
		t.Fatalf("StartCommand failed: %v", err)
	}
	pid1 := proc1.PID()

	time.Sleep(100 * time.Millisecond)

	// Restart it
	proc2, err := pm.Restart(ctx, "test-restart")
	if err != nil {
		t.Fatalf("Restart failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Should have different PID
	pid2 := proc2.PID()
	if pid1 == pid2 {
		t.Error("expected different PID after restart")
	}

	// Original should be stopped
	if proc1.State() != StateStopped && proc1.State() != StateFailed {
		t.Errorf("expected original to be stopped, got %s", proc1.State())
	}

	// New should be running
	if proc2.State() != StateRunning {
		t.Errorf("expected new process to be running, got %s", proc2.State())
	}

	// Cleanup
	pm.Stop(ctx, proc2.ID)
}

func TestProcessManager_DuplicateID(t *testing.T) {
	pm := NewProcessManager(DefaultManagerConfig())
	defer pm.Shutdown(context.Background())

	ctx := context.Background()

	// Start first process
	_, err := pm.StartCommand(ctx, ProcessConfig{
		ID:          "duplicate",
		ProjectPath: "/tmp",
		Command:     "sleep",
		Args:        []string{"60"},
	})
	if err != nil {
		t.Fatalf("first StartCommand failed: %v", err)
	}

	// Try to start another with same ID
	_, err = pm.StartCommand(ctx, ProcessConfig{
		ID:          "duplicate",
		ProjectPath: "/tmp",
		Command:     "echo",
		Args:        []string{"test"},
	})
	if err != ErrProcessExists {
		t.Errorf("expected ErrProcessExists, got %v", err)
	}
}

func TestProcessManager_OutputCapture(t *testing.T) {
	pm := NewProcessManager(DefaultManagerConfig())
	defer pm.Shutdown(context.Background())

	ctx := context.Background()

	// Create a temp script that writes to both stdout and stderr
	script := `echo "stdout line"
echo "stderr line" >&2
echo "stdout again"`

	proc, err := pm.StartCommand(ctx, ProcessConfig{
		ID:          "test-output",
		ProjectPath: "/tmp",
		Command:     "sh",
		Args:        []string{"-c", script},
	})
	if err != nil {
		t.Fatalf("StartCommand failed: %v", err)
	}

	<-proc.Done()

	stdout, _ := proc.Stdout()
	stderr, _ := proc.Stderr()

	if string(stdout) != "stdout line\nstdout again\n" {
		t.Errorf("unexpected stdout: %q", string(stdout))
	}
	if string(stderr) != "stderr line\n" {
		t.Errorf("unexpected stderr: %q", string(stderr))
	}
}

func TestProcessManager_Labels(t *testing.T) {
	pm := NewProcessManager(DefaultManagerConfig())
	defer pm.Shutdown(context.Background())

	ctx := context.Background()

	// Start processes with different labels
	proc1, _ := pm.StartCommand(ctx, ProcessConfig{
		ID:          "test1",
		ProjectPath: "/tmp",
		Command:     "sleep",
		Args:        []string{"60"},
		Labels:      map[string]string{"type": "test", "lang": "go"},
	})
	proc2, _ := pm.StartCommand(ctx, ProcessConfig{
		ID:          "test2",
		ProjectPath: "/tmp",
		Command:     "sleep",
		Args:        []string{"60"},
		Labels:      map[string]string{"type": "test", "lang": "python"},
	})
	pm.StartCommand(ctx, ProcessConfig{
		ID:          "lint1",
		ProjectPath: "/tmp",
		Command:     "sleep",
		Args:        []string{"60"},
		Labels:      map[string]string{"type": "lint", "lang": "go"},
	})

	// Filter by label
	tests := pm.ListByLabel("type", "test")
	if len(tests) != 2 {
		t.Errorf("expected 2 test processes, got %d", len(tests))
	}

	goProcs := pm.ListByLabel("lang", "go")
	if len(goProcs) != 2 {
		t.Errorf("expected 2 go processes, got %d", len(goProcs))
	}

	// Cleanup
	pm.Stop(ctx, proc1.ID)
	pm.Stop(ctx, proc2.ID)
	pm.Stop(ctx, "lint1")
}

func TestProcessManager_WorkingDirectory(t *testing.T) {
	pm := NewProcessManager(DefaultManagerConfig())
	defer pm.Shutdown(context.Background())

	ctx := context.Background()

	// Get temp dir
	tmpDir := os.TempDir()

	proc, err := pm.StartCommand(ctx, ProcessConfig{
		ID:          "test-pwd",
		ProjectPath: tmpDir,
		Command:     "pwd",
		Args:        nil,
	})
	if err != nil {
		t.Fatalf("StartCommand failed: %v", err)
	}

	<-proc.Done()

	stdout, _ := proc.Stdout()
	// The output should be the temp dir (may have trailing newline)
	got := string(stdout)
	if len(got) > 0 && got[len(got)-1] == '\n' {
		got = got[:len(got)-1]
	}

	// Resolve symlinks for comparison (macOS /tmp -> /private/tmp)
	expectedDir, _ := os.Readlink(tmpDir)
	if expectedDir == "" {
		expectedDir = tmpDir
	}

	if got != tmpDir && got != expectedDir {
		t.Errorf("expected pwd=%q or %q, got %q", tmpDir, expectedDir, got)
	}
}

func TestProcessManager_KillProcessByPort(t *testing.T) {
	pm := NewProcessManager(DefaultManagerConfig())
	defer pm.Shutdown(context.Background())

	ctx := context.Background()

	// Start a simple HTTP server on a random port (using Python as it's widely available)
	// We'll use port 0 to get an assigned port, but for testing cleanup, we'll use a specific port
	testPort := 18765 // Use a high port number unlikely to be in use

	// Start a process listening on the test port
	proc, err := pm.StartCommand(ctx, ProcessConfig{
		ID:          "test-http-server",
		ProjectPath: "/tmp",
		Command:     "python3",
		Args:        []string{"-m", "http.server", "--bind", "127.0.0.1", fmt.Sprintf("%d", testPort)},
	})
	if err != nil {
		t.Fatalf("StartCommand failed: %v", err)
	}

	// Give the server time to start
	time.Sleep(500 * time.Millisecond)

	// Verify the process is running
	if proc.State() != StateRunning {
		t.Fatalf("expected process to be running, got %s", proc.State())
	}

	// Try to clean up the port
	pids, err := pm.KillProcessByPort(ctx, testPort)
	if err != nil {
		t.Fatalf("KillProcessByPort failed: %v", err)
	}

	// Should have killed at least one process
	if len(pids) == 0 {
		t.Error("expected to kill at least one process")
	}

	// Verify the Python process was killed
	select {
	case <-proc.Done():
		// Process exited, which is expected
	case <-time.After(2 * time.Second):
		t.Error("process did not exit after port cleanup")
	}

	// Test cleanup of non-existent port (should not error)
	pids, err = pm.KillProcessByPort(ctx, 54321)
	if err != nil {
		t.Errorf("KillProcessByPort should not error on non-existent port: %v", err)
	}
	if len(pids) != 0 {
		t.Errorf("expected 0 PIDs for non-existent port, got %d", len(pids))
	}
}

func TestProcessManager_KillProcessByPort_InvalidPort(t *testing.T) {
	pm := NewProcessManager(DefaultManagerConfig())
	defer pm.Shutdown(context.Background())

	ctx := context.Background()

	// Test with invalid port numbers (these should still work with lsof, just return no results)
	testCases := []int{0, -1, 65536, 100000}

	for _, port := range testCases {
		pids, _ := pm.KillProcessByPort(ctx, port)
		// These should just return empty results, not error
		if len(pids) != 0 {
			t.Errorf("port %d: expected 0 PIDs, got %d", port, len(pids))
		}
	}
}

func TestProcessManager_AggressiveShutdown(t *testing.T) {
	pm := NewProcessManager(DefaultManagerConfig())

	ctx := context.Background()

	// Start a long-running process
	proc, err := pm.StartCommand(ctx, ProcessConfig{
		ID:          "test-sleep",
		ProjectPath: "/tmp",
		Command:     "sleep",
		Args:        []string{"300"}, // 5 minutes - should not complete normally
	})
	if err != nil {
		t.Fatalf("StartCommand failed: %v", err)
	}

	// Give process time to start
	time.Sleep(100 * time.Millisecond)

	// Verify it's running
	if proc.State() != StateRunning {
		t.Fatalf("expected process to be running, got %s", proc.State())
	}

	// Use aggressive shutdown with tight deadline (1 second)
	shutdownStart := time.Now()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	err = pm.Shutdown(shutdownCtx)
	shutdownDuration := time.Since(shutdownStart)

	// Shutdown should complete quickly (under 500ms for aggressive mode)
	if shutdownDuration > 500*time.Millisecond {
		t.Errorf("aggressive shutdown took too long: %v (expected <500ms)", shutdownDuration)
	}

	// Process should be stopped (either StateStopped or StateFailed)
	state := proc.State()
	if state != StateStopped && state != StateFailed {
		t.Errorf("expected process to be stopped, got %s", state)
	}

	t.Logf("Aggressive shutdown completed in %v", shutdownDuration)
}

func TestProcessManager_GracefulShutdown(t *testing.T) {
	pm := NewProcessManager(DefaultManagerConfig())

	ctx := context.Background()

	// Start a process that will exit quickly
	proc, err := pm.StartCommand(ctx, ProcessConfig{
		ID:          "test-echo",
		ProjectPath: "/tmp",
		Command:     "echo",
		Args:        []string{"test"},
	})
	if err != nil {
		t.Fatalf("StartCommand failed: %v", err)
	}

	// Wait for it to complete
	<-proc.Done()

	// Use graceful shutdown with long deadline (10 seconds)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = pm.Shutdown(shutdownCtx)
	if err != nil {
		t.Errorf("graceful shutdown failed: %v", err)
	}
}

func TestProcessManager_StartOrReuse_NewProcess(t *testing.T) {
	pm := NewProcessManager(DefaultManagerConfig())
	defer pm.Shutdown(context.Background())

	ctx := context.Background()

	result, err := pm.StartOrReuse(ctx, ProcessConfig{
		ID:          "test-new",
		ProjectPath: "/tmp",
		Command:     "echo",
		Args:        []string{"hello"},
	})
	if err != nil {
		t.Fatalf("StartOrReuse failed: %v", err)
	}

	if result.Reused {
		t.Error("expected Reused=false for new process")
	}
	if result.Cleaned {
		t.Error("expected Cleaned=false for new process")
	}
	if result.PortRetried {
		t.Error("expected PortRetried=false for new process")
	}
	if result.Process == nil {
		t.Fatal("expected Process to be set")
	}

	<-result.Process.Done()
}

func TestProcessManager_StartOrReuse_ReusesRunning(t *testing.T) {
	pm := NewProcessManager(DefaultManagerConfig())
	defer pm.Shutdown(context.Background())

	ctx := context.Background()

	// Start a long-running process
	result1, err := pm.StartOrReuse(ctx, ProcessConfig{
		ID:          "test-reuse",
		ProjectPath: "/tmp",
		Command:     "sleep",
		Args:        []string{"60"},
	})
	if err != nil {
		t.Fatalf("first StartOrReuse failed: %v", err)
	}
	pid1 := result1.Process.PID()

	time.Sleep(100 * time.Millisecond)

	// Try to start another with same ID+path - should reuse
	result2, err := pm.StartOrReuse(ctx, ProcessConfig{
		ID:          "test-reuse",
		ProjectPath: "/tmp",
		Command:     "sleep",
		Args:        []string{"60"},
	})
	if err != nil {
		t.Fatalf("second StartOrReuse failed: %v", err)
	}

	if !result2.Reused {
		t.Error("expected Reused=true for running process")
	}
	if result2.Process.PID() != pid1 {
		t.Error("expected same PID when reusing")
	}

	pm.Stop(ctx, "test-reuse")
}

func TestProcessManager_StartOrReuse_CleansUpStopped(t *testing.T) {
	pm := NewProcessManager(DefaultManagerConfig())
	defer pm.Shutdown(context.Background())

	ctx := context.Background()

	// Start and wait for a short process
	result1, err := pm.StartOrReuse(ctx, ProcessConfig{
		ID:          "test-cleanup",
		ProjectPath: "/tmp",
		Command:     "echo",
		Args:        []string{"first"},
	})
	if err != nil {
		t.Fatalf("first StartOrReuse failed: %v", err)
	}
	<-result1.Process.Done()
	pid1 := result1.Process.PID()

	// Try to start another with same ID+path - should cleanup and start new
	result2, err := pm.StartOrReuse(ctx, ProcessConfig{
		ID:          "test-cleanup",
		ProjectPath: "/tmp",
		Command:     "echo",
		Args:        []string{"second"},
	})
	if err != nil {
		t.Fatalf("second StartOrReuse failed: %v", err)
	}

	if result2.Reused {
		t.Error("expected Reused=false for cleaned up process")
	}
	if !result2.Cleaned {
		t.Error("expected Cleaned=true for cleaned up process")
	}
	if result2.Process.PID() == pid1 {
		t.Error("expected different PID after cleanup")
	}

	<-result2.Process.Done()
}

func TestProcessManager_StartOrReuse_DifferentPaths(t *testing.T) {
	pm := NewProcessManager(DefaultManagerConfig())
	defer pm.Shutdown(context.Background())

	ctx := context.Background()

	// Start process with ID "server" in /tmp
	result1, err := pm.StartOrReuse(ctx, ProcessConfig{
		ID:          "server",
		ProjectPath: "/tmp",
		Command:     "sleep",
		Args:        []string{"60"},
	})
	if err != nil {
		t.Fatalf("first StartOrReuse failed: %v", err)
	}
	pid1 := result1.Process.PID()

	time.Sleep(100 * time.Millisecond)

	// Start process with same ID but different path - should create new
	result2, err := pm.StartOrReuse(ctx, ProcessConfig{
		ID:          "server",
		ProjectPath: "/var/tmp",
		Command:     "sleep",
		Args:        []string{"60"},
	})
	if err != nil {
		t.Fatalf("second StartOrReuse failed: %v", err)
	}

	if result2.Reused {
		t.Error("expected Reused=false for different path")
	}
	if result2.Process.PID() == pid1 {
		t.Error("expected different PID for different path")
	}

	// Should have 2 processes with same ID but different paths
	procs := pm.List()
	count := 0
	for _, p := range procs {
		if p.ID == "server" {
			count++
		}
	}
	if count != 2 {
		t.Errorf("expected 2 processes with ID 'server', got %d", count)
	}

	pm.Stop(ctx, "server") // This will stop one of them
	pm.StopProcess(ctx, result2.Process)
}

func TestProcessManager_GetByPID(t *testing.T) {
	pm := NewProcessManager(DefaultManagerConfig())
	defer pm.Shutdown(context.Background())

	ctx := context.Background()

	// Start a process
	proc, err := pm.StartCommand(ctx, ProcessConfig{
		ID:          "test-pid",
		ProjectPath: "/tmp",
		Command:     "sleep",
		Args:        []string{"60"},
	})
	if err != nil {
		t.Fatalf("StartCommand failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	pid := proc.PID()
	if pid <= 0 {
		t.Fatal("expected valid PID")
	}

	// Should find the process by PID
	found := pm.GetByPID(pid)
	if found == nil {
		t.Error("expected to find process by PID")
	}
	if found != proc {
		t.Error("expected to find the same process")
	}

	// IsManagedPID should return true
	if !pm.IsManagedPID(pid) {
		t.Error("expected IsManagedPID to return true")
	}

	// Non-existent PID should return nil
	if pm.GetByPID(999999) != nil {
		t.Error("expected nil for non-existent PID")
	}

	// Stop the process
	pm.Stop(ctx, proc.ID)

	// After stopping, should not find by PID (not running)
	if pm.IsManagedPID(pid) {
		t.Error("expected IsManagedPID to return false after stop")
	}
}

func TestDetectPortConflict(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected int
	}{
		{
			name:     "Node.js EADDRINUSE",
			output:   "Error: listen EADDRINUSE: address already in use :::3000",
			expected: 3000,
		},
		{
			name:     "Node.js EADDRINUSE with port",
			output:   "Error: listen EADDRINUSE: address already in use 0.0.0.0:8080",
			expected: 8080,
		},
		{
			name:     "Go listen error",
			output:   "listen tcp :8000: address already in use",
			expected: 8000,
		},
		{
			name:     "Python bind error",
			output:   "OSError: [Errno 98] Address already in use: bind ':5000'",
			expected: 5000,
		},
		{
			name:     "Generic error",
			output:   "address already in use 9000",
			expected: 9000,
		},
		{
			name:     "No port conflict",
			output:   "Some other error message",
			expected: 0,
		},
		{
			name:     "Empty output",
			output:   "",
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := detectPortConflict([]byte(tt.output))
			if result != tt.expected {
				t.Errorf("detectPortConflict(%q) = %d, want %d", tt.output, result, tt.expected)
			}
		})
	}
}
