// Package config contains configuration types for agnt.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	kdl "github.com/sblinch/kdl-go"

	"github.com/standardbeagle/agnt/internal/debug"
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

	// Alerts configuration for process output monitoring
	Alerts *AlertsConfig `kdl:"alerts"`
}

// AgntProjectMeta contains optional project metadata in .agnt.kdl.
// This is informational only and doesn't affect behavior.
type AgntProjectMeta struct {
	Type string `kdl:"type"`
	Name string `kdl:"name"`
}

// ScriptConfig defines a script to run.
type ScriptConfig struct {
	// Run is a shell command string (executed via platform shell)
	// On Unix: sh -c "..."
	// On Windows: cmd.exe /c "..."
	// Override with Shell field
	Run string `kdl:"run"`
	// Command is the executable name (used with Args)
	Command string `kdl:"command"`
	// Args are command arguments (used with Command)
	Args []string `kdl:"args"`
	// Shell overrides the shell used for "run" commands.
	// Examples: "bash", "powershell", "cmd.exe", "sh"
	// If empty, uses platform default (sh on Unix, cmd.exe on Windows)
	Shell string `kdl:"shell"`
	// ShellArgs overrides the shell arguments. Default: ["-c"] for sh/bash, ["/c"] for cmd.exe
	ShellArgs []string `kdl:"shell-args"`
	// Autostart starts the script when session opens
	Autostart bool `kdl:"autostart"`
	// URLMatchers are patterns for URL detection in output
	URLMatchers []string `kdl:"url-matchers"`
	// Env are environment variables for the script
	Env map[string]string `kdl:"env"`
	// Cwd is the working directory for the script
	Cwd string `kdl:"cwd"`
	// DependsOn lists scripts that must be ready before this script starts.
	DependsOn DependsOnList `kdl:"depends-on"`
}

// ResolveShell returns the shell command and arguments for executing a "run" command.
// Priority: explicit Shell/ShellArgs config > platform default.
func (s *ScriptConfig) ResolveShell() (shell string, shellArgs []string) {
	if s.Shell != "" {
		shell = s.Shell
		if len(s.ShellArgs) > 0 {
			shellArgs = append(s.ShellArgs, s.Run)
		} else {
			// Infer shell args from shell name
			base := strings.ToLower(filepath.Base(shell))
			base = strings.TrimSuffix(base, ".exe")
			switch base {
			case "cmd":
				shellArgs = []string{"/c", s.Run}
			case "powershell", "pwsh":
				shellArgs = []string{"-NoLogo", "-Command", s.Run}
			default:
				// sh, bash, zsh, etc.
				shellArgs = []string{"-c", s.Run}
			}
		}
		return shell, shellArgs
	}

	// Platform default
	if runtime.GOOS == "windows" {
		return "cmd.exe", []string{"/c", s.Run}
	}
	return "sh", []string{"-c", s.Run}
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

// AlertsConfig configures process output alert monitoring.
type AlertsConfig struct {
	// Enabled controls whether alerts are active. Default: true.
	Enabled *bool `kdl:"enabled"`

	// Patterns defines custom alert patterns keyed by ID.
	Patterns map[string]*AlertPatternConfig `kdl:"patterns"`

	// Disable is a list of built-in pattern IDs to disable.
	Disable []string `kdl:"disable"`

	// BatchWindow is the batching window in seconds before delivering alerts.
	// Default: 3.
	BatchWindow int `kdl:"batch-window"`

	// DedupeWindow is the deduplication window in seconds.
	// Duplicate alerts within this window are suppressed. Default: 60.
	DedupeWindow int `kdl:"dedupe-window"`
}

// AlertPatternConfig defines a custom alert pattern in configuration.
type AlertPatternConfig struct {
	// Pattern is a regular expression to match against output lines.
	Pattern string `kdl:"pattern"`

	// Severity is "error", "warning", or "info".
	Severity string `kdl:"severity"`
}

// IsEnabled returns whether alerts are enabled (defaults to true).
func (c *AlertsConfig) IsEnabled() bool {
	if c == nil || c.Enabled == nil {
		return true
	}
	return *c.Enabled
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
		debug.Log("config", "LoadAgntConfig: no config file found for dir %s", dir)
		return DefaultAgntConfig(), nil
	}

	debug.Log("config", "LoadAgntConfig: found config file at %s", configPath)
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

	// Validate dependencies if any scripts have them
	if len(cfg.Scripts) > 0 {
		warnings, err := ValidateDependencies(cfg.Scripts)
		if err != nil {
			return nil, fmt.Errorf("dependency validation failed: %w", err)
		}
		for _, w := range warnings {
			debug.Log("config", "WARNING: %s", w)
		}
	}

	debug.Log("config", "ParseAgntConfig: parsed %d scripts, %d proxies", len(cfg.Scripts), len(cfg.Proxies))
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

// HasExplicitTarget returns true if the proxy has an explicitly configured target
// (URL, Target, or Port) rather than being linked to a script for URL detection.
func (p *ProxyConfig) HasExplicitTarget() bool {
	return p.URL != "" || p.Target != "" || p.Port > 0
}

// ShouldAutostart returns true if this proxy should start automatically.
// A proxy auto-starts if Autostart is explicitly true, or if it has an explicit
// target (URL/Target/Port) without being script-linked (script-linked proxies
// are created automatically when URLs are detected in script output).
func (p *ProxyConfig) ShouldAutostart() bool {
	return p.Autostart || (p.HasExplicitTarget() && p.Script == "")
}

// GetAutostartProxies returns proxies that should auto-start.
func (c *AgntConfig) GetAutostartProxies() map[string]*ProxyConfig {
	result := make(map[string]*ProxyConfig)
	for name, proxy := range c.Proxies {
		if proxy.ShouldAutostart() {
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
			if proxy.ShouldAutostart() {
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

    // Example: custom shell (e.g., Git Bash on Windows)
    // dev {
    //     run "npm run dev"
    //     shell "C:\\Program Files\\Git\\bin\\bash.exe"
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

// Process output alert monitoring
// alerts {
//     enabled true
//     batch-window 3
//     dedupe-window 60
//
//     // Custom patterns (keyed by ID)
//     // patterns {
//     //     "my-custom" {
//     //         pattern "MY_APP_ERROR:"
//     //         severity "error"
//     //     }
//     // }
//
//     // Disable built-in patterns by ID
//     // disable "connection-refused"
// }

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
