package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseAgntConfigFormats tests all supported standard KDL format combinations
func TestParseAgntConfigFormats(t *testing.T) {
	tests := []struct {
		name            string
		input           string
		expectScripts   map[string]ScriptConfig
		expectProxies   map[string]ProxyConfig
		expectAutostart int
		expectError     bool
	}{
		{
			name:            "empty config",
			input:           "",
			expectScripts:   map[string]ScriptConfig{},
			expectProxies:   map[string]ProxyConfig{},
			expectAutostart: 0,
		},
		{
			name:            "comments only",
			input:           "// This is a comment\n// Another comment\n",
			expectScripts:   map[string]ScriptConfig{},
			expectProxies:   map[string]ProxyConfig{},
			expectAutostart: 0,
		},
		{
			name: "single script with run",
			input: `scripts {
    dev {
        run "npm run dev"
    }
}`,
			expectScripts: map[string]ScriptConfig{
				"dev": {Run: "npm run dev"},
			},
			expectAutostart: 0,
		},
		{
			name: "script with autostart true",
			input: `scripts {
    dev {
        run "npm run dev"
        autostart true
    }
}`,
			expectScripts: map[string]ScriptConfig{
				"dev": {Run: "npm run dev", Autostart: true},
			},
			expectAutostart: 1,
		},
		{
			name: "script with autostart false",
			input: `scripts {
    build {
        run "npm run build"
        autostart false
    }
}`,
			expectScripts: map[string]ScriptConfig{
				"build": {Run: "npm run build", Autostart: false},
			},
			expectAutostart: 0,
		},
		{
			name: "script with cwd",
			input: `scripts {
    frontend {
        run "npm run dev"
        cwd "packages/frontend"
        autostart true
    }
}`,
			expectScripts: map[string]ScriptConfig{
				"frontend": {Run: "npm run dev", Cwd: "packages/frontend", Autostart: true},
			},
			expectAutostart: 1,
		},
		{
			name: "script with url-matchers",
			input: `scripts {
    dev {
        run "npm run dev"
        url-matchers "(Local|Network):\\s*{url}"
        autostart true
    }
}`,
			expectScripts: map[string]ScriptConfig{
				"dev": {Run: "npm run dev", URLMatchers: []string{`(Local|Network):\s*{url}`}, Autostart: true},
			},
			expectAutostart: 1,
		},
		{
			name: "script with command instead of run",
			input: `scripts {
    dev {
        command "npm"
        autostart true
    }
}`,
			expectScripts: map[string]ScriptConfig{
				"dev": {Command: "npm", Autostart: true},
			},
			expectAutostart: 1,
		},
		{
			name: "multiple scripts",
			input: `scripts {
    dev {
        run "npm run dev"
        autostart true
    }
    build {
        run "npm run build"
    }
    test {
        run "npm test"
        autostart true
    }
}`,
			expectScripts: map[string]ScriptConfig{
				"dev":   {Run: "npm run dev", Autostart: true},
				"build": {Run: "npm run build", Autostart: false},
				"test":  {Run: "npm test", Autostart: true},
			},
			expectAutostart: 2,
		},
		{
			name: "proxy with script link",
			input: `proxies {
    dev {
        script "dev-script"
    }
}`,
			expectProxies: map[string]ProxyConfig{
				"dev": {Script: "dev-script"},
			},
		},
		{
			name: "proxy with target",
			input: `proxies {
    api {
        target "http://localhost:8080"
        autostart true
    }
}`,
			expectProxies: map[string]ProxyConfig{
				"api": {Target: "http://localhost:8080", Autostart: true},
			},
		},
		{
			name: "proxy with port",
			input: `proxies {
    frontend {
        port 3000
        autostart true
    }
}`,
			expectProxies: map[string]ProxyConfig{
				"frontend": {Port: 3000, Autostart: true},
			},
		},
		{
			name: "proxy with fallback-port",
			input: `proxies {
    dev {
        script "dev"
        fallback-port 3000
    }
}`,
			expectProxies: map[string]ProxyConfig{
				"dev": {Script: "dev", FallbackPort: 3000},
			},
		},
		{
			name: "proxy with bind address",
			input: `proxies {
    mobile {
        target "http://localhost:3000"
        bind "0.0.0.0"
        autostart true
    }
}`,
			expectProxies: map[string]ProxyConfig{
				"mobile": {Target: "http://localhost:3000", Bind: "0.0.0.0", Autostart: true},
			},
		},
		{
			name: "proxy with max-log-size",
			input: `proxies {
    verbose {
        target "http://localhost:3000"
        max-log-size 5000
    }
}`,
			expectProxies: map[string]ProxyConfig{
				"verbose": {Target: "http://localhost:3000", MaxLogSize: 5000},
			},
		},
		{
			name: "proxy with websocket",
			input: `proxies {
    ws {
        target "http://localhost:3000"
        websocket true
    }
}`,
			expectProxies: map[string]ProxyConfig{
				"ws": {Target: "http://localhost:3000", Websocket: true},
			},
		},
		{
			name: "multiple proxies",
			input: `proxies {
    frontend {
        script "dev"
        fallback-port 3000
    }
    backend {
        target "http://localhost:8080"
        autostart true
    }
}`,
			expectProxies: map[string]ProxyConfig{
				"frontend": {Script: "dev", FallbackPort: 3000},
				"backend":  {Target: "http://localhost:8080", Autostart: true},
			},
		},
		{
			name: "project block",
			input: `project {
    type "wails"
    name "my-app"
}
scripts {
    dev {
        run "wails dev"
        autostart true
    }
}`,
			expectScripts: map[string]ScriptConfig{
				"dev": {Run: "wails dev", Autostart: true},
			},
			expectAutostart: 1,
		},
		{
			name: "scripts and proxies together",
			input: `scripts {
    dev {
        run "npm run dev"
        autostart true
    }
    build {
        run "npm run build"
    }
}
proxies {
    dev {
        script "dev"
        fallback-port 3000
    }
}`,
			expectScripts: map[string]ScriptConfig{
				"dev":   {Run: "npm run dev", Autostart: true},
				"build": {Run: "npm run build", Autostart: false},
			},
			expectProxies: map[string]ProxyConfig{
				"dev": {Script: "dev", FallbackPort: 3000},
			},
			expectAutostart: 1,
		},
		{
			name: "full config with all sections",
			input: `project {
    type "node"
    name "my-project"
}

scripts {
    dev {
        run "npm run dev"
        cwd "frontend"
        autostart true
    }
    api {
        run "go run ./cmd/server"
        autostart true
    }
    build {
        run "npm run build"
    }
}

proxies {
    frontend {
        script "dev"
        fallback-port 3000
    }
    backend {
        target "http://localhost:8080"
        bind "0.0.0.0"
        autostart true
    }
}
`,
			expectScripts: map[string]ScriptConfig{
				"dev":   {Run: "npm run dev", Cwd: "frontend", Autostart: true},
				"api":   {Run: "go run ./cmd/server", Autostart: true},
				"build": {Run: "npm run build", Autostart: false},
			},
			expectProxies: map[string]ProxyConfig{
				"frontend": {Script: "dev", FallbackPort: 3000},
				"backend":  {Target: "http://localhost:8080", Bind: "0.0.0.0", Autostart: true},
			},
			expectAutostart: 2,
		},
		{
			name: "wails project config",
			input: `project {
    type "wails"
    name "beagle-term"
}

scripts {
    frontend-dev {
        run "npm run dev"
        cwd "frontend"
    }
    wails-dev {
        run "wails dev"
        autostart true
        url-matchers "Using DevServer URL:\\s*{url}"
    }
    build {
        run "wails build"
    }
}

proxies {
    wails-dev {
        script "wails-dev"
        fallback-port 34115
        websocket true
    }
}
`,
			expectScripts: map[string]ScriptConfig{
				"frontend-dev": {Run: "npm run dev", Cwd: "frontend", Autostart: false},
				"wails-dev":    {Run: "wails dev", Autostart: true, URLMatchers: []string{`Using DevServer URL:\s*{url}`}},
				"build":        {Run: "wails build", Autostart: false},
			},
			expectProxies: map[string]ProxyConfig{
				"wails-dev": {Script: "wails-dev", FallbackPort: 34115, Websocket: true},
			},
			expectAutostart: 1,
		},
		// Non-standard format tests - should fail
		{
			name: "non-standard proxy format rejected",
			input: `proxy "dev" {
    script "dev"
}`,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseAgntConfig(tt.input)

			if tt.expectError {
				assert.Error(t, err, "expected error for non-standard format")
				return
			}

			require.NoError(t, err)
			require.NotNil(t, cfg)

			// Check scripts count
			assert.Len(t, cfg.Scripts, len(tt.expectScripts), "script count mismatch")

			// Check each expected script
			for name, expected := range tt.expectScripts {
				actual, ok := cfg.Scripts[name]
				assert.True(t, ok, "missing script: %s", name)
				if !ok {
					continue
				}

				if expected.Run != "" {
					assert.Equal(t, expected.Run, actual.Run, "script %s: Run mismatch", name)
				}
				if expected.Command != "" {
					assert.Equal(t, expected.Command, actual.Command, "script %s: Command mismatch", name)
				}
				if expected.Cwd != "" {
					assert.Equal(t, expected.Cwd, actual.Cwd, "script %s: Cwd mismatch", name)
				}
				if len(expected.URLMatchers) > 0 {
					assert.Equal(t, expected.URLMatchers, actual.URLMatchers, "script %s: URLMatchers mismatch", name)
				}
				assert.Equal(t, expected.Autostart, actual.Autostart, "script %s: Autostart mismatch", name)
			}

			// Check proxies count
			assert.Len(t, cfg.Proxies, len(tt.expectProxies), "proxy count mismatch")

			// Check each expected proxy
			for name, expected := range tt.expectProxies {
				actual, ok := cfg.Proxies[name]
				assert.True(t, ok, "missing proxy: %s", name)
				if !ok {
					continue
				}

				if expected.Target != "" {
					assert.Equal(t, expected.Target, actual.Target, "proxy %s: Target mismatch", name)
				}
				if expected.Script != "" {
					assert.Equal(t, expected.Script, actual.Script, "proxy %s: Script mismatch", name)
				}
				if expected.Port != 0 {
					assert.Equal(t, expected.Port, actual.Port, "proxy %s: Port mismatch", name)
				}
				if expected.FallbackPort != 0 {
					assert.Equal(t, expected.FallbackPort, actual.FallbackPort, "proxy %s: FallbackPort mismatch", name)
				}
				if expected.Bind != "" {
					assert.Equal(t, expected.Bind, actual.Bind, "proxy %s: Bind mismatch", name)
				}
				if expected.MaxLogSize != 0 {
					assert.Equal(t, expected.MaxLogSize, actual.MaxLogSize, "proxy %s: MaxLogSize mismatch", name)
				}
				assert.Equal(t, expected.Autostart, actual.Autostart, "proxy %s: Autostart mismatch", name)
				assert.Equal(t, expected.Websocket, actual.Websocket, "proxy %s: Websocket mismatch", name)
			}

			// Check autostart scripts count
			autostartScripts := cfg.GetAutostartScripts()
			assert.Len(t, autostartScripts, tt.expectAutostart, "autostart script count mismatch")
		})
	}
}

