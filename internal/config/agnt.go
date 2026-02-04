// Package config contains configuration types for agnt.
package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	kdl "github.com/sblinch/kdl-go"
)

// AgntConfigFileName is the name of the agnt configuration file.
const AgntConfigFileName = ".agnt.kdl"

// AgntConfig represents the agnt configuration.
// All fields use standard KDL format with child nodes.
type AgntConfig struct {
	// Project metadata (optional, for documentation/info only)
	Project *AgntProjectMeta `kdl:"project"`

	// Scripts to manage
	Scripts map[string]*ScriptConfig `kdl:"scripts"`

	// Proxies to manage
	Proxies map[string]*ProxyConfig `kdl:"proxies"`

	// AI configuration for run and ai commands
	AI *AIConfig `kdl:"ai"`

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
	// Run is a shell command string (executed via sh -c)
	Run string `kdl:"run"`
	// Command is the executable name (used with Args)
	Command string `kdl:"command"`
	// Args are command arguments (used with Command)
	Args []string `kdl:"args"`
	// Autostart starts the script when session opens
	Autostart bool `kdl:"autostart"`
	// URLMatchers are patterns for URL detection in output
	URLMatchers []string `kdl:"url-matchers"`
	// Env are environment variables for the script
	Env map[string]string `kdl:"env"`
	// Cwd is the working directory for the script
	Cwd string `kdl:"cwd"`
}

