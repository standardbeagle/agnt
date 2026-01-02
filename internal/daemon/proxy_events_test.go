package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/proxy"
)

func TestMakeProxyIDFromURL(t *testing.T) {
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
			wantPattern: `^my-project-[0-9a-f]{4}:dev:localhost-3000$`,
		},
		{
			name:        "IP address URL",
			projectPath: "/home/user/project",
			proxyName:   "api",
			urlStr:      "http://127.0.0.1:8080",
			wantPattern: `^project-[0-9a-f]{4}:api:127-0-0-1-8080$`,
		},
		{
			name:        "URL with default HTTP port",
			projectPath: "/home/user/app",
			proxyName:   "web",
			urlStr:      "http://localhost",
			wantPattern: `^app-[0-9a-f]{4}:web:localhost-80$`,
		},
		{
			name:        "HTTPS URL",
			projectPath: "/home/user/secure",
			proxyName:   "ssl",
			urlStr:      "https://localhost",
			wantPattern: `^secure-[0-9a-f]{4}:ssl:localhost-443$`,
		},
		{
			name:        "URL with explicit port",
			projectPath: "/tmp/test",
			proxyName:   "srv",
			urlStr:      "http://192.168.1.1:9000",
			wantPattern: `^test-[0-9a-f]{4}:srv:192-168-1-1-9000$`,
		},
		{
			name:        "invalid URL falls back to simple ID",
			projectPath: "/home/user/project",
			proxyName:   "test",
			urlStr:      "://invalid",
			wantPattern: `^project-[0-9a-f]{4}:test$`,
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
			wantPattern: `^my-project-[0-9a-f]{4}:dev$`,
		},
		{
			name:        "nested path",
			projectPath: "/home/user/work/apps/frontend",
			scriptName:  "start",
			wantPattern: `^frontend-[0-9a-f]{4}:start$`,
		},
		{
			name:        "empty project path returns name only",
			projectPath: "",
			scriptName:  "build",
			wantPattern: `^build$`,
		},
		{
			name:        "root path",
			projectPath: "/",
			scriptName:  "test",
			wantPattern: `^/-[0-9a-f]{4}:test$`,
		},
		{
			name:        "trailing slash handled",
			projectPath: "/home/user/project/",
			scriptName:  "lint",
			wantPattern: `^project-[0-9a-f]{4}:lint$`,
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
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	daemon := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	if err := daemon.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		daemon.Stop(ctx)
	}()

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
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	daemon := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	if err := daemon.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		daemon.Stop(ctx)
	}()

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
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	daemon := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	if err := daemon.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		daemon.Stop(ctx)
	}()

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
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	daemon := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	if err := daemon.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		daemon.Stop(ctx)
	}()

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