func TestLoadAgntConfig(t *testing.T) {
	// Create temp directory with .agnt.kdl
	tmpDir := t.TempDir()

	configContent := `scripts {
    dev {
        run "npm run dev"
        autostart true
    }
    test {
        run "npm test"
    }
}

proxies {
    dev {
        script "dev"
        fallback-port 3000
    }
}
`
	configPath := filepath.Join(tmpDir, AgntConfigFileName)
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	// Test loading from the directory
	cfg, err := LoadAgntConfig(tmpDir)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify scripts loaded
	assert.Len(t, cfg.Scripts, 2)
	dev, ok := cfg.Scripts["dev"]
	assert.True(t, ok)
	if ok {
		assert.Equal(t, "npm run dev", dev.Run)
		assert.True(t, dev.Autostart)
	}

	// Verify proxies loaded
	assert.Len(t, cfg.Proxies, 1)
	proxy, ok := cfg.Proxies["dev"]
	assert.True(t, ok)
	if ok {
		assert.Equal(t, "dev", proxy.Script)
		assert.Equal(t, 3000, proxy.FallbackPort)
	}

	// Verify GetAutostartScripts
	autostartScripts := cfg.GetAutostartScripts()
	assert.Len(t, autostartScripts, 1)
	_, ok = autostartScripts["dev"]
	assert.True(t, ok)
}

func TestFindAgntConfigFile(t *testing.T) {
	// Create temp directory with nested subdirectory
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "src", "components")
	err := os.MkdirAll(subDir, 0755)
	require.NoError(t, err)

	// Create .agnt.kdl in root
	configContent := `scripts {
    dev {
        autostart true
    }
}`
	configPath := filepath.Join(tmpDir, AgntConfigFileName)
	err = os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	// cwd-only: a subdirectory without its own config does NOT inherit the
	// parent's config (walk-up was removed — config scope is the cwd only).
	found := FindAgntConfigFile(subDir)
	assert.Equal(t, "", found, "must not walk up to parent .agnt.kdl")

	// The directory that actually holds the config finds it directly.
	found = FindAgntConfigFile(tmpDir)
	assert.Equal(t, configPath, found)

	// Non-existent directory returns empty.
	found = FindAgntConfigFile("/nonexistent/path")
	assert.Equal(t, "", found)
}

func TestResolveConfigPath(t *testing.T) {
	tmpDir := t.TempDir()
	cwdConfig := filepath.Join(tmpDir, AgntConfigFileName)
	require.NoError(t, os.WriteFile(cwdConfig, []byte("scripts {}"), 0644))

	overrideDir := t.TempDir()
	overrideConfig := filepath.Join(overrideDir, "custom.kdl")
	require.NoError(t, os.WriteFile(overrideConfig, []byte("scripts {}"), 0644))

	// No override: resolves to <dir>/.agnt.kdl when present.
	got, err := ResolveConfigPath(tmpDir, "")
	require.NoError(t, err)
	assert.Equal(t, cwdConfig, got)

	// No override, no cwd config: empty, no error.
	emptyDir := t.TempDir()
	got, err = ResolveConfigPath(emptyDir, "")
	require.NoError(t, err)
	assert.Equal(t, "", got)

	// Override present: used verbatim (absolute), regardless of cwd config.
	got, err = ResolveConfigPath(tmpDir, overrideConfig)
	require.NoError(t, err)
	assert.Equal(t, overrideConfig, got)

	// Override that points at the cwd config from a different dir still works.
	got, err = ResolveConfigPath(emptyDir, overrideConfig)
	require.NoError(t, err)
	assert.Equal(t, overrideConfig, got)

	// Missing override is a loud error — the user named a file they expect.
	_, err = ResolveConfigPath(tmpDir, filepath.Join(overrideDir, "nope.kdl"))
	require.Error(t, err)
}

func TestHasExplicitTarget(t *testing.T) {
	tests := []struct {
		name   string
		proxy  ProxyConfig
		expect bool
	}{
		{"URL set", ProxyConfig{URL: "http://localhost:3000"}, true},
		{"Target set", ProxyConfig{Target: "http://localhost:8080"}, true},
		{"Port set", ProxyConfig{Port: 3000}, true},
		{"Script only", ProxyConfig{Script: "dev"}, false},
		{"Script with fallback port", ProxyConfig{Script: "dev", FallbackPort: 3000}, false},
		{"Empty config", ProxyConfig{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expect, tt.proxy.HasExplicitTarget())
		})
	}
}

func TestShouldAutostart(t *testing.T) {
	tests := []struct {
		name   string
		proxy  ProxyConfig
		expect bool
	}{
		{"Autostart flag", ProxyConfig{Autostart: true, Script: "dev"}, true},
		{"Explicit target without script", ProxyConfig{Target: "http://localhost:3000"}, true},
		{"Explicit URL without script", ProxyConfig{URL: "http://localhost:3000"}, true},
		{"Explicit port without script", ProxyConfig{Port: 3000}, true},
		{"Script-linked with target", ProxyConfig{Script: "dev", Port: 3000}, false},
		{"Script-linked no target", ProxyConfig{Script: "dev"}, false},
		{"Empty config", ProxyConfig{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expect, tt.proxy.ShouldAutostart())
		})
	}
}

func TestGetAutostartMethods(t *testing.T) {
	cfg := &AgntConfig{
		Scripts: map[string]*ScriptConfig{
			"dev":   {Run: "npm run dev", Autostart: true},
			"api":   {Run: "go run ./cmd/server", Autostart: true},
			"build": {Run: "npm run build", Autostart: false},
			"test":  {Run: "go test ./...", Autostart: false},
		},
		Proxies: map[string]*ProxyConfig{
			"frontend": {Target: "http://localhost:3000", Autostart: true},
			"backend":  {Target: "http://localhost:8080", Autostart: true},
			"manual":   {Target: "http://localhost:9000", Autostart: false},
		},
	}

	// Test GetAutostartScripts
	autostartScripts := cfg.GetAutostartScripts()
	assert.Len(t, autostartScripts, 2)
	assert.Contains(t, autostartScripts, "dev")
	assert.Contains(t, autostartScripts, "api")
	assert.NotContains(t, autostartScripts, "build")
	assert.NotContains(t, autostartScripts, "test")

	// Test GetAutostartProxies - now includes explicit-target proxies
	autostartProxies := cfg.GetAutostartProxies()
	assert.Len(t, autostartProxies, 3, "explicit-target proxy 'manual' should auto-start")
	assert.Contains(t, autostartProxies, "frontend")
	assert.Contains(t, autostartProxies, "backend")
	assert.Contains(t, autostartProxies, "manual")
}

