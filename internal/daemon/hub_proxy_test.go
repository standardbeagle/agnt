//go:build unix

package daemon

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/daemonclient"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/require"
)

// newHubProxyTestDaemon spins up a daemon with state persistence enabled and
// returns the daemon, a connected client, the backing httptest backend, and
// the project tmp dir. Covers the common setup for the refactored
// hubHandleProxyStart path (T3): state-manager persistence, proxy registry
// bookkeeping, overlay wiring, and response-shape assertions all depend on a
// live daemon + live proxym. Defers teardown via t.Cleanup so each test is
// self-contained.
func newHubProxyTestDaemon(t *testing.T) (*Daemon, *daemonclient.Client, *httptest.Server, string) {
	t.Helper()
	tmpDir := shortTempDir(t)
	sockPath := shortSockPath(t)
	statePath := filepath.Join(tmpDir, "daemon-state.json")

	// NewForTest skips cleanupOrphans/startupPortCleanup/startupOrphanPGIDScan/
	// restoreProxies — none of which these proxy-subsystem tests exercise. The
	// skip makes parallel execution safe; Start() walks /proc and issues kill(2)
	// on scan-discovered PIDs, so N parallel daemons race on host-global state.
	daemon := NewForTest(t, DaemonConfig{
		SocketPath:             sockPath,
		MaxClients:             10,
		WriteTimeout:           5 * time.Second,
		EnableStatePersistence: true,
		StatePath:              statePath,
	})

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html><body>backend</body></html>"))
	}))
	t.Cleanup(backend.Close)

	client := daemonclient.NewClient(daemonclient.WithSocketPath(sockPath))
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
	t.Parallel()
	daemon, client, backend, tmpDir := newHubProxyTestDaemon(t)

	// RegistersAdminEntry: PROXY START via the MCP tool path must register
	// a proxy-kind entry in the admin surface (proxyEntryStore). Before T3
	// the tool handler called proxym.Create directly without populating the
	// admin registry, so tool-started proxies never appeared in the overlay
	// status bar or SCRIPT LIST. T3 unifies the paths.
	t.Run("RegistersAdminEntry", func(t *testing.T) {
		proxyID := "admin-entry"
		if _, err := client.ProxyStartWithConfig(proxyID, backend.URL, 0, 100, daemonclient.ProxyStartConfig{
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
		if _, err := client.ProxyStartWithConfig(proxyID, backend.URL, 0, 250, daemonclient.ProxyStartConfig{
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
		resp, err := client.ProxyStartWithConfig(proxyID, backend.URL, 0, 100, daemonclient.ProxyStartConfig{
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
		resp, err := client.ProxyStartWithConfig("bind-test", backend.URL, 0, 100, daemonclient.ProxyStartConfig{
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
		if _, err := client.ProxyStartWithConfig(proxyID, backend.URL, 0, 100, daemonclient.ProxyStartConfig{
			Path: tmpDir,
		}); err != nil {
			t.Fatalf("ProxyStart failed: %v", err)
		}

		require.Eventually(t, func() bool {
			entries := daemon.startupErrorStore.Query(StartupLogFilter{})
			for _, e := range entries {
				if e.EventType == "proxy_no_overlay" && e.ProcessID == proxyID {
					return true
				}
			}
			return false
		}, 2*time.Second, 10*time.Millisecond, "expected proxy_no_overlay entry for %q in startupErrorStore", proxyID)
	})
}

// TestHubHandleProxyStats pins the uptime / total_requests contract on the
// PROXY STATUS and PROXY LIST responses. Before T6 both handlers dropped
// the human-readable uptime string and request counter on the floor — the
// ProxyStats block embedded under "stats" in PROXY STATUS marshalled Uptime
// as a nanosecond integer (not parseable by getString), and PROXY LIST
// didn't even include the fields. haam-prod observed total_requests:0 and
// uptime:"" in production while proxylog showed hundreds of requests.
//
// Two creation paths covered: the MCP tool path (client.ProxyStartWithConfig)
// and the autostart-driven explicit-start event (daemon.handleExplicitStart
// called directly, which is what autostartProxy queues via proxyEvents).
func TestHubHandleProxyStats(t *testing.T) {
	t.Parallel()
	daemon, client, backend, tmpDir := newHubProxyTestDaemon(t)

	// fireRequests hits the proxy listen-addr N times and drains the body.
	// Each request bumps requestSeq on the ProxyServer; the daemon logs HTTP
	// entries through the TrafficLogger onLogEntry hook. We want the counter
	// increment, not the log entry count — they track independently but both
	// must be non-zero for a serving proxy.
	fireRequests := func(t *testing.T, listenAddr string, n int) {
		t.Helper()
		clientHTTP := &http.Client{Timeout: 10 * time.Second}
		for i := 0; i < n; i++ {
			resp, err := clientHTTP.Get("http://" + listenAddr + "/")
			if err != nil {
				t.Fatalf("request %d failed: %v", i, err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}

	// StatusReturnsUptimeAndTotalRequests: after N requests, PROXY STATUS
	// must report total_requests >= N at the top level (where handleProxyStatus
	// reads it via getInt64(result, "total_requests")) and a parseable uptime
	// duration at the top level. Failing either returns "" / 0 to the AI
	// agent — the exact bug haam-prod surfaced.
	t.Run("StatusReturnsUptimeAndTotalRequests_MCPPath", func(t *testing.T) {
		proxyID := "stats-mcp"
		startResp, err := client.ProxyStartWithConfig(proxyID, backend.URL, 0, 100, daemonclient.ProxyStartConfig{
			Path: tmpDir,
		})
		if err != nil {
			t.Fatalf("ProxyStart failed: %v", err)
		}
		listenAddr, _ := startResp["listen_addr"].(string)
		if listenAddr == "" {
			t.Fatalf("no listen_addr in start response")
		}

		const n = 5
		fireRequests(t, listenAddr, n)

		statusResp, err := client.ProxyStatus(proxyID)
		if err != nil {
			t.Fatalf("ProxyStatus failed: %v", err)
		}

		// total_requests must be readable from the top level as a number
		// (json decodes numbers as float64). getInt64 in daemon_proxy.go
		// does the same conversion, so this is the literal wire contract.
		raw, ok := statusResp["total_requests"]
		if !ok {
			t.Fatalf("response missing total_requests field: %v", statusResp)
		}
		got, ok := raw.(float64)
		if !ok {
			t.Fatalf("total_requests wrong type: got %T (%v), want float64", raw, raw)
		}
		if int64(got) < n {
			t.Errorf("total_requests: got %v, want >= %d (fired %d requests)", got, n, n)
		}

		// uptime must be a parseable non-empty duration string at the top
		// level. Go's time.Duration marshals to JSON as nanosecond int; the
		// MCP layer expects a string (getString). Handler must format it.
		uptimeStr, ok := statusResp["uptime"].(string)
		if !ok || uptimeStr == "" {
			t.Fatalf("uptime missing or wrong type: got %v (%T)", statusResp["uptime"], statusResp["uptime"])
		}
		d, err := time.ParseDuration(uptimeStr)
		if err != nil {
			t.Fatalf("uptime %q is not a parseable duration: %v", uptimeStr, err)
		}
		if d <= 0 {
			t.Errorf("uptime %v should be > 0", d)
		}
	})

	// UptimeMonotonicallyIncreases: two polls 1s apart must show increasing
	// uptime. Acceptance criterion 2 from the task description. Any fixed-
	// value bug (e.g. formatting with Truncate(0)) would fail this.
	t.Run("UptimeMonotonicallyIncreases", func(t *testing.T) {
		proxyID := "stats-mono"
		if _, err := client.ProxyStartWithConfig(proxyID, backend.URL, 0, 100, daemonclient.ProxyStartConfig{
			Path: tmpDir,
		}); err != nil {
			t.Fatalf("ProxyStart failed: %v", err)
		}

		first, err := client.ProxyStatus(proxyID)
		if err != nil {
			t.Fatalf("first ProxyStatus: %v", err)
		}
		firstStr, _ := first["uptime"].(string)
		firstDur, err := time.ParseDuration(firstStr)
		if err != nil {
			t.Fatalf("first uptime %q not parseable: %v", firstStr, err)
		}

		time.Sleep(1100 * time.Millisecond)

		second, err := client.ProxyStatus(proxyID)
		if err != nil {
			t.Fatalf("second ProxyStatus: %v", err)
		}
		secondStr, _ := second["uptime"].(string)
		secondDur, err := time.ParseDuration(secondStr)
		if err != nil {
			t.Fatalf("second uptime %q not parseable: %v", secondStr, err)
		}

		if secondDur <= firstDur {
			t.Errorf("uptime did not increase: first=%v, second=%v", firstDur, secondDur)
		}
		if secondDur-firstDur < 900*time.Millisecond {
			t.Errorf("uptime delta %v smaller than elapsed ~1s", secondDur-firstDur)
		}
	})

	// ListReturnsUptimeAndTotalRequests_MCPPath: PROXY LIST dropped these
	// fields entirely before T6 — the response dict had id/listen_addr/
	// target_url/status/running/path only. The MCP tool layer read them
	// via getString/getInt64 and always got "" / 0, which is what the bug
	// report showed.
	t.Run("ListReturnsUptimeAndTotalRequests_MCPPath", func(t *testing.T) {
		proxyID := "stats-list-mcp"
		startResp, err := client.ProxyStartWithConfig(proxyID, backend.URL, 0, 100, daemonclient.ProxyStartConfig{
			Path: tmpDir,
		})
		if err != nil {
			t.Fatalf("ProxyStart failed: %v", err)
		}
		listenAddr, _ := startResp["listen_addr"].(string)

		const n = 3
		fireRequests(t, listenAddr, n)

		listResp, err := client.ProxyList(protocolDirFilterGlobal())
		if err != nil {
			t.Fatalf("ProxyList failed: %v", err)
		}
		entry := findListEntry(t, listResp, proxyID)

		raw, ok := entry["total_requests"]
		if !ok {
			t.Fatalf("list entry missing total_requests: %v", entry)
		}
		got, ok := raw.(float64)
		if !ok {
			t.Fatalf("total_requests wrong type: got %T (%v), want float64", raw, raw)
		}
		if int64(got) < n {
			t.Errorf("total_requests: got %v, want >= %d", got, n)
		}

		uptimeStr, ok := entry["uptime"].(string)
		if !ok || uptimeStr == "" {
			t.Fatalf("uptime missing or wrong type: got %v (%T)", entry["uptime"], entry["uptime"])
		}
		if _, err := time.ParseDuration(uptimeStr); err != nil {
			t.Fatalf("uptime %q not parseable: %v", uptimeStr, err)
		}
	})

	// ListReturnsUptimeAndTotalRequests_AutostartPath: acceptance criterion
	// 3 — both paths must work. The autostart-surrogate here is a direct
	// handleExplicitStart call (what autostartProxy queues to proxyEvents;
	// the daemon's event loop dispatches to handleExplicitStart verbatim).
	// Using the event path directly avoids spinning up a full .agnt.kdl
	// fixture while exercising the same creation sequence.
	t.Run("ListReturnsUptimeAndTotalRequests_AutostartPath", func(t *testing.T) {
		proxyID := "stats-list-autostart"
		daemon.handleExplicitStart(ProxyEvent{
			Type:    ExplicitStart,
			ProxyID: proxyID,
			Config: &config.ProxyConfig{
				URL:        backend.URL,
				ListenPort: 0,
				MaxLogSize: 100,
			},
			Path: tmpDir,
		})

		proxyServer, err := daemon.proxym.Get(proxyID)
		if err != nil {
			t.Fatalf("autostart proxy not created: %v", err)
		}
		listenAddr := proxyServer.ListenAddr

		const n = 2
		fireRequests(t, listenAddr, n)

		listResp, err := client.ProxyList(protocolDirFilterGlobal())
		if err != nil {
			t.Fatalf("ProxyList failed: %v", err)
		}
		entry := findListEntry(t, listResp, proxyID)

		raw, ok := entry["total_requests"]
		if !ok {
			t.Fatalf("list entry missing total_requests: %v", entry)
		}
		got, ok := raw.(float64)
		if !ok || int64(got) < n {
			t.Errorf("total_requests: got %v (type %T), want >= %d", raw, raw, n)
		}

		uptimeStr, ok := entry["uptime"].(string)
		if !ok || uptimeStr == "" {
			t.Fatalf("uptime missing or wrong type: got %v (%T)", entry["uptime"], entry["uptime"])
		}
		if _, err := time.ParseDuration(uptimeStr); err != nil {
			t.Fatalf("uptime %q not parseable: %v", uptimeStr, err)
		}
	})
}

// protocolDirFilterGlobal returns a directory filter that matches every
// proxy regardless of project path. The list subtests bypass the session-
// registry path scoping that production callers use.
func protocolDirFilterGlobal() protocol.DirectoryFilter {
	return protocol.DirectoryFilter{Global: true}
}

// findListEntry locates a proxy entry by ID in a PROXY LIST response or
// fails the test. Keeps the assertion-heavy subtests above readable.
func findListEntry(t *testing.T, listResp map[string]interface{}, proxyID string) map[string]interface{} {
	t.Helper()
	proxies, ok := listResp["proxies"].([]interface{})
	if !ok {
		t.Fatalf("list response missing proxies array: %v", listResp)
	}
	for _, p := range proxies {
		pm, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		if id, _ := pm["id"].(string); id == proxyID {
			return pm
		}
	}
	t.Fatalf("proxy %q not found in list: %v", proxyID, proxies)
	return nil
}

// TestHubHandleProxyStart_FailureInlinesCause asserts that when proxy creation
// fails, PROXY START does not return a bare "not created" — it inlines the
// recorded proxy_creation_failed cause and points at the durable surfaces.
// Regression: the handler surfaced only the terse proxym.Get "not found",
// hiding the real reason from the MCP caller.
func TestHubHandleProxyStart_FailureInlinesCause(t *testing.T) {
	t.Parallel()
	_, client, backend, tmpDir := newHubProxyTestDaemon(t)

	// Binding to 0.0.0.0 without allow_external makes NewProxyServer (via
	// handleExplicitStart -> Create) fail with a deterministic error that is
	// recorded as proxy_creation_failed.
	_, err := client.ProxyStartWithConfig("fail-proxy", backend.URL, 0, 100, daemonclient.ProxyStartConfig{
		Path:          tmpDir,
		BindAddress:   "0.0.0.0",
		AllowExternal: false,
	})
	require.Error(t, err, "external bind without allow_external must fail the start")
	msg := err.Error()
	require.Contains(t, msg, "was not created")
	require.Contains(t, msg, "cause:", "the recorded proxy_creation_failed detail must be inlined")
	require.Contains(t, msg, "allow_external", "the inlined cause must carry the real reason")
	require.Contains(t, msg, "startup_log", "the message must point at the durable startup-log surface")
}
