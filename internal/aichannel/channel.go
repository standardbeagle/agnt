// Package aichannel provides an interface for communicating with AI coding agents via CLI.
package aichannel

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
)

// Common errors
var (
	ErrNotConfigured = errors.New("channel not configured")
	ErrNotAvailable  = errors.New("AI agent not available")
	ErrTimeout       = errors.New("request timed out")
)

// DefaultMaxTokens is the API-mode response cap applied when Config.MaxTokens
// is unset.
const DefaultMaxTokens = 1024

// AgentType represents a known AI coding agent.
type AgentType string

const (
	AgentClaude   AgentType = "claude"
	AgentCopilot  AgentType = "copilot"
	AgentGemini   AgentType = "gemini"
	AgentOpenCode AgentType = "opencode"
	AgentKimi     AgentType = "kimi-cli"
	AgentAuggie   AgentType = "auggie"
	AgentAider    AgentType = "aider"
	AgentCursor   AgentType = "cursor-agent"
	AgentCustom   AgentType = "custom"
)

// Config holds the configuration for an AI channel.
type Config struct {
	// Agent is the type of AI agent to use
	Agent AgentType `json:"agent"`

	// Command is the executable command (defaults based on agent type)
	Command string `json:"command,omitempty"`

	// Args are additional arguments to pass to the command
	Args []string `json:"args,omitempty"`

	// NonInteractiveFlag is the flag to use for non-interactive mode (e.g., "-p")
	NonInteractiveFlag string `json:"non_interactive_flag,omitempty"`

	// QuietFlag suppresses progress output (e.g., "-s" for copilot, "-q" for gemini)
	QuietFlag string `json:"quiet_flag,omitempty"`

	// OutputFormat specifies desired output format ("text", "json", "stream-json")
	// Note: Not all agents support all formats. Use SupportsJSONOutput() to check.
	OutputFormat string `json:"output_format,omitempty"`

	// OutputFormatFlag is the CLI flag for output format (e.g., "--output-format")
	// Set automatically based on agent type.
	OutputFormatFlag string `json:"output_format_flag,omitempty"`

	// SupportsJSON indicates if this agent supports structured JSON output
	SupportsJSON bool `json:"supports_json,omitempty"`

	// UseStdin determines if context should be piped via stdin
	UseStdin bool `json:"use_stdin"`

	// UsePTY runs the command in a pseudo-terminal (required for some CLI tools)
	UsePTY bool `json:"use_pty"`

	// Timeout for the request (default 2 minutes)
	Timeout time.Duration `json:"timeout,omitempty"`

	// Environment variables to set
	Env map[string]string `json:"env,omitempty"`

	// --- API Mode Configuration ---

	// UseAPI enables API mode instead of CLI mode.
	// When true, uses the Provider interface instead of executing CLI commands.
	UseAPI bool `json:"use_api,omitempty"`

	// LLMProvider specifies which LLM provider to use in API mode.
	// If empty, auto-detects based on available API keys.
	LLMProvider LLMProvider `json:"llm_provider,omitempty"`

	// APIKey is the authentication key for API-based providers.
	// If empty, uses environment variables based on provider.
	APIKey string `json:"api_key,omitempty"`

	// Model specifies the model to use.
	// For CLI mode (e.g., Claude Code): passed via --model flag. Defaults to "haiku".
	// For API mode: uses the provider's default model if empty.
	Model string `json:"model,omitempty"`

	// MaxTokens limits API response length (default 1024).
	MaxTokens int `json:"max_tokens,omitempty"`

	// SystemPrompt provides context/instructions for API-based completions.
	SystemPrompt string `json:"system_prompt,omitempty"`
}

// Channel represents a communication channel to an AI coding agent.
type Channel struct {
	config     Config
	configured bool
	provider   Provider // For API mode
	// providerErr is why API-mode provider setup failed, if it did.
	providerErr error
}

// New creates a new AI channel.
func New() *Channel {
	return &Channel{}
}

// NewWithConfig creates a new AI channel with the given configuration.
func NewWithConfig(config Config) *Channel {
	ch := &Channel{}
	ch.Configure(config)
	return ch
}

// Configure sets up the channel with the given configuration.
func (c *Channel) Configure(config Config) {
	// Apply defaults based on agent type
	config = applyDefaults(config)
	c.config = config
	c.configured = true
	c.providerErr = nil

	// Set up provider for API mode
	if config.UseAPI {
		c.provider = c.createProvider()
	}
}

