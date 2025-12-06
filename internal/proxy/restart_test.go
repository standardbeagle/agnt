package proxy

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestProxyServer_AutoRestart(t *testing.T) {
	config := ProxyConfig{
		ID:          "test-restart",
		TargetURL:   "http://localhost:9999",
		ListenPort:  0, // Auto-assign port
		MaxLogSize:  100,
		AutoRestart: true,
	}

	proxy, err := NewProxyServer(config)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}

	ctx := context.Background()
	err = proxy.Start(ctx)
	if err != nil {
		t.Fatalf("Failed to start proxy: %v", err)
	}

	// Verify proxy is running
	if !proxy.IsRunning() {
		t.Fatal("Proxy should be running")
	}

	originalAddr := proxy.ListenAddr
	t.Logf("Proxy started on: %s", originalAddr)

	// Simulate a crash by closing the server
	err = proxy.httpServer.Close()
	if err != nil {
		t.Logf("Error closing server (expected): %v", err)
	}

	// Wait a moment for auto-restart to kick in
	time.Sleep(100 * time.Millisecond)

	// Proxy should have restarted
	if !proxy.IsRunning() {
		stats := proxy.Stats()
		t.Fatalf("Proxy should have auto-restarted. Last error: %s, Restarts: %d",
			stats.LastError, stats.RestartCount)
	}

	stats := proxy.Stats()
	t.Logf("Proxy stats after restart: running=%v, restarts=%d, auto_restart=%v",
		stats.Running, stats.RestartCount, stats.AutoRestart)

	// Clean up
	proxy.Stop(ctx)
}

func TestProxyServer_NoAutoRestart(t *testing.T) {
	config := ProxyConfig{
		ID:          "test-no-restart",
		TargetURL:   "http://localhost:9999",
		ListenPort:  0,
		MaxLogSize:  100,
		AutoRestart: false, // Disable auto-restart
	}

	proxy, err := NewProxyServer(config)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}

	// Verify auto-restart is disabled
	stats := proxy.Stats()
	if stats.AutoRestart {
		t.Error("AutoRestart should be false")
	}

	// Start proxy
	ctx := context.Background()
	err = proxy.Start(ctx)
	if err != nil {
		t.Fatalf("Failed to start proxy: %v", err)
	}

	if !proxy.IsRunning() {
		t.Fatal("Proxy should be running")
	}

	// Clean up
	proxy.Stop(ctx)
}

func TestProxyServer_RestartLimit(t *testing.T) {
	config := ProxyConfig{
		ID:          "test-restart-limit",
		TargetURL:   "http://localhost:9999",
		ListenPort:  0,
		MaxLogSize:  100,
		AutoRestart: true,
	}

	proxy, err := NewProxyServer(config)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}

	// Set aggressive restart limits for testing
	proxy.maxRestarts = 3
	proxy.restartWindow = 10 * time.Second

	ctx := context.Background()
	err = proxy.Start(ctx)
	if err != nil {
		t.Fatalf("Failed to start proxy: %v", err)
	}

	// Simulate hitting the restart limit
	for i := 0; i < 5; i++ {
		proxy.recordRestart()
	}

	stats := proxy.Stats()
	t.Logf("After recording restarts: restarts=%d", stats.RestartCount)

	// Should have recorded all restarts (within window)
	if stats.RestartCount != 5 {
		t.Errorf("Expected 5 restarts in window, got %d", stats.RestartCount)
	}

	// shouldRestart should return false when at limit
	if proxy.shouldRestart() {
		t.Error("Should not allow restart when over limit")
	}

	// Clean up
	proxy.Stop(ctx)
}

func TestProxyServer_RestartWindowExpiry(t *testing.T) {
	config := ProxyConfig{
		ID:          "test-restart-window",
		TargetURL:   "http://localhost:9999",
		ListenPort:  0,
		MaxLogSize:  100,
		AutoRestart: true,
	}

	proxy, err := NewProxyServer(config)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}

	// Set short restart window for testing
	proxy.maxRestarts = 2
	proxy.restartWindow = 100 * time.Millisecond

	// Record restarts
	proxy.recordRestart()
	proxy.recordRestart()

	// Should be at limit now
	if proxy.shouldRestart() {
		t.Error("Should not allow restart when at limit")
	}

	// Wait for window to expire
	time.Sleep(150 * time.Millisecond)

	// Should allow restart now
	if !proxy.shouldRestart() {
		t.Error("Should allow restart after window expires")
	}
}

func TestProxyServer_HealthyRestart(t *testing.T) {
	// This test verifies basic auto-restart functionality
	pm := NewProxyManager()
	ctx := context.Background()

	config := ProxyConfig{
		ID:          "healthy-restart",
		TargetURL:   "http://localhost:9999",
		ListenPort:  0,
		MaxLogSize:  100,
		AutoRestart: true,
	}

	proxy, err := pm.Create(ctx, config)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer pm.Stop(ctx, "healthy-restart")

	originalAddr := proxy.ListenAddr
	t.Logf("Proxy listening on: %s", originalAddr)

	// Verify we can connect to the proxy
	conn, err := net.DialTimeout("tcp", proxy.ListenAddr, 1*time.Second)
	if err != nil {
		t.Fatalf("Failed to connect to proxy: %v", err)
	}
	conn.Close()

	stats := proxy.Stats()
	if !stats.AutoRestart {
		t.Error("AutoRestart should be enabled")
	}

	t.Logf("Proxy stats: running=%v, auto_restart=%v, restarts=%d",
		stats.Running, stats.AutoRestart, stats.RestartCount)
}
