package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultStateManagerConfig(t *testing.T) {
	config := DefaultStateManagerConfig()
	if config.StatePath == "" {
		t.Error("Expected non-empty state path")
	}
	if config.SaveInterval == 0 {
		t.Error("Expected non-zero save interval")
	}
	if !config.AutoLoad {
		t.Error("Expected AutoLoad to be true by default")
	}
}

func TestDefaultStatePath(t *testing.T) {
	path := DefaultStatePath()
	if path == "" {
		t.Error("Expected non-empty default state path")
	}
	t.Logf("Default state path: %s", path)
}

func TestDefaultStatePath_WithXDGStateHome(t *testing.T) {
	// Save original value
	orig := os.Getenv("XDG_STATE_HOME")
	defer os.Setenv("XDG_STATE_HOME", orig)

	// Set custom XDG_STATE_HOME
	tmpDir := t.TempDir()
	os.Setenv("XDG_STATE_HOME", tmpDir)

	path := DefaultStatePath()
	expected := filepath.Join(tmpDir, "devtool-mcp", "state.json")
	if path != expected {
		t.Errorf("Expected path %s, got %s", expected, path)
	}
}

func TestNewStateManager(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "test-state.json")

	config := StateManagerConfig{
		StatePath:    statePath,
		SaveInterval: 100 * time.Millisecond,
		AutoLoad:     false,
	}

	sm := NewStateManager(config)
	if sm == nil {
		t.Fatal("Expected non-nil StateManager")
	}
}

func TestNewStateManager_WithDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "test-state.json")

	config := StateManagerConfig{
		StatePath: statePath,
		AutoLoad:  false,
		// SaveInterval left as 0 to test default
	}

	sm := NewStateManager(config)
	if sm == nil {
		t.Fatal("Expected non-nil StateManager")
	}
}

func TestStateManager_LoadSave(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "test-state.json")

	config := StateManagerConfig{
		StatePath:    statePath,
		SaveInterval: 100 * time.Millisecond,
		AutoLoad:     false,
	}

	sm := NewStateManager(config)

	// Save initial state
	err := sm.Save()
	if err != nil {
		t.Fatalf("Failed to save state: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(statePath); os.IsNotExist(err) {
		t.Error("State file should exist after save")
	}

	// Load state
	err = sm.Load()
	if err != nil {
		t.Fatalf("Failed to load state: %v", err)
	}
}

func TestStateManager_LoadNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "nonexistent", "test-state.json")

	config := StateManagerConfig{
		StatePath: statePath,
		AutoLoad:  false,
	}

	sm := NewStateManager(config)

	// Load should succeed with no file (empty state)
	err := sm.Load()
	if err != nil {
		t.Errorf("Load of nonexistent file should not error: %v", err)
	}
}

func TestStateManager_OverlayEndpoint(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "test-state.json")

	config := StateManagerConfig{
		StatePath: statePath,
		AutoLoad:  false,
	}

	sm := NewStateManager(config)

	// Set overlay endpoint
	sm.SetOverlayEndpoint("http://localhost:19191")

	// Get overlay endpoint
	endpoint := sm.GetOverlayEndpoint()
	if endpoint != "http://localhost:19191" {
		t.Errorf("Expected http://localhost:19191, got %s", endpoint)
	}
}

func TestStateManager_ProxyOperations(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "test-state.json")

	config := StateManagerConfig{
		StatePath: statePath,
		AutoLoad:  false,
	}

	sm := NewStateManager(config)

	// Add proxy
	proxy := PersistentProxyConfig{
		ID:        "test-proxy",
		TargetURL: "http://localhost:3000",
		Port:      8080,
		Path:      "/test/project",
	}
	sm.AddProxy(proxy)

	// Get proxies
	proxies := sm.GetProxies()
	if len(proxies) != 1 {
		t.Errorf("Expected 1 proxy, got %d", len(proxies))
	}

	// Get specific proxy
	p, found := sm.GetProxy("test-proxy")
	if !found {
		t.Error("Expected to find proxy")
	}
	if p.ID != "test-proxy" {
		t.Errorf("Expected ID test-proxy, got %s", p.ID)
	}

	// Remove proxy
	sm.RemoveProxy("test-proxy")
	proxies = sm.GetProxies()
	if len(proxies) != 0 {
		t.Errorf("Expected 0 proxies after remove, got %d", len(proxies))
	}
}

