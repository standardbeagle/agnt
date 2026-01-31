package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAgntConfigSimple(t *testing.T) {
	input := `// .agnt.kdl - agnt project configuration
// Session-aware configuration for development

// Scripts to manage
scripts {
    // Next.js development server with Turbopack on port 3847
    // Auto-start so proxy can connect to it
    dev auto-start=true
    // Test watcher - start manually when needed
    "test:watch"
}

// Proxy configuration for debugging
proxy "dev" {
    // Target the actual port directly (don't watch script)
    // This avoids circular dependency: dev script → proxy watches script → proxy needs script
    target "http://localhost:3847"
}
`

	cfg, err := ParseAgntConfig(input)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify scripts
	assert.Len(t, cfg.Scripts, 2, "should have 2 scripts: dev and test:watch")

	dev, ok := cfg.Scripts["dev"]
	assert.True(t, ok, "should have 'dev' script")
	if ok {
		assert.True(t, dev.Autostart, "dev script should have Autostart=true")
	}

	testWatch, ok := cfg.Scripts["test:watch"]
	assert.True(t, ok, "should have 'test:watch' script")
	if ok {
		assert.False(t, testWatch.Autostart, "test:watch should NOT have Autostart=true")
	}

	// Verify proxies
	assert.Len(t, cfg.Proxies, 1, "should have 1 proxy: dev")

	proxy, ok := cfg.Proxies["dev"]
	assert.True(t, ok, "should have 'dev' proxy")
	if ok {
		assert.Equal(t, "http://localhost:3847", proxy.Target, "proxy target should be http://localhost:3847")
		assert.True(t, proxy.Autostart, "proxy should have Autostart=true by default")
	}

	// Verify GetAutostartScripts
	autostartScripts := cfg.GetAutostartScripts()
	assert.Len(t, autostartScripts, 1, "should have 1 autostart script: dev")
	_, ok = autostartScripts["dev"]
	assert.True(t, ok, "dev should be in autostart scripts")

	// Verify GetAutostartProxies
	autostartProxies := cfg.GetAutostartProxies()
	assert.Len(t, autostartProxies, 1, "should have 1 autostart proxy: dev")
	_, ok = autostartProxies["dev"]
	assert.True(t, ok, "dev should be in autostart proxies")
}

func TestParseScriptLine(t *testing.T) {
	tests := []struct {
		name              string
		line              string
		expectedName      string
		expectedAutostart bool
	}{
		{
			name:              "simple script with autostart",
			line:              "dev auto-start=true",
			expectedName:      "dev",
			expectedAutostart: true,
		},
		{
			name:              "simple script without autostart",
			line:              "dev",
			expectedName:      "dev",
			expectedAutostart: false,
		},
		{
			name:              "quoted script name",
			line:              `"test:watch"`,
			expectedName:      "test:watch",
			expectedAutostart: false,
		},
		{
			name:              "quoted script with autostart",
			line:              `"test:watch" auto-start=true`,
			expectedName:      "test:watch",
			expectedAutostart: true,
		},
		{
			name:              "alternate autostart syntax",
			line:              "dev autostart=true",
			expectedName:      "dev",
			expectedAutostart: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultAgntConfig()
			parseScriptLine(tt.line, cfg)

			script, ok := cfg.Scripts[tt.expectedName]
			assert.True(t, ok, "should have script named %s", tt.expectedName)
			if ok {
				assert.Equal(t, tt.expectedAutostart, script.Autostart, "autostart mismatch for %s", tt.expectedName)
			}
		})
	}
}

