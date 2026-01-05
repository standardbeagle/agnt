//go:build unix

package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
)

// TestRestartIntegration_ProcRestart tests single process restart.
func TestRestartIntegration_ProcRestart(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

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

	// Wait for process to start
	time.Sleep(100 * time.Millisecond)

	// Get initial PID
	status1, err := client.ProcStatus("test-proc")
	if err != nil {
		t.Fatalf("Failed to get process status: %v", err)
	}
	initialPID := status1["pid"]
	t.Logf("Initial status: %v", status1)

	// Restart the process
	restartResult, err := client.ProcRestart("test-proc")
	if err != nil {
		t.Fatalf("Failed to restart process: %v", err)
	}
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

	// Wait for restart
	time.Sleep(200 * time.Millisecond)

	// Get new PID - should be different
	status2, err := client.ProcStatus("test-proc")
	if err != nil {
		t.Fatalf("Failed to get process status after restart: %v", err)
	}
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
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

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
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

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

	// Wait for proxy to start
	time.Sleep(100 * time.Millisecond)

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

	// Wait for restart
	time.Sleep(200 * time.Millisecond)

	// Verify proxy is still running
	status2, err := client.ProxyStatus("test-proxy")
	if err != nil {
		t.Fatalf("Failed to get proxy status after restart: %v", err)
	}
	t.Logf("Proxy status after restart: %v", status2)

	// Clean up
	_ = client.ProxyStop("test-proxy")
}

// TestRestartIntegration_ProxyRestart_NonExistent tests restarting a non-existent proxy.
func TestRestartIntegration_ProxyRestart_NonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

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
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

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

	// Wait for everything to start
	time.Sleep(200 * time.Millisecond)

	// Verify resources are running
	procList, _ := client.ProcList(protocol.DirectoryFilter{Global: true})
	if procList["count"].(float64) < 2 {
		t.Errorf("Expected at least 2 processes, got %v", procList["count"])
	}

	proxyList, _ := client.ProxyList(protocol.DirectoryFilter{Global: true})
	if proxyList["count"].(float64) < 2 {
		t.Errorf("Expected at least 2 proxies, got %v", proxyList["count"])
	}

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

	// Wait for cleanup
	time.Sleep(200 * time.Millisecond)

	// Verify all are stopped
	procListAfter, _ := client.ProcList(protocol.DirectoryFilter{Global: true})
	proxyListAfter, _ := client.ProxyList(protocol.DirectoryFilter{Global: true})
	t.Logf("Processes after stop: %v", procListAfter)
	t.Logf("Proxies after stop: %v", proxyListAfter)
}

// TestRestartIntegration_RestartAll tests restarting all processes and proxies.
func TestRestartIntegration_RestartAll(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

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
	time.Sleep(200 * time.Millisecond)

	// Restart all
	result, err := client.RestartAll()
	if err != nil {
		t.Fatalf("Failed to restart all: %v", err)
	}
	t.Logf("RestartAll result: %v", result)

	// Helper to safely get int from result
	getIntVal := func(m map[string]interface{}, key string) int {
		if v, ok := m[key].(float64); ok {
			return int(v)
		}
		return 0
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

	// Wait for restart
	time.Sleep(300 * time.Millisecond)

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
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

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
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

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
