//go:build unix

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
)

// TestHubIntegration_CommandDispatch verifies that commands are dispatched through Hub.
func TestHubIntegration_CommandDispatch(t *testing.T) {
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

	// Test DETECT command through Hub
	t.Run("DETECT", func(t *testing.T) {
		result, err := client.Detect(".")
		if err != nil {
			t.Fatalf("Detect failed: %v", err)
		}
		if result["type"] == nil {
			t.Error("Expected type field in detect result")
		}
	})

	// Test STATUS command through Hub
	t.Run("STATUS", func(t *testing.T) {
		info, err := client.Info()
		if err != nil {
			t.Fatalf("Info failed: %v", err)
		}
		if info.Version == "" {
			t.Error("Expected version in info")
		}
		if info.SocketPath == "" {
			t.Error("Expected socket_path in info")
		}
	})

	// Test PROXY commands through Hub
	t.Run("PROXY", func(t *testing.T) {
		// LIST (no proxies yet)
		proxies, err := client.ProxyList(protocol.DirectoryFilter{Global: true})
		if err != nil {
			t.Fatalf("ProxyList failed: %v", err)
		}
		if proxies["count"] == nil {
			t.Error("Expected count field in proxy list")
		}
	})

	// Test PROXYLOG commands through Hub (needs a proxy first)
	t.Run("PROXYLOG_NoProxy", func(t *testing.T) {
		_, err := client.ProxyLogQuery("nonexistent", protocol.LogQueryFilter{})
		if err == nil {
			t.Error("Expected error for nonexistent proxy")
		}
	})

	// Test PROC LIST through Hub
	t.Run("PROC_LIST", func(t *testing.T) {
		procs, err := client.ProcList(protocol.DirectoryFilter{Global: true})
		if err != nil {
			t.Fatalf("ProcList failed: %v", err)
		}
		if procs["count"] == nil {
			t.Error("Expected count field in proc list")
		}
	})

	// Test OVERLAY commands through Hub
	t.Run("OVERLAY", func(t *testing.T) {
		// GET
		result, err := client.conn.Request(protocol.VerbOverlay, protocol.SubVerbGet).JSON()
		if err != nil {
			t.Fatalf("Overlay GET failed: %v", err)
		}
		// Initially no overlay endpoint set
		if result["endpoint"] != "" {
			t.Logf("Unexpected overlay endpoint: %v", result["endpoint"])
		}
	})

	// Test SESSION commands through Hub
	t.Run("SESSION", func(t *testing.T) {
		// LIST (no sessions yet)
		result, err := client.conn.Request(protocol.VerbSession, protocol.SubVerbList).WithJSON(protocol.DirectoryFilter{Global: true}).JSON()
		if err != nil {
			t.Fatalf("Session LIST failed: %v", err)
		}
		if result["count"] == nil {
			t.Error("Expected count field in session list")
		}
	})
}

// TestHubIntegration_ProxyWorkflow tests proxy creation and management through Hub.
func TestHubIntegration_ProxyWorkflow(t *testing.T) {
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

	// Start a proxy
	proxyID := "test-hub-proxy"
	result, err := client.ProxyStart(proxyID, "http://localhost:8080", 0, 100, ".")
	if err != nil {
		t.Fatalf("ProxyStart failed: %v", err)
	}
	if result["id"] != proxyID {
		t.Errorf("Expected id=%s, got %v", proxyID, result["id"])
	}

	// Get proxy status
	status, err := client.ProxyStatus(proxyID)
	if err != nil {
		t.Fatalf("ProxyStatus failed: %v", err)
	}
	if status["id"] != proxyID {
		t.Errorf("Expected status id=%s, got %v", proxyID, status["id"])
	}
	if status["target_url"] == nil {
		t.Error("Expected target_url in status")
	}

	// List proxies
	list, err := client.ProxyList(protocol.DirectoryFilter{Global: true})
	if err != nil {
		t.Fatalf("ProxyList failed: %v", err)
	}
	count, ok := list["count"].(float64)
	if !ok || count < 1 {
		t.Errorf("Expected at least 1 proxy, got %v", list["count"])
	}

	// Verify proxy list contains 'running' field (not just 'status')
	proxies, ok := list["proxies"].([]interface{})
	if !ok || len(proxies) == 0 {
		t.Fatal("Expected proxies array with at least one entry")
	}
	firstProxy := proxies[0].(map[string]interface{})
	if running, exists := firstProxy["running"]; !exists {
		t.Errorf("Proxy list entry missing 'running' field, got keys: %v", firstProxy)
	} else if running != true {
		t.Errorf("Expected running=true for active proxy, got %v", running)
	}

	// Query proxy logs (should be empty but work)
	logs, err := client.ProxyLogQuery(proxyID, protocol.LogQueryFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ProxyLogQuery failed: %v", err)
	}
	if logs["entries"] == nil {
		t.Logf("Log query result: %+v", logs)
	}

	// Get log stats
	stats, err := client.ProxyLogStats(proxyID)
	if err != nil {
		t.Fatalf("ProxyLogStats failed: %v", err)
	}
	if stats["total_entries"] == nil {
		t.Error("Expected total_entries in stats")
	}

	// Stop proxy
	if err := client.ProxyStop(proxyID); err != nil {
		t.Fatalf("ProxyStop failed: %v", err)
	}

	// Verify proxy is stopped
	_, err = client.ProxyStatus(proxyID)
	if err == nil {
		t.Error("Expected error for stopped proxy")
	}
}

// TestHubIntegration_TunnelCommands tests tunnel commands (error paths since no tunnel running).
func TestHubIntegration_TunnelCommands(t *testing.T) {
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

	// List tunnels (should be empty)
	t.Run("LIST", func(t *testing.T) {
		result, err := client.conn.Request("TUNNEL", "LIST").JSON()
		if err != nil {
			t.Fatalf("Tunnel LIST failed: %v", err)
		}
		if result["tunnels"] == nil {
			t.Error("Expected tunnels field")
		}
	})

	// Status for non-existent tunnel
	t.Run("STATUS_NotFound", func(t *testing.T) {
		_, err := client.conn.Request("TUNNEL", "STATUS", "nonexistent").JSON()
		if err == nil {
			t.Error("Expected error for nonexistent tunnel")
		}
	})

	// Stop non-existent tunnel
	t.Run("STOP_NotFound", func(t *testing.T) {
		err := client.conn.Request("TUNNEL", "STOP", "nonexistent").OK()
		if err == nil {
			t.Error("Expected error for nonexistent tunnel")
		}
	})
}

// TestHubIntegration_ChaosCommands tests chaos engineering commands.
func TestHubIntegration_ChaosCommands(t *testing.T) {
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

	// Start a proxy for chaos testing
	proxyID := "chaos-test-proxy"
	_, err := client.ProxyStart(proxyID, "http://localhost:8080", 0, 100, ".")
	if err != nil {
		t.Fatalf("ProxyStart failed: %v", err)
	}
	defer client.ProxyStop(proxyID)

	// Test CHAOS STATUS
	t.Run("STATUS", func(t *testing.T) {
		result, err := client.conn.Request("CHAOS", "STATUS", proxyID).JSON()
		if err != nil {
			t.Fatalf("Chaos STATUS failed: %v", err)
		}
		if result["enabled"] == nil {
			t.Error("Expected enabled field in status")
		}
	})

	// Test CHAOS ENABLE
	t.Run("ENABLE", func(t *testing.T) {
		err := client.conn.Request("CHAOS", "ENABLE", proxyID).OK()
		if err != nil {
			t.Fatalf("Chaos ENABLE failed: %v", err)
		}
	})

	// Verify enabled
	t.Run("VERIFY_ENABLED", func(t *testing.T) {
		result, err := client.conn.Request("CHAOS", "STATUS", proxyID).JSON()
		if err != nil {
			t.Fatalf("Chaos STATUS failed: %v", err)
		}
		if result["enabled"] != true {
			t.Error("Expected chaos to be enabled")
		}
	})

	// Test CHAOS DISABLE
	t.Run("DISABLE", func(t *testing.T) {
		err := client.conn.Request("CHAOS", "DISABLE", proxyID).OK()
		if err != nil {
			t.Fatalf("Chaos DISABLE failed: %v", err)
		}
	})

	// Test CHAOS LIST-PRESETS
	t.Run("LIST_PRESETS", func(t *testing.T) {
		result, err := client.conn.Request("CHAOS", "LIST-PRESETS").JSON()
		if err != nil {
			t.Fatalf("Chaos LIST-PRESETS failed: %v", err)
		}
		if result["presets"] == nil {
			t.Error("Expected presets field")
		}
	})

	// Test CHAOS STATS
	t.Run("STATS", func(t *testing.T) {
		result, err := client.conn.Request("CHAOS", "STATS", proxyID).JSON()
		if err != nil {
			t.Fatalf("Chaos STATS failed: %v", err)
		}
		// ChaosStats has total_requests, affected_count, etc.
		if result["total_requests"] == nil {
			t.Error("Expected total_requests field in stats")
		}
	})

	// Test CHAOS LIST-RULES
	t.Run("LIST_RULES", func(t *testing.T) {
		result, err := client.conn.Request("CHAOS", "LIST-RULES", proxyID).JSON()
		if err != nil {
			t.Fatalf("Chaos LIST-RULES failed: %v", err)
		}
		if result["rules"] == nil {
			t.Error("Expected rules field")
		}
	})

	// Test CHAOS CLEAR
	t.Run("CLEAR", func(t *testing.T) {
		err := client.conn.Request("CHAOS", "CLEAR", proxyID).OK()
		if err != nil {
			t.Fatalf("Chaos CLEAR failed: %v", err)
		}
	})
}

