// Package config contains configuration types for agnt.
package config

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	kdl "github.com/sblinch/kdl-go"
)

// AgntConfigFileName is the name of the agnt configuration file.
const AgntConfigFileName = ".agnt.kdl"

// AgntConfig represents the agnt configuration.
type AgntConfig struct {
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
	// Target is the explicit target URL (e.g., "http://localhost:3000")
	Target string `kdl:"target"`
	// Port is the proxy listen port (0 = auto-assign)
	Port int `kdl:"port"`
	// Autostart indicates whether to start on session open
	Autostart bool `kdl:"autostart"`
	// MaxLogSize is the max number of log entries to keep
	MaxLogSize int `kdl:"max-log-size"`

	// Script links this proxy to a script for port detection
	Script string `kdl:"script"`
	// PortDetect is the detection mode: "auto", "output", "pid", or a regex pattern
	PortDetect string `kdl:"port-detect"`
	// FallbackPort is used if port detection fails
	FallbackPort int `kdl:"fallback-port"`
	// Host is the target host (default: localhost)
	Host string `kdl:"host"`
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

// DefaultAgntConfig returns a config with sensible defaults.
func DefaultAgntConfig() *AgntConfig {
	return &AgntConfig{
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

// LoadAgntConfig loads configuration from the specified directory.
// It looks for .agnt.kdl in the directory and its parents.
func LoadAgntConfig(dir string) (*AgntConfig, error) {
	configPath := FindAgntConfigFile(dir)
	if configPath == "" {
		log.Printf("[DEBUG] LoadAgntConfig: no config file found for dir %s", dir)
		return DefaultAgntConfig(), nil
	}

	log.Printf("[DEBUG] LoadAgntConfig: found config file at %s", configPath)
	return LoadAgntConfigFile(configPath)
}

// FindAgntConfigFile searches for .agnt.kdl starting from dir and walking up.
func FindAgntConfigFile(dir string) string {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}

	for {
		configPath := filepath.Join(absDir, AgntConfigFileName)
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

// LoadAgntConfigFile loads configuration from a specific file.
func LoadAgntConfigFile(path string) (*AgntConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	return ParseAgntConfig(string(data))
}

// ParseAgntConfig parses KDL configuration data.
// It tries kdl-go first, then falls back to a simpler regex parser for
// formats like: scripts { dev auto-start=true } proxy "dev" { script "dev" }
func ParseAgntConfig(data string) (*AgntConfig, error) {
	cfg := DefaultAgntConfig()

	// Try kdl-go first
	if err := kdl.Unmarshal([]byte(data), cfg); err == nil {
		// Check if we got anything useful
		if len(cfg.Scripts) > 0 || len(cfg.Proxies) > 0 {
			log.Printf("[DEBUG] ParseAgntConfig: kdl-go parsed %d scripts, %d proxies", len(cfg.Scripts), len(cfg.Proxies))
			return cfg, nil
		}
		log.Printf("[DEBUG] ParseAgntConfig: kdl-go succeeded but got empty config, falling back to simple parser")
	} else {
		log.Printf("[DEBUG] ParseAgntConfig: kdl-go failed: %v, falling back to simple parser", err)
	}

	// Fallback to simpler parser for alternate format
	result, err := parseAgntConfigSimple(data)
	if result != nil {
		log.Printf("[DEBUG] ParseAgntConfig: simple parser got %d scripts, %d proxies", len(result.Scripts), len(result.Proxies))
	}
	return result, err
}

// parseAgntConfigSimple parses a simpler KDL format:
//
//	scripts { dev auto-start=true }
//	proxy "name" { script "dev" port-detect "auto" }
func parseAgntConfigSimple(data string) (*AgntConfig, error) {
	cfg := DefaultAgntConfig()

	scanner := bufio.NewScanner(strings.NewReader(data))
	var currentBlock string
	var currentProxy *ProxyConfig
	var currentProxyName string

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip comments and empty lines
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}

		// Block start
		if strings.HasSuffix(line, "{") {
			if strings.HasPrefix(line, "scripts") {
				currentBlock = "scripts"
			} else if strings.HasPrefix(line, "proxy") {
				currentBlock = "proxy"
				// Extract proxy ID: proxy "dev" {
				re := regexp.MustCompile(`proxy\s+"([^"]+)"`)
				if matches := re.FindStringSubmatch(line); len(matches) > 1 {
					currentProxyName = matches[1]
					currentProxy = &ProxyConfig{
						FallbackPort: 3000,
						Host:         "localhost",
						Autostart:    true, // proxies in config are autostart by default
					}
				}
			} else if strings.HasPrefix(line, "proxies") {
				currentBlock = "proxies"
			}
			continue
		}

		// Block end
		if line == "}" {
			if currentBlock == "proxy" && currentProxy != nil && currentProxyName != "" {
				cfg.Proxies[currentProxyName] = currentProxy
				currentProxy = nil
				currentProxyName = ""
			}
			currentBlock = ""
			continue
		}

		// Parse content based on current block
		switch currentBlock {
		case "scripts":
			parseScriptLine(line, cfg)

		case "proxy":
			if currentProxy != nil {
				parseProxyProperty(line, currentProxy)
			}

		case "proxies":
			// Nested proxy block in proxies { name { ... } }
			if strings.HasSuffix(line, "{") {
				// Start of nested block: extract name
				parts := strings.Fields(line)
				if len(parts) >= 1 {
					currentProxyName = strings.Trim(parts[0], "\"")
					currentProxy = &ProxyConfig{
						FallbackPort: 3000,
						Host:         "localhost",
						Autostart:    true,
					}
					currentBlock = "proxy" // Switch to proxy mode
				}
			}
		}
	}

	return cfg, scanner.Err()
}

// parseScriptLine parses a script line like: dev auto-start=true
func parseScriptLine(line string, cfg *AgntConfig) {
	// Format: script-name auto-start=true
	// or: "script:name" auto-start=true
	parts := strings.Fields(line)
	if len(parts) < 1 {
		return
	}

	name := parts[0]
	// Handle quoted names
	if strings.HasPrefix(line, "\"") {
		re := regexp.MustCompile(`"([^"]+)"`)
		if matches := re.FindStringSubmatch(line); len(matches) > 1 {
			name = matches[1]
		}
	}

	autoStart := false
	for _, part := range parts[1:] {
		if part == "auto-start=true" || part == "autostart=true" {
			autoStart = true
		}
	}

	cfg.Scripts[name] = &ScriptConfig{
		Autostart: autoStart,
	}
}

// parseProxyProperty parses a property line inside a proxy block.
func parseProxyProperty(line string, proxy *ProxyConfig) {
	// Match: property "value" or property value
	stringRe := regexp.MustCompile(`^(\S+)\s+"([^"]+)"`)
	intRe := regexp.MustCompile(`^(\S+)\s+(\d+)`)

	if matches := stringRe.FindStringSubmatch(line); len(matches) > 2 {
		switch matches[1] {
		case "script":
			proxy.Script = matches[2]
		case "port-detect":
			proxy.PortDetect = matches[2]
		case "target", "target-url":
			proxy.Target = matches[2]
		case "host":
			proxy.Host = matches[2]
		}
		return
	}

	if matches := intRe.FindStringSubmatch(line); len(matches) > 2 {
		val, _ := strconv.Atoi(matches[2])
		switch matches[1] {
		case "fallback-port":
			proxy.FallbackPort = val
		case "port":
			proxy.Port = val
		case "max-log-size":
			proxy.MaxLogSize = val
		}
		return
	}

	// Boolean properties
	if strings.Contains(line, "autostart") {
		proxy.Autostart = strings.Contains(line, "true")
	}
}

// GetAutostartScripts returns scripts configured for autostart.
func (c *AgntConfig) GetAutostartScripts() map[string]*ScriptConfig {
	result := make(map[string]*ScriptConfig)
	for name, script := range c.Scripts {
		if script.Autostart {
			result[name] = script
		}
	}
	return result
}

// GetAutostartProxies returns proxies configured for autostart.
func (c *AgntConfig) GetAutostartProxies() map[string]*ProxyConfig {
	result := make(map[string]*ProxyConfig)
	for name, proxy := range c.Proxies {
		if proxy.Autostart {
			result[name] = proxy
		}
	}
	return result
}

// WriteDefaultAgntConfig writes a default configuration file with documentation.
func WriteDefaultAgntConfig(path string) error {
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
