// Package agnt provides configuration and runtime management for the agnt CLI.
package agnt

import (
	"fmt"
	"os"
	"path/filepath"

	kdl "github.com/sblinch/kdl-go"
)

// ConfigFileName is the name of the agnt configuration file.
const ConfigFileName = ".agnt.kdl"

// Config represents the agnt configuration.
type Config struct {
	// Scripts to manage
	Scripts map[string]*ScriptConfig `kdl:"scripts"`

	// Proxies to manage
	Proxies map[string]*ProxyConfig `kdl:"proxies"`

	// Hooks configuration
	Hooks *HooksConfig `kdl:"hooks"`

	// Toast notification settings
	Toast *ToastConfig `kdl:"toast"`
}

// ScriptConfig defines a script to run.
type ScriptConfig struct {
	Command   string            `kdl:"command"`
	Args      []string          `kdl:"args"`
	Autostart bool              `kdl:"autostart"`
	Env       map[string]string `kdl:"env"`
	Cwd       string            `kdl:"cwd"`
}

// ProxyConfig defines a reverse proxy to start.
type ProxyConfig struct {
	Target     string `kdl:"target"`
	Port       int    `kdl:"port"`
	Autostart  bool   `kdl:"autostart"`
	MaxLogSize int    `kdl:"max-log-size"`
}

// HooksConfig defines hook behavior.
type HooksConfig struct {
	// OnResponse controls what happens when Claude responds
	OnResponse *ResponseHookConfig `kdl:"on-response"`
}

// ResponseHookConfig controls response notification behavior.
type ResponseHookConfig struct {
	// Toast shows a toast notification in the browser
	Toast bool `kdl:"toast"`
	// Indicator updates the bug indicator
	Indicator bool `kdl:"indicator"`
	// Sound plays a notification sound
	Sound bool `kdl:"sound"`
}

// ToastConfig configures toast notifications.
type ToastConfig struct {
	// Duration in milliseconds (default 4000)
	Duration int `kdl:"duration"`
	// Position: "top-right", "top-left", "bottom-right", "bottom-left"
	Position string `kdl:"position"`
	// MaxVisible is the max number of visible toasts (default 3)
	MaxVisible int `kdl:"max-visible"`
}

// DefaultConfig returns a config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Scripts: make(map[string]*ScriptConfig),
		Proxies: make(map[string]*ProxyConfig),
		Hooks: &HooksConfig{
			OnResponse: &ResponseHookConfig{
				Toast:     true,
				Indicator: true,
				Sound:     false,
			},
		},
		Toast: &ToastConfig{
			Duration:   4000,
			Position:   "bottom-right",
			MaxVisible: 3,
		},
	}
}

// LoadConfig loads configuration from the specified directory.
// It looks for .agnt.kdl in the directory and its parents.
func LoadConfig(dir string) (*Config, error) {
	configPath := FindConfigFile(dir)
	if configPath == "" {
		return DefaultConfig(), nil
	}

	return LoadConfigFile(configPath)
}

// FindConfigFile searches for .agnt.kdl starting from dir and walking up.
func FindConfigFile(dir string) string {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}

	for {
		configPath := filepath.Join(absDir, ConfigFileName)
		if _, err := os.Stat(configPath); err == nil {
			return configPath
		}

		parent := filepath.Dir(absDir)
		if parent == absDir {
			// Reached root
			break
		}
		absDir = parent
	}

	return ""
}

// LoadConfigFile loads configuration from a specific file.
func LoadConfigFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	return ParseConfig(string(data))
}

// ParseConfig parses KDL configuration data.
func ParseConfig(data string) (*Config, error) {
	cfg := DefaultConfig()

	if err := kdl.Unmarshal([]byte(data), cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	return cfg, nil
}

// GetAutostartScripts returns scripts configured for autostart.
func (c *Config) GetAutostartScripts() map[string]*ScriptConfig {
	result := make(map[string]*ScriptConfig)
	for name, script := range c.Scripts {
		if script.Autostart {
			result[name] = script
		}
	}
	return result
}

// GetAutostartProxies returns proxies configured for autostart.
func (c *Config) GetAutostartProxies() map[string]*ProxyConfig {
	result := make(map[string]*ProxyConfig)
	for name, proxy := range c.Proxies {
		if proxy.Autostart {
			result[name] = proxy
		}
	}
	return result
}

// WriteDefaultConfig writes a default configuration file with documentation.
func WriteDefaultConfig(path string) error {
	defaultKDL := `// Agnt Configuration
// This file configures scripts and proxies to auto-start with agnt run

// Scripts to run (use daemon process management)
scripts {
    // Example: dev server
    // dev {
    //     command "npm"
    //     args "run" "dev"
    //     autostart true
    //     env {
    //         NODE_ENV "development"
    //     }
    // }

    // Example: API server
    // api {
    //     command "go"
    //     args "run" "./cmd/server"
    //     autostart true
    // }
}

// Reverse proxies to start
proxies {
    // Example: frontend proxy
    // frontend {
    //     target "http://localhost:3000"
    //     autostart true
    // }

    // Example: API proxy with custom port
    // api {
    //     target "http://localhost:8080"
    //     port 18080
    //     autostart true
    //     max-log-size 2000
    // }
}

// Hook configuration for notifications
hooks {
    // What to do when Claude responds
    on-response {
        toast true      // Show toast notification in browser
        indicator true  // Flash the bug indicator
        sound false     // Play notification sound
    }
}

// Toast notification settings
toast {
    duration 4000           // Duration in ms
    position "bottom-right" // top-right, top-left, bottom-right, bottom-left
    max-visible 3           // Max simultaneous toasts
}
`
	return os.WriteFile(path, []byte(defaultKDL), 0644)
}