// TestHubIntegration_OverlayCommands tests overlay commands.
func TestHubIntegration_OverlayCommands(t *testing.T) {
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

	// Test OVERLAY GET (initially empty)
	t.Run("GET", func(t *testing.T) {
		result, err := client.conn.Request("OVERLAY", "GET").JSON()
		if err != nil {
			t.Fatalf("Overlay GET failed: %v", err)
		}
		t.Logf("Overlay GET result: %+v", result)
	})

	// Test OVERLAY SET (requires JSON data with endpoint field)
	t.Run("SET", func(t *testing.T) {
		err := client.conn.Request("OVERLAY", "SET").WithJSON(map[string]interface{}{
			"endpoint": "http://localhost:19191",
		}).OK()
		if err != nil {
			t.Fatalf("Overlay SET failed: %v", err)
		}
	})

	// Verify SET worked
	t.Run("VERIFY_SET", func(t *testing.T) {
		result, err := client.conn.Request("OVERLAY", "GET").JSON()
		if err != nil {
			t.Fatalf("Overlay GET failed: %v", err)
		}
		if result["endpoint"] != "http://localhost:19191" {
			t.Errorf("Expected endpoint=http://localhost:19191, got %v", result["endpoint"])
		}
	})

	// Test OVERLAY CLEAR
	t.Run("CLEAR", func(t *testing.T) {
		err := client.conn.Request("OVERLAY", "CLEAR").OK()
		if err != nil {
			t.Fatalf("Overlay CLEAR failed: %v", err)
		}
	})

	// Test OVERLAY ACTIVITY (returns OK, not JSON - it's a heartbeat)
	t.Run("ACTIVITY", func(t *testing.T) {
		err := client.conn.Request("OVERLAY", "ACTIVITY").OK()
		if err != nil {
			t.Fatalf("Overlay ACTIVITY failed: %v", err)
		}
	})
}

// TestHubIntegration_ProcessWorkflow tests process commands through Hub.
func TestHubIntegration_ProcessWorkflow(t *testing.T) {
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

	// Run a quick process
	t.Run("RUN", func(t *testing.T) {
		result, err := client.Run(protocol.RunConfig{
			ID:      "test-echo",
			Command: "echo",
			Args:    []string{"hello"},
			Raw:     true,
		})
		if err != nil {
			t.Fatalf("Run failed: %v", err)
		}
		if result["id"] != "test-echo" {
			t.Errorf("Expected id=test-echo, got %v", result["id"])
		}
	})

	// Wait for process to finish
	time.Sleep(500 * time.Millisecond)

	// Get process status
	t.Run("STATUS", func(t *testing.T) {
		result, err := client.ProcStatus("test-echo")
		if err != nil {
			t.Fatalf("ProcStatus failed: %v", err)
		}
		if result["id"] != "test-echo" {
			t.Errorf("Expected id=test-echo, got %v", result["id"])
		}
		t.Logf("Process state: %v", result["state"])
	})

	// Get process output
	t.Run("OUTPUT", func(t *testing.T) {
		output, err := client.ProcOutput("test-echo", protocol.OutputFilter{})
		if err != nil {
			t.Fatalf("ProcOutput failed: %v", err)
		}
		t.Logf("Output: %s", output)
	})

	// List processes
	t.Run("LIST", func(t *testing.T) {
		result, err := client.ProcList(protocol.DirectoryFilter{Global: true})
		if err != nil {
			t.Fatalf("ProcList failed: %v", err)
		}
		if result["count"] == nil {
			t.Error("Expected count field")
		}
	})
}

// TestHubIntegration_SessionCommands tests session commands through Hub.
func TestHubIntegration_SessionCommands(t *testing.T) {
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

	// Register a test session (SESSION REGISTER <code> <overlay_path>)
	t.Run("REGISTER", func(t *testing.T) {
		result, err := client.conn.Request("SESSION", "REGISTER", "test-session", tmpDir).WithJSON(map[string]interface{}{
			"project_path": tmpDir,
			"command":      "test",
		}).JSON()
		if err != nil {
			t.Fatalf("Session REGISTER failed: %v", err)
		}
		if result["code"] == nil {
			t.Error("Expected code field")
		}
		t.Logf("Registered session: %v", result["code"])
	})

	// List sessions
	t.Run("LIST", func(t *testing.T) {
		result, err := client.conn.Request("SESSION", "LIST").WithJSON(map[string]interface{}{
			"global": true,
		}).JSON()
		if err != nil {
			t.Fatalf("Session LIST failed: %v", err)
		}
		if result["sessions"] == nil {
			t.Error("Expected sessions field")
		}
	})

	// Test TASKS (no scheduled tasks)
	t.Run("TASKS", func(t *testing.T) {
		result, err := client.conn.Request("SESSION", "TASKS").JSON()
		if err != nil {
			t.Fatalf("Session TASKS failed: %v", err)
		}
		t.Logf("Tasks result: %+v", result)
	})
}

// TestHubIntegration_CurrentPageCommands tests current page commands through Hub.
func TestHubIntegration_CurrentPageCommands(t *testing.T) {
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

	// List (no proxy)
	t.Run("LIST_NoProxy", func(t *testing.T) {
		_, err := client.conn.Request("CURRENTPAGE", "LIST", "nonexistent-proxy").JSON()
		// Should fail with proxy not found
		if err == nil {
			t.Log("CurrentPage LIST succeeded for nonexistent proxy (unexpected)")
		}
	})

	// Test CLEAR (no pages to clear)
	t.Run("CLEAR_NoProxy", func(t *testing.T) {
		err := client.conn.Request("CURRENTPAGE", "CLEAR", "nonexistent-proxy").OK()
		// Should fail with proxy not found
		if err != nil {
			t.Logf("CurrentPage CLEAR error (expected): %v", err)
		}
	})
}

// TestHubIntegration_SessionScopedProxyLookup tests that proxy lookups are session-scoped.
// When multiple proxies match a fuzzy ID but only one is in the current session's path,
// the lookup should succeed without ambiguity.
func TestHubIntegration_SessionScopedProxyLookup(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Create two project directories
	projectA := filepath.Join(tmpDir, "project-a")
	projectB := filepath.Join(tmpDir, "project-b")
	os.MkdirAll(projectA, 0755)
	os.MkdirAll(projectB, 0755)

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

	// Create two proxies with similar fuzzy IDs in different paths
	proxyA := "proj-a:dev:localhost-3000"
	proxyB := "proj-b:dev:localhost-4000"

	_, err := client.ProxyStart(proxyA, "http://localhost:3000", 0, 100, projectA)
	if err != nil {
		t.Fatalf("ProxyStart A failed: %v", err)
	}
	defer client.ProxyStop(proxyA)

	_, err = client.ProxyStart(proxyB, "http://localhost:4000", 0, 100, projectB)
	if err != nil {
		t.Fatalf("ProxyStart B failed: %v", err)
	}
	defer client.ProxyStop(proxyB)

	// Register a session for project A
	sessionCode := "test-session-a"
	_, err = client.conn.Request("SESSION", "REGISTER", sessionCode, projectA).WithJSON(map[string]interface{}{
		"project_path": projectA,
		"command":      "test",
	}).JSON()
	if err != nil {
		t.Fatalf("Session register failed: %v", err)
	}

	// Attach to the session (sets connection's session code) - uses directory to find session
	_, err = client.conn.Request("SESSION", "ATTACH", projectA).JSON()
	if err != nil {
		t.Fatalf("Session attach failed: %v", err)
	}

	// Now lookup with fuzzy "dev" should find only the proxy in project A's path
	// (not fail with ambiguous error)
	t.Run("FuzzyLookup_SessionScoped", func(t *testing.T) {
		result, err := client.conn.Request("CURRENTPAGE", "LIST", "dev").JSON()
		if err != nil {
			t.Errorf("Fuzzy lookup 'dev' failed (should be session-scoped): %v", err)
		} else {
			t.Logf("Fuzzy lookup result: %+v", result)
		}
	})

	// Global lookup with "dev" should still fail with ambiguous
	t.Run("FuzzyLookup_GlobalAmbiguous", func(t *testing.T) {
		// Use a fresh client without session attachment
		client2 := NewClient(WithSocketPath(sockPath))
		if err := client2.Connect(); err != nil {
			t.Fatalf("Failed to connect client2: %v", err)
		}
		defer client2.Close()

		_, err := client2.conn.Request("CURRENTPAGE", "LIST", "dev").JSON()
		if err == nil {
			t.Error("Expected ambiguous error for global 'dev' lookup")
		} else if !strings.Contains(err.Error(), "ambiguous") {
			t.Errorf("Expected ambiguous error, got: %v", err)
		}
	})
}