func TestLoadAgntConfig(t *testing.T) {
	// Create temp directory with .agnt.kdl
	tmpDir := t.TempDir()

	configContent := `// .agnt.kdl
scripts {
    dev auto-start=true
    "test:watch"
}

proxy "dev" {
    target "http://localhost:3847"
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
		assert.True(t, dev.Autostart)
	}

	// Verify proxies loaded
	assert.Len(t, cfg.Proxies, 1)
	proxy, ok := cfg.Proxies["dev"]
	assert.True(t, ok)
	if ok {
		assert.Equal(t, "http://localhost:3847", proxy.Target)
	}

	// Verify GetAutostartScripts
	autostartScripts := cfg.GetAutostartScripts()
	assert.Len(t, autostartScripts, 1)
	_, ok = autostartScripts["dev"]
	assert.True(t, ok)
}

func TestParseAgntConfigWithRun(t *testing.T) {
	input := `scripts {
    serve {
        run "python3 -m http.server 9500"
        autostart true
    }
    build {
        run "npm run build && npm run test"
        autostart false
    }
}`

	cfg, err := ParseAgntConfig(input)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify scripts
	assert.Len(t, cfg.Scripts, 2, "should have 2 scripts")

	serve, ok := cfg.Scripts["serve"]
	assert.True(t, ok, "should have 'serve' script")
	if ok {
		assert.Equal(t, "python3 -m http.server 9500", serve.Run, "serve.Run should match")
		assert.True(t, serve.Autostart, "serve should have Autostart=true")
		assert.Empty(t, serve.Command, "serve.Command should be empty when using run")
	}

	build, ok := cfg.Scripts["build"]
	assert.True(t, ok, "should have 'build' script")
	if ok {
		assert.Equal(t, "npm run build && npm run test", build.Run, "build.Run should match")
		assert.False(t, build.Autostart, "build should have Autostart=false")
	}
}

func TestFindAgntConfigFile(t *testing.T) {
	// Create temp directory with nested subdirectory
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "src", "components")
	err := os.MkdirAll(subDir, 0755)
	require.NoError(t, err)

	// Create .agnt.kdl in root
	configContent := `scripts { dev auto-start=true }`
	configPath := filepath.Join(tmpDir, AgntConfigFileName)
	err = os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	// Find from subdirectory should walk up and find it
	found := FindAgntConfigFile(subDir)
	assert.Equal(t, configPath, found)

	// Find from root should find it directly
	found = FindAgntConfigFile(tmpDir)
	assert.Equal(t, configPath, found)

	// Find from non-existent directory should return empty
	found = FindAgntConfigFile("/nonexistent/path")
	assert.Equal(t, "", found)
}

func TestParseBeagleTermConfig(t *testing.T) {
	input := `// Beagle Terminal - Wails Desktop Application
// Auto-generated by setup-project

project {
    type "wails"
    name "beagle-term"
}

// Frontend development server (Vite)
scripts {
    // Frontend dev server - runs in frontend directory (standalone)
    frontend-dev {
        run "npm run dev"
        cwd "frontend"
    }

    // Wails development mode - launches full app with hot reload
    wails-dev {
        run "wails dev"
        autostart true
        // Wails outputs: "Using DevServer URL: http://localhost:34115"
        url-matchers "DevServer URL:\\s*{url}"
    }

    // Go backend scripts
    build {
        run "wails build"
    }

    test {
        run "go test ./..."
    }

    lint {
        run "golangci-lint run ./..."
    }
}

// Proxy configuration for browser debugging
proxy "wails-dev" {
    // Link to wails-dev script - proxy created when dev URL is detected
    script "wails-dev"

    // Wails dev server typically runs on port 34115
    fallback-port 34115

    // Enable WebSocket proxying for HMR
    websocket true
}
`

	cfg, err := ParseAgntConfig(input)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify scripts
	assert.Len(t, cfg.Scripts, 5, "should have 5 scripts")

	// Check wails-dev script has autostart true
	wailsDev, ok := cfg.Scripts["wails-dev"]
	assert.True(t, ok, "should have 'wails-dev' script")
	if ok {
		assert.Equal(t, "wails dev", wailsDev.Run)
		assert.True(t, wailsDev.Autostart, "wails-dev should have Autostart=true")
	}

	// Check frontend-dev script does not have autostart
	frontendDev, ok := cfg.Scripts["frontend-dev"]
	assert.True(t, ok, "should have 'frontend-dev' script")
	if ok {
		assert.Equal(t, "npm run dev", frontendDev.Run)
		assert.Equal(t, "frontend", frontendDev.Cwd)
		assert.False(t, frontendDev.Autostart, "frontend-dev should have Autostart=false")
	}

	// Verify GetAutostartScripts returns wails-dev
	autostartScripts := cfg.GetAutostartScripts()
	assert.Len(t, autostartScripts, 1, "should have 1 autostart script: wails-dev")
	_, ok = autostartScripts["wails-dev"]
	assert.True(t, ok, "wails-dev should be in autostart scripts")

	// Verify proxy
	assert.Len(t, cfg.Proxies, 1, "should have 1 proxy")
	proxy, ok := cfg.Proxies["wails-dev"]
	assert.True(t, ok, "should have 'wails-dev' proxy")
	if ok {
		assert.Equal(t, "wails-dev", proxy.Script)
		assert.Equal(t, 34115, proxy.Port)
	}
}

func TestParseProxyWithBind(t *testing.T) {
	input := `proxy "mobile" {
    target "http://localhost:3000"
    bind "0.0.0.0"
    autostart true
}

proxy "tailscale" {
    target "http://localhost:8080"
    bind-address "100.64.0.1"
}
`
	cfg, err := ParseAgntConfig(input)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify mobile proxy with 0.0.0.0 bind
	mobile, ok := cfg.Proxies["mobile"]
	assert.True(t, ok, "should have 'mobile' proxy")
	if ok {
		assert.Equal(t, "http://localhost:3000", mobile.Target)
		assert.Equal(t, "0.0.0.0", mobile.Bind, "mobile proxy should bind to 0.0.0.0")
		assert.True(t, mobile.Autostart)
	}

	// Verify tailscale proxy with specific IP bind (using bind-address alias)
	tailscale, ok := cfg.Proxies["tailscale"]
	assert.True(t, ok, "should have 'tailscale' proxy")
	if ok {
		assert.Equal(t, "http://localhost:8080", tailscale.Target)
		assert.Equal(t, "100.64.0.1", tailscale.Bind, "tailscale proxy should bind to 100.64.0.1")
	}
}

// TestParseAgntConfigFormats tests all supported .agnt.kdl format combinations
func TestParseAgntConfigFormats(t *testing.T) {
	tests := []struct {
		name            string
		input           string
		expectScripts   map[string]ScriptConfig
		expectProxies   map[string]ProxyConfig
		expectAutostart int // number of autostart scripts
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
			name: "simple script format with auto-start=true",
			input: `scripts {
    dev auto-start=true
}`,
			expectScripts: map[string]ScriptConfig{
				"dev": {Autostart: true},
			},
			expectAutostart: 1,
		},
		{
			name: "simple script format with autostart=true",
			input: `scripts {
    dev autostart=true
}`,
			expectScripts: map[string]ScriptConfig{
				"dev": {Autostart: true},
			},
			expectAutostart: 1,
		},
		{
			name: "simple script format without autostart",
			input: `scripts {
    build
    test
}`,
			expectScripts: map[string]ScriptConfig{
				"build": {Autostart: false},
				"test":  {Autostart: false},
			},
			expectAutostart: 0,
		},
		{
			name: "simple script format with quoted name",
			input: `scripts {
    "test:watch" auto-start=true
    "npm:build"
}`,
			expectScripts: map[string]ScriptConfig{
				"test:watch": {Autostart: true},
				"npm:build":  {Autostart: false},
			},
			expectAutostart: 1,
		},
		{
			name: "nested script format with run",
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
			name: "nested script format with auto-start",
			input: `scripts {
    dev {
        run "npm run dev"
        auto-start true
    }
}`,
			expectScripts: map[string]ScriptConfig{
				"dev": {Run: "npm run dev", Autostart: true},
			},
			expectAutostart: 1,
		},
		{
			name: "nested script format with autostart false",
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
			name: "nested script format with command",
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
			name: "nested script format with cwd",
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
			name: "nested script format with url-matchers",
			input: `scripts {
    dev {
        run "npm run dev"
        url-matchers "(Local|Network):\\s*{url}"
        autostart true
    }
}`,
			// Note: backslashes are preserved as-is from the KDL input
			expectScripts: map[string]ScriptConfig{
				"dev": {Run: "npm run dev", URLMatchers: []string{`(Local|Network):\\s*{url}`}, Autostart: true},
			},
			expectAutostart: 1,
		},
		{
			name: "mixed simple and nested scripts",
			input: `scripts {
    simple-script auto-start=true
    nested-script {
        run "npm run dev"
        autostart true
    }
    another-simple
}`,
			expectScripts: map[string]ScriptConfig{
				"simple-script":  {Autostart: true},
				"nested-script":  {Run: "npm run dev", Autostart: true},
				"another-simple": {Autostart: false},
			},
			expectAutostart: 2,
		},
		{
			name: "singular proxy format with target",
			input: `proxy "dev" {
    target "http://localhost:3000"
}`,
			expectProxies: map[string]ProxyConfig{
				"dev": {Target: "http://localhost:3000", Host: "localhost", Autostart: true},
			},
		},
		{
			name: "singular proxy format with url",
			input: `proxy "dev" {
    url "http://localhost:3000"
}`,
			expectProxies: map[string]ProxyConfig{
				"dev": {URL: "http://localhost:3000", Host: "localhost", Autostart: true},
			},
		},
		{
			name: "singular proxy format with script",
			input: `proxy "dev" {
    script "dev-script"
}`,
			expectProxies: map[string]ProxyConfig{
				"dev": {Script: "dev-script", Host: "localhost", Autostart: true},
			},
		},
		{
			name: "singular proxy format with port",
			input: `proxy "api" {
    port 8080
}`,
			expectProxies: map[string]ProxyConfig{
				"api": {Port: 8080, Host: "localhost", Autostart: true},
			},
		},
		{
			name: "singular proxy format with fallback-port",
			input: `proxy "api" {
    fallback-port 8080
}`,
			expectProxies: map[string]ProxyConfig{
				"api": {Port: 8080, Host: "localhost", Autostart: true},
			},
		},
		{
			name: "singular proxy format with bind",
			input: `proxy "mobile" {
    target "http://localhost:3000"
    bind "0.0.0.0"
}`,
			expectProxies: map[string]ProxyConfig{
				"mobile": {Target: "http://localhost:3000", Bind: "0.0.0.0", Host: "localhost", Autostart: true},
			},
		},
		{
			name: "singular proxy format with bind-address",
			input: `proxy "tailscale" {
    target "http://localhost:3000"
    bind-address "100.64.0.1"
}`,
			expectProxies: map[string]ProxyConfig{
				"tailscale": {Target: "http://localhost:3000", Bind: "100.64.0.1", Host: "localhost", Autostart: true},
			},
		},
		{
			name: "singular proxy format with host",
			input: `proxy "remote" {
    port 8080
    host "192.168.1.100"
}`,
			expectProxies: map[string]ProxyConfig{
				"remote": {Port: 8080, Host: "192.168.1.100", Autostart: true},
			},
		},
		{
			name: "singular proxy format with max-log-size",
			input: `proxy "verbose" {
    target "http://localhost:3000"
    max-log-size 5000
}`,
			expectProxies: map[string]ProxyConfig{
				"verbose": {Target: "http://localhost:3000", MaxLogSize: 5000, Host: "localhost", Autostart: true},
			},
		},
		{
			name: "singular proxy format with autostart false",
			input: `proxy "manual" {
    target "http://localhost:3000"
    autostart false
}`,
			expectProxies: map[string]ProxyConfig{
				"manual": {Target: "http://localhost:3000", Host: "localhost", Autostart: false},
			},
		},
		{
			name: "multiple singular proxies",
			input: `proxy "frontend" {
    target "http://localhost:3000"
}
proxy "backend" {
    target "http://localhost:8080"
}`,
			expectProxies: map[string]ProxyConfig{
				"frontend": {Target: "http://localhost:3000", Host: "localhost", Autostart: true},
				"backend":  {Target: "http://localhost:8080", Host: "localhost", Autostart: true},
			},
		},
		{
			name: "plural proxies format",
			input: `proxies {
    frontend {
        target "http://localhost:3000"
    }
    backend {
        target "http://localhost:8080"
    }
}`,
			expectProxies: map[string]ProxyConfig{
				"frontend": {Target: "http://localhost:3000", Host: "localhost", Autostart: true},
				"backend":  {Target: "http://localhost:8080", Host: "localhost", Autostart: true},
			},
		},
		{
			name: "project block ignored",
			input: `project {
    type "wails"
    name "my-app"
}
scripts {
    dev auto-start=true
}`,
			expectScripts: map[string]ScriptConfig{
				"dev": {Autostart: true},
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
proxy "dev" {
    script "dev"
}`,
			expectScripts: map[string]ScriptConfig{
				"dev":   {Run: "npm run dev", Autostart: true},
				"build": {Run: "npm run build", Autostart: false},
			},
			expectProxies: map[string]ProxyConfig{
				"dev": {Script: "dev", Host: "localhost", Autostart: true},
			},
			expectAutostart: 1,
		},
		{
			name: "full config with all sections",
			input: `// Full configuration example
project {
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

proxy "frontend" {
    script "dev"
    fallback-port 3000
}

proxy "backend" {
    target "http://localhost:8080"
    bind "0.0.0.0"
}
`,
			expectScripts: map[string]ScriptConfig{
				"dev":   {Run: "npm run dev", Cwd: "frontend", Autostart: true},
				"api":   {Run: "go run ./cmd/server", Autostart: true},
				"build": {Run: "npm run build", Autostart: false},
			},
			expectProxies: map[string]ProxyConfig{
				"frontend": {Script: "dev", Port: 3000, Host: "localhost", Autostart: true},
				"backend":  {Target: "http://localhost:8080", Bind: "0.0.0.0", Host: "localhost", Autostart: true},
			},
			expectAutostart: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseAgntConfig(tt.input)
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
				if expected.URL != "" {
					assert.Equal(t, expected.URL, actual.URL, "proxy %s: URL mismatch", name)
				}
				if expected.Script != "" {
					assert.Equal(t, expected.Script, actual.Script, "proxy %s: Script mismatch", name)
				}
				if expected.Port != 0 {
					assert.Equal(t, expected.Port, actual.Port, "proxy %s: Port mismatch", name)
				}
				if expected.Host != "" {
					assert.Equal(t, expected.Host, actual.Host, "proxy %s: Host mismatch", name)
				}
				if expected.Bind != "" {
					assert.Equal(t, expected.Bind, actual.Bind, "proxy %s: Bind mismatch", name)
				}
				if expected.MaxLogSize != 0 {
					assert.Equal(t, expected.MaxLogSize, actual.MaxLogSize, "proxy %s: MaxLogSize mismatch", name)
				}
				assert.Equal(t, expected.Autostart, actual.Autostart, "proxy %s: Autostart mismatch", name)
			}

			// Check autostart scripts count
			autostartScripts := cfg.GetAutostartScripts()
			assert.Len(t, autostartScripts, tt.expectAutostart, "autostart script count mismatch")
		})
	}
}