// createProvider creates the appropriate Provider based on configuration.
func (c *Channel) createProvider() Provider {
	// Determine which provider to use
	provider := c.config.LLMProvider
	if provider == "" {
		// Auto-detect based on available API keys
		provider = GetDefaultProvider()
	}

	if provider == "" {
		// No provider available
		return nil
	}

	langchainProvider, err := NewLangChainProvider(LangChainConfig{
		Provider:  provider,
		APIKey:    c.config.APIKey,
		Model:     c.config.Model,
		MaxTokens: c.config.MaxTokens,
	})
	if err != nil {
		// Kept on the channel, not just logged: the debug log is invisible in
		// a normal session, and without the cause every later call could only
		// say "provider not configured". Send/SendAndParse return it.
		c.providerErr = fmt.Errorf("failed to initialize %s provider: %w", provider, err)
		debug.Warn("aichannel", "%v", c.providerErr)
		return nil
	}

	return langchainProvider
}

// errProviderUnavailable is the error for an API-mode call with no usable
// provider, carrying the initialization failure when there was one.
func (c *Channel) errProviderUnavailable() error {
	if c.providerErr != nil {
		return fmt.Errorf("%w: %v", ErrNotAvailable, c.providerErr)
	}
	return fmt.Errorf("%w: provider not configured (no API key found for any supported provider)", ErrNotAvailable)
}

// applyDefaults fills in default values based on agent type.
func applyDefaults(config Config) Config {
	if config.Timeout == 0 {
		config.Timeout = 2 * time.Minute
	}
	// The documented default. It used to exist only in the field's comment:
	// nothing applied it, so API-mode calls ran at whatever cap the provider
	// library chose.
	if config.MaxTokens == 0 {
		config.MaxTokens = DefaultMaxTokens
	}

	// Set command and flags based on agent type if not specified
	switch config.Agent {
	case AgentClaude:
		if config.Command == "" {
			config.Command = "claude"
		}
		if config.NonInteractiveFlag == "" {
			config.NonInteractiveFlag = "-p"
		}
		if config.OutputFormat == "" {
			config.OutputFormat = "text"
		}
		// Default to haiku for Claude Code CLI (fast and cost-effective for
		// summaries). "haiku" is a CLI alias, not an API model id: in API mode
		// it would reach the auto-detected provider (possibly not Anthropic at
		// all) and fail every call, so there the provider's default applies.
		if config.Model == "" && !config.UseAPI {
			config.Model = "haiku"
		}
		config.OutputFormatFlag = "--output-format"
		config.SupportsJSON = true // Claude supports json and stream-json
		config.UseStdin = true
		config.UsePTY = true // Claude Code requires a PTY for -p mode

	case AgentCopilot:
		if config.Command == "" {
			config.Command = "copilot"
		}
		if config.NonInteractiveFlag == "" {
			config.NonInteractiveFlag = "-p"
		}
		if config.QuietFlag == "" {
			config.QuietFlag = "-s"
		}
		config.SupportsJSON = false // Copilot uses -s for clean output, no structured JSON
		config.UseStdin = true

	case AgentGemini:
		if config.Command == "" {
			config.Command = "gemini"
		}
		if config.NonInteractiveFlag == "" {
			config.NonInteractiveFlag = "-e"
		}
		if config.QuietFlag == "" {
			config.QuietFlag = "-q"
		}
		config.OutputFormatFlag = "--output-format"
		config.SupportsJSON = true // Gemini CLI supports --output-format json
		config.UseStdin = true

	case AgentOpenCode:
		if config.Command == "" {
			config.Command = "opencode"
		}
		config.SupportsJSON = false // TBD - uses TUI

	case AgentKimi:
		if config.Command == "" {
			config.Command = "kimi-cli"
		}
		config.SupportsJSON = false // No documented JSON output
		config.UseStdin = true

	case AgentAuggie:
		if config.Command == "" {
			config.Command = "auggie"
		}
		config.SupportsJSON = false // TBD

	case AgentAider:
		if config.Command == "" {
			config.Command = "aider"
		}
		// Aider uses --message for non-interactive
		if config.NonInteractiveFlag == "" {
			config.NonInteractiveFlag = "--message"
		}
		config.SupportsJSON = false // No documented JSON output

	case AgentCursor:
		if config.Command == "" {
			config.Command = "cursor-agent"
		}
		if config.NonInteractiveFlag == "" {
			config.NonInteractiveFlag = "-p"
		}
		config.SupportsJSON = false // Documented but format unclear

	case AgentCustom:
		// For custom agents, allow user to specify SupportsJSON and OutputFormatFlag
		// If OutputFormat is json/stream-json and OutputFormatFlag is set, enable JSON support
		if config.OutputFormatFlag != "" &&
			(config.OutputFormat == "json" || config.OutputFormat == "stream-json") {
			config.SupportsJSON = true
		}
	}

	return config
}