func TestScriptLifecycleHooks_Parse(t *testing.T) {
	kdl := `
scripts {
    backend {
        command "pwsh"
        hooks {
            on-start  "scripts/on-start.ps1"
            on-stop   "scripts/on-stop.ps1"
            on-crash  "scripts/on-crash.ps1"
            on-restart "scripts/on-restart.ps1"
        }
    }
}`
	cfg, err := ParseAgntConfig(kdl)
	require.NoError(t, err)
	h := cfg.Scripts["backend"].Hooks
	require.NotNil(t, h)
	assert.Equal(t, "scripts/on-start.ps1", h.OnStart)
	assert.Equal(t, "scripts/on-stop.ps1", h.OnStop)
	assert.Equal(t, "scripts/on-crash.ps1", h.OnCrash)
	assert.Equal(t, "scripts/on-restart.ps1", h.OnRestart)
}

func TestScriptLifecycleHooks_Nil_WhenAbsent(t *testing.T) {
	cfg, err := ParseAgntConfig("scripts {\n    backend {\n        command \"node\"\n    }\n}")
	require.NoError(t, err)
	assert.Nil(t, cfg.Scripts["backend"].Hooks)
}

func TestGetAutostartProxiesExplicitTarget(t *testing.T) {
	cfg := &AgntConfig{
		Scripts: map[string]*ScriptConfig{},
		Proxies: map[string]*ProxyConfig{
			// Explicit target without autostart flag - should auto-start
			"dev":       {Target: "http://localhost:3847"},
			"with-url":  {URL: "http://localhost:3000"},
			"with-port": {Port: 8080},
			// Script-linked without explicit target - should NOT auto-start
			"script-only": {Script: "dev", FallbackPort: 3000},
			// Script-linked with explicit target - should NOT auto-start (script handles it)
			"script-with-port": {Script: "dev", Port: 3000},
			// Explicit autostart false with target - should NOT auto-start
			// Note: Autostart:false is zero value, same as unset for bool.
			// Since we can't distinguish, explicit-target always auto-starts.
		},
	}

	autostartProxies := cfg.GetAutostartProxies()
	assert.Contains(t, autostartProxies, "dev", "explicit target should auto-start")
	assert.Contains(t, autostartProxies, "with-url", "explicit URL should auto-start")
	assert.Contains(t, autostartProxies, "with-port", "explicit port should auto-start")
	assert.NotContains(t, autostartProxies, "script-only", "script-only should not auto-start")
	assert.NotContains(t, autostartProxies, "script-with-port", "script-linked with port should not auto-start")
}

// TestParseProxyListenPortAndSkipTLSVerify covers the two new
// .agnt.kdl proxy fields. Behavior pinned:
//
//   - `listen-port` is parsed as an int, round-trips through
//     ParseAgntConfig, and is reachable via cfg.Proxies[name].
//   - Omitting `listen-port` leaves the field zero (caller treats
//     that as "use the hash-based allocator").
//   - `skip-tls-verify` is parsed as a bool, defaults to false when
//     absent, and flips to true when declared.
//   - Both fields coexist with existing fields (url, bind, autostart,
//     max-log-size, etc.) without interfering.
//   - A proxy with all three new-style fields (url + listen-port +
//     skip-tls-verify) still enters GetAutostartProxies via the
//     explicit-target auto-start path.
func TestParseProxyListenPortAndSkipTLSVerify(t *testing.T) {
	tests := []struct {
		name              string
		input             string
		wantProxyName     string
		wantListenPort    int
		wantSkipTLSVerify bool
		wantURL           string
		wantBind          string
		wantAutostart     bool
	}{
		{
			name: "listen-port only",
			input: `proxies {
    dev {
        url "http://localhost:3000"
        listen-port 4444
    }
}`,
			wantProxyName:  "dev",
			wantListenPort: 4444,
			wantURL:        "http://localhost:3000",
		},
		{
			name: "skip-tls-verify only",
			input: `proxies {
    dev {
        url "https://self-signed.local"
        skip-tls-verify true
    }
}`,
			wantProxyName:     "dev",
			wantURL:           "https://self-signed.local",
			wantSkipTLSVerify: true,
		},
		{
			name: "both listen-port and skip-tls-verify with bind and autostart",
			input: `proxies {
    tdo {
        url "https://tdo-local.sbdev.io"
        listen-port 4444
        skip-tls-verify true
        bind "127.0.0.1"
        autostart true
    }
}`,
			wantProxyName:     "tdo",
			wantURL:           "https://tdo-local.sbdev.io",
			wantListenPort:    4444,
			wantSkipTLSVerify: true,
			wantBind:          "127.0.0.1",
			wantAutostart:     true,
		},
		{
			name: "both fields omitted keeps defaults",
			input: `proxies {
    dev {
        url "http://localhost:3000"
    }
}`,
			wantProxyName:     "dev",
			wantURL:           "http://localhost:3000",
			wantListenPort:    0,
			wantSkipTLSVerify: false,
		},
		{
			name: "skip-tls-verify false is explicit default",
			input: `proxies {
    dev {
        url "https://api.local"
        skip-tls-verify false
    }
}`,
			wantProxyName:     "dev",
			wantURL:           "https://api.local",
			wantSkipTLSVerify: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseAgntConfig(tt.input)
			require.NoError(t, err)
			require.NotNil(t, cfg)

			p, ok := cfg.Proxies[tt.wantProxyName]
			require.True(t, ok, "proxy %q not found", tt.wantProxyName)
			assert.Equal(t, tt.wantListenPort, p.ListenPort, "ListenPort mismatch")
			assert.Equal(t, tt.wantSkipTLSVerify, p.SkipTLSVerify, "SkipTLSVerify mismatch")
			assert.Equal(t, tt.wantURL, p.URL, "URL mismatch")
			if tt.wantBind != "" {
				assert.Equal(t, tt.wantBind, p.Bind, "Bind mismatch")
			}
			assert.Equal(t, tt.wantAutostart, p.Autostart, "Autostart mismatch")
		})
	}
}

// TestProxyListenPortExplicitTargetAutostart verifies that a proxy
// declaring url + listen-port + skip-tls-verify still flows through
// the explicit-target autostart path (ShouldAutostart returns true).
// Regression guard: the new fields must not accidentally shadow the
// explicit-target detection logic in HasExplicitTarget.
func TestProxyListenPortExplicitTargetAutostart(t *testing.T) {
	input := `proxies {
    tdo {
        url "https://tdo-local.sbdev.io"
        listen-port 4444
        skip-tls-verify true
    }
}`
	cfg, err := ParseAgntConfig(input)
	require.NoError(t, err)

	autostart := cfg.GetAutostartProxies()
	assert.Contains(t, autostart, "tdo",
		"explicit-url proxy with listen-port and skip-tls-verify should auto-start")

	p := autostart["tdo"]
	require.NotNil(t, p)
	assert.True(t, p.HasExplicitTarget(), "HasExplicitTarget should be true for url-only proxy")
	assert.Equal(t, 4444, p.ListenPort)
	assert.True(t, p.SkipTLSVerify)
}

// TestProxyListenPortBadValueCoercesToZero pins the kdl-go parser's
// current behavior for malformed int values: instead of failing the
// unmarshal, it silently coerces the field to zero. A zero ListenPort
// is treated as "use the hash-based allocator" downstream, so a
// typo in .agnt.kdl falls back to the default port rather than
// killing autostart — gracefully degraded, at the cost of a silent
// misconfiguration. Regression test for the fallback path.
//
// If kdl-go ever tightens its numeric coercion, this test will flip
// to the stricter contract and we should remove the coerce-to-zero
// note from the ProxyConfig.ListenPort docstring.
func TestProxyListenPortBadValueCoercesToZero(t *testing.T) {
	input := `proxies {
    dev {
        url "http://localhost:3000"
        listen-port "not-a-number"
    }
}`
	cfg, err := ParseAgntConfig(input)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	p, ok := cfg.Proxies["dev"]
	require.True(t, ok)
	assert.Equal(t, 0, p.ListenPort,
		"malformed int falls back to zero → hash-based allocator")
}

func TestDefaultAgntConfig(t *testing.T) {
	cfg := DefaultAgntConfig()

	assert.NotNil(t, cfg.Scripts)
	assert.Len(t, cfg.Scripts, 0)

	assert.NotNil(t, cfg.Proxies)
	assert.Len(t, cfg.Proxies, 0)
}

