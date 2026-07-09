package daemon

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/proxy"
)

func TestMakeProxyIDFromURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		projectPath string
		proxyName   string
		urlStr      string
		wantPattern string // regex pattern to match (basename-hash:proxyName:host-port)
	}{
		{
			name:        "localhost URL with port",
			projectPath: "/home/user/my-project",
			proxyName:   "dev",
			urlStr:      "http://localhost:3000",
			wantPattern: `^my-project-[0-9a-f]{8}:dev:localhost-3000$`,
		},
		{
			name:        "IP address URL",
			projectPath: "/home/user/project",
			proxyName:   "api",
			urlStr:      "http://127.0.0.1:8080",
			wantPattern: `^project-[0-9a-f]{8}:api:127-0-0-1-8080$`,
		},
		{
			name:        "URL with default HTTP port",
			projectPath: "/home/user/app",
			proxyName:   "web",
			urlStr:      "http://localhost",
			wantPattern: `^app-[0-9a-f]{8}:web:localhost-80$`,
		},
		{
			name:        "HTTPS URL",
			projectPath: "/home/user/secure",
			proxyName:   "ssl",
			urlStr:      "https://localhost",
			wantPattern: `^secure-[0-9a-f]{8}:ssl:localhost-443$`,
		},
		{
			name:        "URL with explicit port",
			projectPath: "/tmp/test",
			proxyName:   "srv",
			urlStr:      "http://192.168.1.1:9000",
			wantPattern: `^test-[0-9a-f]{8}:srv:192-168-1-1-9000$`,
		},
		{
			name:        "invalid URL falls back to simple ID",
			projectPath: "/home/user/project",
			proxyName:   "test",
			urlStr:      "://invalid",
			wantPattern: `^project-[0-9a-f]{8}:test$`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := makeProxyIDFromURL(tt.projectPath, tt.proxyName, tt.urlStr)
			matched, err := regexp.MatchString(tt.wantPattern, got)
			if err != nil {
				t.Fatalf("invalid pattern %q: %v", tt.wantPattern, err)
			}
			if !matched {
				t.Errorf("makeProxyIDFromURL() = %q, want pattern %q", got, tt.wantPattern)
			}
		})
	}
}

func TestMakeProxyIDFromURL_Uniqueness(t *testing.T) {
	t.Parallel()
	// Verify that different project paths produce different IDs
	id1 := makeProxyIDFromURL("/home/user/myapp", "dev", "http://localhost:3000")
	id2 := makeProxyIDFromURL("/home/work/myapp", "dev", "http://localhost:3000")

	if id1 == id2 {
		t.Errorf("Same ID generated for different paths: %q", id1)
	}

	// Verify they both have the expected structure
	if !strings.HasPrefix(id1, "myapp-") || !strings.HasPrefix(id2, "myapp-") {
		t.Errorf("IDs don't start with basename: id1=%q, id2=%q", id1, id2)
	}
	if !strings.Contains(id1, ":dev:") || !strings.Contains(id2, ":dev:") {
		t.Errorf("IDs don't contain proxy name: id1=%q, id2=%q", id1, id2)
	}
}

func TestMakeProcessID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		projectPath string
		scriptName  string
		wantPattern string // regex pattern to match (basename-hash:scriptName)
	}{
		{
			name:        "standard path",
			projectPath: "/home/user/my-project",
			scriptName:  "dev",
			wantPattern: `^my-project-[0-9a-f]{8}:dev$`,
		},
		{
			name:        "nested path",
			projectPath: "/home/user/work/apps/frontend",
			scriptName:  "start",
			wantPattern: `^frontend-[0-9a-f]{8}:start$`,
		},
		{
			name:        "empty project path returns name only",
			projectPath: "",
			scriptName:  "build",
			wantPattern: `^build$`,
		},
		{
			name:        "root path",
			projectPath: rootPath(),
			scriptName:  "test",
			wantPattern: `^` + regexp.QuoteMeta(filepath.Base(rootPath())) + `-[0-9a-f]{8}:test$`,
		},
		{
			name:        "trailing slash handled",
			projectPath: "/home/user/project/",
			scriptName:  "lint",
			wantPattern: `^project-[0-9a-f]{8}:lint$`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := makeProcessID(tt.projectPath, tt.scriptName)
			matched, err := regexp.MatchString(tt.wantPattern, got)
			if err != nil {
				t.Fatalf("invalid pattern %q: %v", tt.wantPattern, err)
			}
			if !matched {
				t.Errorf("makeProcessID() = %q, want pattern %q", got, tt.wantPattern)
			}
		})
	}
}

func TestMakeProcessID_Uniqueness(t *testing.T) {
	t.Parallel()
	// Verify that different project paths produce different IDs
	id1 := makeProcessID("/home/user/myapp", "dev")
	id2 := makeProcessID("/home/work/myapp", "dev")

	if id1 == id2 {
		t.Errorf("Same ID generated for different paths: %q", id1)
	}

	// Same path should produce same ID (deterministic)
	id3 := makeProcessID("/home/user/myapp", "dev")
	if id1 != id3 {
		t.Errorf("Different IDs for same path: %q vs %q", id1, id3)
	}
}

func TestMapKeys(t *testing.T) {
	t.Parallel()
	t.Run("empty map", func(t *testing.T) {
		m := map[string]*config.ScriptConfig{}
		keys := mapKeys(m)
		if len(keys) != 0 {
			t.Errorf("Expected 0 keys, got %d", len(keys))
		}
	})

	t.Run("single key", func(t *testing.T) {
		m := map[string]*config.ScriptConfig{
			"dev": {},
		}
		keys := mapKeys(m)
		if len(keys) != 1 {
			t.Errorf("Expected 1 key, got %d", len(keys))
		}
		if keys[0] != "dev" {
			t.Errorf("Expected key 'dev', got %q", keys[0])
		}
	})

	t.Run("multiple keys", func(t *testing.T) {
		m := map[string]*config.ScriptConfig{
			"dev":   {},
			"build": {},
			"test":  {},
		}
		keys := mapKeys(m)
		if len(keys) != 3 {
			t.Errorf("Expected 3 keys, got %d", len(keys))
		}
		// Check all keys present (order not guaranteed)
		keySet := make(map[string]bool)
		for _, k := range keys {
			keySet[k] = true
		}
		for _, expected := range []string{"dev", "build", "test"} {
			if !keySet[expected] {
				t.Errorf("Missing expected key %q", expected)
			}
		}
	})
}

