// Package aichannel provides an interface for communicating with AI coding agents via CLI.
package aichannel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/creack/pty"
)

// Common errors
var (
	ErrNotConfigured = errors.New("channel not configured")
	ErrNotAvailable  = errors.New("AI agent not available")
	ErrTimeout       = errors.New("request timed out")
)

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

	// Model specifies the model for API-based providers.
	// If empty, uses the provider's default model.
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
		// Log error but return nil - IsAvailable will handle this
		return nil
	}

	return langchainProvider
}

// applyDefaults fills in default values based on agent type.
func applyDefaults(config Config) Config {
	if config.Timeout == 0 {
		config.Timeout = 2 * time.Minute
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
// If context is non-empty and UseStdin is true, context is piped via stdin.
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

	// Use PTY mode if configured (required for some CLI tools like Claude Code)
	if c.config.UsePTY {
		return c.sendWithPTY(ctx, prompt, inputContext)
	}

	return c.sendWithPipe(ctx, prompt, inputContext)
}

// sendWithAPI sends a prompt using the API provider.
func (c *Channel) sendWithAPI(ctx context.Context, prompt string, inputContext string) (string, error) {
	if c.provider == nil {
		return "", fmt.Errorf("%w: provider not configured", ErrNotAvailable)
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

// SendAndParse sends a prompt and parses the response based on the configured OutputFormat.
// This provides a structured response with metadata for JSON/stream-json formats.
// For agents that don't support JSON output, it falls back to text parsing.
// For API mode, always returns a structured Response directly from the provider.
func (c *Channel) SendAndParse(ctx context.Context, prompt string, inputContext string) (*Response, error) {
	// For API mode, get response directly from provider
	if c.config.UseAPI {
		if c.provider == nil {
			return nil, fmt.Errorf("%w: provider not configured", ErrNotAvailable)
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

	return ParseResponse(rawOutput, format)
}

// sendWithPipe runs the command with standard pipes (no PTY).
func (c *Channel) sendWithPipe(ctx context.Context, prompt string, inputContext string) (string, error) {
	// Build command arguments
	args := c.buildArgs(prompt)

	// Create context with timeout
	execCtx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()

	// Create command
	cmd := exec.CommandContext(execCtx, c.config.Command, args...)

	// Set environment - inherit current environment and add custom vars
	if len(c.config.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range c.config.Env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
	}

	// Setup stdin if context is provided
	var stdin bytes.Buffer
	if inputContext != "" && c.config.UseStdin {
		stdin.WriteString(inputContext)
		cmd.Stdin = &stdin
	}

	// Capture output
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Run command
	err := cmd.Run()

	// Check for context timeout
	if execCtx.Err() == context.DeadlineExceeded {
		return "", ErrTimeout
	}

	if err != nil {
		// Include stderr in error message if available
		errMsg := stderr.String()
		if errMsg != "" {
			return "", fmt.Errorf("command failed: %w: %s", err, strings.TrimSpace(errMsg))
		}
		return "", fmt.Errorf("command failed: %w", err)
	}

	return strings.TrimSpace(stdout.String()), nil
}

// sendWithPTY runs the command in a pseudo-terminal.
// This is required for CLI tools that need a TTY (like Claude Code).
func (c *Channel) sendWithPTY(ctx context.Context, prompt string, inputContext string) (string, error) {
	// Build command arguments
	args := c.buildArgs(prompt)

	// Create context with timeout
	execCtx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()

	// Create command
	cmd := exec.CommandContext(execCtx, c.config.Command, args...)

	// Set environment - inherit current environment and add custom vars
	cmd.Env = os.Environ()
	for k, v := range c.config.Env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}

	// Start the command with a PTY
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return "", fmt.Errorf("failed to start PTY: %w", err)
	}
	defer ptmx.Close()

	// Write input context to PTY if configured
	if inputContext != "" && c.config.UseStdin {
		if _, err := ptmx.WriteString(inputContext); err != nil {
			return "", fmt.Errorf("failed to write to PTY: %w", err)
		}
	}

	// Read all output from PTY
	var output bytes.Buffer
	done := make(chan error, 1)

	go func() {
		_, err := io.Copy(&output, ptmx)
		done <- err
	}()

	// Wait for command to complete or context to be cancelled
	waitErr := cmd.Wait()

	// Wait for output reading to complete (with timeout for cleanup)
	select {
	case <-done:
		// Output reading completed
	case <-time.After(100 * time.Millisecond):
		// Give up waiting for output
	}

	// Check for context timeout
	if execCtx.Err() == context.DeadlineExceeded {
		return "", ErrTimeout
	}

	if waitErr != nil {
		// Check if it's just a signal error from context cancellation
		if execCtx.Err() != nil {
			return "", ErrTimeout
		}
		return "", fmt.Errorf("command failed: %w", waitErr)
	}

	// Clean ANSI escape sequences from output
	result := stripANSI(output.String())
	return strings.TrimSpace(result), nil
}

// stripANSI removes ANSI escape sequences from a string.
func stripANSI(s string) string {
	var result strings.Builder
	inEscape := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			// Check for CSI sequences (ESC [)
			if s[i] == '[' {
				// Skip until we hit a letter (the command byte)
				for i++; i < len(s); i++ {
					if (s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z') {
						break
					}
				}
				inEscape = false
				continue
			}
			// Check for OSC sequences (ESC ])
			if s[i] == ']' {
				// Skip until BEL or ST
				for i++; i < len(s); i++ {
					if s[i] == '\x07' { // BEL
						break
					}
					if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '\\' { // ST
						i++
						break
					}
				}
				inEscape = false
				continue
			}
			// Single character escape
			inEscape = false
			continue
		}
		// Skip carriage returns (terminal line endings)
		if s[i] == '\r' {
			continue
		}
		result.WriteByte(s[i])
	}
	return result.String()
}

// buildArgs constructs the command arguments based on configuration.
func (c *Channel) buildArgs(prompt string) []string {
	var args []string

	// Add configured args first
	args = append(args, c.config.Args...)

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
