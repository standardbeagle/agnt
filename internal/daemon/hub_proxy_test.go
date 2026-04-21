//go:build unix

package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newHubProxyTestDaemon spins up a daemon with state persistence enabled and
// returns the daemon, a connected client, the backing httptest backend, and
// the project tmp dir. Covers the common setup for the refactored
// hubHandleProxyStart path (T3): state-manager persistence, proxy registry
// bookkeeping, overlay wiring, and response-shape assertions all depend on a
// live daemon + live proxym. Defers teardown via t.Cleanup so each test is
// self-contained.
func newHubProxyTestDaemon(t *testing.T) (*Daemon, *Client, *httptest.Server, string) {
	t.Helper()
	tmpDir := shortTempDir(t)
	sockPath := shortSockPath(t)
	statePath := filepath.Join(tmpDir, "daemon-state.json")

	daemon := New(DaemonConfig{
		SocketPath:             sockPath,
		MaxClients:             10,
		WriteTimeout:           5 * time.Second,
		EnableStatePersistence: true,
		StatePath:              statePath,
	})
	if err := daemon.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		daemon.Stop(ctx)
	})

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html><body>backend</body></html>"))
	}))
	t.Cleanup(backend.Close)

	client := NewClient(WithSocketPath(sockPath))
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return daemon, client, backend, tmpDir
}

// TestHubHandleProxyStart_RegistersAdminEntry verifies that PROXY START via
// the MCP tool path registers a proxy-kind entry in the admin surface
// (proxyEntryStore). Before T3, the tool handler called proxym.Create
// directly without also populating the admin registry, so proxies started
// via the MCP tool never appeared in the overlay status bar or SCRIPT LIST.
// The T2 handleExplicitStart path does this wiring — T3 unifies the paths
// so the tool gets the same behavior.
func TestHubHandleProxyStart_RegistersAdminEntry(t *testing.T) {
	daemon, client, backend, tmpDir := newHubProxyTestDaemon(t)

	proxyID := "admin-entry"
	if _, err := client.ProxyStartWithConfig(proxyID, backend.URL, 0, 100, ProxyStartConfig{
		Path: tmpDir,
	}); err != nil {
		t.Fatalf("ProxyStart failed: %v", err)
	}

	// Admin entry must be registered under the derived name.
	entries := daemon.proxyEntries.List(tmpDir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 proxy-kind admin entry, got %d", len(entries))
	}
	entry := entries[0]
	// The MCP tool path takes the raw ID (no makeProcessID rewrite), so the
	// admin name equals the ID (no prefix to strip).
	if entry.Name() != proxyID {
		t.Errorf("entry.Name: got %q, want %q", entry.Name(), proxyID)
	}
	if entry.ProxyID() != proxyID {
		t.Errorf("entry.ProxyID: got %q, want %q", entry.ProxyID(), proxyID)
	}
	if entry.ProjectPath() != tmpDir {
		t.Errorf("entry.ProjectPath: got %q, want %q", entry.ProjectPath(), tmpDir)
	}
}

// TestHubHandleProxyStart_PersistsToStateManager verifies that PROXY START
// persists the proxy into the StateManager so MCP-tool-started proxies
// survive a daemon restart. This is a regression guard — the tool path
// already did this before T3, but after T3 collapses it into
// handleExplicitStart we need to confirm the behavior is preserved end to
// end.
func TestHubHandleProxyStart_PersistsToStateManager(t *testing.T) {
	daemon, client, backend, tmpDir := newHubProxyTestDaemon(t)

	proxyID := "persisted"
	if _, err := client.ProxyStartWithConfig(proxyID, backend.URL, 0, 250, ProxyStartConfig{
		Path: tmpDir,
	}); err != nil {
		t.Fatalf("ProxyStart failed: %v", err)
	}

	persisted, ok := daemon.stateMgr.GetProxy(proxyID)
	if !ok {
		t.Fatalf("expected proxy %q to be persisted to stateMgr", proxyID)
	}
	if persisted.ID != proxyID {
		t.Errorf("persisted.ID: got %q, want %q", persisted.ID, proxyID)
	}
	if persisted.TargetURL != backend.URL {
		t.Errorf("persisted.TargetURL: got %q, want %q", persisted.TargetURL, backend.URL)
	}
	if persisted.MaxLogSize != 250 {
		t.Errorf("persisted.MaxLogSize: got %d, want %d", persisted.MaxLogSize, 250)
	}
	if persisted.Path != tmpDir {
		t.Errorf("persisted.Path: got %q, want %q", persisted.Path, tmpDir)
	}
}

