package proxy

import (
	"context"
	"testing"
	"time"
)

func TestProxyManager_Create(t *testing.T) {
	pm := NewProxyManager()
	ctx := context.Background()

	config := ProxyConfig{
		ID:         "test",
		TargetURL:  "http://localhost:9999",
		ListenPort: 0, // Use port 0 to get a random available port
		MaxLogSize: 100,
	}

	proxy, err := pm.Create(ctx, config)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer pm.Stop(ctx, "test")

	if proxy.ID != "test" {
		t.Errorf("Expected ID 'test', got %q", proxy.ID)
	}

	if !proxy.IsRunning() {
		t.Error("Proxy should be running")
	}

	if pm.ActiveCount() != 1 {
		t.Errorf("Expected active count 1, got %d", pm.ActiveCount())
	}
}

func TestProxyManager_PortConflict(t *testing.T) {
	pm := NewProxyManager()
	ctx := context.Background()

	// Start first proxy on a fixed port
	config1 := ProxyConfig{
		ID:         "proxy1",
		TargetURL:  "http://localhost:9999",
		ListenPort: 18080,
		MaxLogSize: 100,
	}

	proxy1, err := pm.Create(ctx, config1)
	if err != nil {
		t.Fatalf("Failed to create first proxy: %v", err)
	}
	defer pm.Stop(ctx, "proxy1")

	// Give it a moment to start
	time.Sleep(10 * time.Millisecond)

	if !proxy1.IsRunning() {
		t.Fatal("First proxy should be running")
	}

	port1 := proxy1.ListenAddr
	t.Logf("First proxy listening on: %s", port1)

	// Try to start second proxy on the same port
	// It should auto-assign a different port
	config2 := ProxyConfig{
		ID:         "proxy2",
		TargetURL:  "http://localhost:9999",
		ListenPort: 18080, // Same port as proxy1
		MaxLogSize: 100,
	}

	proxy2, err := pm.Create(ctx, config2)
	if err != nil {
		t.Fatalf("Failed to create second proxy (should auto-find port): %v", err)
	}
	defer pm.Stop(ctx, "proxy2")

	if proxy2 == nil {
		t.Fatal("Expected proxy2 to be created")
	}

	if !proxy2.IsRunning() {
		t.Fatal("Second proxy should be running")
	}

	port2 := proxy2.ListenAddr
	t.Logf("Second proxy auto-assigned to: %s", port2)

	// Verify they're on different ports
	if port1 == port2 {
		t.Errorf("Expected different ports, both are: %s", port1)
	}

	// Verify both proxies are running
	if !proxy1.IsRunning() {
		t.Error("First proxy should still be running")
	}

	// Verify active count is 2
	if pm.ActiveCount() != 2 {
		t.Errorf("Expected active count 2, got %d", pm.ActiveCount())
	}
}

func TestProxyManager_DuplicateID(t *testing.T) {
	pm := NewProxyManager()
	ctx := context.Background()

	config1 := ProxyConfig{
		ID:         "duplicate",
		TargetURL:  "http://localhost:9999",
		ListenPort: 0,
		MaxLogSize: 100,
	}

	_, err := pm.Create(ctx, config1)
	if err != nil {
		t.Fatalf("Failed to create first proxy: %v", err)
	}
	defer pm.Stop(ctx, "duplicate")

	// Try to create another proxy with the same ID
	config2 := ProxyConfig{
		ID:         "duplicate",
		TargetURL:  "http://localhost:9998",
		ListenPort: 0,
		MaxLogSize: 100,
	}

	_, err = pm.Create(ctx, config2)
	if err != ErrProxyExists {
		t.Errorf("Expected ErrProxyExists, got %v", err)
	}
}

func TestProxyManager_GetNotFound(t *testing.T) {
	pm := NewProxyManager()

	_, err := pm.Get("nonexistent")
	if err != ErrProxyNotFound {
		t.Errorf("Expected ErrProxyNotFound, got %v", err)
	}
}

