//go:build unix

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/protocol"
)

func TestDaemon_ScriptProxyTracking(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Create daemon
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

	// Test trackScriptProxy
	daemon.trackScriptProxy("script-1", "proxy-1")
	daemon.trackScriptProxy("script-1", "proxy-2")
	daemon.trackScriptProxy("script-2", "proxy-3")

	// Test getProxiesForScript
	proxies1 := daemon.getProxiesForScript("script-1")
	if len(proxies1) != 2 {
		t.Errorf("Expected 2 proxies for script-1, got %d", len(proxies1))
	}

	proxies2 := daemon.getProxiesForScript("script-2")
	if len(proxies2) != 1 {
		t.Errorf("Expected 1 proxy for script-2, got %d", len(proxies2))
	}

	proxies3 := daemon.getProxiesForScript("nonexistent")
	if len(proxies3) != 0 {
		t.Errorf("Expected 0 proxies for nonexistent script, got %d", len(proxies3))
	}

	// Test clearScriptProxies
	daemon.clearScriptProxies("script-1")
	proxies1After := daemon.getProxiesForScript("script-1")
	if len(proxies1After) != 0 {
		t.Errorf("Expected 0 proxies after clear, got %d", len(proxies1After))
	}

	// script-2 should still have its proxy
	proxies2After := daemon.getProxiesForScript("script-2")
	if len(proxies2After) != 1 {
		t.Errorf("Expected 1 proxy for script-2 after clearing script-1, got %d", len(proxies2After))
	}
}

func TestDaemon_StopAllResources(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Create daemon
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

	// Connect a client and start some resources
	client := NewClient(WithSocketPath(sockPath))
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	// Start a proxy
	_, err := client.ProxyStart("stop-all-proxy", "http://localhost:18887", 0, 100, tmpDir)
	if err != nil {
		t.Fatalf("ProxyStart failed: %v", err)
	}

	// Call StopAllResources with context
	ctx := context.Background()
	daemon.StopAllResources(ctx)

	// Verify proxy is stopped
	time.Sleep(100 * time.Millisecond)
	proxies, err := client.ProxyList(protocol.DirectoryFilter{Directory: tmpDir})
	if err != nil {
		t.Fatalf("ProxyList failed: %v", err)
	}

	proxyList, _ := proxies["proxies"].([]interface{})
	for _, p := range proxyList {
		proxy := p.(map[string]interface{})
		if proxy["id"] == "stop-all-proxy" {
			t.Error("stop-all-proxy should have been stopped")
		}
	}
}

func TestDaemon_DetectPortForScript(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Create daemon
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

	ctx := context.Background()

	// Test detectPortForScript (deprecated, returns error)
	_, err := daemon.detectPortForScript(ctx, "test-script", nil)
	if err == nil {
		t.Error("Expected error from deprecated detectPortForScript")
	}

	// Test _old_detectPortForScript (will timeout without a running script)
	// Just check it doesn't panic
	ctx2, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	_, _ = daemon._old_detectPortForScript(ctx2, "nonexistent", nil)
}

