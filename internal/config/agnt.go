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

	// Channel configuration for MCP push-based event forwarding
	Channel *ChannelConfig `kdl:"channel"`

	// Session lifecycle configuration (daemon-side cleanup policies)
	Session *SessionConfig `kdl:"session"`
}

// SessionConfig controls daemon-side session lifecycle behaviors.
//
// The daemon reads this at startup to decide which cleanup steps run.
// Every field must stay optional so a project with no `session {}` block
// still gets sensible defaults from DefaultAgntConfig().
type SessionConfig struct {
	// OrphanPGIDScan controls whether daemon startup scans /proc for
	// orphaned POSIX process groups (pgid leader dead, members still
	// running) and kills them. Defaults to true.
	//
	// This handles the case where the daemon crashed or was restarted
	// mid-session: the SessionRegistry is lost so slice-A session-pgid
	// tracking cannot fire on cleanup. The startup scan is the crash-
	// recovery fallback.
	//
	// Set to false to disable if the scan is interfering with other
	// long-running session leaders on the system (e.g. a tmux session
	// daemon that shares a pgid layout the scan cannot distinguish).
	OrphanPGIDScan *bool `kdl:"orphan-pgid-scan"`
}

// OrphanPGIDScanEnabled returns whether orphaned-pgid scanning is enabled
// at daemon startup. Defaults to true when the SessionConfig block is
// absent or the field is unset.
func (c *SessionConfig) OrphanPGIDScanEnabled() bool {
	if c == nil || c.OrphanPGIDScan == nil {
		return true
	}
	return *c.OrphanPGIDScan
}

// validSeverities is the set of accepted severity strings for ChannelConfig.
var validSeverities = map[string]struct{}{
	"trace": {}, "debug": {}, "info": {}, "warning": {}, "error": {},
}

// validEventTypes is the set of accepted event type strings for ChannelConfig.
var validEventTypes = map[string]struct{}{
	"error": {}, "diagnostic": {}, "interaction": {}, "http": {}, "custom": {}, "panel_message": {},
}

// ChannelConfig configures the MCP push-based channel for event forwarding.
// When enabled, the daemon pushes events to connected MCP sessions via a
// dedicated channel tool. This is pure configuration plumbing — no runtime
// behavior change until later tasks wire consumers.
type ChannelConfig struct {
	// Enabled controls whether the channel is active. Default: false.
	Enabled *bool `kdl:"enabled"`

	// Events is an allowlist of event types to forward.
	// Empty/omitted means all types. Valid values: "error", "diagnostic", "interaction".
	Events []string `kdl:"events"`

	// Severity is the minimum event severity to forward.
	// Valid values: "trace", "debug", "info", "warning", "error".
	// Default: "warning".
	Severity string `kdl:"severity"`

	// DedupeWindow is the per-event deduplication window in milliseconds.
	// 0 disables deduplication. Default: 2000.
	DedupeWindow int `kdl:"dedupe-window"`

	// ReplyTool controls whether the channel-reply MCP tool is registered.
	// Default: true.
	ReplyTool *bool `kdl:"reply-tool"`
}

// IsEnabled returns whether the channel is enabled (defaults to false).
func (c *ChannelConfig) IsEnabled() bool {
	if c == nil || c.Enabled == nil {
		return false
	}
	return *c.Enabled
}

// ReplyToolEnabled returns whether the reply tool is registered (defaults to true).
func (c *ChannelConfig) ReplyToolEnabled() bool {
	if c == nil || c.ReplyTool == nil {
		return true
	}
	return *c.ReplyTool
}

// GetSeverity returns the minimum severity (defaults to "warning").
func (c *ChannelConfig) GetSeverity() string {
	if c == nil || c.Severity == "" {
		return "warning"
	}
	return c.Severity
}

// GetDedupeWindow returns the deduplication window in milliseconds (defaults to 2000).
func (c *ChannelConfig) GetDedupeWindow() int {
	if c == nil || c.DedupeWindow == 0 {
		return 2000
	}
	return c.DedupeWindow
}

// GetEvents returns the event type allowlist (defaults to empty = all types).
func (c *ChannelConfig) GetEvents() []string {
	if c == nil {
		return nil
	}
	return c.Events
}