func TestParseAIConfig_AdapterAliases(t *testing.T) {
	cfg, err := ParseAgntConfig(`ai {
    adapters {
        claude {
            aliases "cdsp" "cc"
        }
    }
}`)
	require.NoError(t, err)
	require.NotNil(t, cfg.AI)
	require.Contains(t, cfg.AI.Adapters, "claude")
	assert.Equal(t, []string{"cdsp", "cc"}, cfg.AI.Adapters["claude"].Aliases)
}

func TestParseGlobalConfig_AIAdapterAliases(t *testing.T) {
	cfg, err := ParseKDLConfig(`ai {
    adapters {
        claude {
            aliases "cdsp" "cc"
        }
    }
}`)
	require.NoError(t, err)
	require.NotNil(t, cfg.AI)
	require.Contains(t, cfg.AI.Adapters, "claude")
	assert.Equal(t, []string{"cdsp", "cc"}, cfg.AI.Adapters["claude"].Aliases)
}

func TestParseAIConfig(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected *AIConfig
	}{
		{
			name:     "no ai section",
			input:    `scripts {}`,
			expected: nil,
		},
		{
			name: "ai with skill only",
			input: `ai {
    skill "debugging"
}`,
			expected: &AIConfig{Skill: "debugging"},
		},
		{
			name: "ai with system-prompt override",
			input: `ai {
    system-prompt "You are a helpful assistant."
}`,
			expected: &AIConfig{SystemPrompt: "You are a helpful assistant."},
		},
		{
			name: "ai with append-system-prompt",
			input: `ai {
    append-system-prompt "Focus on security."
}`,
			expected: &AIConfig{AppendSystemPrompt: "Focus on security."},
		},
		{
			name: "ai with context",
			input: `ai {
    context "React + FastAPI app. Dev: port 3000, API: port 8080."
}`,
			expected: &AIConfig{Context: "React + FastAPI app. Dev: port 3000, API: port 8080."},
		},
		{
			name: "ai with all options",
			input: `ai {
    skill "code-review"
    system-prompt ""
    context "Node.js monorepo."
    append-system-prompt "This is a Node.js project."
}`,
			expected: &AIConfig{
				Skill:              "code-review",
				Context:            "Node.js monorepo.",
				AppendSystemPrompt: "This is a Node.js project.",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseAgntConfig(tt.input)
			require.NoError(t, err)

			if tt.expected == nil {
				assert.Nil(t, cfg.AI)
			} else {
				require.NotNil(t, cfg.AI)
				if tt.expected.Skill != "" {
					assert.Equal(t, tt.expected.Skill, cfg.AI.Skill)
				}
				if tt.expected.SystemPrompt != "" {
					assert.Equal(t, tt.expected.SystemPrompt, cfg.AI.SystemPrompt)
				}
				if tt.expected.Context != "" {
					assert.Equal(t, tt.expected.Context, cfg.AI.Context)
				}
				if tt.expected.AppendSystemPrompt != "" {
					assert.Equal(t, tt.expected.AppendSystemPrompt, cfg.AI.AppendSystemPrompt)
				}
			}
		})
	}
}

func TestBuildSystemPrompt(t *testing.T) {
	t.Run("full system prompt override", func(t *testing.T) {
		cfg := &AgntConfig{
			AI: &AIConfig{
				SystemPrompt: "Custom system prompt.",
			},
			Scripts: map[string]*ScriptConfig{
				"dev": {Run: "npm run dev"},
			},
		}

		prompt := cfg.BuildSystemPrompt()
		assert.Equal(t, "Custom system prompt.", prompt)
	})

	t.Run("default prompt with agnt features", func(t *testing.T) {
		cfg := DefaultAgntConfig()

		prompt := cfg.BuildSystemPrompt()
		assert.Contains(t, prompt, "agnt Tools")
		assert.Contains(t, prompt, "get_incidents")
		// get_errors is retired; the prompt must not send an agent at a tool the
		// MCP server no longer registers.
		assert.NotContains(t, prompt, "get_errors")
		assert.Contains(t, prompt, "proxy")
		assert.Contains(t, prompt, "proc")
		assert.Contains(t, prompt, "responsive_audit")
		assert.Contains(t, prompt, "currentpage")
		assert.Contains(t, prompt, "Debugging Workflow")
		assert.Contains(t, prompt, "A `proxy_id required` response is an argument-validation error")
		assert.Contains(t, prompt, "Common Patterns")
		assert.Contains(t, prompt, "Process Management")
	})

	t.Run("prompt includes configured scripts", func(t *testing.T) {
		cfg := DefaultAgntConfig()
		cfg.Scripts = map[string]*ScriptConfig{
			"dev":   {Run: "npm run dev", Autostart: true},
			"build": {Run: "npm run build"},
		}

		prompt := cfg.BuildSystemPrompt()
		assert.Contains(t, prompt, "Configured Scripts")
		assert.Contains(t, prompt, "dev")
		assert.Contains(t, prompt, "npm run dev")
		assert.Contains(t, prompt, "(autostart)")
		assert.Contains(t, prompt, "build")
	})

	t.Run("prompt includes configured proxies", func(t *testing.T) {
		cfg := DefaultAgntConfig()
		cfg.Proxies = map[string]*ProxyConfig{
			"frontend": {URL: "http://localhost:3000", Autostart: true},
			"backend":  {Port: 8080},
		}

		prompt := cfg.BuildSystemPrompt()
		assert.Contains(t, prompt, "Configured Proxies")
		assert.Contains(t, prompt, "frontend")
		assert.Contains(t, prompt, "http://localhost:3000")
		assert.Contains(t, prompt, "backend")
		assert.Contains(t, prompt, "http://localhost:8080")
	})

	t.Run("prompt includes script-linked proxy", func(t *testing.T) {
		cfg := DefaultAgntConfig()
		cfg.Proxies = map[string]*ProxyConfig{
			"dev": {Script: "dev-script"},
		}

		prompt := cfg.BuildSystemPrompt()
		assert.Contains(t, prompt, "linked to script 'dev-script'")
	})

	t.Run("context appears before agnt tools", func(t *testing.T) {
		cfg := DefaultAgntConfig()
		cfg.AI = &AIConfig{
			Context: "React + FastAPI app. Dev: port 3000, API: port 8080.",
		}

		prompt := cfg.BuildSystemPrompt()
		assert.Contains(t, prompt, "Project Context")
		assert.Contains(t, prompt, "React + FastAPI app.")
		// Context section must precede the agnt Tools section.
		contextIdx := strings.Index(prompt, "Project Context")
		toolsIdx := strings.Index(prompt, "agnt Tools")
		assert.Less(t, contextIdx, toolsIdx, "context should appear before agnt Tools")
	})

	t.Run("append system prompt", func(t *testing.T) {
		cfg := DefaultAgntConfig()
		cfg.AI = &AIConfig{
			AppendSystemPrompt: "Focus on security best practices.",
		}

		prompt := cfg.BuildSystemPrompt()
		assert.Contains(t, prompt, "agnt")                              // Has base prompt
		assert.Contains(t, prompt, "Focus on security best practices.") // Has appended content
	})

	t.Run("system prompt takes precedence over append", func(t *testing.T) {
		cfg := DefaultAgntConfig()
		cfg.AI = &AIConfig{
			SystemPrompt:       "Full override.",
			AppendSystemPrompt: "This should be ignored.",
		}

		prompt := cfg.BuildSystemPrompt()
		assert.Equal(t, "Full override.", prompt)
		assert.NotContains(t, prompt, "This should be ignored.")
	})

	// Acceptance criterion: "New Claude Code session shows proc-first
	// guidance in injected system prompt." The default-built prompt must
	// contain the prominent rule block AND a concrete before/after example
	// pair so the agent has unambiguous guidance about reaching for proc
	// instead of running long-lived commands via Bash.
	t.Run("default prompt includes prominent proc-first guidance", func(t *testing.T) {
		cfg := DefaultAgntConfig()
		prompt := cfg.BuildSystemPrompt()

		// Header is the most important hook: agents scan for ALL CAPS /
		// "CRITICAL" markers when deciding what is load-bearing.
		assert.Contains(t, prompt, "CRITICAL: Use proc",
			"prompt must call out the proc-first rule prominently")

		// Concrete commands the agent must NOT use raw.
		assert.Contains(t, prompt, "npm run dev",
			"prompt must show npm run dev as the bad example")
		assert.Contains(t, prompt, "go run ",
			"prompt must show go run as a bad example")

		// Concrete tool calls the agent SHOULD use instead.
		assert.Contains(t, prompt, `proc {action: "run"`,
			"prompt must show proc run as the good example")
		assert.Contains(t, prompt, `proc {action: "output"`,
			"prompt must show proc output for inspecting streamed lines")
		assert.Contains(t, prompt, `watch {target: "process"`,
			"prompt must mention watch for live tailing")

		// Proc-first block must come before the generic agnt Tools section
		// so the agent reads the rule before the tool catalog. This is the
		// "prominence" half of the acceptance criterion.
		procIdx := strings.Index(prompt, "CRITICAL: Use proc")
		toolsIdx := strings.Index(prompt, "## agnt Tools")
		assert.Greater(t, procIdx, -1, "proc-first block missing")
		assert.Greater(t, toolsIdx, -1, "agnt Tools header missing")
		assert.Less(t, procIdx, toolsIdx,
			"proc-first guidance must appear before the generic Tools list")
	})
}