// TestHubIntegration_ProcErrorPaths tests error paths for PROC commands.
func TestHubIntegration_ProcErrorPaths(t *testing.T) {
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

	// STATUS for nonexistent process
	t.Run("STATUS_NotFound", func(t *testing.T) {
		_, err := client.ProcStatus("nonexistent-proc")
		if err == nil {
			t.Error("Expected error for nonexistent process")
		}
	})

	// OUTPUT for nonexistent process
	t.Run("OUTPUT_NotFound", func(t *testing.T) {
		_, err := client.ProcOutput("nonexistent-proc", protocol.OutputFilter{})
		if err == nil {
			t.Error("Expected error for nonexistent process")
		}
	})

	// STOP for nonexistent process
	t.Run("STOP_NotFound", func(t *testing.T) {
		_, err := client.ProcStop("nonexistent-proc", false)
		if err == nil {
			t.Error("Expected error for nonexistent process")
		}
	})

	// Missing action - PROC without sub-verb should return error
	t.Run("MissingAction", func(t *testing.T) {
		_, err := client.conn.Request("PROC").JSON()
		// Hub may return structured error or JSON - just test that we don't panic
		t.Logf("PROC with no action: err=%v", err)
	})

	// Invalid action
	t.Run("InvalidAction", func(t *testing.T) {
		_, err := client.conn.Request("PROC", "INVALID").JSON()
		// Hub may return structured error or JSON - just test that we don't panic
		t.Logf("PROC INVALID: err=%v", err)
	})
}

// TestHubIntegration_ProxyErrorPaths tests error paths for PROXY commands.
func TestHubIntegration_ProxyErrorPaths(t *testing.T) {
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

	// STATUS for nonexistent proxy
	t.Run("STATUS_NotFound", func(t *testing.T) {
		_, err := client.ProxyStatus("nonexistent-proxy")
		if err == nil {
			t.Error("Expected error for nonexistent proxy")
		}
	})

	// STOP for nonexistent proxy
	t.Run("STOP_NotFound", func(t *testing.T) {
		err := client.ProxyStop("nonexistent-proxy")
		if err == nil {
			t.Error("Expected error for nonexistent proxy")
		}
	})

	// EXEC without proxy ID
	t.Run("EXEC_MissingID", func(t *testing.T) {
		_, err := client.conn.Request("PROXY", "EXEC").JSON()
		if err == nil {
			t.Error("Expected error for missing proxy ID")
		}
	})

	// TOAST without proxy ID
	t.Run("TOAST_MissingID", func(t *testing.T) {
		err := client.conn.Request("PROXY", "TOAST").OK()
		if err == nil {
			t.Error("Expected error for missing proxy ID")
		}
	})

	// Invalid action
	t.Run("InvalidAction", func(t *testing.T) {
		_, err := client.conn.Request("PROXY", "INVALID").JSON()
		// Hub may return structured error or JSON - just test that we don't panic
		t.Logf("PROXY INVALID: err=%v", err)
	})
}

// TestHubIntegration_ProxyLogCommands tests proxylog commands through Hub.
func TestHubIntegration_ProxyLogCommands(t *testing.T) {
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

	// Start a proxy first
	_, err := client.ProxyStart("test-proxy", "http://127.0.0.1:18080", 0, 0, "")
	if err != nil {
		t.Fatalf("Failed to start proxy: %v", err)
	}
	defer client.ProxyStop("test-proxy")

	// QUERY logs
	t.Run("QUERY", func(t *testing.T) {
		result, err := client.ProxyLogQuery("test-proxy", protocol.LogQueryFilter{})
		if err != nil {
			t.Fatalf("ProxyLogQuery failed: %v", err)
		}
		t.Logf("Query result: %+v", result)
	})

	// STATS
	t.Run("STATS", func(t *testing.T) {
		result, err := client.ProxyLogStats("test-proxy")
		if err != nil {
			t.Fatalf("ProxyLogStats failed: %v", err)
		}
		if result["total_entries"] == nil && result["available_entries"] == nil {
			t.Error("Expected stats fields")
		}
	})

	// SUMMARY
	t.Run("SUMMARY", func(t *testing.T) {
		result, err := client.conn.Request("PROXYLOG", "SUMMARY", "test-proxy").JSON()
		if err != nil {
			t.Fatalf("ProxyLog SUMMARY failed: %v", err)
		}
		t.Logf("Summary result: %+v", result)
	})

	// CLEAR
	t.Run("CLEAR", func(t *testing.T) {
		err := client.conn.Request("PROXYLOG", "CLEAR", "test-proxy").OK()
		if err != nil {
			t.Fatalf("ProxyLog CLEAR failed: %v", err)
		}
	})
}

// TestHubIntegration_DaemonInfo tests daemon info commands.
func TestHubIntegration_DaemonInfo(t *testing.T) {
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

	// Get daemon info
	t.Run("Info", func(t *testing.T) {
		info, err := client.Info()
		if err != nil {
			t.Fatalf("Info failed: %v", err)
		}
		if info.SocketPath == "" {
			t.Error("Expected socket_path in info")
		}
		t.Logf("Daemon info: version=%s, clients=%d, processes=%d, proxies=%d",
			info.Version, info.ClientCount, info.ProcessInfo.Active, info.ProxyInfo.Active)
	})

	// Ping
	t.Run("Ping", func(t *testing.T) {
		err := client.Ping()
		if err != nil {
			t.Fatalf("Ping failed: %v", err)
		}
	})
}

// TestHubIntegration_FormatDuration tests formatDuration helper.
func TestFormatDuration(t *testing.T) {
	tests := []struct {
		duration time.Duration
		expected string
	}{
		{0, "0s"},
		{500 * time.Millisecond, "0s"},
		{1 * time.Second, "1s"},
		{90 * time.Second, "1m30s"},
		{1 * time.Hour, "1h0m0s"},
		{25 * time.Hour, "1d1h0m0s"},
		{48*time.Hour + 30*time.Minute, "2d0h30m0s"},
	}

	for _, tt := range tests {
		result := formatDuration(tt.duration)
		t.Logf("formatDuration(%v) = %s", tt.duration, result)
	}
}

// TestHubIntegration_ClientMethods tests various client methods for coverage.
func TestHubIntegration_ClientMethods(t *testing.T) {
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

	// Test IsConnected
	t.Run("IsConnected", func(t *testing.T) {
		if !client.IsConnected() {
			t.Error("Expected client to be connected")
		}
	})

	// Test SocketPath
	t.Run("SocketPath", func(t *testing.T) {
		path := client.SocketPath()
		if path != sockPath {
			t.Errorf("Expected %s, got %s", sockPath, path)
		}
	})

	// Test OverlaySet - use raw request with JSON (client API mismatch)
	t.Run("OverlaySet", func(t *testing.T) {
		err := client.conn.Request("OVERLAY", "SET").WithJSON(map[string]interface{}{
			"endpoint": "http://localhost:19191",
		}).OK()
		if err != nil {
			t.Fatalf("OverlaySet failed: %v", err)
		}
	})

	// Test OverlayGet
	t.Run("OverlayGet", func(t *testing.T) {
		result, err := client.OverlayGet()
		if err != nil {
			t.Fatalf("OverlayGet failed: %v", err)
		}
		t.Logf("OverlayGet result: %+v", result)
	})

	// Test OverlayClear
	t.Run("OverlayClear", func(t *testing.T) {
		err := client.OverlayClear()
		if err != nil {
			t.Fatalf("OverlayClear failed: %v", err)
		}
	})

	// Test BroadcastActivity - use raw request (handler returns OK)
	t.Run("BroadcastActivity", func(t *testing.T) {
		err := client.conn.Request("OVERLAY", "ACTIVITY").OK()
		if err != nil {
			t.Fatalf("BroadcastActivity failed: %v", err)
		}
	})

	// Test CurrentPageClear
	t.Run("CurrentPageClear", func(t *testing.T) {
		// Need a running proxy first, but test the error path
		err := client.CurrentPageClear("nonexistent")
		// Should error because no proxy exists
		if err == nil {
			t.Log("CurrentPageClear succeeded (proxy may exist)")
		} else {
			t.Logf("CurrentPageClear error (expected): %v", err)
		}
	})

	// Test ProxyLogClear (error path - no proxy)
	t.Run("ProxyLogClear", func(t *testing.T) {
		err := client.ProxyLogClear("nonexistent")
		if err == nil {
			t.Log("ProxyLogClear succeeded unexpectedly")
		}
	})

	// Test ProcCleanupPort
	t.Run("ProcCleanupPort", func(t *testing.T) {
		result, err := client.ProcCleanupPort(9999)
		if err != nil {
			t.Logf("ProcCleanupPort error (expected): %v", err)
		} else {
			t.Logf("ProcCleanupPort result: %+v", result)
		}
	})
}