// TestExtractBlockName tests the block name extraction helper
func TestExtractBlockName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"dev {", "dev"},
		{"frontend-dev {", "frontend-dev"},
		{"my_script {", "my_script"},
		{`"test:watch" {`, "test:watch"},
		{`"npm:build" {`, "npm:build"},
		{"  dev  {", "dev"},
		{"{", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := extractBlockName(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestParseScriptProperty tests script property parsing
func TestParseScriptProperty(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		expected ScriptConfig
	}{
		{
			name:     "run property",
			line:     `run "npm run dev"`,
			expected: ScriptConfig{Run: "npm run dev"},
		},
		{
			name:     "command property",
			line:     `command "npm"`,
			expected: ScriptConfig{Command: "npm"},
		},
		{
			name:     "cwd property",
			line:     `cwd "frontend"`,
			expected: ScriptConfig{Cwd: "frontend"},
		},
		{
			name:     "url-matchers property",
			line:     `url-matchers "(Local|Network):\\s*{url}"`,
			expected: ScriptConfig{URLMatchers: []string{`(Local|Network):\\s*{url}`}},
		},
		{
			name:     "autostart true",
			line:     `autostart true`,
			expected: ScriptConfig{Autostart: true},
		},
		{
			name:     "autostart false",
			line:     `autostart false`,
			expected: ScriptConfig{Autostart: false},
		},
		{
			name:     "auto-start true",
			line:     `auto-start true`,
			expected: ScriptConfig{Autostart: true},
		},
		{
			name:     "auto-start false",
			line:     `auto-start false`,
			expected: ScriptConfig{Autostart: false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			script := &ScriptConfig{}
			parseScriptProperty(tt.line, script)

			if tt.expected.Run != "" {
				assert.Equal(t, tt.expected.Run, script.Run)
			}
			if tt.expected.Command != "" {
				assert.Equal(t, tt.expected.Command, script.Command)
			}
			if tt.expected.Cwd != "" {
				assert.Equal(t, tt.expected.Cwd, script.Cwd)
			}
			if len(tt.expected.URLMatchers) > 0 {
				assert.Equal(t, tt.expected.URLMatchers, script.URLMatchers)
			}
			assert.Equal(t, tt.expected.Autostart, script.Autostart)
		})
	}
}

// TestParseProxyProperty tests proxy property parsing
func TestParseProxyProperty(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		expected ProxyConfig
	}{
		{
			name:     "script property",
			line:     `script "dev"`,
			expected: ProxyConfig{Script: "dev"},
		},
		{
			name:     "target property",
			line:     `target "http://localhost:3000"`,
			expected: ProxyConfig{Target: "http://localhost:3000"},
		},
		{
			name:     "target-url property",
			line:     `target-url "http://localhost:3000"`,
			expected: ProxyConfig{Target: "http://localhost:3000"},
		},
		{
			name:     "url property",
			line:     `url "http://localhost:3000"`,
			expected: ProxyConfig{URL: "http://localhost:3000"},
		},
		{
			name:     "host property",
			line:     `host "192.168.1.100"`,
			expected: ProxyConfig{Host: "192.168.1.100"},
		},
		{
			name:     "bind property",
			line:     `bind "0.0.0.0"`,
			expected: ProxyConfig{Bind: "0.0.0.0"},
		},
		{
			name:     "bind-address property",
			line:     `bind-address "100.64.0.1"`,
			expected: ProxyConfig{Bind: "100.64.0.1"},
		},
		{
			name:     "port property",
			line:     `port 8080`,
			expected: ProxyConfig{Port: 8080},
		},
		{
			name:     "fallback-port property",
			line:     `fallback-port 3000`,
			expected: ProxyConfig{Port: 3000},
		},
		{
			name:     "max-log-size property",
			line:     `max-log-size 5000`,
			expected: ProxyConfig{MaxLogSize: 5000},
		},
		{
			name:     "autostart true",
			line:     `autostart true`,
			expected: ProxyConfig{Autostart: true},
		},
		{
			name:     "autostart false",
			line:     `autostart false`,
			expected: ProxyConfig{Autostart: false},
		},
		{
			name:     "auto-start true",
			line:     `auto-start true`,
			expected: ProxyConfig{Autostart: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxy := &ProxyConfig{}
			parseProxyProperty(tt.line, proxy)

			if tt.expected.Script != "" {
				assert.Equal(t, tt.expected.Script, proxy.Script)
			}
			if tt.expected.Target != "" {
				assert.Equal(t, tt.expected.Target, proxy.Target)
			}
			if tt.expected.URL != "" {
				assert.Equal(t, tt.expected.URL, proxy.URL)
			}
			if tt.expected.Host != "" {
				assert.Equal(t, tt.expected.Host, proxy.Host)
			}
			if tt.expected.Bind != "" {
				assert.Equal(t, tt.expected.Bind, proxy.Bind)
			}
			if tt.expected.Port != 0 {
				assert.Equal(t, tt.expected.Port, proxy.Port)
			}
			if tt.expected.MaxLogSize != 0 {
				assert.Equal(t, tt.expected.MaxLogSize, proxy.MaxLogSize)
			}
			assert.Equal(t, tt.expected.Autostart, proxy.Autostart)
		})
	}
}