func TestParseAgntConfigErrors(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectError string
	}{
		{
			name: "non-standard proxy format",
			input: `proxy "dev" {
    script "dev"
}`,
			expectError: "no struct field into which to unmarshal node",
		},
		{
			name: "invalid KDL syntax",
			input: `scripts {
    dev {
        run "unclosed string
    }
}`,
			expectError: "failed to parse KDL config",
		},
		{
			name: "unknown field in script",
			input: `scripts {
    dev {
        run "npm start"
        unknown-field "should error"
    }
}`,
			expectError: "no struct field into which to unmarshal node",
		},
		{
			name: "unknown field in proxy",
			input: `proxies {
    dev {
        script "dev"
        invalid-option true
    }
}`,
			expectError: "no struct field into which to unmarshal node",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseAgntConfig(tt.input)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectError)
		})
	}
}

func TestParseAlertsConfig(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		validate func(t *testing.T, cfg *AgntConfig)
	}{
		{
			name:  "no alerts section",
			input: `scripts {}`,
			validate: func(t *testing.T, cfg *AgntConfig) {
				assert.Nil(t, cfg.Alerts)
			},
		},
		{
			name: "alerts enabled explicitly",
			input: `alerts {
    enabled true
}`,
			validate: func(t *testing.T, cfg *AgntConfig) {
				require.NotNil(t, cfg.Alerts)
				require.NotNil(t, cfg.Alerts.Enabled)
				assert.True(t, *cfg.Alerts.Enabled)
				assert.True(t, cfg.Alerts.IsEnabled())
			},
		},
		{
			name: "alerts disabled",
			input: `alerts {
    enabled false
}`,
			validate: func(t *testing.T, cfg *AgntConfig) {
				require.NotNil(t, cfg.Alerts)
				require.NotNil(t, cfg.Alerts.Enabled)
				assert.False(t, *cfg.Alerts.Enabled)
				assert.False(t, cfg.Alerts.IsEnabled())
			},
		},
		{
			name: "alerts with custom batch window",
			input: `alerts {
    batch-window 5
    dedupe-window 120
}`,
			validate: func(t *testing.T, cfg *AgntConfig) {
				require.NotNil(t, cfg.Alerts)
				assert.Equal(t, 5, cfg.Alerts.BatchWindow)
				assert.Equal(t, 120, cfg.Alerts.DedupeWindow)
			},
		},
		{
			name: "alerts with disable list",
			input: `alerts {
    disable "connection-refused"
}`,
			validate: func(t *testing.T, cfg *AgntConfig) {
				require.NotNil(t, cfg.Alerts)
				assert.Contains(t, cfg.Alerts.Disable, "connection-refused")
			},
		},
		{
			name: "alerts with custom patterns",
			input: `alerts {
    patterns {
        "my-custom" {
            pattern "MY_APP_ERROR:"
            severity "error"
        }
    }
}`,
			validate: func(t *testing.T, cfg *AgntConfig) {
				require.NotNil(t, cfg.Alerts)
				require.NotNil(t, cfg.Alerts.Patterns)
				p, ok := cfg.Alerts.Patterns["my-custom"]
				require.True(t, ok)
				assert.Equal(t, "MY_APP_ERROR:", p.Pattern)
				assert.Equal(t, "error", p.Severity)
			},
		},
		{
			name: "alerts with full config",
			input: `alerts {
    enabled true
    batch-window 3
    dedupe-window 60
    patterns {
        "custom-warn" {
            pattern "DEPRECATION:"
            severity "warning"
        }
    }
    disable "generic-segfault"
}`,
			validate: func(t *testing.T, cfg *AgntConfig) {
				require.NotNil(t, cfg.Alerts)
				assert.True(t, cfg.Alerts.IsEnabled())
				assert.Equal(t, 3, cfg.Alerts.BatchWindow)
				assert.Equal(t, 60, cfg.Alerts.DedupeWindow)
				require.Contains(t, cfg.Alerts.Patterns, "custom-warn")
				assert.Equal(t, "DEPRECATION:", cfg.Alerts.Patterns["custom-warn"].Pattern)
				assert.Contains(t, cfg.Alerts.Disable, "generic-segfault")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseAgntConfig(tt.input)
			require.NoError(t, err)
			tt.validate(t, cfg)
		})
	}
}

func TestPortConflictPolicy_Parsing(t *testing.T) {
	tests := []struct {
		name     string
		kdl      string
		expected string
	}{
		{
			"default when unset",
			"scripts {\n    api {\n        run \"go run .\"\n    }\n}",
			"",
		},
		{
			"prompt",
			"project {\n    port-conflict \"prompt\"\n}\nscripts {\n    api {\n        run \"go run .\"\n    }\n}",
			"prompt",
		},
		{
			"auto-kill",
			"project {\n    port-conflict \"auto-kill\"\n}\nscripts {\n    api {\n        run \"go run .\"\n    }\n}",
			"auto-kill",
		},
		{
			"skip",
			"project {\n    port-conflict \"skip\"\n}\nscripts {\n    api {\n        run \"go run .\"\n    }\n}",
			"skip",
		},
		{
			"fail",
			"project {\n    port-conflict \"fail\"\n}\nscripts {\n    api {\n        run \"go run .\"\n    }\n}",
			"fail",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseAgntConfig(tt.kdl)
			require.NoError(t, err)
			got := cfg.PortConflictPolicy()
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestPortConflictPolicy_DefaultsToPrompt(t *testing.T) {
	cfg, err := ParseAgntConfig("scripts {\n    api {\n        run \"go run .\"\n    }\n}")
	require.NoError(t, err)
	assert.Equal(t, "prompt", cfg.EffectivePortConflictPolicy())
}

func TestAlertsConfigIsEnabled(t *testing.T) {
	// nil config defaults to true
	var nilCfg *AlertsConfig
	assert.True(t, nilCfg.IsEnabled())

	// nil Enabled field defaults to true
	cfg := &AlertsConfig{}
	assert.True(t, cfg.IsEnabled())

	// Explicit true
	trueVal := true
	cfg = &AlertsConfig{Enabled: &trueVal}
	assert.True(t, cfg.IsEnabled())

	// Explicit false
	falseVal := false
	cfg = &AlertsConfig{Enabled: &falseVal}
	assert.False(t, cfg.IsEnabled())
}

func TestDefaultHealthPatterns(t *testing.T) {
	patterns := DefaultHealthPatterns()
	assert.NotEmpty(t, patterns.Error, "error pattern should not be empty")
	assert.NotEmpty(t, patterns.Healthy, "healthy pattern should not be empty")

	// Verify both patterns compile as valid regex
	_, err := regexp.Compile(patterns.Error)
	require.NoError(t, err, "error pattern must compile as valid regex")
	_, err = regexp.Compile(patterns.Healthy)
	require.NoError(t, err, "healthy pattern must compile as valid regex")
}

func TestDefaultHealthPatterns_ErrorMatches(t *testing.T) {
	patterns := DefaultHealthPatterns()

	tests := []struct {
		name    string
		message string
	}{
		// Go
		{"go panic", "panic: runtime error: index out of range"},
		// Node.js
		{"node module not found", "Error: Cannot find module 'express'"},
		{"node EADDRINUSE", "Error: listen EADDRINUSE: address already in use :::3000"},
		{"node syntax error", "/app/src/index.js: SyntaxError: Unexpected token"},
		// TypeScript
		{"typescript error", "src/app.ts(10,5): error TS2322: Type 'string' is not assignable"},
		// Rust
		{"rust compiler error", "error[E0277]: the trait bound `String: Copy` is not satisfied"},
		// Python
		{"python traceback", "Traceback (most recent call last):\n  File \"app.py\", line 10"},
		// .NET
		{"dotnet build failed", "Build FAILED."},
		{"dotnet unhandled exception", "Unhandled exception. System.InvalidOperationException"},
		// General
		{"ERROR keyword", "ERROR in src/main.ts"},
		{"FAIL keyword", "FAIL  test_foo.py::test_bar"},
		{"FATAL keyword", "FATAL: database connection failed"},
		{"segfault", "Segmentation fault (core dumped)"},
		{"out of memory", "Fatal error: out of memory"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Regexp(t, patterns.Error, tt.message, "should match error: %s", tt.message)
		})
	}
}

func TestDefaultHealthPatterns_ErrorNoFalsePositive(t *testing.T) {
	patterns := DefaultHealthPatterns()

	tests := []struct {
		name    string
		message string
	}{
		{"error count zero", "0 errors found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotRegexp(t, patterns.Error, tt.message,
				"should NOT match as error: %s", tt.message)
		})
	}
}