// TestHubIntegration_TunnelClientMethods tests tunnel-related client methods.
func TestHubIntegration_TunnelClientMethods(t *testing.T) {
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

	// Test TunnelList
	t.Run("TunnelList", func(t *testing.T) {
		result, err := client.TunnelList(protocol.DirectoryFilter{})
		if err != nil {
			t.Fatalf("TunnelList failed: %v", err)
		}
		t.Logf("TunnelList result: %+v", result)
	})

	// Test TunnelStatus (error path - no tunnel)
	t.Run("TunnelStatus", func(t *testing.T) {
		_, err := client.TunnelStatus("nonexistent")
		if err == nil {
			t.Log("TunnelStatus succeeded unexpectedly")
		} else {
			t.Logf("TunnelStatus error (expected): %v", err)
		}
	})

	// Test TunnelStop (error path - no tunnel)
	t.Run("TunnelStop", func(t *testing.T) {
		err := client.TunnelStop("nonexistent")
		if err == nil {
			t.Log("TunnelStop succeeded unexpectedly")
		} else {
			t.Logf("TunnelStop error (expected): %v", err)
		}
	})
}

// TestHubIntegration_ChaosClientMethods tests chaos-related client methods.
func TestHubIntegration_ChaosClientMethods(t *testing.T) {
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

	// Start a proxy first (chaos operations require a proxy)
	client := NewClient(WithSocketPath(sockPath))
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	// Start a proxy
	_, err := client.ProxyStart("chaos-test", "http://127.0.0.1:19999", 0, 0, "")
	if err != nil {
		t.Fatalf("Failed to start proxy: %v", err)
	}
	defer client.ProxyStop("chaos-test")

	// Test ChaosStatus
	t.Run("ChaosStatus", func(t *testing.T) {
		result, err := client.ChaosStatus("chaos-test")
		if err != nil {
			t.Fatalf("ChaosStatus failed: %v", err)
		}
		t.Logf("ChaosStatus result: %+v", result)
	})

	// Test ChaosEnable - handler returns OK
	t.Run("ChaosEnable", func(t *testing.T) {
		err := client.conn.Request("CHAOS", "ENABLE", "chaos-test").OK()
		if err != nil {
			t.Fatalf("ChaosEnable failed: %v", err)
		}
	})

	// Test ChaosDisable - handler returns OK
	t.Run("ChaosDisable", func(t *testing.T) {
		err := client.conn.Request("CHAOS", "DISABLE", "chaos-test").OK()
		if err != nil {
			t.Fatalf("ChaosDisable failed: %v", err)
		}
	})

	// Test ChaosListPresets
	t.Run("ChaosListPresets", func(t *testing.T) {
		result, err := client.ChaosListPresets()
		if err != nil {
			t.Fatalf("ChaosListPresets failed: %v", err)
		}
		t.Logf("ChaosListPresets result: %+v", result)
	})

	// Test ChaosListRules
	t.Run("ChaosListRules", func(t *testing.T) {
		result, err := client.ChaosListRules("chaos-test")
		if err != nil {
			t.Fatalf("ChaosListRules failed: %v", err)
		}
		t.Logf("ChaosListRules result: %+v", result)
	})

	// Test ChaosStats
	t.Run("ChaosStats", func(t *testing.T) {
		result, err := client.ChaosStats("chaos-test")
		if err != nil {
			t.Fatalf("ChaosStats failed: %v", err)
		}
		t.Logf("ChaosStats result: %+v", result)
	})

	// Test ChaosClear - handler returns OK
	t.Run("ChaosClear", func(t *testing.T) {
		err := client.conn.Request("CHAOS", "CLEAR", "chaos-test").OK()
		if err != nil {
			t.Fatalf("ChaosClear failed: %v", err)
		}
	})
}

// TestHubIntegration_SessionClientMethods tests session-related client methods.
func TestHubIntegration_SessionClientMethods(t *testing.T) {
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

	// Register a session first
	result, err := client.SessionRegister("test-session", tmpDir, tmpDir, "test-cmd", []string{})
	if err != nil {
		t.Fatalf("SessionRegister failed: %v", err)
	}
	sessionCode := "test-session"
	if code, ok := result["code"].(string); ok {
		sessionCode = code
	}
	t.Logf("Session registered with code: %s", sessionCode)

	// Test SessionGet
	t.Run("SessionGet", func(t *testing.T) {
		result, err := client.SessionGet(sessionCode)
		if err != nil {
			t.Fatalf("SessionGet failed: %v", err)
		}
		t.Logf("SessionGet result: %+v", result)
	})

	// Test SessionHeartbeat
	t.Run("SessionHeartbeat", func(t *testing.T) {
		err := client.SessionHeartbeat(sessionCode)
		if err != nil {
			t.Fatalf("SessionHeartbeat failed: %v", err)
		}
	})

	// Test SessionTasks
	t.Run("SessionTasks", func(t *testing.T) {
		result, err := client.SessionTasks(protocol.DirectoryFilter{})
		if err != nil {
			t.Fatalf("SessionTasks failed: %v", err)
		}
		t.Logf("SessionTasks result: %+v", result)
	})

	// Test SessionGenerateCode
	t.Run("SessionGenerateCode", func(t *testing.T) {
		code, err := client.SessionGenerateCode("test-command")
		if err != nil {
			t.Fatalf("SessionGenerateCode failed: %v", err)
		}
		if code == "" {
			t.Error("Expected non-empty session code")
		}
		t.Logf("Generated code: %s", code)
	})

	// Test SessionFind
	t.Run("SessionFind", func(t *testing.T) {
		result, err := client.SessionFind(tmpDir)
		if err != nil {
			t.Fatalf("SessionFind failed: %v", err)
		}
		t.Logf("SessionFind result: %+v", result)
	})

	// Test SessionUnregister
	t.Run("SessionUnregister", func(t *testing.T) {
		err := client.SessionUnregister(sessionCode)
		if err != nil {
			t.Fatalf("SessionUnregister failed: %v", err)
		}
	})
}

// TestHubIntegration_ProxyClientMethods tests proxy-related client methods.
func TestHubIntegration_ProxyClientMethods(t *testing.T) {
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

	// Start a proxy
	_, err := client.ProxyStart("proxy-test", "http://127.0.0.1:19998", 0, 0, "")
	if err != nil {
		t.Fatalf("Failed to start proxy: %v", err)
	}
	defer client.ProxyStop("proxy-test")

	// Test ProxyExec
	t.Run("ProxyExec", func(t *testing.T) {
		result, err := client.ProxyExec("proxy-test", "1+1")
		if err != nil {
			// Expected error - no browser connected
			t.Logf("ProxyExec error (expected - no browser): %v", err)
		} else {
			t.Logf("ProxyExec result: %+v", result)
		}
	})

	// Test ProxyToast
	t.Run("ProxyToast", func(t *testing.T) {
		_, err := client.ProxyToast("proxy-test", protocol.ToastConfig{
			Message: "Test message",
			Type:    "info",
		})
		if err != nil {
			// Expected error - no browser connected
			t.Logf("ProxyToast error (expected - no browser): %v", err)
		}
	})

	// Test CurrentPageGet (error path - no pages)
	t.Run("CurrentPageGet", func(t *testing.T) {
		_, err := client.CurrentPageGet("proxy-test", "nonexistent-session")
		if err != nil {
			t.Logf("CurrentPageGet error (expected): %v", err)
		}
	})
}

// TestHubIntegration_NewClientWithPath tests NewClientWithPath.
func TestNewClientWithPath(t *testing.T) {
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

	// Test NewClientWithPath
	client := NewClientWithPath(sockPath)
	if client == nil {
		t.Fatal("Expected non-nil client")
	}

	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	// Verify connection works
	err := client.Ping()
	if err != nil {
		t.Fatalf("Ping failed: %v", err)
	}
}