// TestGetAutostartMethods tests GetAutostartScripts and GetAutostartProxies
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

	// Test GetAutostartProxies
	autostartProxies := cfg.GetAutostartProxies()
	assert.Len(t, autostartProxies, 2)
	assert.Contains(t, autostartProxies, "frontend")
	assert.Contains(t, autostartProxies, "backend")
	assert.NotContains(t, autostartProxies, "manual")
}

// TestDefaultAgntConfig tests that defaults are properly set
func TestDefaultAgntConfig(t *testing.T) {
	cfg := DefaultAgntConfig()

	assert.NotNil(t, cfg.Scripts)
	assert.Len(t, cfg.Scripts, 0)

	assert.NotNil(t, cfg.Proxies)
	assert.Len(t, cfg.Proxies, 0)

	assert.NotNil(t, cfg.Hooks)
	assert.NotNil(t, cfg.Hooks.OnResponse)
	assert.True(t, cfg.Hooks.OnResponse.Toast)
	assert.True(t, cfg.Hooks.OnResponse.Indicator)
	assert.False(t, cfg.Hooks.OnResponse.Sound)

	assert.NotNil(t, cfg.Toast)
	assert.Equal(t, 4000, cfg.Toast.Duration)
	assert.Equal(t, "bottom-right", cfg.Toast.Position)
	assert.Equal(t, 3, cfg.Toast.MaxVisible)
}
