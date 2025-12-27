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