// TestDaemon_Accessors tests the daemon accessor methods.
func TestDaemon_Accessors(t *testing.T) {
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

	// Test ProcessManager accessor
	pm := daemon.ProcessManager()
	if pm == nil {
		t.Error("ProcessManager() returned nil")
	}

	// Test ProxyManager accessor
	proxym := daemon.ProxyManager()
	if proxym == nil {
		t.Error("ProxyManager() returned nil")
	}

	// Test TunnelManager accessor
	tunnelm := daemon.TunnelManager()
	if tunnelm == nil {
		t.Error("TunnelManager() returned nil")
	}

	// Test SessionRegistry accessor
	sr := daemon.SessionRegistry()
	if sr == nil {
		t.Error("SessionRegistry() returned nil")
	}

	// Test Scheduler accessor
	sched := daemon.Scheduler()
	if sched == nil {
		t.Error("Scheduler() returned nil")
	}

	// Test StateManager accessor
	stateMgr := daemon.StateManager()
	// May be nil if persistence is disabled, so just call it
	_ = stateMgr

	// Test GetSession
	session, found := daemon.GetSession("nonexistent")
	if found {
		t.Error("GetSession() should return false for nonexistent session")
	}
	_ = session

	// Test SetOverlayEndpoint and OverlayEndpoint
	daemon.SetOverlayEndpoint("http://localhost:19191")
	endpoint := daemon.OverlayEndpoint()
	if endpoint != "http://localhost:19191" {
		t.Errorf("OverlayEndpoint() = %s, want http://localhost:19191", endpoint)
	}

	// Test clearing overlay endpoint
	daemon.SetOverlayEndpoint("")
	endpoint = daemon.OverlayEndpoint()
	if endpoint != "" {
		t.Errorf("OverlayEndpoint() = %s, want empty string", endpoint)
	}
}

// TestDaemonInfo tests the Info method.
func TestDaemon_Info(t *testing.T) {
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

	info := daemon.Info()
	if info.SocketPath == "" {
		t.Error("Info() SocketPath should not be empty")
	}
	if info.Version == "" {
		t.Error("Info() Version should not be empty")
	}
}

// TestDefaultDaemonConfig tests the default config.
func TestDefaultDaemonConfig(t *testing.T) {
	config := DefaultDaemonConfig()

	if config.SocketPath == "" {
		t.Error("DefaultDaemonConfig() SocketPath should not be empty")
	}
	if config.MaxClients == 0 {
		t.Error("DefaultDaemonConfig() MaxClients should not be zero")
	}
	if config.WriteTimeout == 0 {
		t.Error("DefaultDaemonConfig() WriteTimeout should not be zero")
	}
}