func TestMapKeysProxy(t *testing.T) {
	t.Parallel()
	t.Run("empty map", func(t *testing.T) {
		m := map[string]*config.ProxyConfig{}
		keys := mapKeysProxy(m)
		if len(keys) != 0 {
			t.Errorf("Expected 0 keys, got %d", len(keys))
		}
	})

	t.Run("single key", func(t *testing.T) {
		m := map[string]*config.ProxyConfig{
			"api": {},
		}
		keys := mapKeysProxy(m)
		if len(keys) != 1 {
			t.Errorf("Expected 1 key, got %d", len(keys))
		}
		if keys[0] != "api" {
			t.Errorf("Expected key 'api', got %q", keys[0])
		}
	})

	t.Run("multiple keys", func(t *testing.T) {
		m := map[string]*config.ProxyConfig{
			"api":     {},
			"web":     {},
			"metrics": {},
		}
		keys := mapKeysProxy(m)
		if len(keys) != 3 {
			t.Errorf("Expected 3 keys, got %d", len(keys))
		}
		// Check all keys present (order not guaranteed)
		keySet := make(map[string]bool)
		for _, k := range keys {
			keySet[k] = true
		}
		for _, expected := range []string{"api", "web", "metrics"} {
			if !keySet[expected] {
				t.Errorf("Missing expected key %q", expected)
			}
		}
	})
}

