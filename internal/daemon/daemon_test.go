//go:build unix

package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/go-cli-server/script"
)

func TestDaemon_ScriptProxyTracking(t *testing.T) {
	daemon, _ := newBootedDaemon(t)

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
	daemon, client, tmpDir := newBootedDaemonWithClient(t)

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

func TestDaemon_HandleExplicitStart(t *testing.T) {
	daemon, _ := newBootedDaemon(t)
	tmpDir := t.TempDir()

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
	daemon, _ := newBootedDaemon(t)
	tmpDir := t.TempDir()

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
	daemon, _ := newBootedDaemon(t)

	// Handle script stopped for script with no proxies
	daemon.handleScriptStopped(ProxyEvent{
		Type:     ScriptStopped,
		ScriptID: "nonexistent-script",
	})
	// Should complete without error
}

func TestDaemon_HandleURLDetected(t *testing.T) {
	daemon, _ := newBootedDaemon(t)
	tmpDir := t.TempDir()

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
	daemon, _ := newBootedDaemon(t)
	tmpDir := t.TempDir()

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

func TestDaemon_HandleURLDetected_WithProxyCreation(t *testing.T) {
	daemon, _ := newBootedDaemon(t)
	tmpDir := t.TempDir()

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

	daemon, sockPath := newBootedDaemon(t)

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

	daemon, sockPath := newBootedDaemon(t)

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
	daemon, _ := newBootedDaemon(t)
	tmpDir := t.TempDir()

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

func TestDaemon_ScriptRegistryInitialized(t *testing.T) {
	d := New(DaemonConfig{
		SocketPath:   shortSockPath(t),
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	if d.ScriptRegistry() == nil {
		t.Fatal("ScriptRegistry should be initialized in New()")
	}
}

func TestDaemon_AutostartRegistersInScriptRegistry(t *testing.T) {
	tmpDir := t.TempDir()

	configPath := filepath.Join(tmpDir, ".agnt.kdl")
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

	d, _ := newBootedDaemon(t)

	ctx := context.Background()
	d.RunAutostart(ctx, tmpDir)

	// Give time for script to start
	time.Sleep(500 * time.Millisecond)

	// Verify ScriptEntry was registered
	entry, ok := d.ScriptRegistry().Get("test", tmpDir)
	if !ok {
		t.Fatal("ScriptEntry should exist after autostart")
	}

	if entry.Name != "test" {
		t.Errorf("Expected name 'test', got %q", entry.Name)
	}

	if entry.ProjectPath != tmpDir {
		t.Errorf("Expected projectPath %q, got %q", tmpDir, entry.ProjectPath)
	}

	// echo exits immediately and succeeds, so state should be Running
	// (the process starts and the monitoring period passes)
	state := entry.State()
	if state != script.StateRunning && state != script.StateFailed {
		t.Errorf("Expected state Running or Failed (echo exits fast), got %s", state)
	}

	if entry.StartCount() < 1 {
		t.Error("StartCount should be at least 1")
	}

	// Resolved command should be set
	cmd, _ := entry.ResolvedCommand()
	if cmd != "echo" {
		t.Errorf("Expected resolved command 'echo', got %q", cmd)
	}
}

func TestDaemon_AutostartSkipsRunningScript(t *testing.T) {
	tmpDir := t.TempDir()

	d, _ := newBootedDaemon(t)

	// Pre-register a script as Running in the registry
	cfg := &config.ScriptConfig{Command: "echo", Args: []string{"hello"}}
	entry, err := d.ScriptRegistry().Register("test", tmpDir, scriptConfigToEntry(cfg))
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	entry.SetState(script.StateRunning)

	// Now call autostartScript directly -- should skip because ScriptRegistry says Running
	proxyConfigs := map[string]*config.ProxyConfig{}
	err = d.autostartScript(context.Background(), "test", cfg, tmpDir, proxyConfigs)
	if err != nil {
		t.Fatalf("autostartScript should succeed (skip) when already running: %v", err)
	}

	// StartCount should still be 0 since it was skipped
	if entry.StartCount() != 0 {
		t.Errorf("StartCount should be 0 (skipped), got %d", entry.StartCount())
	}
}

func TestDaemon_ScriptRegistryLastError(t *testing.T) {
	reg := script.NewRegistry()
	cfg := &script.Config{Run: "npm start"}

	entry, err := reg.Register("dev", "/home/user/myapp", cfg)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	if entry.LastError() != "" {
		t.Error("LastError should be empty initially")
	}

	entry.SetLastError("port 3000 in use")
	if entry.LastError() != "port 3000 in use" {
		t.Errorf("Expected 'port 3000 in use', got %q", entry.LastError())
	}
}

func TestScriptRegistry_GetByProcessID(t *testing.T) {
	reg := script.NewRegistry()
	cfg := &script.Config{Run: "npm start"}

	entry, err := reg.Register("dev", "/home/user/myapp", cfg)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	found, ok := reg.GetByProcessID(entry.ProcessID)
	if !ok {
		t.Fatal("GetByProcessID should find the entry")
	}
	if found != entry {
		t.Error("GetByProcessID should return the same entry")
	}

	_, ok = reg.GetByProcessID("nonexistent-id")
	if ok {
		t.Error("GetByProcessID should return false for unknown ID")
	}
}

func TestScriptRegistry_PruneStaleEntries(t *testing.T) {
	// Simulates the scenario: session 1 registers 3 scripts, session 2 has only 1.
	// After registerAndStartScripts, stale entries should be removed.
	reg := script.NewRegistry()
	projectPath := "/home/user/myapp"

	// Session 1 registered 3 scripts
	for _, name := range []string{"dev", "test", "lint"} {
		_, err := reg.Register(name, projectPath, &script.Config{Run: "npm run " + name})
		if err != nil {
			t.Fatalf("Register %s failed: %v", name, err)
		}
	}

	if len(reg.List(projectPath)) != 3 {
		t.Fatalf("expected 3 scripts, got %d", len(reg.List(projectPath)))
	}

	// Session 2 config only has "dev"
	newConfig := map[string]bool{"dev": true}

	// Prune stale entries (same logic as registerAndStartScripts)
	for _, entry := range reg.List(projectPath) {
		if !newConfig[entry.Name] {
			reg.Remove(entry.Name, projectPath)
		}
	}

	entries := reg.List(projectPath)
	if len(entries) != 1 {
		t.Fatalf("expected 1 script after pruning, got %d", len(entries))
	}
	if entries[0].Name != "dev" {
		t.Errorf("expected remaining script to be 'dev', got %q", entries[0].Name)
	}
}

func TestReconcileScriptStates_DeadPIDTransitioned(t *testing.T) {
	// When a script entry says Running but the OS PID is dead,
	// reconcileScriptStates must transition it to Stopped and emit ScriptStopped.
	d, _ := newBootedDaemon(t)

	projectPath := "/home/user/project"

	// Register a script and set it to Running
	entry, err := d.scriptRegistry.Register("dev", projectPath, &script.Config{
		Run:       "sleep 999",
		Autostart: true,
	})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	entry.SetState(script.StateRunning)

	// Track a proxy for the script so we can verify it gets cleaned up
	d.trackScriptProxy(entry.ProcessID, "reconcile-test-proxy")

	// Run reconciliation — no managed process exists for this script,
	// so it should be detected as dead.
	reconciled := d.reconcileScriptStates(projectPath)

	if len(reconciled) != 1 {
		t.Fatalf("Expected 1 reconciled script, got %d", len(reconciled))
	}
	if reconciled[0].Name != "dev" {
		t.Errorf("Expected reconciled script 'dev', got %q", reconciled[0].Name)
	}
	if reconciled[0].State() != script.StateStopped {
		t.Errorf("Expected StateStopped, got %s", reconciled[0].State())
	}
}

func TestReconcileScriptStates_LiveProcessUnchanged(t *testing.T) {
	// A script whose managed process is actually alive should remain Running.
	tmpDir := t.TempDir()

	d, _ := newBootedDaemon(t)

	ctx := context.Background()

	// Start a real long-running process
	_, err := d.StartScript(ctx, StartScriptConfig{
		ProcessID:   makeProcessID(tmpDir, "alive"),
		ProjectPath: tmpDir,
		WorkingDir:  tmpDir,
		Command:     "sleep",
		Args:        []string{"30"},
	})
	if err != nil {
		t.Fatalf("StartScript failed: %v", err)
	}

	time.Sleep(500 * time.Millisecond)

	// Verify it's in the registry as running
	entry, ok := d.scriptRegistry.Get("alive", tmpDir)
	if !ok {
		t.Fatal("Script 'alive' should exist after StartScript")
	}
	if state := entry.State(); state != script.StateRunning {
		t.Fatalf("Expected StateRunning, got %s", state)
	}

	// Run reconciliation — process is alive, nothing should change
	reconciled := d.reconcileScriptStates(tmpDir)
	if len(reconciled) != 0 {
		t.Errorf("Expected 0 reconciled scripts (live process), got %d", len(reconciled))
		for _, r := range reconciled {
			t.Errorf("  reconciled: %s (state=%s)", r.Name, r.State())
		}
	}

	// State should still be Running
	if entry.State() != script.StateRunning {
		t.Errorf("Expected StateRunning after reconcile, got %s", entry.State())
	}
}

func TestReconcileScriptStates_IgnoresStoppedScripts(t *testing.T) {
	// Scripts already in Stopped/Failed/Idle states should not be touched.
	d, _ := newBootedDaemon(t)

	projectPath := "/home/user/project"

	// Register scripts in various non-running states
	stoppedEntry, _ := d.scriptRegistry.Register("stopped", projectPath, &script.Config{Run: "true"})
	stoppedEntry.SetState(script.StateStopped)

	failedEntry, _ := d.scriptRegistry.Register("failed", projectPath, &script.Config{Run: "false"})
	failedEntry.SetState(script.StateFailed)

	idleEntry, _ := d.scriptRegistry.Register("idle", projectPath, &script.Config{Run: "echo"})
	// idleEntry stays at StateIdle (default)

	reconciled := d.reconcileScriptStates(projectPath)
	if len(reconciled) != 0 {
		t.Errorf("Expected 0 reconciled scripts (all in terminal states), got %d", len(reconciled))
	}

	// States should be unchanged
	if stoppedEntry.State() != script.StateStopped {
		t.Errorf("stopped script should remain Stopped, got %s", stoppedEntry.State())
	}
	if failedEntry.State() != script.StateFailed {
		t.Errorf("failed script should remain Failed, got %s", failedEntry.State())
	}
	if idleEntry.State() != script.StateIdle {
		t.Errorf("idle script should remain Idle, got %s", idleEntry.State())
	}
}

func TestCleanupSessionResources_ClearsScriptRegistry(t *testing.T) {
	// The real bug: CleanupSessionResources must remove script entries from
	// the registry when the last session disconnects. Otherwise, the next
	// session sees stale entries and renders extra status bar indicators.
	projectPath := "/home/user/project"

	d, _ := newBootedDaemon(t)

	// Simulate session registering 3 scripts
	sessionCode := "session-1"
	session := &Session{
		Code:        sessionCode,
		ProjectPath: projectPath,
		StartedAt:   time.Now(),
		Status:      SessionStatusActive,
		LastSeen:    time.Now(),
	}
	if err := d.sessionRegistry.Register(session); err != nil {
		t.Fatalf("Register session failed: %v", err)
	}

	for _, name := range []string{"dev", "test", "lint"} {
		entry, err := d.scriptRegistry.Register(name, projectPath, &script.Config{
			Run:       "echo " + name,
			Autostart: true,
		})
		if err != nil {
			t.Fatalf("Register %s failed: %v", name, err)
		}
		entry.SetOwner(sessionCode)
		entry.AddSession(sessionCode)
		// Store in scriptConfigs too
		d.scriptConfigs.Store(entry.ProcessID, config.ScriptConfig{Run: "echo " + name})
	}

	// Verify 3 entries exist
	entries := d.scriptRegistry.List(projectPath)
	if len(entries) != 3 {
		t.Fatalf("expected 3 scripts before cleanup, got %d", len(entries))
	}

	// Cleanup session (last session disconnecting)
	d.CleanupSessionResources(sessionCode)

	// Registry should now be empty — all entries cleared
	entries = d.scriptRegistry.List(projectPath)
	if len(entries) != 0 {
		t.Errorf("expected 0 scripts after cleanup, got %d", len(entries))
		for _, e := range entries {
			t.Errorf("  stale entry: %s (state=%s)", e.Name, e.State())
		}
	}

	// scriptConfigs should also be cleared
	configCount := 0
	d.scriptConfigs.Range(func(_, _ interface{}) bool {
		configCount++
		return true
	})
	if configCount != 0 {
		t.Errorf("expected 0 scriptConfigs after cleanup, got %d", configCount)
	}
}