// AgntProjectMeta contains optional project metadata in .agnt.kdl.
type AgntProjectMeta struct {
	Type         string `kdl:"type"`
	Name         string `kdl:"name"`
	PortConflict string `kdl:"port-conflict"`
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
	// Ports lists the ports this script uses. Used for pre-flight orphan cleanup
	// and EADDRINUSE recovery. Multiple ports supported (e.g., API + WebSocket).
	Ports []int `kdl:"ports"`
	// ErrorPattern is a regex that flags the process as unhealthy when matched.
	// If empty, DefaultHealthPatterns() are used based on common frameworks.
	ErrorPattern string `kdl:"error-pattern"`
	// HealthyPattern is a regex that clears the unhealthy flag when matched.
	// If empty, DefaultHealthPatterns() are used based on common frameworks.
	HealthyPattern string `kdl:"healthy-pattern"`
	// AutoRestart enables automatic restart when the process exits.
	// Default: false — user restarts manually from the overlay.
	AutoRestart bool `kdl:"auto-restart"`
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

	// ListenPort is the explicit port the proxy binds to. When zero or
	// unset, the hash-based stable allocator in
	// proxy.DefaultPortForURL picks a port in the 10000-60000 range.
	//
	// When set, the proxy MUST bind to Bind:ListenPort or fail — the
	// daemon does not silently fall back to an auto-assigned port, and
	// the runtime Start() path honors StrictListenPort so a bind
	// conflict surfaces as a visible error event instead of getting
	// papered over with a random port. Intended for CORS origin
	// registration, shareable dev URLs that must not drift across
	// sessions, and reverse proxies to external hostnames that pin to
	// a specific listen port.
	ListenPort int `kdl:"listen-port"`

	// SkipTLSVerify disables TLS certificate verification on the
	// upstream (target) connection. Defaults to false — certs are
	// verified. Set to true to proxy to targets with self-signed,
	// expired, or otherwise untrusted certificates (common in local
	// dev environments with wildcard self-signed certs). The listen
	// side of the proxy is unaffected; this only controls the client
	// TLS config used when dialing the upstream URL.
	SkipTLSVerify bool `kdl:"skip-tls-verify"`

	// Websocket enables WebSocket proxying
	Websocket bool `kdl:"websocket"`

	// WaitFor lists script ids that must reach Running Healthy (or
	// Running With Errors) before this proxy begins forwarding
	// requests. While any entry is pending, the proxy binds and is
	// visible to `proxy list`, but every incoming request receives a
	// 503 with the `agnt_proxy_not_ready` sentinel body. When every
	// listed script signals ready (URL detected, port bound, or
	// ready-signal fired), the proxy flips to ready atomically.
	//
	// The proxy is a readiness gate, not a build gate. Per
	// .claude/rules/daemon-lifecycle.md, a process that is bound and
	// producing output (including compile errors) counts as "running"
	// — the readiness signal fires as soon as the port is listening,
	// regardless of whether the child is in a clean or errored state.
	//
	// Every entry must resolve to a declared script in the same
	// `scripts {}` block; unknown names fail config parsing. Proxy →
	// proxy dependencies are not supported.
	WaitFor []string `kdl:"wait-for"`
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

	// AutoForward configures automatic forwarding of browser/proxy errors to the AI agent.
	AutoForward *AutoForwardConfig `kdl:"auto-forward"`

	// Push configures which push channels are active for alert delivery.
	Push *PushConfig `kdl:"push"`

	// Preset names a predefined push channel configuration (e.g., "claude-code", "universal").
	// When set, it expands into a PushConfig. An explicit Push block takes precedence.
	Preset string `kdl:"preset"`
}

// AutoForwardConfig configures automatic error forwarding to the AI agent.
type AutoForwardConfig struct {
	// Enabled controls whether browser/proxy errors are auto-forwarded. Default: true.
	Enabled *bool `kdl:"enabled"`

	// Sources specifies which error sources to forward. Default: ["browser", "http"].
	Sources []string `kdl:"sources"`

	// Debounce is the minimum seconds between forwarded errors. Default: 10.
	Debounce int `kdl:"debounce"`

	// Severity is the minimum severity to forward: "error" or "warning". Default: "error".
	Severity string `kdl:"severity"`
}