func TestProxyEvent_HandleURLDetected_ProxyLimit(t *testing.T) {
	t.Parallel()
	tmpDir := shortTempDir(t)
	sockPath := shortSockPath(t)

	daemon := NewForTest(t, DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	// Create config with proxy linked to script
	configPath := filepath.Join(tmpDir, "agnt.kdl")
	configContent := `
proxies {
    dev {
        script "dev"
    }
}
`
	if err := writeFile(configPath, configContent); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	scriptID := tmpDir + ":dev"

	// Track 5 proxies manually to hit limit
	for i := 0; i < 5; i++ {
		proxyID := fmt.Sprintf("proxy-%d", i)
		daemon.trackScriptProxy(scriptID, proxyID)
	}

	// Try to detect a 6th URL - should hit limit and skip
	daemon.handleURLDetected(ProxyEvent{
		Type:     URLDetected,
		ScriptID: scriptID,
		URL:      "http://localhost:3006",
	})

	time.Sleep(100 * time.Millisecond)

	// Verify proxy count didn't exceed 5
	proxies := daemon.getProxiesForScript(scriptID)
	if len(proxies) > 5 {
		t.Errorf("Expected max 5 proxies, got %d", len(proxies))
	}
}

func TestProxyEvent_HandleURLDetected_ParseError(t *testing.T) {
	t.Parallel()
	sockPath := shortSockPath(t)

	daemon := NewForTest(t, DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	// Send event with invalid script ID (no colon separator)
	daemon.handleURLDetected(ProxyEvent{
		Type:     URLDetected,
		ScriptID: "invalid-no-separator",
		URL:      "http://localhost:3000",
	})

	// Should log warning and return early, no proxy created
	time.Sleep(50 * time.Millisecond)
}

func TestProxyEvent_HandleURLDetected_NoMatchingProxyConfig(t *testing.T) {
	t.Parallel()
	tmpDir := shortTempDir(t)
	sockPath := shortSockPath(t)

	daemon := NewForTest(t, DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	// Create config with proxy for different script
	configPath := filepath.Join(tmpDir, "agnt.kdl")
	configContent := `
proxies {
    api {
        script "build"
    }
}
`
	if err := writeFile(configPath, configContent); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// Send event for "dev" script - no matching proxy config
	daemon.handleURLDetected(ProxyEvent{
		Type:     URLDetected,
		ScriptID: tmpDir + ":dev",
		URL:      "http://localhost:3000",
	})

	time.Sleep(100 * time.Millisecond)

	// Verify no proxy was created
	proxies := daemon.getProxiesForScript(tmpDir + ":dev")
	if len(proxies) != 0 {
		t.Errorf("Expected 0 proxies for non-matching script, got %d", len(proxies))
	}
}

func TestProxyEvent_HandleURLDetected_DuplicateProxy(t *testing.T) {
	t.Parallel()
	tmpDir := shortTempDir(t)
	sockPath := shortSockPath(t)

	daemon := NewForTest(t, DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	// Create config
	configPath := filepath.Join(tmpDir, "agnt.kdl")
	configContent := `
proxies {
    dev {
        script "dev"
    }
}
`
	if err := writeFile(configPath, configContent); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// Create proxy manually first
	proxyID := makeProxyIDFromURL(tmpDir, "dev", "http://localhost:3000")
	_, err := daemon.proxym.Create(daemon.ctx, proxy.ProxyConfig{
		ID:         proxyID,
		TargetURL:  "http://localhost:3000",
		ListenPort: -1,
		Path:       tmpDir,
	})
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}

	// Try to detect same URL again - should skip
	daemon.handleURLDetected(ProxyEvent{
		Type:     URLDetected,
		ScriptID: tmpDir + ":dev",
		URL:      "http://localhost:3000",
	})

	time.Sleep(100 * time.Millisecond)

	// Should still only have 1 proxy
	proxies := daemon.getProxiesForScript(tmpDir + ":dev")
	if len(proxies) > 1 {
		t.Errorf("Expected 1 proxy (duplicate skipped), got %d", len(proxies))
	}
}

// newFallbackTestDaemon spins up a full daemon with a short temp socket and
// registers a deferred cleanup. Returns the daemon and the tmp project dir.
// Shared by the FallbackPortCheck handler tests. Uses the full daemon.Start
// path (rather than the minimal newTestDaemon helper in proxy_waitfor_test.go)
// because handleFallbackPortCheck depends on startupErrorStore and
// sessionRegistry, which the minimal constructor doesn't populate.
func newFallbackTestDaemon(t *testing.T) (*Daemon, string) {
	t.Helper()
	tmpDir := shortTempDir(t)
	sockPath := shortSockPath(t)

	daemon := NewForTest(t, DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})
	return daemon, tmpDir
}

// countStartupEntriesByEvent returns the number of startupErrorStore entries
// with the given event type.
func countStartupEntriesByEvent(daemon *Daemon, eventType string) int {
	entries := daemon.startupErrorStore.Query(StartupLogFilter{})
	count := 0
	for _, e := range entries {
		if e.EventType == eventType {
			count++
		}
	}
	return count
}

// TestProxyEvent_HandleFallbackPortCheck_CreatesProxy verifies that a
// FallbackPortCheck event with no pre-existing proxy creates a proxy targeting
// localhost:<fallback-port> and records a startup_proxy_fallback_used entry.
func TestProxyEvent_HandleFallbackPortCheck_CreatesProxy(t *testing.T) {
	t.Parallel()
	daemon, tmpDir := newFallbackTestDaemon(t)

	proxyName := "dev"
	scriptName := "weird-dev-server"
	scriptID := makeProcessID(tmpDir, scriptName)
	proxyCfg := &config.ProxyConfig{
		Script:       scriptName,
		FallbackPort: 8080,
	}

	daemon.handleFallbackPortCheck(ProxyEvent{
		Type:      FallbackPortCheck,
		ScriptID:  scriptID,
		ProxyName: proxyName,
		Config:    proxyCfg,
		Path:      tmpDir,
	})

	// Proxy should exist under the explicit-start id scheme.
	expectedID := makeProcessID(tmpDir, proxyName)
	server, err := daemon.proxym.Get(expectedID)
	if err != nil {
		t.Fatalf("Expected fallback proxy %q to exist, got error: %v", expectedID, err)
	}
	if server == nil {
		t.Fatalf("Expected non-nil proxy server for %q", expectedID)
	}

	// Target URL must point at localhost:<fallback-port>.
	wantTarget := "http://localhost:8080"
	if got := server.Stats().TargetURL; got != wantTarget {
		t.Errorf("Expected target URL %q, got %q", wantTarget, got)
	}

	// startup_proxy_fallback_used entry must be recorded.
	if n := countStartupEntriesByEvent(daemon, "startup_proxy_fallback_used"); n != 1 {
		t.Errorf("Expected 1 startup_proxy_fallback_used entry, got %d", n)
	}
	if n := countStartupEntriesByEvent(daemon, "startup_proxy_fallback_failed"); n != 0 {
		t.Errorf("Expected 0 startup_proxy_fallback_failed entries, got %d", n)
	}

	// Proxy should be tracked under the script so ScriptStopped tears it down.
	tracked := daemon.getProxiesForScript(scriptID)
	found := false
	for _, id := range tracked {
		if id == expectedID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected fallback proxy %q to be tracked under script %q, got %v", expectedID, scriptID, tracked)
	}
}

// TestProxyEvent_HandleFallbackPortCheck_SkipsWhenProxyExists verifies that a
// FallbackPortCheck event whose target proxy was already created (URL
// detection won the race) does NOT create a duplicate proxy and records a
// startup_proxy_fallback_skipped_already_running entry instead.
func TestProxyEvent_HandleFallbackPortCheck_SkipsWhenProxyExists(t *testing.T) {
	t.Parallel()
	daemon, tmpDir := newFallbackTestDaemon(t)

	proxyName := "dev"
	scriptName := "weird-dev-server"
	scriptID := makeProcessID(tmpDir, scriptName)

	// Simulate URL detection winning: create a proxy via makeProxyIDFromURL
	// and track it under the script.
	urlProxyID := makeProxyIDFromURL(tmpDir, proxyName, "http://localhost:3000")
	if _, err := daemon.proxym.Create(daemon.ctx, proxy.ProxyConfig{
		ID:         urlProxyID,
		TargetURL:  "http://localhost:3000",
		ListenPort: -1,
		Path:       tmpDir,
	}); err != nil {
		t.Fatalf("Failed to pre-create proxy: %v", err)
	}
	daemon.trackScriptProxy(scriptID, urlProxyID)

	proxyCfg := &config.ProxyConfig{
		Script:       scriptName,
		FallbackPort: 8080,
	}

	daemon.handleFallbackPortCheck(ProxyEvent{
		Type:      FallbackPortCheck,
		ScriptID:  scriptID,
		ProxyName: proxyName,
		Config:    proxyCfg,
		Path:      tmpDir,
	})

	// The fallback-id proxy must NOT exist.
	fallbackID := makeProcessID(tmpDir, proxyName)
	if _, err := daemon.proxym.Get(fallbackID); err == nil {
		t.Errorf("Expected fallback proxy %q to NOT exist (URL detection won), but it was created", fallbackID)
	}

	// Script should still have exactly one proxy tracked (the URL-detected one).
	tracked := daemon.getProxiesForScript(scriptID)
	if len(tracked) != 1 {
		t.Errorf("Expected 1 tracked proxy after skip, got %d: %v", len(tracked), tracked)
	}

	// startup_proxy_fallback_skipped_already_running entry must be recorded.
	if n := countStartupEntriesByEvent(daemon, "startup_proxy_fallback_skipped_already_running"); n != 1 {
		t.Errorf("Expected 1 startup_proxy_fallback_skipped_already_running entry, got %d", n)
	}
	if n := countStartupEntriesByEvent(daemon, "startup_proxy_fallback_used"); n != 0 {
		t.Errorf("Expected 0 startup_proxy_fallback_used entries, got %d", n)
	}
}

// TestProxyEvent_HandleFallbackPortCheck_SkipsWhenFallbackIDExists verifies
// that a second FallbackPortCheck for the same proxy name is a no-op (the
// fallback-id proxy already exists from the first check).
func TestProxyEvent_HandleFallbackPortCheck_SkipsWhenFallbackIDExists(t *testing.T) {
	t.Parallel()
	daemon, tmpDir := newFallbackTestDaemon(t)

	proxyName := "dev"
	scriptName := "weird-dev-server"
	scriptID := makeProcessID(tmpDir, scriptName)

	// Pre-create a proxy using the fallback id scheme (simulates a first
	// FallbackPortCheck having already run).
	fallbackID := makeProcessID(tmpDir, proxyName)
	if _, err := daemon.proxym.Create(daemon.ctx, proxy.ProxyConfig{
		ID:         fallbackID,
		TargetURL:  "http://localhost:8080",
		ListenPort: -1,
		Path:       tmpDir,
	}); err != nil {
		t.Fatalf("Failed to pre-create proxy: %v", err)
	}

	proxyCfg := &config.ProxyConfig{
		Script:       scriptName,
		FallbackPort: 8080,
	}

	daemon.handleFallbackPortCheck(ProxyEvent{
		Type:      FallbackPortCheck,
		ScriptID:  scriptID,
		ProxyName: proxyName,
		Config:    proxyCfg,
		Path:      tmpDir,
	})

	if n := countStartupEntriesByEvent(daemon, "startup_proxy_fallback_skipped_already_running"); n != 1 {
		t.Errorf("Expected 1 startup_proxy_fallback_skipped_already_running entry, got %d", n)
	}
}

// TestProxyEvent_HandleFallbackPortCheck_InvalidEvent verifies that events
// missing required fields are not silently dropped — they emit a
// startup_proxy_fallback_failed entry.
func TestProxyEvent_HandleFallbackPortCheck_InvalidEvent(t *testing.T) {
	t.Parallel()
	daemon, tmpDir := newFallbackTestDaemon(t)

	cases := []struct {
		name  string
		event ProxyEvent
	}{
		{
			name: "missing config",
			event: ProxyEvent{
				Type:      FallbackPortCheck,
				ScriptID:  "script",
				ProxyName: "dev",
				Path:      tmpDir,
			},
		},
		{
			name: "missing proxy name",
			event: ProxyEvent{
				Type:     FallbackPortCheck,
				ScriptID: "script",
				Path:     tmpDir,
				Config:   &config.ProxyConfig{FallbackPort: 8080},
			},
		},
		{
			name: "missing path",
			event: ProxyEvent{
				Type:      FallbackPortCheck,
				ScriptID:  "script",
				ProxyName: "dev",
				Config:    &config.ProxyConfig{FallbackPort: 8080},
			},
		},
		{
			name: "zero fallback port",
			event: ProxyEvent{
				Type:      FallbackPortCheck,
				ScriptID:  "script",
				ProxyName: "dev",
				Path:      tmpDir,
				Config:    &config.ProxyConfig{Script: "dev"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := countStartupEntriesByEvent(daemon, "startup_proxy_fallback_failed")
			daemon.handleFallbackPortCheck(tc.event)
			after := countStartupEntriesByEvent(daemon, "startup_proxy_fallback_failed")
			if after != before+1 {
				t.Errorf("Expected startup_proxy_fallback_failed entry count to grow by 1 (before=%d, after=%d)", before, after)
			}
		})
	}
}

// TestProxyEvent_HandleFallbackPortCheck_SiblingProxyDoesNotSkip verifies
// that if a script has multiple linked proxy configs and URL detection
// created a proxy for ONE of them, the fallback handler for a DIFFERENT
// proxy name does NOT skip. The skip must be scoped to the specific proxy
// name, not to "any proxy tracked under this script".
func TestProxyEvent_HandleFallbackPortCheck_SiblingProxyDoesNotSkip(t *testing.T) {
	t.Parallel()
	daemon, tmpDir := newFallbackTestDaemon(t)

	scriptName := "web"
	scriptID := makeProcessID(tmpDir, scriptName)

	// URL detection created a proxy for the "public" proxy config.
	siblingID := makeProxyIDFromURL(tmpDir, "public", "http://localhost:3000")
	if _, err := daemon.proxym.Create(daemon.ctx, proxy.ProxyConfig{
		ID:         siblingID,
		TargetURL:  "http://localhost:3000",
		ListenPort: -1,
		Path:       tmpDir,
	}); err != nil {
		t.Fatalf("Failed to pre-create sibling proxy: %v", err)
	}
	daemon.trackScriptProxy(scriptID, siblingID)

	// Fire a FallbackPortCheck for a DIFFERENT proxy name ("api") linked to
	// the same script. It must create its own fallback proxy rather than
	// skipping because a sibling already exists.
	daemon.handleFallbackPortCheck(ProxyEvent{
		Type:      FallbackPortCheck,
		ScriptID:  scriptID,
		ProxyName: "api",
		Config: &config.ProxyConfig{
			Script:       scriptName,
			FallbackPort: 8081,
		},
		Path: tmpDir,
	})

	// The api fallback proxy must exist.
	apiID := makeProcessID(tmpDir, "api")
	if _, err := daemon.proxym.Get(apiID); err != nil {
		t.Fatalf("Expected fallback proxy %q to exist, got error: %v", apiID, err)
	}

	if n := countStartupEntriesByEvent(daemon, "startup_proxy_fallback_used"); n != 1 {
		t.Errorf("Expected 1 startup_proxy_fallback_used entry, got %d", n)
	}
	if n := countStartupEntriesByEvent(daemon, "startup_proxy_fallback_skipped_already_running"); n != 0 {
		t.Errorf("Expected 0 skipped entries (sibling should not cause skip), got %d", n)
	}
}

// TestProxyEvent_HandleFallbackPortCheck_CustomHost verifies that the Host
// field on the proxy config is honored when building the target URL.
func TestProxyEvent_HandleFallbackPortCheck_CustomHost(t *testing.T) {
	t.Parallel()
	daemon, tmpDir := newFallbackTestDaemon(t)

	proxyCfg := &config.ProxyConfig{
		Script:       "dev",
		FallbackPort: 9090,
		Host:         "127.0.0.1",
	}

	daemon.handleFallbackPortCheck(ProxyEvent{
		Type:      FallbackPortCheck,
		ScriptID:  makeProcessID(tmpDir, "dev"),
		ProxyName: "api",
		Config:    proxyCfg,
		Path:      tmpDir,
	})

	server, err := daemon.proxym.Get(makeProcessID(tmpDir, "api"))
	if err != nil {
		t.Fatalf("Expected fallback proxy to exist: %v", err)
	}
	if got, want := server.Stats().TargetURL, "http://127.0.0.1:9090"; got != want {
		t.Errorf("Expected target URL %q, got %q", want, got)
	}
}

// TestBuildProxyServerConfig pins the mapping from .agnt.kdl
// ProxyConfig → proxy.ProxyConfig for the new listen-port and
// skip-tls-verify fields. Every handler in proxy_events.go uses
// buildProxyServerConfig; exercising it directly is cheaper than
// spinning up three handler tests that all assert the same thing.
func TestBuildProxyServerConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		cfg            *config.ProxyConfig
		wantListenPort int
		wantStrict     bool
		wantSkipTLS    bool
		wantBind       string
		wantMaxLog     int
	}{
		{
			name:           "nil config defaults to hash allocator",
			cfg:            nil,
			wantListenPort: -1,
			wantStrict:     false,
		},
		{
			name: "no listen-port → hash allocator",
			cfg: &config.ProxyConfig{
				Bind:       "127.0.0.1",
				MaxLogSize: 500,
			},
			wantListenPort: -1,
			wantStrict:     false,
			wantBind:       "127.0.0.1",
			wantMaxLog:     500,
		},
		{
			name: "explicit listen-port → strict mode on",
			cfg: &config.ProxyConfig{
				ListenPort: 4444,
				Bind:       "127.0.0.1",
			},
			wantListenPort: 4444,
			wantStrict:     true,
			wantBind:       "127.0.0.1",
		},
		{
			name: "listen-port zero → hash allocator, not strict",
			cfg: &config.ProxyConfig{
				ListenPort: 0,
				Bind:       "127.0.0.1",
			},
			wantListenPort: -1,
			wantStrict:     false,
			wantBind:       "127.0.0.1",
		},
		{
			name: "skip-tls-verify passes through",
			cfg: &config.ProxyConfig{
				SkipTLSVerify: true,
			},
			wantListenPort: -1,
			wantSkipTLS:    true,
		},
		{
			name: "all new fields together (tdo-style config)",
			cfg: &config.ProxyConfig{
				URL:           "https://tdo-local.sbdev.io",
				ListenPort:    4444,
				SkipTLSVerify: true,
				Bind:          "127.0.0.1",
				MaxLogSize:    2000,
			},
			wantListenPort: 4444,
			wantStrict:     true,
			wantSkipTLS:    true,
			wantBind:       "127.0.0.1",
			wantMaxLog:     2000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildProxyServerConfig("test-id", "http://example.com", "/tmp/project", tt.cfg)

			if got.ID != "test-id" {
				t.Errorf("ID: got %q, want %q", got.ID, "test-id")
			}
			if got.TargetURL != "http://example.com" {
				t.Errorf("TargetURL: got %q, want %q", got.TargetURL, "http://example.com")
			}
			if got.Path != "/tmp/project" {
				t.Errorf("Path: got %q, want %q", got.Path, "/tmp/project")
			}
			if got.ListenPort != tt.wantListenPort {
				t.Errorf("ListenPort: got %d, want %d", got.ListenPort, tt.wantListenPort)
			}
			if got.StrictListenPort != tt.wantStrict {
				t.Errorf("StrictListenPort: got %v, want %v", got.StrictListenPort, tt.wantStrict)
			}
			if got.SkipTLSVerify != tt.wantSkipTLS {
				t.Errorf("SkipTLSVerify: got %v, want %v", got.SkipTLSVerify, tt.wantSkipTLS)
			}
			if got.BindAddress != tt.wantBind {
				t.Errorf("BindAddress: got %q, want %q", got.BindAddress, tt.wantBind)
			}
			if got.MaxLogSize != tt.wantMaxLog {
				t.Errorf("MaxLogSize: got %d, want %d", got.MaxLogSize, tt.wantMaxLog)
			}
			if !got.AutoRestart {
				t.Errorf("AutoRestart: got false, want true (always enabled)")
			}
		})
	}
}