func TestProxyManager_GetFuzzy(t *testing.T) {
	pm := NewProxyManager()
	ctx := context.Background()

	// Create a proxy with compound ID (like auto-generated ones)
	fullID := "myapp-abc1:dev:localhost-3000"
	config := ProxyConfig{
		ID:         fullID,
		TargetURL:  "http://localhost:9999",
		ListenPort: 0,
		MaxLogSize: 100,
	}

	_, err := pm.Create(ctx, config)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer pm.Stop(ctx, fullID)

	// Exact match should work
	proxy, err := pm.Get(fullID)
	if err != nil {
		t.Errorf("Exact match failed: %v", err)
	}
	if proxy.ID != fullID {
		t.Errorf("Expected ID %q, got %q", fullID, proxy.ID)
	}

	// Fuzzy match on proxy name part should work
	proxy, err = pm.Get("dev")
	if err != nil {
		t.Fatalf("Fuzzy match on 'dev' failed: %v", err)
	}
	if proxy.ID != fullID {
		t.Errorf("Expected ID %q from fuzzy match, got %q", fullID, proxy.ID)
	}
}

func TestProxyManager_GetFuzzyAmbiguous(t *testing.T) {
	pm := NewProxyManager()
	ctx := context.Background()

	// Create two proxies with same proxy name but different URLs
	id1 := "myapp-abc1:dev:localhost-3000"
	id2 := "myapp-abc1:dev:localhost-4000"

	config1 := ProxyConfig{
		ID:         id1,
		TargetURL:  "http://localhost:3000",
		ListenPort: 0,
		MaxLogSize: 100,
	}
	config2 := ProxyConfig{
		ID:         id2,
		TargetURL:  "http://localhost:4000",
		ListenPort: 0,
		MaxLogSize: 100,
	}

	_, err := pm.Create(ctx, config1)
	if err != nil {
		t.Fatalf("Failed to create proxy1: %v", err)
	}
	defer pm.Stop(ctx, id1)

	_, err = pm.Create(ctx, config2)
	if err != nil {
		t.Fatalf("Failed to create proxy2: %v", err)
	}
	defer pm.Stop(ctx, id2)

	// Fuzzy match on "dev" should fail - ambiguous
	_, err = pm.Get("dev")
	if err == nil {
		t.Error("Expected error for ambiguous fuzzy match")
	}
	if err != ErrProxyAmbiguous {
		t.Errorf("Expected ErrProxyAmbiguous, got %v", err)
	}

	// But exact match should still work
	proxy, err := pm.Get(id1)
	if err != nil {
		t.Errorf("Exact match failed: %v", err)
	}
	if proxy.ID != id1 {
		t.Errorf("Expected ID %q, got %q", id1, proxy.ID)
	}
}

func TestProxyManager_Stop(t *testing.T) {
	pm := NewProxyManager()
	ctx := context.Background()

	config := ProxyConfig{
		ID:         "stop-test",
		TargetURL:  "http://localhost:9999",
		ListenPort: 0,
		MaxLogSize: 100,
	}

	proxy, err := pm.Create(ctx, config)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}

	if !proxy.IsRunning() {
		t.Fatal("Proxy should be running")
	}

	err = pm.Stop(ctx, "stop-test")
	if err != nil {
		t.Errorf("Failed to stop proxy: %v", err)
	}

	if proxy.IsRunning() {
		t.Error("Proxy should not be running after stop")
	}

	if pm.ActiveCount() != 0 {
		t.Errorf("Expected active count 0, got %d", pm.ActiveCount())
	}
}

func TestProxyManager_List(t *testing.T) {
	pm := NewProxyManager()
	ctx := context.Background()

	// Initially empty
	if len(pm.List()) != 0 {
		t.Error("Expected empty list initially")
	}

	// Create multiple proxies
	for i := 0; i < 3; i++ {
		config := ProxyConfig{
			ID:         string(rune('a' + i)),
			TargetURL:  "http://localhost:9999",
			ListenPort: 0,
			MaxLogSize: 100,
		}

		_, err := pm.Create(ctx, config)
		if err != nil {
			t.Fatalf("Failed to create proxy %d: %v", i, err)
		}
	}
	defer func() {
		pm.Stop(ctx, "a")
		pm.Stop(ctx, "b")
		pm.Stop(ctx, "c")
	}()

	proxies := pm.List()
	if len(proxies) != 3 {
		t.Errorf("Expected 3 proxies, got %d", len(proxies))
	}
}

