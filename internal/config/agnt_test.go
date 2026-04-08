package config

import (
	"os"
	"path/filepath"
	"regexp"
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
			name: "ai with all options",
			input: `ai {
    skill "code-review"
    system-prompt ""
    append-system-prompt "This is a Node.js project."
}`,
			expected: &AIConfig{
				Skill:              "code-review",
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
		assert.Contains(t, prompt, "get_errors")
		assert.Contains(t, prompt, "proxy")
		assert.Contains(t, prompt, "proc")
		assert.Contains(t, prompt, "responsive_audit")
		assert.Contains(t, prompt, "currentpage")
		assert.Contains(t, prompt, "Debugging Workflow")
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

func TestParseScriptHealthPatterns(t *testing.T) {
	input := `scripts {
    dev {
        run "npm run dev"
        error-pattern "ERROR|FAIL"
        healthy-pattern "ready in|compiled"
    }
    api {
        run "go run ."
    }
}`
	cfg, err := ParseAgntConfig(input)
	require.NoError(t, err)

	dev := cfg.Scripts["dev"]
	require.NotNil(t, dev)
	assert.Equal(t, "ERROR|FAIL", dev.ErrorPattern)
	assert.Equal(t, "ready in|compiled", dev.HealthyPattern)

	api := cfg.Scripts["api"]
	require.NotNil(t, api)
	assert.Empty(t, api.ErrorPattern)
	assert.Empty(t, api.HealthyPattern)
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