// TestHandleExplicitStart_ListenPortAndTLSVerify verifies that an
// ExplicitStart event for a proxy with listen-port + skip-tls-verify
// plumbs both fields through to the created proxy server. Binds to a
// port requested by net.Listen("tcp", "127.0.0.1:0") so the test never
// collides with a real dev server on the host.
func TestHandleExplicitStart_ListenPortAndTLSVerify(t *testing.T) {
	t.Parallel()
	// Reserve a free ephemeral port for the test. We close the
	// listener immediately; there's a tiny race window before the
	// proxy binds to the same port, but it's acceptable for a unit
	// test since we own the host. A bind conflict here would
	// manifest as strict-port error, which is also a valid signal.
	reservedPort := reserveFreePort(t)

	daemon, tmpDir := newFallbackTestDaemon(t)

	proxyCfg := &config.ProxyConfig{
		URL:           "https://tdo-local.sbdev.io",
		ListenPort:    reservedPort,
		SkipTLSVerify: true,
		Bind:          "127.0.0.1",
	}

	proxyID := makeProcessID(tmpDir, "tdo")
	daemon.handleExplicitStart(ProxyEvent{
		Type:    ExplicitStart,
		ProxyID: proxyID,
		Config:  proxyCfg,
		Path:    tmpDir,
	})

	server, err := daemon.proxym.Get(proxyID)
	if err != nil {
		t.Fatalf("Expected proxy %q to exist: %v", proxyID, err)
	}

	// The proxy must have bound to the requested port. ListenAddr
	// format: "127.0.0.1:<port>". A silent drift to :0 would show
	// a different port here.
	wantAddr := fmt.Sprintf("127.0.0.1:%d", reservedPort)
	if server.ListenAddr != wantAddr {
		t.Errorf("ListenAddr: got %q, want %q (strict listen port must not drift)",
			server.ListenAddr, wantAddr)
	}

	// Target URL must be the HTTPS upstream, verbatim.
	if got := server.Stats().TargetURL; got != "https://tdo-local.sbdev.io" {
		t.Errorf("TargetURL: got %q, want %q", got, "https://tdo-local.sbdev.io")
	}

	// Cleanup: stop the proxy so the test doesn't leave a listener
	// hanging on the reserved port. The daemon.TearDown path in
	// newFallbackTestDaemon covers this, but an explicit stop keeps
	// the window short for parallel test runs.
	_ = daemon.proxym.Stop(context.Background(), proxyID)
}