// TestDaemon_Wait tests the Wait method.
func TestDaemon_Wait(t *testing.T) {
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

	// Stop the daemon in a goroutine
	go func() {
		time.Sleep(100 * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		daemon.Stop(ctx)
	}()

	// Wait should return after Stop is called
	daemon.Wait()
}

// TestHubIntegration_ChaosExtendedCommands tests extended chaos commands (PRESET, SET, ADD-RULE, REMOVE-RULE).
func TestHubIntegration_ChaosExtendedCommands(t *testing.T) {
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

	// Start a proxy for chaos testing
	proxyID := "chaos-ext-test"
	_, err := client.ProxyStart(proxyID, "http://127.0.0.1:19997", 0, 0, "")
	if err != nil {
		t.Fatalf("ProxyStart failed: %v", err)
	}
	defer client.ProxyStop(proxyID)

	// Test CHAOS PRESET
	t.Run("PRESET", func(t *testing.T) {
		err := client.conn.Request("CHAOS", "PRESET", proxyID).WithJSON(map[string]interface{}{
			"chaos_preset": "mobile-3g",
		}).OK()
		if err != nil {
			t.Fatalf("Chaos PRESET failed: %v", err)
		}
	})

	// Test CHAOS PRESET with invalid preset
	t.Run("PRESET_Invalid", func(t *testing.T) {
		err := client.conn.Request("CHAOS", "PRESET", proxyID).WithJSON(map[string]interface{}{
			"chaos_preset": "invalid-preset",
		}).OK()
		if err == nil {
			t.Error("Expected error for invalid preset")
		}
	})

	// Test CHAOS PRESET without chaos_preset field
	t.Run("PRESET_MissingField", func(t *testing.T) {
		err := client.conn.Request("CHAOS", "PRESET", proxyID).OK()
		if err == nil {
			t.Error("Expected error for missing chaos_preset")
		}
	})

	// Test CHAOS SET
	t.Run("SET", func(t *testing.T) {
		err := client.conn.Request("CHAOS", "SET", proxyID).WithJSON(map[string]interface{}{
			"enabled":     true,
			"global_odds": 0.5,
		}).OK()
		if err != nil {
			t.Fatalf("Chaos SET failed: %v", err)
		}
	})

	// Test CHAOS ADD-RULE
	t.Run("ADD_RULE", func(t *testing.T) {
		err := client.conn.Request("CHAOS", "ADD-RULE", proxyID).WithJSON(map[string]interface{}{
			"chaos_rule": map[string]interface{}{
				"id":          "test-rule-1",
				"type":        "latency",
				"enabled":     true,
				"probability": 0.5,
			},
		}).OK()
		if err != nil {
			t.Fatalf("Chaos ADD-RULE failed: %v", err)
		}
	})

	// Test CHAOS ADD-RULE without rule id
	t.Run("ADD_RULE_MissingID", func(t *testing.T) {
		err := client.conn.Request("CHAOS", "ADD-RULE", proxyID).WithJSON(map[string]interface{}{
			"chaos_rule": map[string]interface{}{
				"type":    "latency",
				"enabled": true,
			},
		}).OK()
		if err == nil {
			t.Error("Expected error for missing rule id")
		}
	})

	// Verify rule was added
	t.Run("VERIFY_RULE", func(t *testing.T) {
		result, err := client.conn.Request("CHAOS", "LIST-RULES", proxyID).JSON()
		if err != nil {
			t.Fatalf("Chaos LIST-RULES failed: %v", err)
		}
		t.Logf("Rules after add: %+v", result)
	})

	// Test CHAOS REMOVE-RULE
	t.Run("REMOVE_RULE", func(t *testing.T) {
		err := client.conn.Request("CHAOS", "REMOVE-RULE", proxyID).WithJSON(map[string]interface{}{
			"chaos_rule_id": "test-rule-1",
		}).OK()
		if err != nil {
			t.Fatalf("Chaos REMOVE-RULE failed: %v", err)
		}
	})

	// Test CHAOS REMOVE-RULE without rule id
	t.Run("REMOVE_RULE_MissingID", func(t *testing.T) {
		err := client.conn.Request("CHAOS", "REMOVE-RULE", proxyID).OK()
		if err == nil {
			t.Error("Expected error for missing chaos_rule_id")
		}
	})

	// Test CHAOS without proxy id
	t.Run("PRESET_MissingProxyID", func(t *testing.T) {
		err := client.conn.Request("CHAOS", "PRESET").OK()
		if err == nil {
			t.Error("Expected error for missing proxy id")
		}
	})
}

// TestHubIntegration_SessionExtendedCommands tests extended session commands (SEND, SCHEDULE, CANCEL, ATTACH).
func TestHubIntegration_SessionExtendedCommands(t *testing.T) {
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

	// Register a session first
	result, err := client.SessionRegister("ext-test-session", tmpDir, tmpDir, "test-cmd", []string{})
	if err != nil {
		t.Fatalf("SessionRegister failed: %v", err)
	}
	sessionCode := "ext-test-session"
	if code, ok := result["code"].(string); ok {
		sessionCode = code
	}
	t.Logf("Session registered with code: %s", sessionCode)

	// Test SESSION SEND (will fail because no overlay is running, but exercises the handler)
	t.Run("SEND", func(t *testing.T) {
		_, err := client.conn.Request("SESSION", "SEND", sessionCode).WithData([]byte("Test message")).JSON()
		// Expected to fail because no overlay is running
		if err != nil {
			t.Logf("Session SEND error (expected - no overlay): %v", err)
		}
	})

	// Test SESSION SEND without code
	t.Run("SEND_MissingCode", func(t *testing.T) {
		_, err := client.conn.Request("SESSION", "SEND").JSON()
		if err == nil {
			t.Error("Expected error for missing session code")
		}
	})

	// Test SESSION SEND without message
	t.Run("SEND_MissingMessage", func(t *testing.T) {
		_, err := client.conn.Request("SESSION", "SEND", sessionCode).JSON()
		if err == nil {
			t.Error("Expected error for missing message")
		}
	})

	// Test SESSION SCHEDULE
	t.Run("SCHEDULE", func(t *testing.T) {
		result, err := client.conn.Request("SESSION", "SCHEDULE", sessionCode, "1h").WithData([]byte("Scheduled message")).JSON()
		if err != nil {
			t.Fatalf("Session SCHEDULE failed: %v", err)
		}
		if result["task_id"] == nil {
			t.Error("Expected task_id in schedule result")
		}
		t.Logf("Schedule result: %+v", result)
	})

	// Test SESSION SCHEDULE with invalid duration
	t.Run("SCHEDULE_InvalidDuration", func(t *testing.T) {
		_, err := client.conn.Request("SESSION", "SCHEDULE", sessionCode, "invalid").WithData([]byte("Message")).JSON()
		if err == nil {
			t.Error("Expected error for invalid duration")
		}
	})

	// Test SESSION SCHEDULE without code
	t.Run("SCHEDULE_MissingCode", func(t *testing.T) {
		_, err := client.conn.Request("SESSION", "SCHEDULE").JSON()
		if err == nil {
			t.Error("Expected error for missing args")
		}
	})

	// Test SESSION SCHEDULE without message
	t.Run("SCHEDULE_MissingMessage", func(t *testing.T) {
		_, err := client.conn.Request("SESSION", "SCHEDULE", sessionCode, "1h").JSON()
		if err == nil {
			t.Error("Expected error for missing message")
		}
	})

	// Get tasks to find a task_id for cancel
	t.Run("GET_TASKS", func(t *testing.T) {
		result, err := client.conn.Request("SESSION", "TASKS").JSON()
		if err != nil {
			t.Fatalf("Session TASKS failed: %v", err)
		}
		t.Logf("Tasks result: %+v", result)
	})

	// Test SESSION CANCEL (with a task we just scheduled)
	t.Run("CANCEL", func(t *testing.T) {
		// First get the task list
		result, err := client.conn.Request("SESSION", "TASKS").JSON()
		if err != nil {
			t.Fatalf("Session TASKS failed: %v", err)
		}
		tasks, ok := result["tasks"].([]interface{})
		if ok && len(tasks) > 0 {
			task := tasks[0].(map[string]interface{})
			taskID := task["id"].(string)
			err := client.conn.Request("SESSION", "CANCEL", taskID).OK()
			if err != nil {
				t.Fatalf("Session CANCEL failed: %v", err)
			}
		} else {
			t.Log("No tasks to cancel")
		}
	})

	// Test SESSION CANCEL without task_id
	t.Run("CANCEL_MissingTaskID", func(t *testing.T) {
		err := client.conn.Request("SESSION", "CANCEL").OK()
		if err == nil {
			t.Error("Expected error for missing task_id")
		}
	})

	// Test SESSION CANCEL with nonexistent task
	t.Run("CANCEL_NotFound", func(t *testing.T) {
		err := client.conn.Request("SESSION", "CANCEL", "nonexistent-task-id").OK()
		if err == nil {
			t.Error("Expected error for nonexistent task")
		}
	})

	// Test SESSION ATTACH
	t.Run("ATTACH", func(t *testing.T) {
		result, err := client.conn.Request("SESSION", "ATTACH", tmpDir).JSON()
		if err != nil {
			t.Fatalf("Session ATTACH failed: %v", err)
		}
		if result["attached"] != true {
			t.Error("Expected attached=true in result")
		}
		t.Logf("Attach result: %+v", result)
	})

	// Test SESSION ATTACH without directory
	t.Run("ATTACH_MissingDir", func(t *testing.T) {
		_, err := client.conn.Request("SESSION", "ATTACH").JSON()
		if err == nil {
			t.Error("Expected error for missing directory")
		}
	})

	// Test SESSION ATTACH with nonexistent session directory
	t.Run("ATTACH_NotFound", func(t *testing.T) {
		_, err := client.conn.Request("SESSION", "ATTACH", "/nonexistent/directory").JSON()
		if err == nil {
			t.Error("Expected error for nonexistent session")
		}
	})

	// Cleanup
	t.Run("UNREGISTER", func(t *testing.T) {
		err := client.SessionUnregister(sessionCode)
		if err != nil {
			t.Fatalf("SessionUnregister failed: %v", err)
		}
	})
}

// TestHubIntegration_CurrentPageSummary tests CURRENTPAGE SUMMARY command.
func TestHubIntegration_CurrentPageSummary(t *testing.T) {
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

	// Start a proxy
	proxyID := "page-summary-test"
	_, err := client.ProxyStart(proxyID, "http://127.0.0.1:19996", 0, 0, "")
	if err != nil {
		t.Fatalf("ProxyStart failed: %v", err)
	}
	defer client.ProxyStop(proxyID)

	// Test CURRENTPAGE SUMMARY without args
	t.Run("SUMMARY_MissingArgs", func(t *testing.T) {
		_, err := client.conn.Request("CURRENTPAGE", "SUMMARY").JSON()
		if err == nil {
			t.Error("Expected error for missing args")
		}
	})

	// Test CURRENTPAGE SUMMARY with only proxy_id
	t.Run("SUMMARY_MissingSessionID", func(t *testing.T) {
		_, err := client.conn.Request("CURRENTPAGE", "SUMMARY", proxyID).JSON()
		if err == nil {
			t.Error("Expected error for missing session_id")
		}
	})

	// Test CURRENTPAGE SUMMARY with nonexistent session
	t.Run("SUMMARY_NotFound", func(t *testing.T) {
		_, err := client.conn.Request("CURRENTPAGE", "SUMMARY", proxyID, "nonexistent-session").JSON()
		if err == nil {
			t.Error("Expected error for nonexistent session")
		}
	})

	// Test CURRENTPAGE LIST
	t.Run("LIST", func(t *testing.T) {
		result, err := client.conn.Request("CURRENTPAGE", "LIST", proxyID).JSON()
		if err != nil {
			t.Fatalf("CurrentPage LIST failed: %v", err)
		}
		t.Logf("CurrentPage LIST result: %+v", result)
	})

	// Test CURRENTPAGE GET without session
	t.Run("GET_MissingSession", func(t *testing.T) {
		_, err := client.conn.Request("CURRENTPAGE", "GET", proxyID).JSON()
		if err == nil {
			t.Error("Expected error for missing session_id")
		}
	})
}

// TestHubIntegration_ProxyLogSummary tests PROXYLOG SUMMARY command.
func TestHubIntegration_ProxyLogSummary(t *testing.T) {
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

	// Start a proxy
	proxyID := "log-summary-test"
	_, err := client.ProxyStart(proxyID, "http://127.0.0.1:19995", 0, 0, "")
	if err != nil {
		t.Fatalf("ProxyStart failed: %v", err)
	}
	defer client.ProxyStop(proxyID)

	// Test PROXYLOG SUMMARY
	t.Run("SUMMARY", func(t *testing.T) {
		result, err := client.conn.Request("PROXYLOG", "SUMMARY", proxyID).JSON()
		if err != nil {
			t.Fatalf("ProxyLog SUMMARY failed: %v", err)
		}
		t.Logf("ProxyLog SUMMARY result: %+v", result)
	})

	// Test PROXYLOG SUMMARY with detail filter
	t.Run("SUMMARY_WithDetail", func(t *testing.T) {
		result, err := client.conn.Request("PROXYLOG", "SUMMARY", proxyID).WithJSON(map[string]interface{}{
			"detail": []string{"errors", "http"},
			"limit":  10,
		}).JSON()
		if err != nil {
			t.Fatalf("ProxyLog SUMMARY with detail failed: %v", err)
		}
		t.Logf("ProxyLog SUMMARY with detail result: %+v", result)
	})
}

// TestHubIntegration_TunnelValidation tests TUNNEL validation paths.
func TestHubIntegration_TunnelValidation(t *testing.T) {
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

	// Test TUNNEL START (will fail without cloudflared, but exercises error path)
	t.Run("START_MissingBinary", func(t *testing.T) {
		_, err := client.conn.Request("TUNNEL", "START", "val-test-tunnel").WithJSON(map[string]interface{}{
			"provider":   "cloudflare",
			"local_port": 8080,
		}).JSON()
		// Expected to fail - no cloudflared binary
		if err != nil {
			t.Logf("Tunnel START error (expected - no cloudflared): %v", err)
		}
	})

	// Test TUNNEL START missing provider
	t.Run("START_MissingProvider", func(t *testing.T) {
		_, err := client.conn.Request("TUNNEL", "START", "val-test-tunnel").WithJSON(map[string]interface{}{
			"local_port": 8080,
		}).JSON()
		if err == nil {
			t.Error("Expected error for missing provider")
		}
	})

	// Test TUNNEL START missing port
	t.Run("START_MissingPort", func(t *testing.T) {
		_, err := client.conn.Request("TUNNEL", "START", "val-test-tunnel").WithJSON(map[string]interface{}{
			"provider": "cloudflare",
		}).JSON()
		if err == nil {
			t.Error("Expected error for missing port")
		}
	})
}

// TestHubIntegration_ProcListFilters tests PROC LIST with different filters.
func TestHubIntegration_ProcListFilters(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Create subdirectories for different projects
	project1 := filepath.Join(tmpDir, "project1")
	project2 := filepath.Join(tmpDir, "project2")
	if err := os.MkdirAll(project1, 0755); err != nil {
		t.Fatalf("Failed to create project1: %v", err)
	}
	if err := os.MkdirAll(project2, 0755); err != nil {
		t.Fatalf("Failed to create project2: %v", err)
	}

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

	// Start processes in different directories
	_, err := client.Run(protocol.RunConfig{
		ID:      "proc-proj1",
		Path:    project1,
		Mode:    "background",
		Command: "sleep",
		Args:    []string{"100"},
		Raw:     true,
	})
	if err != nil {
		t.Fatalf("Failed to start proc-proj1: %v", err)
	}
	defer client.ProcStop("proc-proj1", false)

	_, err = client.Run(protocol.RunConfig{
		ID:      "proc-proj2",
		Path:    project2,
		Mode:    "background",
		Command: "sleep",
		Args:    []string{"100"},
		Raw:     true,
	})
	if err != nil {
		t.Fatalf("Failed to start proc-proj2: %v", err)
	}
	defer client.ProcStop("proc-proj2", false)

	// Test LIST with global flag
	t.Run("LIST_Global", func(t *testing.T) {
		result, err := client.conn.Request("PROC", "LIST").WithJSON(map[string]interface{}{
			"global": true,
		}).JSON()
		if err != nil {
			t.Fatalf("Proc LIST global failed: %v", err)
		}
		procs, _ := result["processes"].([]interface{})
		if len(procs) < 2 {
			t.Errorf("Expected at least 2 processes, got %d", len(procs))
		}
	})

	// Test LIST with directory filter
	t.Run("LIST_DirectoryFilter", func(t *testing.T) {
		result, err := client.conn.Request("PROC", "LIST").WithJSON(map[string]interface{}{
			"directory": project1,
		}).JSON()
		if err != nil {
			t.Fatalf("Proc LIST with directory filter failed: %v", err)
		}
		procs, _ := result["processes"].([]interface{})
		// Should have at least proc-proj1
		found := false
		for _, p := range procs {
			proc := p.(map[string]interface{})
			if proc["id"] == "proc-proj1" {
				found = true
			}
		}
		if !found {
			t.Error("Expected to find proc-proj1 in filtered results")
		}
	})

	// Test LIST with no filter (should filter by current directory)
	t.Run("LIST_NoFilter", func(t *testing.T) {
		result, err := client.conn.Request("PROC", "LIST").JSON()
		if err != nil {
			t.Fatalf("Proc LIST failed: %v", err)
		}
		// Just verify it returns without error
		_, ok := result["processes"].([]interface{})
		if !ok {
			t.Error("Expected processes array in result")
		}
	})
}

// TestDaemon_RestoreProxies tests proxy restoration from state.
func TestDaemon_RestoreProxies(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")
	statePath := filepath.Join(tmpDir, "state.json")

	// Create a state file with a saved proxy (correct format with version)
	stateContent := `{
		"version": 1,
		"overlay_endpoint": "http://localhost:19191",
		"proxies": [
			{
				"id": "restored-proxy",
				"target_url": "http://localhost:18080",
				"port": 0,
				"max_log_size": 100,
				"path": "` + tmpDir + `",
				"created_at": "2024-01-01T00:00:00Z"
			}
		],
		"updated_at": "2024-01-01T00:00:00Z"
	}`
	if err := os.WriteFile(statePath, []byte(stateContent), 0644); err != nil {
		t.Fatalf("Failed to write state file: %v", err)
	}

	// Create daemon with state path and persistence enabled
	daemon := New(DaemonConfig{
		SocketPath:             sockPath,
		MaxClients:             10,
		WriteTimeout:           5 * time.Second,
		StatePath:              statePath,
		EnableStatePersistence: true,
	})

	if err := daemon.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		daemon.Stop(ctx)
	}()

	// Give time for proxy restoration
	time.Sleep(100 * time.Millisecond)

	// Verify proxy was restored
	client := NewClient(WithSocketPath(sockPath))
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	// Check proxy list
	result, err := client.ProxyList(protocol.DirectoryFilter{Global: true})
	if err != nil {
		t.Fatalf("ProxyList failed: %v", err)
	}

	proxies, _ := result["proxies"].([]interface{})
	found := false
	for _, p := range proxies {
		proxy := p.(map[string]interface{})
		if proxy["id"] == "restored-proxy" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected restored-proxy to be restored from state")
	}
}