func TestDefaultHealthPatterns_HealthyMatches(t *testing.T) {
	patterns := DefaultHealthPatterns()

	tests := []struct {
		name    string
		message string
	}{
		// Go
		{"go stdlib", "2024/01/15 10:00:00 listening on :8080"},
		// Node.js - Vite
		{"vite ready", "  VITE v5.0.0  ready in 300ms"},
		// Node.js - Next.js
		{"next ready", "  ready in 2.3s"},
		// Node.js - Webpack
		{"webpack compiled", "webpack compiled successfully"},
		{"webpack Compiled", "Compiled successfully!"},
		// .NET
		{"dotnet run", "Now listening on: https://localhost:5001"},
		{"dotnet watch", "watch : Started"},
		// Python - Flask
		{"flask", " * Running on http://127.0.0.1:5000"},
		// Python - uvicorn
		{"uvicorn", "INFO:     Uvicorn running on http://127.0.0.1:8000 (Press CTRL+C to quit)"},
		// Python - gunicorn
		{"gunicorn", "[2024-01-15 10:00:00] [INFO] Listening at: http://127.0.0.1:8000"},
		// Python - Django
		{"django", "Starting development server at http://127.0.0.1:8000/"},
		// Rust - cargo
		// (cargo outputs "Finished" which is too broad; relies on user app output)
		// Java - Spring Boot
		{"spring boot", "Started MyApp in 2.345 seconds (process running for 3.1)"},
		// Java - Gradle
		{"gradle", "BUILD SUCCESSFUL in 5s"},
		// Java - Maven
		{"maven", "BUILD SUCCESS"},
		// Ruby - Rails
		{"rails", "* Listening on http://127.0.0.1:3000"},
		// PHP - artisan
		{"php artisan", "Starting Laravel development server: http://127.0.0.1:8000"},
		// PHP - built-in
		{"php built-in", "Started PHP development server on http://localhost:8000"},
		// Swift - Vapor
		{"vapor", "Server starting on http://127.0.0.1:8080"},
		// Elixir - Phoenix
		{"phoenix", "[info] Running MyAppWeb.Endpoint with cowboy http://127.0.0.1:4000"},
		// Generic
		{"server running", "Server running at http://localhost:3000/"},
		{"build succeeded", "build succeeded"},
		{"serving", "Serving!"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Regexp(t, patterns.Healthy, tt.message, "should match healthy: %s", tt.message)
		})
	}
}

func TestDefaultHealthPatterns_HealthyNoFalsePositive(t *testing.T) {
	patterns := DefaultHealthPatterns()

	tests := []struct {
		name    string
		message string
	}{
		{"build failure", "BUILD FAILURE in 10s"},
		{"compilation failed", "compilation failed with 3 errors"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotRegexp(t, patterns.Healthy, tt.message,
				"should NOT match as healthy: %s", tt.message)
		})
	}
}

// ── AutoForwardConfig ────────────────────────────────────────────────────────

func TestAutoForwardConfig_IsEnabled_Nil(t *testing.T) {
	var cfg *AutoForwardConfig
	assert.True(t, cfg.IsEnabled(), "nil config should default to enabled")
}

func TestAutoForwardConfig_IsEnabled_NilBool(t *testing.T) {
	cfg := &AutoForwardConfig{}
	assert.True(t, cfg.IsEnabled(), "nil Enabled should default to true")
}

func TestAutoForwardConfig_IsEnabled_True(t *testing.T) {
	enabled := true
	cfg := &AutoForwardConfig{Enabled: &enabled}
	assert.True(t, cfg.IsEnabled())
}

func TestAutoForwardConfig_IsEnabled_False(t *testing.T) {
	enabled := false
	cfg := &AutoForwardConfig{Enabled: &enabled}
	assert.False(t, cfg.IsEnabled())
}

func TestAutoForwardConfig_GetSources_Default(t *testing.T) {
	var cfg *AutoForwardConfig
	sources := cfg.GetSources()
	assert.Equal(t, []string{"browser", "http"}, sources)
}

func TestAutoForwardConfig_GetSources_Custom(t *testing.T) {
	cfg := &AutoForwardConfig{Sources: []string{"browser"}}
	sources := cfg.GetSources()
	assert.Equal(t, []string{"browser"}, sources)
}

func TestAutoForwardConfig_GetDebounceSeconds_Default(t *testing.T) {
	var cfg *AutoForwardConfig
	assert.Equal(t, 10, cfg.GetDebounceSeconds())
}

func TestAutoForwardConfig_GetDebounceSeconds_Custom(t *testing.T) {
	cfg := &AutoForwardConfig{Debounce: 30}
	assert.Equal(t, 30, cfg.GetDebounceSeconds())
}

func TestAutoForwardConfig_GetDebounceSeconds_Zero(t *testing.T) {
	cfg := &AutoForwardConfig{Debounce: 0}
	assert.Equal(t, 10, cfg.GetDebounceSeconds(), "zero should use default")
}

func TestAutoForwardConfig_GetSeverity_Default(t *testing.T) {
	var cfg *AutoForwardConfig
	assert.Equal(t, "error", cfg.GetSeverity())
}

func TestAutoForwardConfig_GetSeverity_Warning(t *testing.T) {
	cfg := &AutoForwardConfig{Severity: "warning"}
	assert.Equal(t, "warning", cfg.GetSeverity())
}

func TestAutoForwardConfig_ShouldForwardSource(t *testing.T) {
	var cfg *AutoForwardConfig
	assert.True(t, cfg.ShouldForwardSource("browser"), "nil config should forward all sources")
	assert.True(t, cfg.ShouldForwardSource("http"), "nil config should forward all sources")

	cfg2 := &AutoForwardConfig{Sources: []string{"browser"}}
	assert.True(t, cfg2.ShouldForwardSource("browser"))
	assert.False(t, cfg2.ShouldForwardSource("http"))
}

func TestParseAgntConfig_AutoForward(t *testing.T) {
	cfg, err := ParseAgntConfig(`alerts {
    enabled true
    auto-forward {
        enabled true
        sources "browser" "http"
        debounce 30
        severity "warning"
    }
}`)
	require.NoError(t, err)
	require.NotNil(t, cfg.Alerts)
	require.NotNil(t, cfg.Alerts.AutoForward)
	assert.True(t, cfg.Alerts.AutoForward.IsEnabled())
	assert.Equal(t, []string{"browser", "http"}, cfg.Alerts.AutoForward.GetSources())
	assert.Equal(t, 30, cfg.Alerts.AutoForward.GetDebounceSeconds())
	assert.Equal(t, "warning", cfg.Alerts.AutoForward.GetSeverity())
}

func TestParseAgntConfig_AutoForwardDisabled(t *testing.T) {
	cfg, err := ParseAgntConfig(`alerts {
    auto-forward {
        enabled false
    }
}`)
	require.NoError(t, err)
	require.NotNil(t, cfg.Alerts.AutoForward)
	assert.False(t, cfg.Alerts.AutoForward.IsEnabled())
}

func TestParseAgntConfig_NoAutoForward(t *testing.T) {
	cfg, err := ParseAgntConfig(`alerts {
    enabled true
}`)
	require.NoError(t, err)
	require.NotNil(t, cfg.Alerts)
	assert.Nil(t, cfg.Alerts.AutoForward)
}

func TestParseAgntConfig_AutomationMaxSessions(t *testing.T) {
	// A non-default value is parsed AND drives the accessor (config §5: a
	// parsed key must change runtime, not merely be stored).
	cfg, err := ParseAgntConfig(`automation {
    max-sessions 2
}`)
	require.NoError(t, err)
	require.NotNil(t, cfg.Automation)
	require.NotNil(t, cfg.Automation.MaxSessions)
	assert.Equal(t, 2, *cfg.Automation.MaxSessions)
	assert.Equal(t, 2, cfg.Automation.MaxSessionsOrDefault())
}

func TestParseAgntConfig_AutomationDefaults(t *testing.T) {
	// Absent block → default ceiling via the nil-safe accessor.
	cfg, err := ParseAgntConfig(`project {
    name "x"
}`)
	require.NoError(t, err)
	assert.Nil(t, cfg.Automation)
	assert.Equal(t, DefaultAutomationMaxSessions, cfg.Automation.MaxSessionsOrDefault())

	// A non-positive value also falls back to the default rather than
	// silently disabling the cap.
	cfg2, err := ParseAgntConfig(`automation {
    max-sessions 0
}`)
	require.NoError(t, err)
	require.NotNil(t, cfg2.Automation)
	assert.Equal(t, DefaultAutomationMaxSessions, cfg2.Automation.MaxSessionsOrDefault())
}