// reserveFreePort asks the kernel for an unused TCP port and
// returns it. The listener is closed before returning; callers must
// accept the small race window.
// reserveFreePort returns a free port for the strict-listen-port proxy test.
// Routed through allocTestPort so the port is process-unique and outside the
// ephemeral range — the daemon can bind it without a parallel test's :0
// allocation having stolen it first.
func reserveFreePort(t *testing.T) int {
	t.Helper()
	return allocTestPort(t)
}

// TestAutostartProxy_ListenPortConflict_EmitsStartupError verifies
// that when .agnt.kdl declares `listen-port` on a proxy and that
// port is already held by an unmanaged process, autostartProxy
// emits a `proxy_listen_port_conflict` entry to startupErrorStore
// BEFORE handing off to the ExplicitStart handler. This preflight
// surfaces the owning process hint via get_errors so the AI agent
// doesn't have to correlate a terse runtime bind error back to the
// declared config.
func TestAutostartProxy_ListenPortConflict_EmitsStartupError(t *testing.T) {
	t.Parallel()
	daemon, tmpDir := newFallbackTestDaemon(t)

	// Hold a port we'll hand to autostartProxy as the declared
	// listen-port. We keep the blocker alive through the whole test
	// so the conflict is real.
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("blocker listen: %v", err)
	}
	defer blocker.Close()
	blockedPort := blocker.Addr().(*net.TCPAddr).Port

	proxyCfg := &config.ProxyConfig{
		URL:        "http://example.com",
		ListenPort: blockedPort,
	}

	// autostartProxy returns nil for non-fatal issues — the contract
	// is that startupErrorStore is the visible surface for conflicts,
	// not the return value. The function never kills processes or
	// reshapes the event queue, so the side effect we care about is
	// the stored entry.
	if err := daemon.autostartProxy(context.Background(), "tdo", proxyCfg, tmpDir); err != nil {
		t.Fatalf("autostartProxy returned unexpected error: %v", err)
	}

	// Give the ExplicitStart event a moment to drain — it will emit
	// its own proxy_creation_failed entry when Start() hits the
	// strict-listen-port path. We only care about the preflight
	// entry here; the strict path is tested elsewhere.
	time.Sleep(100 * time.Millisecond)

	if n := countStartupEntriesByEvent(daemon, "proxy_listen_port_conflict"); n != 1 {
		t.Errorf("expected 1 proxy_listen_port_conflict entry, got %d", n)
	}

	// The entry must carry the conflicting port so the UI can
	// surface it without parsing the message string.
	entries := daemon.startupErrorStore.Query(StartupLogFilter{})
	var found bool
	for _, e := range entries {
		if e.EventType == "proxy_listen_port_conflict" {
			if e.Port != blockedPort {
				t.Errorf("port mismatch: got %d, want %d", e.Port, blockedPort)
			}
			if !strings.Contains(e.Message, "listen-port") {
				t.Errorf("message should reference listen-port: %q", e.Message)
			}
			if !strings.Contains(e.Message, "tdo") {
				t.Errorf("message should reference proxy name: %q", e.Message)
			}
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no proxy_listen_port_conflict entry found in startupErrorStore")
	}
}