func TestStateManager_Clear(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "test-state.json")

	config := StateManagerConfig{
		StatePath: statePath,
		AutoLoad:  false,
	}

	sm := NewStateManager(config)

	// Add some data
	sm.SetOverlayEndpoint("http://localhost:19191")
	sm.AddProxy(PersistentProxyConfig{ID: "test"})

	// Clear
	sm.Clear()

	// Verify cleared
	if sm.GetOverlayEndpoint() != "" {
		t.Error("Expected empty overlay endpoint after clear")
	}
	if len(sm.GetProxies()) != 0 {
		t.Error("Expected no proxies after clear")
	}
}

func TestStateManager_SaveDebounced(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "test-state.json")

	config := StateManagerConfig{
		StatePath:    statePath,
		SaveInterval: 50 * time.Millisecond,
		AutoLoad:     false,
	}

	sm := NewStateManager(config)

	// Multiple debounced saves
	sm.SaveDebounced()
	sm.SaveDebounced()
	sm.SaveDebounced()

	// Wait for debounce to complete
	time.Sleep(100 * time.Millisecond)

	// File should exist
	if _, err := os.Stat(statePath); os.IsNotExist(err) {
		t.Error("State file should exist after debounced save")
	}
}

func TestStateManager_Flush(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "test-state.json")

	config := StateManagerConfig{
		StatePath:    statePath,
		SaveInterval: 10 * time.Second, // Long interval
		AutoLoad:     false,
	}

	sm := NewStateManager(config)
	sm.SetOverlayEndpoint("http://test")

	// Flush forces immediate save
	err := sm.Flush()
	if err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// File should exist
	if _, err := os.Stat(statePath); os.IsNotExist(err) {
		t.Error("State file should exist after flush")
	}
}

func TestStateManager_State(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "test-state.json")

	config := StateManagerConfig{
		StatePath: statePath,
		AutoLoad:  false,
	}

	sm := NewStateManager(config)
	sm.SetOverlayEndpoint("http://test")

	// Get raw state
	state := sm.State()
	if state.OverlayEndpoint != "http://test" {
		t.Errorf("Expected overlay endpoint http://test, got %s", state.OverlayEndpoint)
	}
}

func TestStateManager_LoadInvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "test-state.json")

	// Write invalid JSON
	err := os.WriteFile(statePath, []byte("invalid json"), 0644)
	if err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	config := StateManagerConfig{
		StatePath: statePath,
		AutoLoad:  false,
	}

	sm := NewStateManager(config)

	// Load should error on invalid JSON
	err = sm.Load()
	if err == nil {
		t.Error("Expected error loading invalid JSON")
	}
}

func TestStateManager_PersistAcrossRestart(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "test-state.json")

	// First state manager
	sm1 := NewStateManager(StateManagerConfig{
		StatePath: statePath,
		AutoLoad:  false,
	})
	sm1.SetOverlayEndpoint("http://persist-test")
	sm1.AddProxy(PersistentProxyConfig{
		ID:        "persist-proxy",
		TargetURL: "http://localhost:8080",
	})
	if err := sm1.Save(); err != nil {
		t.Fatalf("Failed to save: %v", err)
	}

	// Second state manager (simulating restart)
	sm2 := NewStateManager(StateManagerConfig{
		StatePath: statePath,
		AutoLoad:  true, // Enable auto-load
	})

	// Verify data persisted
	if sm2.GetOverlayEndpoint() != "http://persist-test" {
		t.Errorf("Expected overlay endpoint http://persist-test, got %s", sm2.GetOverlayEndpoint())
	}
	proxies := sm2.GetProxies()
	if len(proxies) != 1 {
		t.Errorf("Expected 1 proxy, got %d", len(proxies))
	}
	if proxies[0].ID != "persist-proxy" {
		t.Errorf("Expected proxy ID persist-proxy, got %s", proxies[0].ID)
	}
}