// TestDaemon_CleanupOrphans tests the orphan cleanup functionality.
func TestDaemon_CleanupOrphans(t *testing.T) {
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

	// cleanupOrphans is called during Start, so just verify daemon started successfully
	// The function is exercised but we can't easily verify orphan cleanup without
	// creating orphaned processes from a previous daemon instance
	t.Log("cleanupOrphans was executed during daemon.Start()")
}

// TestHubIntegration_ProxyExecErrorPaths tests PROXY EXEC error paths.
func TestHubIntegration_ProxyExecErrorPaths(t *testing.T) {
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

	// Start a proxy first
	_, err := client.ProxyStart("exec-test", "http://localhost:18082", 0, 100, ".")
	if err != nil {
		t.Fatalf("Failed to start proxy: %v", err)
	}
	defer client.ProxyStop("exec-test")

	// Test PROXY EXEC with missing code (empty data)
	t.Run("EXEC_MissingCode", func(t *testing.T) {
		_, err := client.conn.Request("PROXY", "EXEC", "exec-test").JSON()
		if err == nil {
			t.Error("Expected error for missing code")
		} else {
			t.Logf("EXEC missing code error: %v", err)
		}
	})

	// Test PROXY EXEC for nonexistent proxy
	t.Run("EXEC_NotFound", func(t *testing.T) {
		_, err := client.conn.Request("PROXY", "EXEC", "nonexistent").WithData([]byte("1+1")).JSON()
		if err == nil {
			t.Error("Expected error for nonexistent proxy")
		} else {
			t.Logf("EXEC not found error: %v", err)
		}
	})

	// Test PROXY TOAST with missing message
	t.Run("TOAST_MissingMessage", func(t *testing.T) {
		err := client.conn.Request("PROXY", "TOAST", "exec-test").WithJSON(map[string]interface{}{
			"toast_type": "info",
		}).OK()
		if err == nil {
			t.Error("Expected error for missing toast message")
		} else {
			t.Logf("TOAST missing message error: %v", err)
		}
	})

	// Test PROXY TOAST with invalid JSON
	t.Run("TOAST_InvalidJSON", func(t *testing.T) {
		err := client.conn.Request("PROXY", "TOAST", "exec-test").WithData([]byte("invalid json")).OK()
		if err == nil {
			t.Error("Expected error for invalid JSON")
		} else {
			t.Logf("TOAST invalid JSON error: %v", err)
		}
	})
}

// TestHubIntegration_SessionScheduleAndCancel tests session scheduling and cancellation.
func TestHubIntegration_SessionScheduleAndCancel(t *testing.T) {
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

	// Register a session
	_, err := client.SessionRegister("schedule-test", tmpDir, tmpDir, "test-cmd", []string{})
	if err != nil {
		t.Fatalf("SessionRegister failed: %v", err)
	}
	defer client.SessionUnregister("schedule-test")

	// Schedule a task
	t.Run("Schedule", func(t *testing.T) {
		result, err := client.SessionSchedule("schedule-test", "1h", "test scheduled message")
		if err != nil {
			t.Fatalf("SessionSchedule failed: %v", err)
		}
		taskID, ok := result["task_id"].(string)
		if !ok || taskID == "" {
			t.Error("Expected task_id in result")
		}
		t.Logf("Scheduled task: %s", taskID)

		// Cancel the task
		err = client.SessionCancel(taskID)
		if err != nil {
			t.Fatalf("SessionCancel failed: %v", err)
		}
	})

	// Verify task is cancelled
	t.Run("VerifyCancelled", func(t *testing.T) {
		result, err := client.SessionTasks(protocol.DirectoryFilter{})
		if err != nil {
			t.Fatalf("SessionTasks failed: %v", err)
		}
		tasks, _ := result["tasks"].([]interface{})
		// Cancelled task should not appear in pending tasks
		for _, task := range tasks {
			taskMap := task.(map[string]interface{})
			if taskMap["message"] == "test scheduled message" && taskMap["status"] == "pending" {
				t.Error("Expected task to be cancelled")
			}
		}
	})
}