// TestHandleExplicitStart_PersistsToStateManager verifies that a successful
// ExplicitStart event persists the created proxy into the StateManager so
// autostart-driven proxies survive a daemon restart — matching the
// persistence behavior already implemented in the MCP tool path
// (`hubHandleProxyStart`). Before this was fixed the autostart path created
// the proxy in the runtime ProxyManager but never called stateMgr.AddProxy,
// so a daemon restart lost all config-driven proxies.
func TestHandleExplicitStart_PersistsToStateManager(t *testing.T) {
	t.Parallel()
	tmpDir := shortTempDir(t)
	sockPath := shortSockPath(t)
	statePath := filepath.Join(tmpDir, "daemon-state.json")

	daemon := NewForTest(t, DaemonConfig{
		SocketPath:             sockPath,
		MaxClients:             10,
		WriteTimeout:           5 * time.Second,
		EnableStatePersistence: true,
		StatePath:              statePath,
	})

	if daemon.stateMgr == nil {
		t.Fatalf("Expected stateMgr to be initialized when EnableStatePersistence=true")
	}

	// Sanity check — no persisted proxies before the call.
	if _, ok := daemon.stateMgr.GetProxy("precondition"); ok {
		t.Fatalf("precondition: unexpected proxy in state")
	}

	proxyID := makeProcessID(tmpDir, "standalone")
	proxyCfg := &config.ProxyConfig{
		URL:        "http://127.0.0.1:65001",
		MaxLogSize: 321,
	}

	daemon.handleExplicitStart(ProxyEvent{
		Type:    ExplicitStart,
		ProxyID: proxyID,
		Config:  proxyCfg,
		Path:    tmpDir,
	})

	// The proxy must exist in the runtime manager.
	if _, err := daemon.proxym.Get(proxyID); err != nil {
		t.Fatalf("Expected proxy %q to exist in ProxyManager: %v", proxyID, err)
	}

	// And it must be persisted to the StateManager.
	persisted, ok := daemon.stateMgr.GetProxy(proxyID)
	if !ok {
		t.Fatalf("Expected proxy %q to be persisted to StateManager, but it was not", proxyID)
	}
	if persisted.ID != proxyID {
		t.Errorf("persisted.ID: got %q, want %q", persisted.ID, proxyID)
	}
	if persisted.TargetURL != "http://127.0.0.1:65001" {
		t.Errorf("persisted.TargetURL: got %q, want %q", persisted.TargetURL, "http://127.0.0.1:65001")
	}
	if persisted.Path != tmpDir {
		t.Errorf("persisted.Path: got %q, want %q", persisted.Path, tmpDir)
	}
	if persisted.MaxLogSize != 321 {
		t.Errorf("persisted.MaxLogSize: got %d, want %d", persisted.MaxLogSize, 321)
	}
}

