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

// TestHubHandleProxyStart exercises the MCP tool PROXY START path end-to-end
// against a shared daemon + backend + client. Each subtest uses a unique
// proxyID so lookups in proxyEntryStore / stateMgr / startupErrorStore stay
// disjoint — no cross-contamination. Keeps every assertion from the original
// 5 standalone tests; shares one daemon boot instead of 5.
func TestHubHandleProxyStart(t *testing.T) {
	daemon, client, backend, tmpDir := newHubProxyTestDaemon(t)

	// RegistersAdminEntry: PROXY START via the MCP tool path must register
	// a proxy-kind entry in the admin surface (proxyEntryStore). Before T3
	// the tool handler called proxym.Create directly without populating the
	// admin registry, so tool-started proxies never appeared in the overlay
	// status bar or SCRIPT LIST. T3 unifies the paths.
	t.Run("RegistersAdminEntry", func(t *testing.T) {
		proxyID := "admin-entry"
		if _, err := client.ProxyStartWithConfig(proxyID, backend.URL, 0, 100, ProxyStartConfig{
			Path: tmpDir,
		}); err != nil {
			t.Fatalf("ProxyStart failed: %v", err)
		}

		entries := daemon.proxyEntries.List(tmpDir)
		var entry *proxyScriptEntry
		for _, e := range entries {
			if e.ProxyID() == proxyID {
				entry = e
				break
			}
		}
		if entry == nil {
			t.Fatalf("expected proxy-kind admin entry for %q, entries=%v", proxyID, entries)
		}
		if entry.Name() != proxyID {
			t.Errorf("entry.Name: got %q, want %q", entry.Name(), proxyID)
		}
		if entry.ProjectPath() != tmpDir {
			t.Errorf("entry.ProjectPath: got %q, want %q", entry.ProjectPath(), tmpDir)
		}
	})

	// PersistsToStateManager: PROXY START must persist the proxy into the
	// StateManager so MCP-tool-started proxies survive a daemon restart.
	// Regression guard — the tool path did this before T3; after T3
	// collapses it into handleExplicitStart, the behavior must remain.
	t.Run("PersistsToStateManager", func(t *testing.T) {
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
	})

	// ResponseShape: pins the JSON contract of the PROXY START response.
	// Callers rely on the `id`, `listen_addr`, `target_url`, `status`
	// keys; prevents silent contract breakage across the T3 refactor.
	t.Run("ResponseShape", func(t *testing.T) {
		proxyID := "shape"
		resp, err := client.ProxyStartWithConfig(proxyID, backend.URL, 0, 100, ProxyStartConfig{
			Path: tmpDir,
		})
		if err != nil {
			t.Fatalf("ProxyStart failed: %v", err)
		}

		if got, ok := resp["id"].(string); !ok || got != proxyID {
			t.Errorf("resp.id: got %v (ok=%v), want %q", resp["id"], ok, proxyID)
		}
		addr, ok := resp["listen_addr"].(string)
		if !ok || addr == "" {
			t.Fatalf("resp.listen_addr: got %v (ok=%v), want non-empty", resp["listen_addr"], ok)
		}
		if !strings.Contains(addr, ":") {
			t.Errorf("resp.listen_addr %q should be host:port", addr)
		}
		if got, ok := resp["target_url"].(string); !ok || got != backend.URL {
			t.Errorf("resp.target_url: got %v (ok=%v), want %q", resp["target_url"], ok, backend.URL)
		}
		if got, ok := resp["status"].(string); !ok || got != "running" {
			t.Errorf("resp.status: got %v (ok=%v), want %q", resp["status"], ok, "running")
		}
	})

	// BindAddressInResponse: when the MCP caller supplies a bind_address,
	// it appears in the response (the optional field in the JSON contract).
	t.Run("BindAddressInResponse", func(t *testing.T) {
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
	})

	// NoOverlayEmitsWarning: starting a proxy when no session has been
	// registered emits a proxy_no_overlay warning to the startupErrorStore.
	// Before T3 this warning lived in the hub handler; T3 moves it into
	// handleExplicitStart so the autostart path also surfaces it.
	t.Run("NoOverlayEmitsWarning", func(t *testing.T) {
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
			t.Errorf("expected proxy_no_overlay entry for %q in startupErrorStore", proxyID)
		}
	})
}