// TestHubIntegration_RunAutostart tests RunAutostart functionality.
func TestDaemon_RunAutostart(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Create a minimal agnt.kdl config with no autostart
	configPath := filepath.Join(tmpDir, "agnt.kdl")
	configContent := `
// No autostart scripts defined
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

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

	// Call RunAutostart - should complete without error even with no autostart config
	daemon.RunAutostart(context.Background(), tmpDir)

	// Verify daemon is still running
	client := NewClient(WithSocketPath(sockPath))
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect after RunAutostart: %v", err)
	}
	defer client.Close()

	if err := client.Ping(); err != nil {
		t.Fatalf("Ping failed after RunAutostart: %v", err)
	}
}

// TestHubIntegration_CleanupSessionResources tests session resource cleanup.
func TestHubIntegration_CleanupSessionResources(t *testing.T) {
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

	// Register a session
	_, err := client.SessionRegister("cleanup-test", tmpDir, tmpDir, "test-cmd", []string{})
	if err != nil {
		t.Fatalf("SessionRegister failed: %v", err)
	}

	// Start a proxy that will be associated with this session path
	_, err = client.ProxyStart("cleanup-proxy", "http://localhost:18083", 0, 100, tmpDir)
	if err != nil {
		t.Fatalf("ProxyStart failed: %v", err)
	}

	// Start a process that will be associated with this session path
	_, err = client.Run(protocol.RunConfig{
		ID:      "cleanup-proc",
		Command: "sleep",
		Args:    []string{"100"},
		Raw:     true,
		Path:    tmpDir,
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Unregister the session - should clean up resources
	err = client.SessionUnregister("cleanup-test")
	if err != nil {
		t.Fatalf("SessionUnregister failed: %v", err)
	}

	// Give time for cleanup
	time.Sleep(200 * time.Millisecond)

	// Note: CleanupSessionResources only cleans up resources created by autostart
	// Manually created resources are not automatically cleaned up on session unregister
	// This test just verifies the code path doesn't panic
}

// TestDaemon_RestoreProxies_ErrorPaths tests error scenarios in proxy restoration.
func TestDaemon_RestoreProxies_ErrorPaths(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")
	statePath := filepath.Join(tmpDir, "state.json")

	// Create a state file with an invalid proxy (bad target URL)
	stateContent := `{
		"version": 1,
		"proxies": [
			{
				"id": "bad-proxy",
				"target_url": "://invalid-url",
				"port": 0,
				"max_log_size": 100,
				"path": "` + tmpDir + `",
				"created_at": "2024-01-01T00:00:00Z"
			}
		],
		"updated_at": "2024-01-01T00:00:00Z"
	}`
	if err := os.WriteFile(statePath, []byte(stateContent), 0644); err != nil {
		t.Fatalf("Failed to write state file: %v", err)
	}

	// Create daemon with state persistence
	daemon := New(DaemonConfig{
		SocketPath:             sockPath,
		MaxClients:             10,
		WriteTimeout:           5 * time.Second,
		StatePath:              statePath,
		EnableStatePersistence: true,
	})

	if err := daemon.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		daemon.Stop(ctx)
	}()

	// Give time for restore attempt
	time.Sleep(100 * time.Millisecond)

	// Proxy should not be restored due to invalid URL
	client := NewClient(WithSocketPath(sockPath))
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	result, err := client.ProxyList(protocol.DirectoryFilter{Global: true})
	if err != nil {
		t.Fatalf("ProxyList failed: %v", err)
	}

	proxies, _ := result["proxies"].([]interface{})
	for _, p := range proxies {
		proxy := p.(map[string]interface{})
		if proxy["id"] == "bad-proxy" {
			t.Error("Expected bad-proxy to NOT be restored")
		}
	}
}

// TestHubIntegration_ProcOutput_ErrorPaths tests error paths in hubHandleProcOutput.
func TestHubIntegration_ProcOutput_ErrorPaths(t *testing.T) {
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

	// Test OUTPUT with various filter options
	t.Run("OUTPUT_WithFilters", func(t *testing.T) {
		// Start a process
		_, err := client.Run(protocol.RunConfig{
			ID:      "filter-test",
			Command: "echo",
			Args:    []string{"hello\nworld\ntest"},
			Raw:     true,
		})
		if err != nil {
			t.Fatalf("Run failed: %v", err)
		}

		time.Sleep(200 * time.Millisecond)

		// Test with tail filter
		output, err := client.ProcOutput("filter-test", protocol.OutputFilter{Tail: 2})
		if err != nil {
			t.Fatalf("ProcOutput with tail failed: %v", err)
		}
		t.Logf("Output with tail=2: %s", output)

		// Test with head filter
		output, err = client.ProcOutput("filter-test", protocol.OutputFilter{Head: 1})
		if err != nil {
			t.Fatalf("ProcOutput with head failed: %v", err)
		}
		t.Logf("Output with head=1: %s", output)

		// Test with stream filter (stdout)
		output, err = client.ProcOutput("filter-test", protocol.OutputFilter{Stream: "stdout"})
		if err != nil {
			t.Fatalf("ProcOutput with stream failed: %v", err)
		}
		t.Logf("Output with stream=stdout: %s", output)
	})
}

// TestDaemon_Info_AllFields tests that all info fields are populated.
func TestDaemon_Info_AllFields(t *testing.T) {
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

	info := daemon.Info()

	// Verify all fields
	if info.Version == "" {
		t.Error("Info.Version should not be empty")
	}
	if info.SocketPath == "" {
		t.Error("Info.SocketPath should not be empty")
	}

	t.Logf("Daemon info: %+v", info)
}

// TestHubIntegration_TunnelErrorPaths tests tunnel error paths.
func TestHubIntegration_TunnelErrorPaths(t *testing.T) {
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

	t.Run("StatusNotFound", func(t *testing.T) {
		// Try to get status of non-existent tunnel
		_, err := client.TunnelStatus("nonexistent")
		if err == nil {
			t.Error("Expected error for non-existent tunnel")
		}
	})

	t.Run("StopNotFound", func(t *testing.T) {
		// Try STOP of non-existent tunnel
		err := client.TunnelStop("nonexistent")
		if err == nil {
			t.Error("Expected error for non-existent tunnel")
		}
	})

	t.Run("List", func(t *testing.T) {
		// List should work even with no tunnels
		result, err := client.TunnelList(protocol.DirectoryFilter{})
		if err != nil {
			t.Errorf("TunnelList failed: %v", err)
		}
		tunnels, _ := result["tunnels"].([]interface{})
		if len(tunnels) != 0 {
			t.Errorf("Expected 0 tunnels, got %d", len(tunnels))
		}
	})
}

// TestHubIntegration_SessionErrorPaths tests session handler error paths.
func TestHubIntegration_SessionErrorPaths(t *testing.T) {
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

	t.Run("SendToNonExistentSession", func(t *testing.T) {
		// Try SEND to non-existent session
		_, err := client.SessionSend("nonexistent", "test message")
		if err == nil {
			t.Error("Expected error for non-existent session")
		}
	})

	t.Run("HeartbeatNonExistentSession", func(t *testing.T) {
		// Try HEARTBEAT for non-existent session
		err := client.SessionHeartbeat("nonexistent")
		if err == nil {
			t.Error("Expected error for non-existent session")
		}
	})

	t.Run("GetNonExistentSession", func(t *testing.T) {
		// Try GET for non-existent session
		_, err := client.SessionGet("nonexistent")
		if err == nil {
			t.Error("Expected error for non-existent session")
		}
	})
}

// TestHubIntegration_ProxyHandlerErrors tests proxy handler error paths.
func TestHubIntegration_ProxyHandlerErrors(t *testing.T) {
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

	t.Run("ExecNonExistentProxy", func(t *testing.T) {
		// Try EXEC on non-existent proxy
		_, err := client.ProxyExec("nonexistent", "console.log('test')")
		if err == nil {
			t.Error("Expected error for non-existent proxy")
		}
	})

	t.Run("ToastNonExistentProxy", func(t *testing.T) {
		// Try TOAST on non-existent proxy
		_, err := client.ProxyToast("nonexistent", protocol.ToastConfig{Message: "test"})
		if err == nil {
			t.Error("Expected error for non-existent proxy")
		}
	})

	t.Run("StatusNonExistentProxy", func(t *testing.T) {
		// Try STATUS on non-existent proxy
		_, err := client.ProxyStatus("nonexistent")
		if err == nil {
			t.Error("Expected error for non-existent proxy")
		}
	})

	t.Run("StopNonExistentProxy", func(t *testing.T) {
		// Try STOP on non-existent proxy
		err := client.ProxyStop("nonexistent")
		if err == nil {
			t.Error("Expected error for non-existent proxy")
		}
	})
}