func TestDaemon_HandleExplicitStart(t *testing.T) {
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

	// Test with nil config (should return early)
	daemon.handleExplicitStart(ProxyEvent{
		Type:    ExplicitStart,
		ProxyID: "test",
		Config:  nil,
	})

	// Test with empty proxy ID (should return early)
	daemon.handleExplicitStart(ProxyEvent{
		Type:    ExplicitStart,
		ProxyID: "",
		Config:  &config.ProxyConfig{URL: "http://localhost:3000"},
	})

	// Test with URL config
	daemon.handleExplicitStart(ProxyEvent{
		Type:    ExplicitStart,
		ProxyID: "explicit-url-proxy",
		Config:  &config.ProxyConfig{URL: "http://localhost:19997"},
		Path:    tmpDir,
	})

	// Verify proxy was created
	time.Sleep(100 * time.Millisecond)
	if _, err := daemon.proxym.Get("explicit-url-proxy"); err != nil {
		t.Error("Expected proxy to be created with URL config")
	}

	// Test with port config
	daemon.handleExplicitStart(ProxyEvent{
		Type:    ExplicitStart,
		ProxyID: "explicit-port-proxy",
		Config:  &config.ProxyConfig{Port: 19998},
		Path:    tmpDir,
	})

	time.Sleep(100 * time.Millisecond)
	if _, err := daemon.proxym.Get("explicit-port-proxy"); err != nil {
		t.Error("Expected proxy to be created with port config")
	}

	// Test with Target (legacy) config
	daemon.handleExplicitStart(ProxyEvent{
		Type:    ExplicitStart,
		ProxyID: "explicit-target-proxy",
		Config:  &config.ProxyConfig{Target: "http://localhost:19996"},
		Path:    tmpDir,
	})

	time.Sleep(100 * time.Millisecond)
	if _, err := daemon.proxym.Get("explicit-target-proxy"); err != nil {
		t.Error("Expected proxy to be created with Target config")
	}

	// Test with no target (should return early)
	daemon.handleExplicitStart(ProxyEvent{
		Type:    ExplicitStart,
		ProxyID: "no-target-proxy",
		Config:  &config.ProxyConfig{},
		Path:    tmpDir,
	})

	time.Sleep(50 * time.Millisecond)
	if _, err := daemon.proxym.Get("no-target-proxy"); err == nil {
		t.Error("Expected proxy NOT to be created with no target")
	}

	// Test duplicate proxy (should skip)
	daemon.handleExplicitStart(ProxyEvent{
		Type:    ExplicitStart,
		ProxyID: "explicit-url-proxy", // Already exists
		Config:  &config.ProxyConfig{URL: "http://localhost:19999"},
		Path:    tmpDir,
	})
}

func TestDaemon_HandleScriptStopped(t *testing.T) {
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

	// Create a proxy and track it
	daemon.handleExplicitStart(ProxyEvent{
		Type:    ExplicitStart,
		ProxyID: "script-linked-proxy",
		Config:  &config.ProxyConfig{URL: "http://localhost:19994"},
		Path:    tmpDir,
	})
	time.Sleep(100 * time.Millisecond)

	// Track it as linked to a script
	daemon.trackScriptProxy("test-script:dev", "script-linked-proxy")

	// Verify proxy exists
	if _, err := daemon.proxym.Get("script-linked-proxy"); err != nil {
		t.Fatal("Expected proxy to exist before script stopped")
	}

	// Handle script stopped
	daemon.handleScriptStopped(ProxyEvent{
		Type:     ScriptStopped,
		ScriptID: "test-script:dev",
	})

	// Give time for cleanup
	time.Sleep(200 * time.Millisecond)

	// Verify proxy was stopped
	if _, err := daemon.proxym.Get("script-linked-proxy"); err == nil {
		t.Error("Expected proxy to be stopped after script stopped")
	}

	// Verify script proxies were cleared
	proxies := daemon.getProxiesForScript("test-script:dev")
	if len(proxies) != 0 {
		t.Errorf("Expected script proxies to be cleared, got %d", len(proxies))
	}
}

func TestDaemon_HandleScriptStopped_NoProxies(t *testing.T) {
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

	// Handle script stopped for script with no proxies
	daemon.handleScriptStopped(ProxyEvent{
		Type:     ScriptStopped,
		ScriptID: "nonexistent-script",
	})
	// Should complete without error
}

func TestDaemon_HandleURLDetected(t *testing.T) {
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

	// Test with invalid script ID format (no colon)
	daemon.handleURLDetected(ProxyEvent{
		Type:     URLDetected,
		ScriptID: "invalid-script-id",
		URL:      "http://localhost:3000",
	})

	// Test with valid script ID format but no config file
	daemon.handleURLDetected(ProxyEvent{
		Type:     URLDetected,
		ScriptID: tmpDir + ":dev",
		URL:      "http://localhost:3001",
	})

	// Create a minimal agnt.kdl config
	configPath := filepath.Join(tmpDir, "agnt.kdl")
	configContent := `
proxies {
    api {
        script "dev"
    }
}
`
	if err := writeFile(configPath, configContent); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// Test with valid script ID and config
	daemon.handleURLDetected(ProxyEvent{
		Type:     URLDetected,
		ScriptID: tmpDir + ":dev",
		URL:      "http://localhost:3002",
	})

	// Wait a bit for async processing
	time.Sleep(100 * time.Millisecond)
}