func TestProxyManager_StopAll(t *testing.T) {
	pm := NewProxyManager()
	ctx := context.Background()

	// Create multiple proxies
	for i := 0; i < 3; i++ {
		config := ProxyConfig{
			ID:         string(rune('p' + i)),
			TargetURL:  "http://localhost:9999",
			ListenPort: 0,
			MaxLogSize: 100,
		}

		_, err := pm.Create(ctx, config)
		if err != nil {
			t.Fatalf("Failed to create proxy %d: %v", i, err)
		}
	}

	if pm.ActiveCount() != 3 {
		t.Errorf("Expected 3 active proxies, got %d", pm.ActiveCount())
	}

	// Call StopAll
	stoppedIDs, err := pm.StopAll(ctx)
	if err != nil {
		t.Errorf("StopAll failed: %v", err)
	}

	// Verify all proxies were stopped
	if len(stoppedIDs) != 3 {
		t.Errorf("Expected 3 stopped IDs, got %d", len(stoppedIDs))
	}

	if pm.ActiveCount() != 0 {
		t.Errorf("Expected 0 active proxies after StopAll, got %d", pm.ActiveCount())
	}

	// Verify we can create NEW proxies after StopAll (unlike Shutdown)
	newConfig := ProxyConfig{
		ID:         "after-stopall",
		TargetURL:  "http://localhost:9999",
		ListenPort: 0,
		MaxLogSize: 100,
	}

	newProxy, err := pm.Create(ctx, newConfig)
	if err != nil {
		t.Fatalf("Failed to create proxy after StopAll: %v", err)
	}

	if !newProxy.IsRunning() {
		t.Error("New proxy should be running")
	}

	// Clean up
	pm.Stop(ctx, "after-stopall")
}

func TestProxyManager_StopAll_Empty(t *testing.T) {
	pm := NewProxyManager()
	ctx := context.Background()

	// StopAll on empty manager should succeed
	stoppedIDs, err := pm.StopAll(ctx)
	if err != nil {
		t.Errorf("StopAll on empty manager failed: %v", err)
	}

	if len(stoppedIDs) != 0 {
		t.Errorf("Expected 0 stopped IDs on empty manager, got %d", len(stoppedIDs))
	}
}

func TestProxyManager_Shutdown(t *testing.T) {
	pm := NewProxyManager()
	ctx := context.Background()

	// Create multiple proxies
	for i := 0; i < 3; i++ {
		config := ProxyConfig{
			ID:         string(rune('x' + i)),
			TargetURL:  "http://localhost:9999",
			ListenPort: 0,
			MaxLogSize: 100,
		}

		_, err := pm.Create(ctx, config)
		if err != nil {
			t.Fatalf("Failed to create proxy %d: %v", i, err)
		}
	}

	if pm.ActiveCount() != 3 {
		t.Errorf("Expected 3 active proxies, got %d", pm.ActiveCount())
	}

	// Shutdown all
	err := pm.Shutdown(ctx)
	if err != nil {
		t.Errorf("Shutdown failed: %v", err)
	}

	if pm.ActiveCount() != 0 {
		t.Errorf("Expected 0 active proxies after shutdown, got %d", pm.ActiveCount())
	}

	// Verify all proxies are stopped
	proxies := pm.List()
	for _, p := range proxies {
		if p.IsRunning() {
			t.Errorf("Proxy %s should be stopped after shutdown", p.ID)
		}
	}

	// Verify can't create new proxies during shutdown
	config := ProxyConfig{
		ID:         "after-shutdown",
		TargetURL:  "http://localhost:9999",
		ListenPort: 0,
		MaxLogSize: 100,
	}

	_, err = pm.Create(ctx, config)
	if err == nil {
		t.Error("Expected error when creating proxy after shutdown")
	}
}