// TestHandleExplicitStart_NoStateManager_DoesNotPanic verifies the handler
// tolerates daemons that were built without state persistence — the
// stateMgr nil-guard must prevent a panic and the runtime proxy creation
// still succeeds. Protects the minimal-daemon test paths that don't enable
// persistence.
func TestHandleExplicitStart_NoStateManager_DoesNotPanic(t *testing.T) {
	t.Parallel()
	daemon, tmpDir := newFallbackTestDaemon(t)

	// newFallbackTestDaemon does not enable state persistence.
	if daemon.stateMgr != nil {
		t.Fatalf("precondition: expected stateMgr to be nil for this test harness")
	}

	proxyID := makeProcessID(tmpDir, "no-state")
	daemon.handleExplicitStart(ProxyEvent{
		Type:    ExplicitStart,
		ProxyID: proxyID,
		Config:  &config.ProxyConfig{URL: "http://127.0.0.1:65002"},
		Path:    tmpDir,
	})

	// Runtime proxy creation must still succeed.
	if _, err := daemon.proxym.Get(proxyID); err != nil {
		t.Fatalf("Expected proxy %q to exist even without stateMgr: %v", proxyID, err)
	}
}

// TestAutostartProxy_NoListenPort_NoPreflightError verifies that
// proxies without `listen-port` (or with zero) skip the preflight
// entirely — no spurious conflict entries in the hash-based
// allocator path. Regression guard for the pre-flight gate.
func TestAutostartProxy_NoListenPort_NoPreflightError(t *testing.T) {
	t.Parallel()
	daemon, tmpDir := newFallbackTestDaemon(t)

	proxyCfg := &config.ProxyConfig{
		URL: "http://example.com",
		// ListenPort intentionally zero
	}

	if err := daemon.autostartProxy(context.Background(), "dev", proxyCfg, tmpDir); err != nil {
		t.Fatalf("autostartProxy: %v", err)
	}

	if n := countStartupEntriesByEvent(daemon, "proxy_listen_port_conflict"); n != 0 {
		t.Errorf("expected 0 proxy_listen_port_conflict entries for zero listen-port, got %d", n)
	}
}

// TestHandleExplicitStart_RegistersScriptEntry verifies that an
// ExplicitStart event adds a proxy-kind entry to the admin surface so
// the overlay status bar and SCRIPT LIST can render an indicator for
// the proxy. Without this shim explicit proxies are invisible to the
// admin surface — they only appear in PROXY LIST, which the status bar
// does not consume. Iteration-2 / T2 regression guard.
func TestHandleExplicitStart_RegistersScriptEntry(t *testing.T) {
	t.Parallel()
	daemon, tmpDir := newFallbackTestDaemon(t)

	proxyID := makeProcessID(tmpDir, "standalone")
	daemon.handleExplicitStart(ProxyEvent{
		Type:    ExplicitStart,
		ProxyID: proxyID,
		Config:  &config.ProxyConfig{URL: "http://127.0.0.1:65010"},
		Path:    tmpDir,
	})

	if _, err := daemon.proxym.Get(proxyID); err != nil {
		t.Fatalf("precondition: expected proxy %q to exist in ProxyManager: %v", proxyID, err)
	}

	entries := daemon.proxyEntries.List(tmpDir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 proxy entry for %s, got %d", tmpDir, len(entries))
	}
	entry := entries[0]
	if entry.Name() != "standalone" {
		t.Errorf("entry.Name: got %q, want %q", entry.Name(), "standalone")
	}
	if entry.ProxyID() != proxyID {
		t.Errorf("entry.ProxyID: got %q, want %q", entry.ProxyID(), proxyID)
	}
	if entry.ProjectPath() != tmpDir {
		t.Errorf("entry.ProjectPath: got %q, want %q", entry.ProjectPath(), tmpDir)
	}

	summary := proxyEntryToSummary(entry)
	if summary["kind"] != string(ScriptKindProxy) {
		t.Errorf("summary.kind: got %v, want %q", summary["kind"], ScriptKindProxy)
	}
	if summary["name"] != "standalone" {
		t.Errorf("summary.name: got %v, want %q", summary["name"], "standalone")
	}
	if summary["process_id"] != proxyID {
		t.Errorf("summary.process_id: got %v, want %q", summary["process_id"], proxyID)
	}
}