// SupportsJSONOutput returns true if the configured agent supports JSON output format.
func (c *Channel) SupportsJSONOutput() bool {
	return c.config.SupportsJSON
}

// IsAvailable checks if the configured AI agent is available.
func (c *Channel) IsAvailable() bool {
	if !c.configured {
		return false
	}

	// For API mode, check if provider is configured
	if c.config.UseAPI {
		return c.provider != nil && c.provider.IsConfigured()
	}

	// For CLI mode, check if command exists
	_, err := exec.LookPath(c.config.Command)
	return err == nil
}

// IsAPIMode returns true if the channel is configured to use API mode.
func (c *Channel) IsAPIMode() bool {
	return c.config.UseAPI
}

// Send sends a prompt to the AI agent and returns the response.
// If context is non-empty, it's passed appropriately based on the agent type.
func (c *Channel) Send(ctx context.Context, prompt string, inputContext string) (string, error) {
	if !c.configured {
		return "", ErrNotConfigured
	}

	// Use API mode if configured
	if c.config.UseAPI {
		return c.sendWithAPI(ctx, prompt, inputContext)
	}

	if !c.IsAvailable() {
		return "", fmt.Errorf("%w: %s not found in PATH", ErrNotAvailable, c.config.Command)
	}

	// Use adapter-based execution for CLI mode
	return RunWithAdapter(ctx, c.config, prompt, inputContext, c.config.SystemPrompt)
}

// sendWithAPI sends a prompt using the API provider.
func (c *Channel) sendWithAPI(ctx context.Context, prompt string, inputContext string) (string, error) {
	if c.provider == nil {
		return "", c.errProviderUnavailable()
	}

	if !c.provider.IsConfigured() {
		return "", ErrNoAPIKey
	}

	resp, err := c.provider.CompleteWithContext(ctx, c.config.SystemPrompt, prompt, inputContext)
	if err != nil {
		return "", err
	}

	return resp.Result, nil
}

// ErrAgentError is returned when the AI agent reports an error in its response.
var ErrAgentError = errors.New("agent error")

// SendAndParse sends a prompt and parses the response based on the configured OutputFormat.
// This provides a structured response with metadata for JSON/stream-json formats.
// For agents that don't support JSON output, it falls back to text parsing.
// For API mode, always returns a structured Response directly from the provider.
//
// If the agent returns an error (is_error: true), this method returns ErrAgentError
// with the error message. This ensures errors are reported to users rather than
// being passed to downstream LLM calls.
func (c *Channel) SendAndParse(ctx context.Context, prompt string, inputContext string) (*Response, error) {
	// For API mode, get response directly from provider
	if c.config.UseAPI {
		if c.provider == nil {
			return nil, c.errProviderUnavailable()
		}
		if !c.provider.IsConfigured() {
			return nil, ErrNoAPIKey
		}
		return c.provider.CompleteWithContext(ctx, c.config.SystemPrompt, prompt, inputContext)
	}

	// For CLI mode, send and parse the output
	rawOutput, err := c.Send(ctx, prompt, inputContext)
	if err != nil {
		return nil, err
	}

	// Determine actual output format based on agent support
	format := OutputFormat(c.config.OutputFormat)
	if format == "" {
		format = OutputFormatText
	}

	// If JSON was requested but agent doesn't support it, fall back to text
	if (format == OutputFormatJSON || format == OutputFormatStreamJSON) && !c.config.SupportsJSON {
		format = OutputFormatText
	}

	resp, err := ParseResponse(rawOutput, format)
	if err != nil {
		return nil, err
	}

	// Check for agent-reported errors (is_error: true in JSON response)
	// Report these to the user instead of passing to downstream LLM calls
	if resp.IsError {
		errMsg := resp.Result
		if errMsg == "" {
			errMsg = "unknown error"
		}
		if resp.Subtype != "" {
			return nil, fmt.Errorf("%w (%s): %s", ErrAgentError, resp.Subtype, errMsg)
		}
		return nil, fmt.Errorf("%w: %s", ErrAgentError, errMsg)
	}

	return resp, nil
}