// ProxyConfig defines a reverse proxy to start.
type ProxyConfig struct {
	// Autostart starts the proxy when session opens
	Autostart bool `kdl:"autostart"`
	// MaxLogSize is the max number of log entries to keep
	MaxLogSize int `kdl:"max-log-size"`

	// Script links this proxy to a script for URL detection from output
	Script string `kdl:"script"`
	// URLPattern filters which detected URLs should trigger this proxy.
	// Regex pattern matched against detected URLs. Use to select specific ports
	// when a script outputs multiple URLs (e.g., ":34115" to match Wails backend).
	URLPattern string `kdl:"url-pattern"`

	// URL is the full target URL (e.g., "http://localhost:3000")
	URL string `kdl:"url"`
	// Target is the backend URL (deprecated, use URL instead)
	Target string `kdl:"target"`
	// Port is the target port - shorthand for http://localhost:PORT
	Port int `kdl:"port"`
	// FallbackPort is used when script URL detection fails
	FallbackPort int `kdl:"fallback-port"`
	// Host is the target host (default: localhost)
	Host string `kdl:"host"`

	// Bind is the address the proxy listens on
	// "127.0.0.1" (default) or "0.0.0.0" (all interfaces)
	Bind string `kdl:"bind"`

	// Websocket enables WebSocket proxying
	Websocket bool `kdl:"websocket"`
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

// AIConfig configures AI agent behavior for run and ai commands.
type AIConfig struct {
	// Skill is a skill/persona name to use (e.g., "code-review", "debugging")
	Skill string `kdl:"skill"`
	// Env are environment variables to set for AI commands
	Env map[string]string `kdl:"env"`
	// SystemPrompt is a full system prompt that replaces the default
	SystemPrompt string `kdl:"system-prompt"`
	// AppendSystemPrompt is appended to the default system prompt
	AppendSystemPrompt string `kdl:"append-system-prompt"`
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

// ParseAgntConfig parses KDL configuration data using the official kdl-go parser.
// Only standard KDL format is supported.
func ParseAgntConfig(data string) (*AgntConfig, error) {
	cfg := DefaultAgntConfig()

	if err := kdl.Unmarshal([]byte(data), cfg); err != nil {
		return nil, fmt.Errorf("failed to parse KDL config: %w", err)
	}

	log.Printf("[DEBUG] ParseAgntConfig: parsed %d scripts, %d proxies", len(cfg.Scripts), len(cfg.Proxies))
	return cfg, nil
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

// BuildSystemPrompt generates the system prompt based on configuration.
// If SystemPrompt is set, it returns that directly.
// Otherwise, it builds a prompt describing agnt features and configured services,
// then appends AppendSystemPrompt if set.
func (c *AgntConfig) BuildSystemPrompt() string {
	// If full system prompt override is set, use it
	if c.AI != nil && c.AI.SystemPrompt != "" {
		return c.AI.SystemPrompt
	}

	var sb strings.Builder

	// Base agnt description
	sb.WriteString(`You have access to agnt, a tool that gives AI coding agents browser superpowers.

## agnt Features

agnt provides MCP tools for browser debugging and dev server management:

- **proxy**: Reverse proxy with JS injection for browser instrumentation
  - Start/stop proxies, capture traffic logs, execute JavaScript in browser
  - Take screenshots, inspect elements, run accessibility audits

- **proc**: Process management for dev servers
  - Start/stop/restart scripts, view output, auto-restart on crash

- **proxylog**: Query captured HTTP traffic and browser events
  - Filter by type (error, xhr, console), search request/response bodies

- **automation**: Headless Chrome via chromedp for automated testing
  - Screenshots at multiple viewports, navigate, evaluate JS

- **currentpage**: Track the active browser page/tab for context
`)

	// Add configured scripts
	if len(c.Scripts) > 0 {
		sb.WriteString("\n## Configured Scripts\n\n")
		for name, script := range c.Scripts {
			cmd := script.Run
			if cmd == "" && script.Command != "" {
				cmd = script.Command
				if len(script.Args) > 0 {
					cmd += " " + strings.Join(script.Args, " ")
				}
			}
			autostart := ""
			if script.Autostart {
				autostart = " (autostart)"
			}
			sb.WriteString(fmt.Sprintf("- **%s**: `%s`%s\n", name, cmd, autostart))
		}
	}

	// Add configured proxies
	if len(c.Proxies) > 0 {
		sb.WriteString("\n## Configured Proxies\n\n")
		for name, proxy := range c.Proxies {
			target := proxy.URL
			if target == "" {
				target = proxy.Target
			}
			if target == "" && proxy.Port > 0 {
				target = fmt.Sprintf("http://localhost:%d", proxy.Port)
			}
			if target == "" && proxy.Script != "" {
				target = fmt.Sprintf("(linked to script '%s')", proxy.Script)
			}
			autostart := ""
			if proxy.Autostart {
				autostart = " (autostart)"
			}
			sb.WriteString(fmt.Sprintf("- **%s**: %s%s\n", name, target, autostart))
		}
	}

	sb.WriteString("\n## Usage Notes\n\n")
	sb.WriteString("- Use `proc {action: \"list\"}` to see running processes\n")
	sb.WriteString("- Use `proxy {action: \"list\"}` to see running proxies\n")
	sb.WriteString("- Do NOT start processes or proxies that are already running\n")
	sb.WriteString("- Use `proxy {action: \"exec\", ...}` to run JS in the browser\n")

	// Append custom prompt if set
	if c.AI != nil && c.AI.AppendSystemPrompt != "" {
		sb.WriteString("\n")
		sb.WriteString(c.AI.AppendSystemPrompt)
	}

	return sb.String()
}

// WriteDefaultAgntConfig writes a default configuration file with documentation.
func WriteDefaultAgntConfig(path string) error {
	defaultKDL := `// Agnt Configuration
// This file configures scripts and proxies to auto-start with agnt run
// Uses standard KDL format: https://kdl.dev

// Optional project metadata
// project {
//     type "node"
//     name "my-project"
// }

// Scripts to run (use daemon process management)
scripts {
    // Example: dev server with shell command
    // dev {
    //     run "npm run dev"
    //     autostart true
    // }

    // Example: with working directory
    // frontend {
    //     run "npm run dev"
    //     cwd "packages/frontend"
    //     autostart true
    // }

    // Example: with URL detection for proxy linking
    // api {
    //     run "go run ./cmd/server"
    //     url-matchers "Listening on {url}"
    //     autostart true
    // }
}

// Reverse proxies for browser debugging
proxies {
    // Example: proxy linked to script (auto-creates when URL detected)
    // dev {
    //     script "dev"
    //     fallback-port 3000
    // }

    // Example: Wails app (filter for backend URL, not Vite frontend)
    // wails-dev {
    //     script "wails-dev"
    //     url-pattern ":34115"
    // }

    // Example: explicit target
    // api {
    //     target "http://localhost:8080"
    //     autostart true
    // }

    // Example: accessible from mobile/Tailscale
    // mobile {
    //     target "http://localhost:3000"
    //     bind "0.0.0.0"
    //     autostart true
    // }
}

// Hook configuration for notifications
hooks {
    on-response {
        toast true
        indicator true
        sound false
    }
}

// Toast notification settings
toast {
    duration 4000
    position "bottom-right"
    max-visible 3
}

// AI configuration for agnt run and agnt ai commands
// ai {
//     // Skill/persona to use (e.g., "code-review", "debugging")
//     // skill "debugging"
//
//     // Environment variables for AI commands
//     // env {
//     //     ANTHROPIC_API_KEY "sk-..."
//     // }
//
//     // Full system prompt (replaces the default agnt prompt)
//     // system-prompt "You are a helpful assistant..."
//
//     // Append to the default system prompt (recommended)
//     // append-system-prompt "Additional context for this project..."
// }
`
	return os.WriteFile(path, []byte(defaultKDL), 0644)
}