// TestHandleExplicitStart_ScriptLinkedProxy_DoesNotDuplicate verifies
// that registering a script.Entry under the same name as a later
// ExplicitStart call prevents a duplicate proxy-kind row. Covers the
// autostartScript-then-URLDetected ordering case when a refactor
// routes URL-detected proxies through the explicit path.
func TestHandleExplicitStart_ScriptLinkedProxy_DoesNotDuplicate(t *testing.T) {
	t.Parallel()
	daemon, tmpDir := newFallbackTestDaemon(t)

	// Pretend autostartScript registered a process-kind entry first.
	if _, err := daemon.scriptRegistry.Register("linked", tmpDir, scriptConfigToEntry(&config.ScriptConfig{Run: "true"})); err != nil {
		t.Fatalf("scriptRegistry.Register: %v", err)
	}

	proxyID := makeProcessID(tmpDir, "linked")
	daemon.handleExplicitStart(ProxyEvent{
		Type:    ExplicitStart,
		ProxyID: proxyID,
		Config:  &config.ProxyConfig{URL: "http://127.0.0.1:65011"},
		Path:    tmpDir,
	})

	// The proxy is created in the runtime, but no parallel proxy-kind
	// admin entry is added — the script.Entry owns the admin row.
	if _, err := daemon.proxym.Get(proxyID); err != nil {
		t.Fatalf("expected proxy to exist: %v", err)
	}
	if entries := daemon.proxyEntries.List(tmpDir); len(entries) != 0 {
		t.Errorf("expected 0 proxy-kind entries when script.Entry exists, got %d", len(entries))
	}
}

// TestHandleExplicitStart_RegisterIsIdempotent verifies that two
// successive ExplicitStart events with the same proxy ID do not create
// two proxy-kind admin entries. The second Register is short-circuited
// by proxym.Get returning success, but guard the shim anyway — a
// future refactor that allows re-registration upstream must not
// accidentally create a duplicate admin row.
func TestHandleExplicitStart_RegisterIsIdempotent(t *testing.T) {
	t.Parallel()
	daemon, tmpDir := newFallbackTestDaemon(t)

	// First call goes through the full handler and registers the entry.
	proxyID := makeProcessID(tmpDir, "dup")
	daemon.handleExplicitStart(ProxyEvent{
		Type:    ExplicitStart,
		ProxyID: proxyID,
		Config:  &config.ProxyConfig{URL: "http://127.0.0.1:65012"},
		Path:    tmpDir,
	})

	// Direct shim calls with the same ID must be no-ops. The first
	// handler call above already registered the entry, so both of
	// these return false and don't add a second row.
	if got := daemon.registerExplicitProxyEntry(tmpDir, proxyID, ""); got {
		t.Errorf("second register should be a no-op, got registered=true")
	}
	if got := daemon.registerExplicitProxyEntry(tmpDir, proxyID, ""); got {
		t.Errorf("third register should be a no-op, got registered=true")
	}

	if entries := daemon.proxyEntries.List(tmpDir); len(entries) != 1 {
		t.Errorf("expected exactly 1 proxy-kind entry after duplicate registers, got %d", len(entries))
	}
}

// TestScheduleFallbackPortChecks_SurvivesAutostartContextCancel is the
// regression test for the fallback-port goroutine being bound to the autostart
// context. The autostart context is cancelled the instant autostart returns
// (seconds), well before the fallback delay — so a goroutine that selected on
// it died immediately and the FallbackPortCheck event was never emitted, and
// a script-linked proxy whose URL detection also failed was never created.
//
// A hand-built Daemon is used (not the full Start/bootstrap path) so the
// daemon's own handleProxyEvents goroutine is not running to consume the event
// before this test can observe it.
func TestScheduleFallbackPortChecks_SurvivesAutostartContextCancel(t *testing.T) {
	t.Parallel()

	d := &Daemon{
		ctx:                context.Background(),
		proxyEvents:        make(chan ProxyEvent, 4),
		fallbackCheckDelay: 10 * time.Millisecond,
	}

	cfg := &config.AgntConfig{
		Proxies: map[string]*config.ProxyConfig{
			"dev": {Script: "frontend", FallbackPort: 5173},
		},
	}

	// Autostart context already cancelled — mimics autostart having returned.
	autostartCtx, cancel := context.WithCancel(context.Background())
	cancel()

	d.scheduleFallbackPortChecks(autostartCtx, cfg, "/tmp/proj", nil)

	select {
	case ev := <-d.proxyEvents:
		if ev.Type != FallbackPortCheck {
			t.Fatalf("expected FallbackPortCheck, got %v", ev.Type)
		}
		if ev.ProxyName != "dev" {
			t.Fatalf("expected proxy name %q, got %q", "dev", ev.ProxyName)
		}
		if ev.Config == nil || ev.Config.FallbackPort != 5173 {
			t.Fatalf("expected fallback-port 5173 in event config, got %+v", ev.Config)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("FallbackPortCheck never emitted — autostart ctx cancellation killed the fallback goroutine")
	}
}

// TestProxyEvent_HandleScriptStopped_IgnoresAlreadyStoppedProxy verifies that
// when a script-linked proxy was already removed from the manager (e.g. by
// CleanupSessionResources during a session restart) but is still in the
// scriptProxies index, the ScriptStopped handler treats the "proxy not found"
// stop result as idempotent success — no proxy_stop_failed warning is surfaced
// to the agent. Regression for the bifrost restart warning:
//
//	⚠ [proxy_stop_failed] failed to stop proxy ...:dev:localhost-5173
//	  on script stop: proxy not found
func TestProxyEvent_HandleScriptStopped_IgnoresAlreadyStoppedProxy(t *testing.T) {
	t.Parallel()
	daemon, tmpDir := newFallbackTestDaemon(t)

	scriptID := makeProcessID(tmpDir, "dev-frontend")
	// A proxy id that is NOT registered in the manager (already torn down).
	ghostID := makeProcessID(tmpDir, "dev") + ":localhost-5173"
	daemon.trackScriptProxy(scriptID, ghostID)

	daemon.handleScriptStopped(ProxyEvent{Type: ScriptStopped, ScriptID: scriptID})

	if n := countStartupEntriesByEvent(daemon, "proxy_stop_failed"); n != 0 {
		t.Fatalf("expected 0 proxy_stop_failed entries for an already-stopped proxy, got %d", n)
	}
	if tracked := daemon.getProxiesForScript(scriptID); len(tracked) != 0 {
		t.Errorf("expected script proxy tracking cleared after stop, got %v", tracked)
	}
}
