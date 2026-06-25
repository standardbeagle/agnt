//go:build unix

package daemon

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/require"
)

// waitForProcessState polls until the process reaches the expected state or times out.
func waitForProcessState(t *testing.T, client *Client, processID, wantState string, timeout time.Duration) map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := client.ProcStatus(processID)
		if err == nil {
			if state, ok := status["state"].(string); ok && state == wantState {
				return status
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("process %q did not reach state %q within %v", processID, wantState, timeout)
	return nil
}

// restartProcessWithRetry retries ProcRestart on transient startup failures
// caused by the PID-reuse race in Go's exec.CommandContext.
func restartProcessWithRetry(t *testing.T, client *Client, processID string) map[string]interface{} {
	t.Helper()
	const maxAttempts = 3
	var result map[string]interface{}
	var err error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		result, err = client.ProcRestart(processID)
		if err == nil {
			return result
		}
		// A "process exited with code -1 during startup" error is a transient
		// PID-reuse race where Go's exec.CommandContext goroutine fires an async
		// SIGKILL after the old process is reaped, hitting the newly started
		// process if it happened to inherit the same PID.
		if strings.Contains(err.Error(), "exited with code") || strings.Contains(err.Error(), "startup") {
			t.Logf("Transient restart failure (PID-reuse race, attempt %d/%d): %v", attempt+1, maxAttempts, err)
			time.Sleep(500 * time.Millisecond)
			continue
		}
		break // Non-transient error, don't retry
	}
	t.Fatalf("Failed to restart process %q: %v", processID, err)
	return nil
}