// stripANSI removes terminal escape sequences and carriage returns from s.
func stripANSI(s string) string {
	var result strings.Builder
	result.Grow(len(s))
	for i := 0; i < len(s); {
		switch s[i] {
		case '\x1b':
			i = SkipEscape(s, i)
		case '\r': // terminal line endings
			i++
		default:
			result.WriteByte(s[i])
			i++
		}
	}
	return result.String()
}

// SkipEscape returns the index just past the escape sequence that starts with
// the ESC at s[i], following ECMA-48:
//
//   - CSI (ESC [): parameter and intermediate bytes up to a final byte
//     0x40-0x7E — "~" and "@" end a sequence as much as a letter does.
//   - OSC, DCS, SOS, PM, APC (ESC ] P X ^ _): a string up to BEL or ST
//     (ESC \), so a window title or hyperlink target is skipped whole.
//   - Any other escape: intermediate bytes 0x20-0x2F, then one final byte
//     0x30-0x7E (ESC 7, ESC =, ESC ( B).
//
// A sequence with no terminator is skipped to the end of s, so its tail is
// never read as text. An ESC inside a sequence aborts it (as a terminal
// does) and the returned index points at that ESC. Shared with the overlay's
// line cleaner so both parse escapes the same way.
func SkipEscape(s string, i int) int {
	i++ // the ESC
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '[':
		for i++; i < len(s); i++ {
			switch c := s[i]; {
			case c == '\x1b':
				return i
			case c >= 0x40 && c <= 0x7e:
				return i + 1
			}
		}
		return i
	case ']', 'P', 'X', '^', '_':
		for i++; i < len(s); i++ {
			if s[i] == '\a' {
				return i + 1
			}
			if s[i] == '\x1b' {
				if i+1 < len(s) && s[i+1] == '\\' {
					return i + 2
				}
				return i
			}
		}
		return i
	}
	for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
		i++
	}
	if i < len(s) && s[i] >= 0x30 && s[i] <= 0x7e {
		i++
	}
	return i
}

// buildArgs constructs the command arguments based on configuration.
func (c *Channel) buildArgs(prompt string) []string {
	var args []string

	// Add configured args first
	args = append(args, c.config.Args...)

	// Add model flag for Claude Code CLI
	if c.config.Agent == AgentClaude && c.config.Model != "" {
		args = append(args, "--model", c.config.Model)
	}

	// Add non-interactive flag with prompt (only if prompt is non-empty)
	if prompt != "" {
		if c.config.NonInteractiveFlag != "" {
			args = append(args, c.config.NonInteractiveFlag, prompt)
		} else {
			args = append(args, prompt)
		}
	}

	// Add quiet flag
	if c.config.QuietFlag != "" {
		args = append(args, c.config.QuietFlag)
	}

	// Add output format if agent supports it and format is not "text"
	if c.config.SupportsJSON && c.config.OutputFormatFlag != "" &&
		c.config.OutputFormat != "" && c.config.OutputFormat != "text" {
		args = append(args, c.config.OutputFormatFlag, c.config.OutputFormat)
	}

	return args
}

// Config returns a copy of the current configuration.
func (c *Channel) Config() Config {
	return c.config
}

// GetKnownAgents returns a list of known AI agent types.
func GetKnownAgents() []AgentType {
	return []AgentType{
		AgentClaude,
		AgentCopilot,
		AgentGemini,
		AgentOpenCode,
		AgentKimi,
		AgentAuggie,
		AgentAider,
		AgentCursor,
	}
}

// DetectAvailableAgents returns a list of AI agents that are available in PATH.
func DetectAvailableAgents() []AgentType {
	var available []AgentType
	for _, agent := range GetKnownAgents() {
		ch := NewWithConfig(Config{Agent: agent})
		if ch.IsAvailable() {
			available = append(available, agent)
		}
	}
	return available
}