func TestParsePushConfig(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		validate func(t *testing.T, cfg *AgntConfig)
	}{
		{
			name:  "no push config defaults to all enabled",
			input: `scripts {}`,
			validate: func(t *testing.T, cfg *AgntConfig) {
				pc := cfg.Alerts.GetPushConfig()
				assert.Nil(t, pc, "no alerts config returns nil push config")
			},
		},
		{
			name: "alerts with no push block defaults to all enabled",
			input: `alerts {
	    enabled true
	}`,
			validate: func(t *testing.T, cfg *AgntConfig) {
				pc := cfg.Alerts.GetPushConfig()
				assert.Nil(t, pc, "no push block returns nil")
			},
		},
		{
			name: "push both channels enabled",
			input: `alerts {
	    push {
	        mcp-notifications true
	        pty-injection true
	    }
	}`,
			validate: func(t *testing.T, cfg *AgntConfig) {
				pc := cfg.Alerts.GetPushConfig()
				require.NotNil(t, pc)
				assert.True(t, pc.MCPNotificationsEnabled())
				assert.True(t, pc.PTYInjectionEnabled())
			},
		},
		{
			name: "push both channels disabled",
			input: `alerts {
	    push {
	        mcp-notifications false
	        pty-injection false
	    }
	}`,
			validate: func(t *testing.T, cfg *AgntConfig) {
				pc := cfg.Alerts.GetPushConfig()
				require.NotNil(t, pc)
				assert.False(t, pc.MCPNotificationsEnabled())
				assert.False(t, pc.PTYInjectionEnabled())
			},
		},
		{
			name: "push only mcp-notifications",
			input: `alerts {
	    push {
	        mcp-notifications true
	        pty-injection false
	    }
	}`,
			validate: func(t *testing.T, cfg *AgntConfig) {
				pc := cfg.Alerts.GetPushConfig()
				require.NotNil(t, pc)
				assert.True(t, pc.MCPNotificationsEnabled())
				assert.False(t, pc.PTYInjectionEnabled())
			},
		},
		{
			name: "push only pty-injection",
			input: `alerts {
	    push {
	        mcp-notifications false
	        pty-injection true
	    }
	}`,
			validate: func(t *testing.T, cfg *AgntConfig) {
				pc := cfg.Alerts.GetPushConfig()
				require.NotNil(t, pc)
				assert.False(t, pc.MCPNotificationsEnabled())
				assert.True(t, pc.PTYInjectionEnabled())
			},
		},
		{
			name: "push block with no fields defaults to all enabled",
			input: `alerts {
	    push {
	    }
	}`,
			validate: func(t *testing.T, cfg *AgntConfig) {
				pc := cfg.Alerts.GetPushConfig()
				require.NotNil(t, pc)
				assert.True(t, pc.MCPNotificationsEnabled())
				assert.True(t, pc.PTYInjectionEnabled())
			},
		},
		{
			name: "push with only mcp-notifications set",
			input: `alerts {
	    push {
	        mcp-notifications false
	    }
	}`,
			validate: func(t *testing.T, cfg *AgntConfig) {
				pc := cfg.Alerts.GetPushConfig()
				require.NotNil(t, pc)
				assert.False(t, pc.MCPNotificationsEnabled())
				assert.True(t, pc.PTYInjectionEnabled(), "unset pty-injection defaults to true")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseAgntConfig(tt.input)
			require.NoError(t, err)
			tt.validate(t, cfg)
		})
	}
}

func TestParsePushConfig_Presets(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		validate func(t *testing.T, cfg *AgntConfig)
	}{
		{
			name: "preset claude-code",
			input: `alerts {
	    preset "claude-code"
	}`,
			validate: func(t *testing.T, cfg *AgntConfig) {
				require.NotNil(t, cfg.Alerts)
				assert.Equal(t, "claude-code", cfg.Alerts.Preset)
				pc := cfg.Alerts.GetPushConfig()
				require.NotNil(t, pc)
				assert.True(t, pc.MCPNotificationsEnabled())
				assert.False(t, pc.PTYInjectionEnabled())
			},
		},
		{
			name: "preset universal",
			input: `alerts {
	    preset "universal"
	}`,
			validate: func(t *testing.T, cfg *AgntConfig) {
				require.NotNil(t, cfg.Alerts)
				assert.Equal(t, "universal", cfg.Alerts.Preset)
				pc := cfg.Alerts.GetPushConfig()
				require.NotNil(t, pc)
				assert.True(t, pc.MCPNotificationsEnabled())
				assert.True(t, pc.PTYInjectionEnabled())
			},
		},
		{
			name: "explicit push takes precedence over preset",
			input: `alerts {
	    preset "claude-code"
	    push {
	        mcp-notifications false
	        pty-injection true
	    }
	}`,
			validate: func(t *testing.T, cfg *AgntConfig) {
				require.NotNil(t, cfg.Alerts)
				pc := cfg.Alerts.GetPushConfig()
				require.NotNil(t, pc)
				assert.False(t, pc.MCPNotificationsEnabled(), "explicit push overrides preset")
				assert.True(t, pc.PTYInjectionEnabled(), "explicit push overrides preset")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseAgntConfig(tt.input)
			require.NoError(t, err)
			tt.validate(t, cfg)
		})
	}
}

func TestPresetPushConfig(t *testing.T) {
	t.Run("claude-code preset", func(t *testing.T) {
		pc := PresetPushConfig("claude-code")
		require.NotNil(t, pc)
		assert.True(t, pc.MCPNotificationsEnabled())
		assert.False(t, pc.PTYInjectionEnabled())
	})

	t.Run("universal preset", func(t *testing.T) {
		pc := PresetPushConfig("universal")
		require.NotNil(t, pc)
		assert.True(t, pc.MCPNotificationsEnabled())
		assert.True(t, pc.PTYInjectionEnabled())
	})

	t.Run("unknown preset returns nil", func(t *testing.T) {
		pc := PresetPushConfig("unknown")
		assert.Nil(t, pc)
	})
}

func TestPushConfig_Defaults(t *testing.T) {
	t.Run("nil config defaults to enabled", func(t *testing.T) {
		var pc *PushConfig
		assert.True(t, pc.MCPNotificationsEnabled())
		assert.True(t, pc.PTYInjectionEnabled())
	})

	t.Run("empty config defaults to enabled", func(t *testing.T) {
		pc := &PushConfig{}
		assert.True(t, pc.MCPNotificationsEnabled())
		assert.True(t, pc.PTYInjectionEnabled())
	})

	t.Run("explicit true", func(t *testing.T) {
		v := true
		pc := &PushConfig{MCPNotifications: &v, PTYInjection: &v}
		assert.True(t, pc.MCPNotificationsEnabled())
		assert.True(t, pc.PTYInjectionEnabled())
	})

	t.Run("explicit false", func(t *testing.T) {
		v := false
		pc := &PushConfig{MCPNotifications: &v, PTYInjection: &v}
		assert.False(t, pc.MCPNotificationsEnabled())
		assert.False(t, pc.PTYInjectionEnabled())
	})
}

func TestParseChannelConfig_Defaults(t *testing.T) {
	cfg := DefaultAgntConfig()
	require.NotNil(t, cfg.Channel, "DefaultAgntConfig should include a Channel block")
	assert.False(t, cfg.Channel.IsEnabled(), "channel should default to disabled")
	assert.True(t, cfg.Channel.ReplyToolEnabled(), "reply-tool should default to true")
	assert.Equal(t, "warning", cfg.Channel.GetSeverity(), "severity should default to warning")
	assert.Equal(t, 2000, cfg.Channel.GetDedupeWindow(), "dedupe-window should default to 2000ms")
	assert.Empty(t, cfg.Channel.Events, "events should default to empty (all types)")
}

func TestParseChannelConfig_NilDefaults(t *testing.T) {
	var cc *ChannelConfig
	assert.False(t, cc.IsEnabled(), "nil ChannelConfig should default to disabled")
	assert.True(t, cc.ReplyToolEnabled(), "nil ChannelConfig reply-tool should default to true")
	assert.Equal(t, "warning", cc.GetSeverity(), "nil ChannelConfig severity should default to warning")
	assert.Equal(t, 2000, cc.GetDedupeWindow(), "nil ChannelConfig dedupe-window should default to 2000")
	assert.Empty(t, cc.GetEvents(), "nil ChannelConfig events should default to empty")
}

func TestParseChannelConfig_AllFields(t *testing.T) {
	input := `channel {
    enabled true
    events "error" "diagnostic" "interaction"
    severity "error"
    dedupe-window 5000
    reply-tool false
}`
	cfg, err := ParseAgntConfig(input)
	require.NoError(t, err)
	require.NotNil(t, cfg.Channel)

	assert.True(t, cfg.Channel.IsEnabled())
	assert.Equal(t, []string{"error", "diagnostic", "interaction"}, cfg.Channel.Events)
	assert.Equal(t, "error", cfg.Channel.Severity)
	assert.Equal(t, 5000, cfg.Channel.DedupeWindow)
	assert.False(t, cfg.Channel.ReplyToolEnabled())
}

