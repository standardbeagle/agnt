package daemon

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewDaemon_StatePathExplicit verifies that when DaemonConfig.StatePath is
// set, the daemon's state manager writes to exactly that path (AC#1).
func TestNewDaemon_StatePathExplicit(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	statePath := filepath.Join(stateDir, "explicit-state.json")

	d := New(DaemonConfig{
		EnableStatePersistence: true,
		StatePath:              statePath,
	})
	require.NotNil(t, d.stateMgr, "stateMgr must be non-nil when EnableStatePersistence=true")

	d.stateMgr.SetOverlayEndpoint("http://explicit-test")
	require.NoError(t, d.stateMgr.Flush())

	_, err := os.Stat(statePath)
	require.NoError(t, err, "state file must exist at the explicit StatePath")

	data, err := os.ReadFile(statePath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "http://explicit-test")
}

// TestNewDaemon_StatePathFallsBackToXDGStateHome verifies that when
// DaemonConfig.StatePath is empty, the daemon resolves the path once at
// construction time from XDG_STATE_HOME — not on every access (AC#2).
func TestNewDaemon_StatePathFallsBackToXDGStateHome(t *testing.T) {
	// t.Setenv is incompatible with t.Parallel()
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	d := New(DaemonConfig{
		EnableStatePersistence: true,
		// StatePath intentionally empty — should fall back to XDG_STATE_HOME
	})
	require.NotNil(t, d.stateMgr, "stateMgr must be non-nil when EnableStatePersistence=true")

	d.stateMgr.SetOverlayEndpoint("http://xdg-test")
	require.NoError(t, d.stateMgr.Flush())

	expectedPath := filepath.Join(stateHome, "devtool-mcp", "state.json")
	_, err := os.Stat(expectedPath)
	require.NoError(t, err, "state file must exist under XDG_STATE_HOME when StatePath is empty")

	data, err := os.ReadFile(expectedPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "http://xdg-test")
}

func TestDefaultStateManagerConfig(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	path := DefaultStatePath()
	if path == "" {
		t.Error("Expected non-empty default state path")
	}
	t.Logf("Default state path: %s", path)
}

func TestDefaultStatePath_WithXDGStateHome(t *testing.T) {
	// t.Setenv is incompatible with t.Parallel()
	tmpDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmpDir)

	path := DefaultStatePath()
	expected := filepath.Join(tmpDir, "devtool-mcp", "state.json")
	if path != expected {
		t.Errorf("Expected path %s, got %s", expected, path)
	}
}

func TestNewStateManager(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "test-state.json")

	config := StateManagerConfig{
		StatePath:    statePath,
		SaveInterval: 100 * time.Millisecond,
		AutoLoad:     false,
	}

	sm := NewStateManager(config)
	defer sm.Close()
	if sm == nil {
		t.Fatal("Expected non-nil StateManager")
	}
}

func TestNewStateManager_WithDefaults(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "test-state.json")

	config := StateManagerConfig{
		StatePath: statePath,
		AutoLoad:  false,
		// SaveInterval left as 0 to test default
	}

	sm := NewStateManager(config)
	defer sm.Close()
	if sm == nil {
		t.Fatal("Expected non-nil StateManager")
	}
}

func TestStateManager_LoadSave(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "test-state.json")

	config := StateManagerConfig{
		StatePath:    statePath,
		SaveInterval: 100 * time.Millisecond,
		AutoLoad:     false,
	}

	sm := NewStateManager(config)
	defer sm.Close()

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
	t.Parallel()
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "nonexistent", "test-state.json")

	config := StateManagerConfig{
		StatePath: statePath,
		AutoLoad:  false,
	}

	sm := NewStateManager(config)
	defer sm.Close()

	// Load should succeed with no file (empty state)
	err := sm.Load()
	if err != nil {
		t.Errorf("Load of nonexistent file should not error: %v", err)
	}
}

func TestStateManager_OverlayEndpoint(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "test-state.json")

	config := StateManagerConfig{
		StatePath: statePath,
		AutoLoad:  false,
	}

	sm := NewStateManager(config)
	defer sm.Close()

	// Set overlay endpoint
	sm.SetOverlayEndpoint("http://localhost:19191")

	// Get overlay endpoint
	endpoint := sm.GetOverlayEndpoint()
	if endpoint != "http://localhost:19191" {
		t.Errorf("Expected http://localhost:19191, got %s", endpoint)
	}
}

func TestStateManager_ProxyOperations(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "test-state.json")

	config := StateManagerConfig{
		StatePath: statePath,
		AutoLoad:  false,
	}

	sm := NewStateManager(config)
	defer sm.Close()

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
	t.Parallel()
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "test-state.json")

	config := StateManagerConfig{
		StatePath: statePath,
		AutoLoad:  false,
	}

	sm := NewStateManager(config)
	defer sm.Close()

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
	t.Parallel()
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "test-state.json")

	config := StateManagerConfig{
		StatePath:    statePath,
		SaveInterval: 50 * time.Millisecond,
		AutoLoad:     false,
	}

	sm := NewStateManager(config)
	defer sm.Close()

	// Multiple debounced saves
	sm.SaveDebounced()
	sm.SaveDebounced()
	sm.SaveDebounced()

	// Wait for debounce to complete
	require.Eventually(t, func() bool {
		_, err := os.Stat(statePath)
		return !os.IsNotExist(err)
	}, 2*time.Second, 10*time.Millisecond, "State file should exist after debounced save")
}