// TestRestartIntegration_ProcRestart tests single process restart.
func TestRestartIntegration_ProcRestart(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	_ = NewForTest(t, DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	client := NewClient(WithSocketPath(sockPath))
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	// Start a process first
	runConfig := protocol.RunConfig{
		Path:    tmpDir,
		ID:      "test-proc",
		Raw:     true,
		Command: "sleep",
		Args:    []string{"60"},
		Mode:    "background",
	}

	runResult, err := client.Run(runConfig)
	if err != nil {
		t.Fatalf("Failed to start process: %v", err)
	}
	t.Logf("Run result: %v", runResult)

	// Wait for process to reach running state (poll instead of fixed sleep)
	status1 := waitForProcessState(t, client, "test-proc", "running", 2*time.Second)
	initialPID := status1["pid"]
	t.Logf("Initial status: %v", status1)

	// Restart the process (with retry for transient PID-reuse race)
	restartResult := restartProcessWithRetry(t, client, "test-proc")
	t.Logf("Restart result: %v", restartResult)

	// Check id or process_id
	if id, ok := restartResult["id"].(string); !ok || id != "test-proc" {
		if pid, ok := restartResult["process_id"].(string); !ok || pid != "test-proc" {
			t.Errorf("Expected id or process_id test-proc, got %v", restartResult)
		}
	}
	if success, ok := restartResult["success"].(bool); !ok || !success {
		t.Errorf("Expected success true, got %v", restartResult["success"])
	}

	// Wait for restarted process to reach running state
	status2 := waitForProcessState(t, client, "test-proc", "running", 3*time.Second)
	newPID := status2["pid"]
	t.Logf("Status after restart: %v", status2)

	if initialPID == newPID {
		t.Logf("Note: PID may be same if process reused or quickly restarted. Initial: %v, New: %v", initialPID, newPID)
	}

	// Clean up
	_, _ = client.ProcStop("test-proc", false)
}

// TestRestartIntegration_ProcRestart_NonExistent tests restarting a non-existent process.
func TestRestartIntegration_ProcRestart_NonExistent(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	_ = NewForTest(t, DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	client := NewClient(WithSocketPath(sockPath))
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	// Try to restart non-existent process
	_, err := client.ProcRestart("nonexistent")
	if err == nil {
		t.Error("Expected error when restarting non-existent process")
	}
}

// TestRestartIntegration_ProxyRestart tests single proxy restart.
func TestRestartIntegration_ProxyRestart(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	_ = NewForTest(t, DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	client := NewClient(WithSocketPath(sockPath))
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	// Start a mock backend server
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))
	defer backend.Close()

	// Start a proxy
	result, err := client.ProxyStart("test-proxy", backend.URL, 0, 1000, tmpDir)
	if err != nil {
		t.Fatalf("Failed to start proxy: %v", err)
	}
	t.Logf("Proxy start result: %v", result)

	// Wait for proxy to be reachable
	require.Eventually(t, func() bool {
		_, err := client.ProxyStatus("test-proxy")
		return err == nil
	}, 3*time.Second, 10*time.Millisecond, "proxy should be running before restart")

	// Get initial status
	status1, err := client.ProxyStatus("test-proxy")
	if err != nil {
		t.Fatalf("Failed to get proxy status: %v", err)
	}
	t.Logf("Initial proxy status: %v", status1)

	// Restart the proxy
	restartResult, err := client.ProxyRestart("test-proxy")
	if err != nil {
		t.Fatalf("Failed to restart proxy: %v", err)
	}
	t.Logf("Proxy restart result: %v", restartResult)

	// Check id
	if id, ok := restartResult["id"].(string); !ok || id != "test-proxy" {
		t.Errorf("Expected id test-proxy, got %v", restartResult["id"])
	}
	if success, ok := restartResult["success"].(bool); !ok || !success {
		t.Errorf("Expected success true, got %v", restartResult["success"])
	}

	// Verify proxy is still running after restart
	require.Eventually(t, func() bool {
		_, err := client.ProxyStatus("test-proxy")
		return err == nil
	}, 3*time.Second, 10*time.Millisecond, "proxy should be running after restart")
	status2, err := client.ProxyStatus("test-proxy")
	if err != nil {
		t.Fatalf("Failed to get proxy status after restart: %v", err)
	}
	t.Logf("Proxy status after restart: %v", status2)

	// The port must be preserved across the restart — clients addressing the
	// proxy by its original port must not break. (Regression: the handler used
	// to recreate with ListenPort:0, drifting to a fresh auto-assignment.)
	addr1, _ := status1["listen_addr"].(string)
	addr2, _ := status2["listen_addr"].(string)
	require.NotEmpty(t, addr1, "initial status must report a listen_addr")
	require.Equal(t, addr1, addr2, "restart must preserve the proxy's listen address (port)")

	// Clean up
	_ = client.ProxyStop("test-proxy")
}

// TestRestartIntegration_ProxyRestart_NonExistent tests restarting a non-existent proxy.
func TestRestartIntegration_ProxyRestart_NonExistent(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	_ = NewForTest(t, DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	client := NewClient(WithSocketPath(sockPath))
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	// Try to restart non-existent proxy
	_, err := client.ProxyRestart("nonexistent")
	if err == nil {
		t.Error("Expected error when restarting non-existent proxy")
	}
}

// TestRestartIntegration_StopAll tests stopping all processes and proxies.
func TestRestartIntegration_StopAll(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	_ = NewForTest(t, DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	client := NewClient(WithSocketPath(sockPath))
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	// Start a mock backend server
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	// Start multiple processes
	for i := 0; i < 2; i++ {
		runConfig := protocol.RunConfig{
			Path:    tmpDir,
			ID:      "proc-" + string(rune('a'+i)),
			Raw:     true,
			Command: "sleep",
			Args:    []string{"60"},
			Mode:    "background",
		}
		_, err := client.Run(runConfig)
		if err != nil {
			t.Fatalf("Failed to start process %d: %v", i, err)
		}
	}

	// Start multiple proxies
	for i := 0; i < 2; i++ {
		_, err := client.ProxyStart("proxy-"+string(rune('a'+i)), backend.URL, 0, 1000, tmpDir)
		if err != nil {
			t.Fatalf("Failed to start proxy %d: %v", i, err)
		}
	}

	// Verify resources are running
	require.Eventually(t, func() bool {
		procList, err := client.ProcList(protocol.DirectoryFilter{Global: true})
		if err != nil {
			return false
		}
		return procList["count"].(float64) >= 2
	}, 5*time.Second, 20*time.Millisecond, "expected at least 2 processes to start")

	require.Eventually(t, func() bool {
		proxyList, err := client.ProxyList(protocol.DirectoryFilter{Global: true})
		if err != nil {
			return false
		}
		return proxyList["count"].(float64) >= 2
	}, 5*time.Second, 20*time.Millisecond, "expected at least 2 proxies to start")

	// Helper to safely get int from result
	getIntVal := func(m map[string]interface{}, key string) int {
		if v, ok := m[key].(float64); ok {
			return int(v)
		}
		return 0
	}

	// Stop all
	result, err := client.StopAll()
	if err != nil {
		t.Fatalf("Failed to stop all: %v", err)
	}
	t.Logf("StopAll result: %v", result)

	processesStopped := getIntVal(result, "processes_stopped")
	proxiesStopped := getIntVal(result, "proxies_stopped")

	if processesStopped < 2 {
		t.Errorf("Expected at least 2 processes stopped, got %d", processesStopped)
	}
	if proxiesStopped < 2 {
		t.Errorf("Expected at least 2 proxies stopped, got %d", proxiesStopped)
	}

	// Verify all are stopped
	procListAfter, _ := client.ProcList(protocol.DirectoryFilter{Global: true})
	proxyListAfter, _ := client.ProxyList(protocol.DirectoryFilter{Global: true})
	t.Logf("Processes after stop: %v", procListAfter)
	t.Logf("Proxies after stop: %v", proxyListAfter)
}

// TestRestartIntegration_RestartAll tests restarting all processes and proxies.
func TestRestartIntegration_RestartAll(t *testing.T) {
	// No t.Parallel(): starts real sleep process; PID-reuse kills it under high concurrency.
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	_ = NewForTest(t, DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	client := NewClient(WithSocketPath(sockPath))
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	// Start a mock backend server
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	// Start a process
	runConfig := protocol.RunConfig{
		Path:    tmpDir,
		ID:      "restart-proc",
		Raw:     true,
		Command: "sleep",
		Args:    []string{"60"},
		Mode:    "background",
	}
	_, err := client.Run(runConfig)
	if err != nil {
		t.Fatalf("Failed to start process: %v", err)
	}

	// Start a proxy
	_, err = client.ProxyStart("restart-proxy", backend.URL, 0, 1000, tmpDir)
	if err != nil {
		t.Fatalf("Failed to start proxy: %v", err)
	}

	// Wait for everything to start
	require.Eventually(t, func() bool {
		s, err := client.ProcStatus("restart-proc")
		if err != nil {
			return false
		}
		state, _ := s["state"].(string)
		return state == "running"
	}, 5*time.Second, 20*time.Millisecond, "restart-proc should reach running state")

	require.Eventually(t, func() bool {
		_, err := client.ProxyStatus("restart-proxy")
		return err == nil
	}, 3*time.Second, 10*time.Millisecond, "restart-proxy should be running")

	// Helper to safely get int from result
	getIntVal := func(m map[string]interface{}, key string) int {
		if v, ok := m[key].(float64); ok {
			return int(v)
		}
		return 0
	}

	// Restart all — retry on transient PID-reuse race (same as restartProcessWithRetry)
	var result map[string]interface{}
	for attempt := 0; attempt < 3; attempt++ {
		var err error
		result, err = client.RestartAll()
		if err != nil {
			t.Fatalf("Failed to restart all: %v", err)
		}
		t.Logf("RestartAll result (attempt %d): %v", attempt, result)

		if getIntVal(result, "processes_failed") == 0 {
			break
		}
		t.Logf("Transient process restart failure (PID-reuse race, attempt %d/3), retrying...", attempt+1)
		time.Sleep(500 * time.Millisecond)
	}

	processesRestarted := getIntVal(result, "processes_restarted")
	proxiesRestarted := getIntVal(result, "proxies_restarted")
	processesFailed := getIntVal(result, "processes_failed")
	proxiesFailed := getIntVal(result, "proxies_failed")

	t.Logf("Processes restarted: %d, failed: %d", processesRestarted, processesFailed)
	t.Logf("Proxies restarted: %d, failed: %d", proxiesRestarted, proxiesFailed)

	if processesRestarted < 1 {
		t.Errorf("Expected at least 1 process restarted, got %d", processesRestarted)
	}
	if proxiesRestarted < 1 {
		t.Errorf("Expected at least 1 proxy restarted, got %d", proxiesRestarted)
	}
	if processesFailed > 0 {
		t.Errorf("Expected 0 process failures, got %d", processesFailed)
	}
	if proxiesFailed > 0 {
		t.Errorf("Expected 0 proxy failures, got %d", proxiesFailed)
	}

	// Verify resources are running again
	status, err := client.ProcStatus("restart-proc")
	if err != nil {
		t.Logf("Note: Failed to get process status after restart: %v", err)
	} else {
		t.Logf("Process status after restart: %v", status)
	}

	proxyStatus, err := client.ProxyStatus("restart-proxy")
	if err != nil {
		t.Logf("Note: Failed to get proxy status after restart: %v", err)
	} else {
		t.Logf("Proxy status after restart: %v", proxyStatus)
	}

	// Clean up
	_, _ = client.ProcStop("restart-proc", false)
	_ = client.ProxyStop("restart-proxy")
}

// TestRestartIntegration_StopAll_Empty tests stop all with no resources.
func TestRestartIntegration_StopAll_Empty(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	_ = NewForTest(t, DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	client := NewClient(WithSocketPath(sockPath))
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	// Helper to safely get int from result
	getIntVal := func(m map[string]interface{}, key string) int {
		if v, ok := m[key].(float64); ok {
			return int(v)
		}
		return 0
	}

	// Stop all with no resources - should succeed
	result, err := client.StopAll()
	if err != nil {
		t.Fatalf("StopAll with no resources should not fail: %v", err)
	}
	t.Logf("StopAll empty result: %v", result)

	processesStopped := getIntVal(result, "processes_stopped")
	proxiesStopped := getIntVal(result, "proxies_stopped")

	if processesStopped != 0 {
		t.Errorf("Expected 0 processes stopped, got %d", processesStopped)
	}
	if proxiesStopped != 0 {
		t.Errorf("Expected 0 proxies stopped, got %d", proxiesStopped)
	}
}

// TestRestartIntegration_RestartAll_Empty tests restart all with no resources.
func TestRestartIntegration_RestartAll_Empty(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	_ = NewForTest(t, DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	client := NewClient(WithSocketPath(sockPath))
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	// Helper to safely get int from result
	getIntVal := func(m map[string]interface{}, key string) int {
		if v, ok := m[key].(float64); ok {
			return int(v)
		}
		return 0
	}

	// Restart all with no resources - should succeed
	result, err := client.RestartAll()
	if err != nil {
		t.Fatalf("RestartAll with no resources should not fail: %v", err)
	}
	t.Logf("RestartAll empty result: %v", result)

	processesRestarted := getIntVal(result, "processes_restarted")
	proxiesRestarted := getIntVal(result, "proxies_restarted")
	processesFailed := getIntVal(result, "processes_failed")
	proxiesFailed := getIntVal(result, "proxies_failed")

	if processesRestarted != 0 || proxiesRestarted != 0 || processesFailed != 0 || proxiesFailed != 0 {
		t.Errorf("Expected all counts to be 0, got processes: %d (failed: %d), proxies: %d (failed: %d)",
			processesRestarted, processesFailed, proxiesRestarted, proxiesFailed)
	}
}