func TestParseChannelConfig_PartialFields(t *testing.T) {
	input := `channel {
    enabled true
    severity "info"
}`
	cfg, err := ParseAgntConfig(input)
	require.NoError(t, err)
	require.NotNil(t, cfg.Channel)

	assert.True(t, cfg.Channel.IsEnabled())
	assert.Equal(t, "info", cfg.Channel.Severity)
	assert.Equal(t, 2000, cfg.Channel.GetDedupeWindow(), "unset dedupe-window should use default")
	assert.True(t, cfg.Channel.ReplyToolEnabled(), "unset reply-tool should use default")
	assert.Empty(t, cfg.Channel.Events, "unset events should be empty")
}

func TestParseChannelConfig_NoChannelBlock(t *testing.T) {
	input := `scripts {}`
	cfg, err := ParseAgntConfig(input)
	require.NoError(t, err)
	// Channel should come from defaults
	require.NotNil(t, cfg.Channel)
	assert.False(t, cfg.Channel.IsEnabled())
}

func TestParseChannelConfig_BadSeverity(t *testing.T) {
	input := `channel {
    enabled true
    severity "bogus"
}`
	_, err := ParseAgntConfig(input)
	assert.Error(t, err, "unknown severity should fail validation")
	assert.Contains(t, err.Error(), "severity")
}

func TestParseChannelConfig_BadEventType(t *testing.T) {
	input := `channel {
    enabled true
    events "error" "not-a-real-type"
}`
	_, err := ParseAgntConfig(input)
	assert.Error(t, err, "unknown event type should fail validation")
	assert.Contains(t, err.Error(), "event type")
}

func TestParseChannelConfig_EmptyBlock(t *testing.T) {
	input := `channel {}`
	cfg, err := ParseAgntConfig(input)
	require.NoError(t, err)
	require.NotNil(t, cfg.Channel)
	assert.False(t, cfg.Channel.IsEnabled(), "empty channel block should default to disabled")
	assert.True(t, cfg.Channel.ReplyToolEnabled())
	assert.Equal(t, "warning", cfg.Channel.GetSeverity())
	assert.Equal(t, 2000, cfg.Channel.GetDedupeWindow())
}

func TestAlertsConfig_GetPushConfig(t *testing.T) {
	t.Run("nil alerts config returns nil", func(t *testing.T) {
		var ac *AlertsConfig
		assert.Nil(t, ac.GetPushConfig())
	})

	t.Run("alerts with no push or preset returns nil", func(t *testing.T) {
		ac := &AlertsConfig{}
		assert.Nil(t, ac.GetPushConfig())
	})

	t.Run("alerts with preset returns preset config", func(t *testing.T) {
		ac := &AlertsConfig{Preset: "claude-code"}
		pc := ac.GetPushConfig()
		require.NotNil(t, pc)
		assert.True(t, pc.MCPNotificationsEnabled())
		assert.False(t, pc.PTYInjectionEnabled())
	})

	t.Run("alerts with unknown preset returns nil", func(t *testing.T) {
		ac := &AlertsConfig{Preset: "unknown-preset"}
		pc := ac.GetPushConfig()
		assert.Nil(t, pc)
	})

	t.Run("explicit push takes precedence", func(t *testing.T) {
		v := false
		ac := &AlertsConfig{
			Preset: "universal",
			Push:   &PushConfig{PTYInjection: &v},
		}
		pc := ac.GetPushConfig()
		require.NotNil(t, pc)
		assert.True(t, pc.MCPNotificationsEnabled())
		assert.False(t, pc.PTYInjectionEnabled())
	})
}

func TestParseOutageHoldConfig(t *testing.T) {
	t.Run("full block parses every field", func(t *testing.T) {
		input := `alerts {
    outage-hold {
        enabled true
        window-ms 4000
        transport-err-threshold 2
        transport-err-window-ms 1500
        recovery-debounce-ms 250
        js-cascade-patterns "Failed to fetch" "WebSocket"
    }
}`
		cfg, err := ParseAgntConfig(input)
		require.NoError(t, err)
		require.NotNil(t, cfg.Alerts)
		require.NotNil(t, cfg.Alerts.OutageHold)

		oh := cfg.Alerts.OutageHold
		require.NotNil(t, oh.Enabled)
		assert.True(t, *oh.Enabled)
		assert.Equal(t, 4000, oh.WindowMs)
		assert.Equal(t, 2, oh.TransportErrThreshold)
		assert.Equal(t, 1500, oh.TransportErrWindowMs)
		assert.Equal(t, 250, oh.RecoveryDebounceMs)
		assert.Equal(t, []string{"Failed to fetch", "WebSocket"}, oh.JSCascadePatterns)
	})

	t.Run("missing block leaves nil", func(t *testing.T) {
		cfg, err := ParseAgntConfig("alerts {\n    enabled true\n}")
		require.NoError(t, err)
		require.NotNil(t, cfg.Alerts)
		assert.Nil(t, cfg.Alerts.OutageHold)
	})
}

func TestOutageHoldConfig_Defaults(t *testing.T) {
	var nilCfg *OutageHoldConfig
	assert.True(t, nilCfg.IsEnabled(), "nil config defaults to enabled")
	assert.Equal(t, 3*time.Second, nilCfg.GetWindow())
	assert.Equal(t, 1, nilCfg.GetTransportErrThreshold())
	assert.Equal(t, time.Second, nilCfg.GetTransportErrWindow())
	assert.Equal(t, 500*time.Millisecond, nilCfg.GetRecoveryDebounce())
	assert.Equal(t, DefaultJSCascadePatterns, nilCfg.GetJSCascadePatterns())

	disabled := false
	cfg := &OutageHoldConfig{Enabled: &disabled}
	assert.False(t, cfg.IsEnabled())

	cfg2 := &OutageHoldConfig{
		WindowMs:              0,  // zero falls back to default
		TransportErrThreshold: -1, // negative falls back to default
	}
	assert.Equal(t, 3*time.Second, cfg2.GetWindow())
	assert.Equal(t, 1, cfg2.GetTransportErrThreshold())

	cfg3 := &OutageHoldConfig{
		WindowMs:          1234,
		JSCascadePatterns: []string{"only-this"},
	}
	assert.Equal(t, 1234*time.Millisecond, cfg3.GetWindow())
	assert.Equal(t, []string{"only-this"}, cfg3.GetJSCascadePatterns())
}

func TestParseAgntConfig_AuthBreakout(t *testing.T) {
	// Absent block: disabled, but getters still return sane defaults.
	cfg, err := ParseAgntConfig("project {\n name \"x\"\n}")
	require.NoError(t, err)
	assert.Nil(t, cfg.AuthBreakout)
	assert.False(t, cfg.AuthBreakout.IsEnabled())
	assert.Equal(t, "popup", cfg.AuthBreakout.GetMode())
	assert.Equal(t, DefaultAuthBreakoutPatterns, cfg.AuthBreakout.GetPatterns())

	// Declared block: enabled by default, custom mode + patterns parsed.
	cfg, err = ParseAgntConfig(`
auth-breakout {
    mode "top"
    patterns "login.microsoftonline.com" "figma.com/oauth"
}`)
	require.NoError(t, err)
	require.NotNil(t, cfg.AuthBreakout)
	assert.True(t, cfg.AuthBreakout.IsEnabled())
	assert.Equal(t, "top", cfg.AuthBreakout.GetMode())
	assert.Equal(t, []string{"login.microsoftonline.com", "figma.com/oauth"}, cfg.AuthBreakout.GetPatterns())

	// enabled false disables while keeping the block.
	cfg, err = ParseAgntConfig("auth-breakout {\n enabled false\n}")
	require.NoError(t, err)
	assert.False(t, cfg.AuthBreakout.IsEnabled())

	// Bare enabled block: default mode popup + default IdP patterns.
	cfg, err = ParseAgntConfig("auth-breakout {\n enabled true\n}")
	require.NoError(t, err)
	assert.True(t, cfg.AuthBreakout.IsEnabled())
	assert.Equal(t, "popup", cfg.AuthBreakout.GetMode())
	assert.Equal(t, DefaultAuthBreakoutPatterns, cfg.AuthBreakout.GetPatterns())

	// Invalid mode fails loudly at parse time.
	_, err = ParseAgntConfig("auth-breakout {\n mode \"iframe\"\n}")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth-breakout")

	// Empty pattern fails loudly.
	_, err = ParseAgntConfig("auth-breakout {\n patterns \"\"\n}")
	require.Error(t, err)
}