// TestHubHandleProxyStart_ResponseShape pins the JSON contract of the
// PROXY START response: the caller relies on `id`, `listen_addr`,
// `target_url`, `status` keys; `bind_address` is optional. Prevents silent
// contract breakage during the T3 refactor.
func TestHubHandleProxyStart_ResponseShape(t *testing.T) {
	_, client, backend, tmpDir := newHubProxyTestDaemon(t)

	proxyID := "shape"
	resp, err := client.ProxyStartWithConfig(proxyID, backend.URL, 0, 100, ProxyStartConfig{
		Path: tmpDir,
	})
	if err != nil {
		t.Fatalf("ProxyStart failed: %v", err)
	}

	// id must round-trip verbatim.
	if got, ok := resp["id"].(string); !ok || got != proxyID {
		t.Errorf("resp.id: got %v (ok=%v), want %q", resp["id"], ok, proxyID)
	}
	// listen_addr must be non-empty and look like host:port.
	addr, ok := resp["listen_addr"].(string)
	if !ok || addr == "" {
		t.Fatalf("resp.listen_addr: got %v (ok=%v), want non-empty", resp["listen_addr"], ok)
	}
	if !strings.Contains(addr, ":") {
		t.Errorf("resp.listen_addr %q should be host:port", addr)
	}
	// target_url must equal the backend URL verbatim.
	if got, ok := resp["target_url"].(string); !ok || got != backend.URL {
		t.Errorf("resp.target_url: got %v (ok=%v), want %q", resp["target_url"], ok, backend.URL)
	}
	// status must be "running".
	if got, ok := resp["status"].(string); !ok || got != "running" {
		t.Errorf("resp.status: got %v (ok=%v), want %q", resp["status"], ok, "running")
	}
}

// TestHubHandleProxyStart_BindAddressInResponse verifies that when the MCP
// caller supplies a bind_address, it appears in the response (the optional
// field in the JSON contract).
func TestHubHandleProxyStart_BindAddressInResponse(t *testing.T) {
	_, client, backend, tmpDir := newHubProxyTestDaemon(t)

	resp, err := client.ProxyStartWithConfig("bind-test", backend.URL, 0, 100, ProxyStartConfig{
		Path:        tmpDir,
		BindAddress: "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("ProxyStart failed: %v", err)
	}

	got, ok := resp["bind_address"].(string)
	if !ok {
		t.Fatalf("resp.bind_address: expected string, got %v", resp["bind_address"])
	}
	if got != "127.0.0.1" {
		t.Errorf("resp.bind_address: got %q, want %q", got, "127.0.0.1")
	}
}

// TestHubHandleProxyStart_NoOverlayEmitsWarning verifies that starting a
// proxy when no session has been registered emits a proxy_no_overlay
// warning to the startupErrorStore. Before T3 this warning lived in the
// hub handler; T3 moves it into handleExplicitStart so the autostart path
// also surfaces it. Regression guard for the warning migration.
func TestHubHandleProxyStart_NoOverlayEmitsWarning(t *testing.T) {
	daemon, client, backend, tmpDir := newHubProxyTestDaemon(t)

	// No session is registered for tmpDir, and there is no global overlay
	// endpoint set. The warning should fire.
	proxyID := "no-overlay"
	if _, err := client.ProxyStartWithConfig(proxyID, backend.URL, 0, 100, ProxyStartConfig{
		Path: tmpDir,
	}); err != nil {
		t.Fatalf("ProxyStart failed: %v", err)
	}

	// Give the handler a moment to finish synchronous work.
	time.Sleep(50 * time.Millisecond)

	entries := daemon.startupErrorStore.Query(StartupLogFilter{})
	var found bool
	for _, e := range entries {
		if e.EventType == "proxy_no_overlay" && e.ProcessID == proxyID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected proxy_no_overlay entry for %q in startupErrorStore, entries=%v", proxyID, entries)
	}
}