// IsEnabled returns whether auto-forward is enabled (defaults to true).
func (c *AutoForwardConfig) IsEnabled() bool {
	if c == nil || c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

// GetSources returns the sources to forward (defaults to ["browser", "http"]).
func (c *AutoForwardConfig) GetSources() []string {
	if c == nil || len(c.Sources) == 0 {
		return []string{"browser", "http"}
	}
	return c.Sources
}

// GetDebounceSeconds returns the debounce interval (defaults to 10).
func (c *AutoForwardConfig) GetDebounceSeconds() int {
	if c == nil || c.Debounce <= 0 {
		return 10
	}
	return c.Debounce
}

// GetSeverity returns the minimum severity (defaults to "error").
func (c *AutoForwardConfig) GetSeverity() string {
	if c == nil || c.Severity == "" {
		return "error"
	}
	return c.Severity
}

// ShouldForwardSource checks if a given source is in the forward list.
func (c *AutoForwardConfig) ShouldForwardSource(source string) bool {
	for _, s := range c.GetSources() {
		if s == source {
			return true
		}
	}
	return false
}

// PushConfig controls which push channels are active for delivering alerts
// to the AI client. When no push config is present, all channels are enabled
// (universal behavior).
type PushConfig struct {
	// MCPNotifications controls delivery via MCP session.Log().
	// Works natively in Claude Desktop; may be dropped by other clients.
	// Defaults to true when unset.
	MCPNotifications *bool `kdl:"mcp-notifications"`

	// PTYInjection controls delivery via stdin typing into the PTY.
	// Universal channel that works in all clients (Claude Code, OpenCode, etc).
	// Defaults to true when unset.
	PTYInjection *bool `kdl:"pty-injection"`
}

// PresetPushConfig returns a PushConfig for the named preset.
// Returns nil for unknown preset names.
func PresetPushConfig(name string) *PushConfig {
	switch name {
	case "claude-code":
		// Claude Code: MCP notifications are dropped, rely on Monitor tool.
		// Disable PTY injection since Monitor is preferred.
		f := false
		t := true
		return &PushConfig{
			MCPNotifications: &t,
			PTYInjection:     &f,
		}
	case "universal":
		// Universal: enable all channels for maximum compatibility.
		t := true
		return &PushConfig{
			MCPNotifications: &t,
			PTYInjection:     &t,
		}
	default:
		return nil
	}
}

// MCPNotificationsEnabled returns whether MCP notification delivery is enabled.
// Defaults to true when the config is nil or the field is unset.
func (c *PushConfig) MCPNotificationsEnabled() bool {
	if c == nil || c.MCPNotifications == nil {
		return true
	}
	return *c.MCPNotifications
}

// PTYInjectionEnabled returns whether PTY stdin injection is enabled.
// Defaults to true when the config is nil or the field is unset.
func (c *PushConfig) PTYInjectionEnabled() bool {
	if c == nil || c.PTYInjection == nil {
		return true
	}
	return *c.PTYInjection
}

// GetPushConfig returns the effective PushConfig, applying presets if set.
// If no alerts config exists, returns nil (which means all channels enabled).
// When Preset is set and Push is nil, the preset is expanded into a PushConfig.
// An explicit Push block takes precedence over a preset.
func (c *AlertsConfig) GetPushConfig() *PushConfig {
	if c == nil {
		return nil
	}
	if c.Push != nil {
		return c.Push
	}
	if c.Preset != "" {
		return PresetPushConfig(c.Preset)
	}
	return nil
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
	channelDisabled := false
	channelReplyTool := true
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
		Channel: &ChannelConfig{
			Enabled:      &channelDisabled,
			Severity:     "warning",
			DedupeWindow: 2000,
			ReplyTool:    &channelReplyTool,
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

	// Validate proxy `wait-for` references — every listed script must
	// be declared in the scripts block. Catches typos at parse time
	// rather than waiting for startup races.
	if err := validateProxyWaitFor(cfg.Proxies, cfg.Scripts); err != nil {
		return nil, err
	}

	// Validate channel config fields if present.
	if err := validateChannelConfig(cfg.Channel); err != nil {
		return nil, err
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

// PortConflictPolicy returns the raw port-conflict policy from config.
func (c *AgntConfig) PortConflictPolicy() string {
	if c.Project == nil {
		return ""
	}
	return c.Project.PortConflict
}

// EffectivePortConflictPolicy returns the port-conflict policy, defaulting to "prompt".
func (c *AgntConfig) EffectivePortConflictPolicy() string {
	p := c.PortConflictPolicy()
	if p == "" {
		return "prompt"
	}
	return p
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

## agnt Tools

- **get_errors**: Unified error view across processes and proxies (JS errors, HTTP errors, process failures)
- **proxy**: Reverse proxy with JS injection — start/stop, capture traffic, execute JS, take screenshots
- **proc**: Process management — start/stop/restart scripts, view output, auto-restart on crash
- **proxylog**: Query captured HTTP traffic — filter by type (error, xhr, console), search bodies
- **responsive_audit**: Responsive design audit across viewport sizes (mobile, tablet, desktop)
- **automation**: Headless Chrome via chromedp — screenshots at multiple viewports, navigate, evaluate JS
- **currentpage**: Track the active browser page/URL for context

## Debugging Workflow

When something isn't working:
1. get_errors {} — check for JS errors, HTTP errors, process failures
2. currentpage {} — see what page/URL the user is viewing
3. proc {action: "output", id: "..."} — check process output for crashes/errors
4. proxylog {action: "query", types: ["error"]} — check HTTP/API errors
5. proxy {action: "exec", ...} — run diagnostic JS in the browser
6. Take a screenshot if visual issue suspected

## Common Patterns

- "Page is blank" — get_errors, then proc output for build errors
- "API returns 500" — proxylog query for the specific endpoint
- "Style looks wrong" — responsive_audit or screenshot
- "Process crashed" — proc output, then restart
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

	sb.WriteString("\n## Process Management\n\n")
	sb.WriteString("- proc {action: \"list\"} — see all processes and their states\n")
	sb.WriteString("- proc {action: \"output\", id: \"...\"} — see recent output lines\n")
	sb.WriteString("- proc {action: \"stop\", id: \"...\"} then run {script: \"...\"} — restart\n")
	sb.WriteString("- proxy {action: \"list\"} — see all proxies and their states\n")
	sb.WriteString("- proxy {action: \"exec\", ...} — run JS in the browser\n")
	sb.WriteString("- Do NOT start processes or proxies that are already running\n")

	// Append custom prompt if set
	if c.AI != nil && c.AI.AppendSystemPrompt != "" {
		sb.WriteString("\n")
		sb.WriteString(c.AI.AppendSystemPrompt)
	}

	return sb.String()
}

// HealthPatterns holds compiled error and healthy regex patterns for a script.
type HealthPatterns struct {
	Error   string // Regex that indicates an error
	Healthy string // Regex that clears the error state
}

// DefaultHealthPatterns returns the default error/healthy patterns.
// These cover common dev server frameworks so most users get good behavior
// without explicit configuration.
//
// Covered frameworks (healthy): Vite, Next.js, Webpack, Go stdlib, dotnet run,
// dotnet watch, Flask, uvicorn, gunicorn, Django, Spring Boot, Gradle, Maven,
// Rails, PHP artisan/built-in, Vapor (Swift), Phoenix (Elixir).
//
// Covered frameworks (error): Go panics, Node.js module/syntax errors,
// TypeScript compiler, Rust compiler, Python tracebacks, .NET exceptions,
// EADDRINUSE, segfaults, OOM, fatal errors.
func DefaultHealthPatterns() HealthPatterns {
	return HealthPatterns{
		Error: `(?i)(` +
			`\bERROR\b|` +
			`\bFAIL\b|` +
			`Cannot find module|` +
			`Build FAILED|` +
			`EADDRINUSE|` +
			`SyntaxError:|` +
			`Segmentation fault|` +
			`Traceback \(most recent call last\)|` +
			`error TS\d|` +
			`error\[E\d|` +
			`out of memory|` +
			`panic:|` +
			`unhandled exception|` +
			`\bFATAL\b` +
			`)`,
		Healthy: `(?i)(` +
			`ready in|` +
			`compiled successfully|` +
			`listening (on|at):?|` +
			`started server|` +
			`build (succeeded|success(?:ful)?)\b|` +
			`compiled\b|` +
			`server running|` +
			`serving!|` +
			`running on|` +
			`start(?:ing|ed) .*(?:development|laravel|php) server|` +
			`started \S+ in \d|` +
			`watch.*started|` +
			`server starting|` +
			`running .+endpoint` +
			`)`,
	}
}

// validateChannelConfig checks that the channel block's severity and event
// type fields contain only accepted values. Returns nil when cfg is nil or
// when all fields are valid.
func validateChannelConfig(cfg *ChannelConfig) error {
	if cfg == nil {
		return nil
	}
	if cfg.Severity != "" {
		if _, ok := validSeverities[cfg.Severity]; !ok {
			return fmt.Errorf("channel: unknown severity %q (valid: trace, debug, info, warning, error)", cfg.Severity)
		}
	}
	for _, ev := range cfg.Events {
		if _, ok := validEventTypes[ev]; !ok {
			return fmt.Errorf("channel: unknown event type %q (valid: error, diagnostic, interaction)", ev)
		}
	}
	return nil
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
//     // Auto-forward browser/proxy errors to the AI agent
//     // auto-forward {
//     //     enabled true
//     //     sources "browser" "http"
//     //     debounce 10
//     //     severity "error"
//     // }
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
