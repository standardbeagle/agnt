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
)

// AgntConfigFileName is the name of the agnt configuration file.
const AgntConfigFileName = ".agnt.kdl"

// AgntConfig represents the agnt configuration.
type AgntConfig struct {
	// Project metadata (optional, for documentation/info only)
	Project *AgntProjectMeta `kdl:"project"`

	// Scripts to manage
	Scripts map[string]*ScriptConfig `kdl:"scripts"`

	// Proxies to manage
	Proxies map[string]*ProxyConfig `kdl:"proxies"`

	// Hooks configuration
	Hooks *HooksConfig `kdl:"hooks"`

	// Toast notification settings
	Toast *ToastConfig `kdl:"toast"`
}

// AgntProjectMeta contains optional project metadata in .agnt.kdl.
// This is informational only and doesn't affect behavior.
type AgntProjectMeta struct {
	Type string `kdl:"type"`
	Name string `kdl:"name"`
}

// ScriptConfig defines a script to run.
type ScriptConfig struct {
	Command     string            `kdl:"command"`
	Args        []string          `kdl:"args"`
	Run         string            `kdl:"run"` // Shell command string (executed via sh -c)
	Autostart   bool              `kdl:"autostart"`
	URLMatchers []string          `kdl:"url-matchers"` // Patterns for URL detection: "local:{url}", "network:{url}"
	Env         map[string]string `kdl:"env"`
	Cwd         string            `kdl:"cwd"`
}

// ProxyConfig defines a reverse proxy to start.
type ProxyConfig struct {
	// Autostart indicates whether to start on session open (only for fully-specified proxies)
	Autostart bool `kdl:"autostart"`
	// MaxLogSize is the max number of log entries to keep
	MaxLogSize int `kdl:"max-log-size"`

	// Script links this proxy to a script for URL detection from output
	// When set, proxies are auto-created when the script outputs URLs (not auto-started)
	Script string `kdl:"script"`

	// Direct target configuration (mutually exclusive with Script)
	// URL is the full target URL (e.g., "http://localhost:3000")
	URL string `kdl:"url"`
	// Port is the target port (e.g., 3000) - shorthand for http://localhost:PORT
	Port int `kdl:"port"`
	// Host is the target host (default: localhost) - only used with Port
	Host string `kdl:"host"`

	// Bind is the address the proxy listens on
	// "127.0.0.1" (default, localhost only) or "0.0.0.0" (all interfaces for Tailscale/mobile)
	Bind string `kdl:"bind"`

	// Legacy fields (deprecated)
	// Target is the explicit target URL (use URL instead)
	Target string `kdl:"target"`
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
// Supports multiple formats:
//   - Simple: scripts { dev auto-start=true }
//   - Nested: scripts { dev { run "..." autostart true } }
//   - Proxy:  proxy "name" { script "dev" }
func ParseAgntConfig(data string) (*AgntConfig, error) {
	result, err := parseAgntConfigSimple(data)
	if result != nil {
		log.Printf("[DEBUG] ParseAgntConfig: parsed %d scripts, %d proxies", len(result.Scripts), len(result.Proxies))
	}
	return result, err
}

// parseAgntConfigSimple parses KDL formats:
//
//	Simple: scripts { dev auto-start=true }
//	Nested: scripts { dev { run "..." autostart true } }
//	Proxy:  proxy "name" { script "dev" }
func parseAgntConfigSimple(data string) (*AgntConfig, error) {
	cfg := DefaultAgntConfig()

	scanner := bufio.NewScanner(strings.NewReader(data))
	var currentBlock string
	var currentProxy *ProxyConfig
	var currentProxyName string
	var currentScript *ScriptConfig
	var currentScriptName string
	var blockDepth int // Track nesting depth

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip comments and empty lines
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}

		// Block start
		if strings.HasSuffix(line, "{") {
			blockDepth++
			if blockDepth == 1 {
				// Top-level block
				if strings.HasPrefix(line, "scripts") {
					currentBlock = "scripts"
				} else if strings.HasPrefix(line, "proxy") {
					currentBlock = "proxy"
					// Extract proxy ID: proxy "dev" {
					re := regexp.MustCompile(`proxy\s+"([^"]+)"`)
					if matches := re.FindStringSubmatch(line); len(matches) > 1 {
						currentProxyName = matches[1]
						currentProxy = &ProxyConfig{
							Host:      "localhost",
							Autostart: true, // proxies in config are autostart by default
						}
					}
				} else if strings.HasPrefix(line, "proxies") {
					currentBlock = "proxies"
				} else if strings.HasPrefix(line, "project") {
					currentBlock = "project"
				}
			} else if blockDepth == 2 {
				// Nested block inside scripts or proxies
				if currentBlock == "scripts" {
					// Nested script block: script-name {
					name := extractBlockName(line)
					if name != "" {
						currentScriptName = name
						currentScript = &ScriptConfig{}
					}
				} else if currentBlock == "proxies" {
					// Nested proxy block: proxy-name {
					name := extractBlockName(line)
					if name != "" {
						currentProxyName = name
						currentProxy = &ProxyConfig{
							Host:      "localhost",
							Autostart: true,
						}
					}
				}
			}
			continue
		}

		// Block end
		if line == "}" {
			if blockDepth == 2 {
				// End of nested block
				if currentBlock == "scripts" && currentScript != nil && currentScriptName != "" {
					cfg.Scripts[currentScriptName] = currentScript
					currentScript = nil
					currentScriptName = ""
				} else if (currentBlock == "proxies" || currentBlock == "proxy") && currentProxy != nil && currentProxyName != "" {
					cfg.Proxies[currentProxyName] = currentProxy
					currentProxy = nil
					currentProxyName = ""
				}
			} else if blockDepth == 1 {
				// End of top-level block
				if currentBlock == "proxy" && currentProxy != nil && currentProxyName != "" {
					cfg.Proxies[currentProxyName] = currentProxy
					currentProxy = nil
					currentProxyName = ""
				}
				currentBlock = ""
			}
			blockDepth--
			continue
		}

		// Parse content based on current block
		switch currentBlock {
		case "scripts":
			if currentScript != nil {
				// Inside nested script block - parse properties
				parseScriptProperty(line, currentScript)
			} else {
				// Simple format: dev auto-start=true
				parseScriptLine(line, cfg)
			}

		case "proxy":
			if currentProxy != nil {
				parseProxyProperty(line, currentProxy)
			}

		case "proxies":
			if currentProxy != nil {
				parseProxyProperty(line, currentProxy)
			}

		case "project":
			// Ignore project block contents
		}
	}

	return cfg, scanner.Err()
}