func TestStateManager_Flush(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "test-state.json")

	config := StateManagerConfig{
		StatePath:    statePath,
		SaveInterval: 10 * time.Second, // Long interval
		AutoLoad:     false,
	}

	sm := NewStateManager(config)
	defer sm.Close()
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
	t.Parallel()
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "test-state.json")

	config := StateManagerConfig{
		StatePath: statePath,
		AutoLoad:  false,
	}

	sm := NewStateManager(config)
	defer sm.Close()
	sm.SetOverlayEndpoint("http://test")

	// Get raw state
	state := sm.State()
	if state.OverlayEndpoint != "http://test" {
		t.Errorf("Expected overlay endpoint http://test, got %s", state.OverlayEndpoint)
	}
}

func TestStateManager_LoadInvalidJSON(t *testing.T) {
	t.Parallel()
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
	defer sm.Close()

	// Load should error on invalid JSON
	err = sm.Load()
	if err == nil {
		t.Error("Expected error loading invalid JSON")
	}
}

func TestStateManager_PersistAcrossRestart(t *testing.T) {
	t.Parallel()
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
	sm1.Close()

	// Second state manager (simulating restart)
	sm2 := NewStateManager(StateManagerConfig{
		StatePath: statePath,
		AutoLoad:  true, // Enable auto-load
	})
	defer sm2.Close()

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

// New tests for write-behind channel behavior

func TestStateManager_ConcurrentSaveLoadNoDeadlock(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "concurrent-state.json")

	sm := NewStateManager(StateManagerConfig{
		StatePath:    statePath,
		SaveInterval: 10 * time.Millisecond,
		AutoLoad:     false,
	})
	defer sm.Close()

	var wg sync.WaitGroup
	const goroutines = 20
	const iterations = 50

	// Writers
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				sm.SetOverlayEndpoint("http://test")
				sm.AddProxy(PersistentProxyConfig{
					ID:        "proxy",
					TargetURL: "http://localhost:3000",
				})
				sm.RemoveProxy("proxy")
				sm.SaveDebounced()
			}
		}(i)
	}

	// Readers
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = sm.GetOverlayEndpoint()
				_ = sm.GetProxies()
				_ = sm.State()
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success: no deadlock
	case <-time.After(10 * time.Second):
		t.Fatal("Deadlock detected: concurrent operations did not complete in 10s")
	}

	require.NoError(t, sm.Flush())
}

func TestStateManager_FlushOnClose(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "flush-close-state.json")

	sm := NewStateManager(StateManagerConfig{
		StatePath:    statePath,
		SaveInterval: 10 * time.Second, // Very long debounce
		AutoLoad:     false,
	})

	// Mutate state but don't flush
	sm.SetOverlayEndpoint("http://flush-test")
	sm.AddProxy(PersistentProxyConfig{
		ID:        "flush-proxy",
		TargetURL: ephemeralTargetURL(t),
	})

	// Close should flush pending writes
	require.NoError(t, sm.Close())

	// Verify file was written
	data, err := os.ReadFile(statePath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "flush-test")
	assert.Contains(t, string(data), "flush-proxy")
}

func TestStateManager_DebouncedSavesCoalesce(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "coalesce-state.json")

	sm := NewStateManager(StateManagerConfig{
		StatePath:    statePath,
		SaveInterval: 100 * time.Millisecond,
		AutoLoad:     false,
	})
	defer sm.Close()

	// Rapid mutations — should coalesce into one write
	for i := 0; i < 100; i++ {
		sm.SetOverlayEndpoint("http://test")
	}

	// Wait for debounce. Budget is generous because the race detector adds
	// 5-10x timer overhead, turning 100ms into ~500ms-1s.
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(statePath)
		return err == nil && len(data) > 0
	}, 5*time.Second, 10*time.Millisecond, "State file should be written after debounce")

	data, err := os.ReadFile(statePath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "http://test")
}

func TestStateManager_CloseIdempotent(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "close-idem.json")

	sm := NewStateManager(StateManagerConfig{
		StatePath: statePath,
		AutoLoad:  false,
	})

	require.NoError(t, sm.Close())
	require.NoError(t, sm.Close()) // Second close should be safe
}

func TestStateManager_FlushAfterClose(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "flush-after-close.json")

	sm := NewStateManager(StateManagerConfig{
		StatePath: statePath,
		AutoLoad:  false,
	})

	sm.SetOverlayEndpoint("http://after-close")
	require.NoError(t, sm.Close())

	// Flush after close should still write (direct path)
	err := sm.Flush()
	require.NoError(t, err)
}
