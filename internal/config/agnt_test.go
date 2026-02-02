package config

import (
	"os"
	"path/filepath"
	"testing"

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