// extractBlockName extracts the name from a line like "script-name {" or "proxy-name {"
func extractBlockName(line string) string {
	// Remove trailing {
	line = strings.TrimSuffix(line, "{")
	line = strings.TrimSpace(line)

	// Handle quoted names: "script:name"
	if strings.HasPrefix(line, "\"") {
		re := regexp.MustCompile(`"([^"]+)"`)
		if matches := re.FindStringSubmatch(line); len(matches) > 1 {
			return matches[1]
		}
	}

	// Simple name
	parts := strings.Fields(line)
	if len(parts) >= 1 {
		return parts[0]
	}
	return ""
}

// parseScriptProperty parses a property line inside a script block.
func parseScriptProperty(line string, script *ScriptConfig) {
	// Match: property "value"
	stringRe := regexp.MustCompile(`^(\S+)\s+"([^"]+)"`)

	if matches := stringRe.FindStringSubmatch(line); len(matches) > 2 {
		switch matches[1] {
		case "run":
			script.Run = matches[2]
		case "command":
			script.Command = matches[2]
		case "cwd":
			script.Cwd = matches[2]
		case "url-matchers":
			script.URLMatchers = append(script.URLMatchers, matches[2])
		}
		return
	}

	// Boolean properties: autostart true
	if strings.HasPrefix(line, "autostart") || strings.HasPrefix(line, "auto-start") {
		script.Autostart = strings.Contains(line, "true")
	}
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
		case "target", "target-url":
			proxy.Target = matches[2]
		case "url":
			proxy.URL = matches[2]
		case "host":
			proxy.Host = matches[2]
		case "bind", "bind-address":
			proxy.Bind = matches[2]
		}
		return
	}

	if matches := intRe.FindStringSubmatch(line); len(matches) > 2 {
		val, _ := strconv.Atoi(matches[2])
		switch matches[1] {
		case "port", "fallback-port":
			// Both "port" and "fallback-port" set the target port
			proxy.Port = val
		case "max-log-size":
			proxy.MaxLogSize = val
		}
		return
	}

	// Boolean properties (handle both "autostart" and "auto-start")
	if strings.Contains(line, "autostart") || strings.Contains(line, "auto-start") {
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
    // Example: Simple shell command (recommended for quick commands)
    // serve {
    //     run "python3 -m http.server 9500"
    //     autostart true
    // }

    // Example: dev server with command/args (for complex configurations)
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
    //     run "go run ./cmd/server"
    //     autostart true
    // }

    // Monorepo example: Run frontend from subdirectory
    // frontend {
    //     command "npm"
    //     args "run" "dev"
    //     cwd "./packages/frontend"    // Runs in monorepo/packages/frontend
    //     autostart true
    // }

    // Monorepo example: Run backend from subdirectory
    // backend {
    //     run "go run ./cmd/server"
    //     cwd "./services/api"         // Runs in monorepo/services/api
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

    // Example: proxy accessible from Tailscale/mobile devices
    // mobile {
    //     target "http://localhost:3000"
    //     bind "0.0.0.0"    // Listen on all interfaces (Tailscale, LAN, etc.)
    //     autostart true
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
