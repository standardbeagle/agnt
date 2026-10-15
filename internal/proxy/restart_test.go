package proxy

import (
	"context"
	"errors"
	"net"
	"strings"
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
	err = proxy.httpServer.Load().Close()
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

// crashingListener is a loopback listener whose Accept fails permanently once
// crash is closed, which is what makes http.Server.Serve return an unexpected
// error (a crash) rather than ErrServerClosed.
type crashingListener struct {
	net.Listener
	crash chan struct{}
}

func (l *crashingListener) Accept() (net.Conn, error) {
	<-l.crash
	return nil, errors.New("listener died")
}

// startCrashableProxy starts a proxy whose first listener crashes on demand;
// later binds (the auto-restart rebind) go through rebind.
func startCrashableProxy(t *testing.T, autoRestart bool, rebind func(network, addr string) (net.Listener, error)) (*ProxyServer, func()) {
	t.Helper()
	ps, err := NewProxyServer(ProxyConfig{
		ID: "crash", TargetURL: "http://localhost:9999", ListenPort: 0,
		MaxLogSize: 100, AutoRestart: autoRestart,
	})
	if err != nil {
		t.Fatalf("NewProxyServer: %v", err)
	}
	crash := make(chan struct{})
	first := true
	ps.listen = func(network, addr string) (net.Listener, error) {
		if !first {
			return rebind(network, addr)
		}
		first = false
		l, err := net.Listen(network, "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		return &crashingListener{Listener: l, crash: crash}, nil
	}
	if err := ps.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ps.Stop(context.Background()) })
	return ps, func() { close(crash) }
}

// waitDiagnostic waits for an error-level diagnostic with the given event.
func waitDiagnostic(t *testing.T, ps *ProxyServer, event string) *ProxyDiagnostic {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range ps.Logger().Query(LogFilter{Types: []LogEntryType{LogTypeDiagnostic}, Limit: 1000}) {
			if e.Diagnostic != nil && e.Diagnostic.Event == event {
				return e.Diagnostic
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no %q diagnostic; got %v", event, diagnosticEvents(ps))
	return nil
}

// A proxy that stops serving for good must say so on the agent-visible path
// (an error diagnostic, which the incident pipeline ingests), not only in the
// stats lastError nobody is told to read.
func TestRunServer_TerminalFailuresReachTheAgent(t *testing.T) {
	t.Run("auto-restart off", func(t *testing.T) {
		ps, crash := startCrashableProxy(t, false, nil)
		crash()
		d := waitDiagnostic(t, ps, "proxy_serve_failed")
		if d.Level != DiagnosticError || !strings.Contains(d.Message, "listener died") {
			t.Errorf("diagnostic = %+v, want an error naming the cause", d)
		}
	})

	t.Run("restart limit reached", func(t *testing.T) {
		ps, crash := startCrashableProxy(t, true, nil)
		ps.maxRestarts = 0
		crash()
		d := waitDiagnostic(t, ps, "proxy_restart_exhausted")
		if d.Level != DiagnosticError {
			t.Errorf("diagnostic level = %q, want error", d.Level)
		}
		if got := ps.Stats().LastError; !strings.Contains(got, "max restarts exceeded") {
			t.Errorf("stats last error = %q, want it kept", got)
		}
	})

	t.Run("rebind fails", func(t *testing.T) {
		ps, crash := startCrashableProxy(t, true, func(string, string) (net.Listener, error) {
			return nil, errors.New("bind refused")
		})
		crash()
		d := waitDiagnostic(t, ps, "proxy_restart_failed")
		if d.Level != DiagnosticError || !strings.Contains(d.Message, "bind refused") {
			t.Errorf("diagnostic = %+v, want an error naming the rebind failure", d)
		}
	})
}