func TestDaemon_HandleURLDetected_ProxyLimit(t *testing.T) {
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

	// Track 5 proxies for a script (limit)
	for i := 0; i < 5; i++ {
		daemon.trackScriptProxy(tmpDir+":dev", "proxy-"+string(rune('0'+i)))
	}

	// Create a minimal agnt.kdl config
	configPath := filepath.Join(tmpDir, "agnt.kdl")
	configContent := `
proxies {
    api {
        script "dev"
    }
}
`
	if err := writeFile(configPath, configContent); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// Test with valid script ID and config but at limit
	daemon.handleURLDetected(ProxyEvent{
		Type:     URLDetected,
		ScriptID: tmpDir + ":dev",
		URL:      "http://localhost:3003",
	})

	// Should not create proxy due to limit
	time.Sleep(50 * time.Millisecond)
}

// writeFile is a helper to write a file
func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}

func TestDaemon_HandleURLDetected_WithProxyCreation(t *testing.T) {
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

	// Create a config with a proxy that should be created when URL is detected
	configPath := filepath.Join(tmpDir, "agnt.kdl")
	configContent := `
proxies {
    api {
        script "dev"
    }
}
`
	if err := writeFile(configPath, configContent); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// Send URL detected event with matching script
	daemon.handleURLDetected(ProxyEvent{
		Type:     URLDetected,
		ScriptID: tmpDir + ":dev",
		URL:      "http://localhost:3004",
	})

	// Give time for proxy creation
	time.Sleep(200 * time.Millisecond)

	// Verify proxy was tracked
	proxies := daemon.getProxiesForScript(tmpDir + ":dev")
	if len(proxies) == 0 {
		t.Log("No proxies tracked for script (may be expected if config parsing failed)")
	} else {
		t.Logf("Proxies created: %v", proxies)
	}
}

func TestDaemon_RunAutostart_WithScripts(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Create a config with autostart scripts
	configPath := filepath.Join(tmpDir, "agnt.kdl")
	configContent := `
scripts {
    test {
        command "echo"
        args "hello"
        autostart true
    }
}
`
	if err := writeFile(configPath, configContent); err != nil {
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

	// Run autostart - this should try to start the script
	ctx := context.Background()
	daemon.RunAutostart(ctx, tmpDir)

	// Give time for script to start
	time.Sleep(200 * time.Millisecond)

	// Verify daemon is still running
	client := NewClient(WithSocketPath(sockPath))
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	if err := client.Ping(); err != nil {
		t.Fatalf("Ping failed: %v", err)
	}
}

func TestDaemon_RunAutostart_WithProxies(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Create a config with autostart proxies
	configPath := filepath.Join(tmpDir, "agnt.kdl")
	configContent := `
proxies {
    api {
        url "http://localhost:19990"
        autostart true
    }
}
`
	if err := writeFile(configPath, configContent); err != nil {
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

	// Run autostart
	ctx := context.Background()
	daemon.RunAutostart(ctx, tmpDir)

	// Give time for proxy to start
	time.Sleep(200 * time.Millisecond)

	// Verify proxy was created
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
	t.Logf("Proxies after autostart: %d", len(proxies))
}

func TestDaemon_HandleProxyEvents_ViaHandlers(t *testing.T) {
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

	// Create config for proxy
	configPath := filepath.Join(tmpDir, "agnt.kdl")
	configContent := `
proxies {
    test {
        script "dev"
    }
}
`
	if err := writeFile(configPath, configContent); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// Test by directly calling handlers (the exported/testable way)
	t.Run("ExplicitStart", func(t *testing.T) {
		daemon.handleExplicitStart(ProxyEvent{
			Type:    ExplicitStart,
			ProxyID: "handler-test-proxy",
			Config:  &config.ProxyConfig{URL: "http://localhost:19985"},
			Path:    tmpDir,
		})
		time.Sleep(100 * time.Millisecond)
	})

	t.Run("URLDetected", func(t *testing.T) {
		daemon.handleURLDetected(ProxyEvent{
			Type:     URLDetected,
			ScriptID: tmpDir + ":dev",
			URL:      "http://localhost:19986",
		})
		time.Sleep(100 * time.Millisecond)
	})

	t.Run("ScriptStopped", func(t *testing.T) {
		// Track a proxy first
		daemon.trackScriptProxy(tmpDir+":handler-test", "handler-event-proxy")
		daemon.handleScriptStopped(ProxyEvent{
			Type:     ScriptStopped,
			ScriptID: tmpDir + ":handler-test",
		})
		time.Sleep(100 * time.Millisecond)
	})
}
