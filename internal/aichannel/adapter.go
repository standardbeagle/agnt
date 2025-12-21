// Package aichannel provides an interface for communicating with AI coding agents.
package aichannel

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/creack/pty"
)

// AgentAdapter defines how to invoke a specific AI agent CLI.
// Each agent may have different CLI patterns for:
// - Passing prompts (flags, arguments, stdin)
// - Passing context (embedded in prompt, stdin, or files)
// - Passing system prompts (flags or not supported)
// - Output formats
type AgentAdapter interface {
	// Name returns the adapter name for logging
	Name() string

	// BuildCommand constructs the exec.Cmd for invoking the agent.
	// It receives the full config, prompt, context, and system prompt.
	BuildCommand(ctx context.Context, config Config, prompt, inputContext, systemPrompt string) (*exec.Cmd, error)

	// RequiresPTY returns true if this agent needs a pseudo-terminal
	RequiresPTY() bool

	// StdinData returns any data that should be written to stdin after command starts.
	// Returns empty string if all data is passed via command args.
	StdinData(config Config, prompt, inputContext, systemPrompt string) string

	// ParseOutput processes the raw output from the agent and returns clean text.
	ParseOutput(output string) string
}

// GetAdapter returns the appropriate adapter for the given agent type.
func GetAdapter(agent AgentType) AgentAdapter {
	switch agent {
	case AgentClaude:
		return &ClaudeAdapter{}
	case AgentCopilot:
		return &CopilotAdapter{}
	case AgentGemini:
		return &GeminiAdapter{}
	case AgentAider:
		return &AiderAdapter{}
	default:
		return &GenericAdapter{agentType: agent}
	}
}

// ========== Claude Adapter ==========

// ClaudeAdapter handles Claude Code CLI invocation.
// Claude CLI uses:
// - `-p <prompt>` for non-interactive mode (prompt includes context)
// - `--system-prompt <prompt>` for custom system prompt
// - `--model <model>` for model selection
// - `--output-format <format>` for structured output
// - Does NOT read from stdin in -p mode
type ClaudeAdapter struct{}

func (a *ClaudeAdapter) Name() string {
	return "claude"
}

func (a *ClaudeAdapter) RequiresPTY() bool {
	return true // Claude Code requires PTY for -p mode
}

func (a *ClaudeAdapter) BuildCommand(ctx context.Context, config Config, prompt, inputContext, systemPrompt string) (*exec.Cmd, error) {
	var args []string

	// Add any pre-configured args
	args = append(args, config.Args...)

	// Add model flag
	if config.Model != "" {
		args = append(args, "--model", config.Model)
	}

	// Add system prompt if provided
	if systemPrompt != "" {
		args = append(args, "--system-prompt", systemPrompt)
	}

	// Build the full prompt with embedded context
	fullPrompt := a.buildFullPrompt(prompt, inputContext)

	// Add non-interactive flag with the full prompt
	args = append(args, "-p", fullPrompt)

	// Add output format if configured
	if config.SupportsJSON && config.OutputFormat != "" && config.OutputFormat != "text" {
		args = append(args, "--output-format", config.OutputFormat)
	}

	cmd := exec.CommandContext(ctx, config.Command, args...)

	// Set environment
	cmd.Env = os.Environ()
	for k, v := range config.Env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}

	return cmd, nil
}

func (a *ClaudeAdapter) buildFullPrompt(prompt, inputContext string) string {
	if inputContext == "" {
		return prompt
	}

	// Embed context in the prompt using XML-style tags for clarity
	return fmt.Sprintf("<context>\n%s\n</context>\n\n%s", inputContext, prompt)
}

func (a *ClaudeAdapter) StdinData(config Config, prompt, inputContext, systemPrompt string) string {
	// Claude -p mode doesn't use stdin - everything is in the -p argument
	return ""
}

func (a *ClaudeAdapter) ParseOutput(output string) string {
	return stripANSI(output)
}

// ========== Copilot Adapter ==========

// CopilotAdapter handles GitHub Copilot CLI invocation.
type CopilotAdapter struct{}

func (a *CopilotAdapter) Name() string {
	return "copilot"
}

func (a *CopilotAdapter) RequiresPTY() bool {
	return false
}

func (a *CopilotAdapter) BuildCommand(ctx context.Context, config Config, prompt, inputContext, systemPrompt string) (*exec.Cmd, error) {
	var args []string

	args = append(args, config.Args...)

	// Copilot uses -p for prompt and -s for silent mode
	fullPrompt := a.buildFullPrompt(prompt, inputContext)
	if config.NonInteractiveFlag != "" {
		args = append(args, config.NonInteractiveFlag, fullPrompt)
	} else {
		args = append(args, "-p", fullPrompt)
	}

	if config.QuietFlag != "" {
		args = append(args, config.QuietFlag)
	} else {
		args = append(args, "-s")
	}

	cmd := exec.CommandContext(ctx, config.Command, args...)

	if len(config.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range config.Env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
	}

	return cmd, nil
}

func (a *CopilotAdapter) buildFullPrompt(prompt, inputContext string) string {
	if inputContext == "" {
		return prompt
	}
	return fmt.Sprintf("Context:\n%s\n\nRequest:\n%s", inputContext, prompt)
}

func (a *CopilotAdapter) StdinData(config Config, prompt, inputContext, systemPrompt string) string {
	return ""
}

func (a *CopilotAdapter) ParseOutput(output string) string {
	return strings.TrimSpace(output)
}

// ========== Gemini Adapter ==========

// GeminiAdapter handles Google Gemini CLI invocation.
type GeminiAdapter struct{}

func (a *GeminiAdapter) Name() string {
	return "gemini"
}

func (a *GeminiAdapter) RequiresPTY() bool {
	return false
}

func (a *GeminiAdapter) BuildCommand(ctx context.Context, config Config, prompt, inputContext, systemPrompt string) (*exec.Cmd, error) {
	var args []string

	args = append(args, config.Args...)

	// Gemini uses -e for execute mode
	fullPrompt := a.buildFullPrompt(prompt, inputContext)
	if config.NonInteractiveFlag != "" {
		args = append(args, config.NonInteractiveFlag, fullPrompt)
	} else {
		args = append(args, "-e", fullPrompt)
	}

	if config.QuietFlag != "" {
		args = append(args, config.QuietFlag)
	}

	// Output format
	if config.SupportsJSON && config.OutputFormat != "" && config.OutputFormat != "text" {
		args = append(args, "--output-format", config.OutputFormat)
	}

	cmd := exec.CommandContext(ctx, config.Command, args...)

	if len(config.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range config.Env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
	}

	return cmd, nil
}

func (a *GeminiAdapter) buildFullPrompt(prompt, inputContext string) string {
	if inputContext == "" {
		return prompt
	}
	return fmt.Sprintf("<context>\n%s\n</context>\n\n%s", inputContext, prompt)
}

func (a *GeminiAdapter) StdinData(config Config, prompt, inputContext, systemPrompt string) string {
	return ""
}

func (a *GeminiAdapter) ParseOutput(output string) string {
	return strings.TrimSpace(output)
}

// ========== Aider Adapter ==========

// AiderAdapter handles Aider CLI invocation.
type AiderAdapter struct{}

func (a *AiderAdapter) Name() string {
	return "aider"
}

func (a *AiderAdapter) RequiresPTY() bool {
	return false
}

func (a *AiderAdapter) BuildCommand(ctx context.Context, config Config, prompt, inputContext, systemPrompt string) (*exec.Cmd, error) {
	var args []string

	args = append(args, config.Args...)

	// Aider uses --message for non-interactive
	fullPrompt := a.buildFullPrompt(prompt, inputContext)
	args = append(args, "--message", fullPrompt)

	// Aider can use --no-stream for non-streaming output
	args = append(args, "--no-stream")

	cmd := exec.CommandContext(ctx, config.Command, args...)

	if len(config.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range config.Env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
	}

	return cmd, nil
}

func (a *AiderAdapter) buildFullPrompt(prompt, inputContext string) string {
	if inputContext == "" {
		return prompt
	}
	return fmt.Sprintf("Context:\n%s\n\nRequest:\n%s", inputContext, prompt)
}

func (a *AiderAdapter) StdinData(config Config, prompt, inputContext, systemPrompt string) string {
	return ""
}

func (a *AiderAdapter) ParseOutput(output string) string {
	return strings.TrimSpace(output)
}

// ========== Generic Adapter ==========

// GenericAdapter provides a fallback for unknown agents.
// It uses stdin for context if UseStdin is configured.
type GenericAdapter struct {
	agentType AgentType
}

func (a *GenericAdapter) Name() string {
	return string(a.agentType)
}

func (a *GenericAdapter) RequiresPTY() bool {
	return false
}

func (a *GenericAdapter) BuildCommand(ctx context.Context, config Config, prompt, inputContext, systemPrompt string) (*exec.Cmd, error) {
	var args []string

	args = append(args, config.Args...)

	// Use non-interactive flag if available
	if config.NonInteractiveFlag != "" && prompt != "" {
		args = append(args, config.NonInteractiveFlag, prompt)
	} else if prompt != "" {
		args = append(args, prompt)
	}

	if config.QuietFlag != "" {
		args = append(args, config.QuietFlag)
	}

	cmd := exec.CommandContext(ctx, config.Command, args...)

	if len(config.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range config.Env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
	}

	return cmd, nil
}

func (a *GenericAdapter) StdinData(config Config, prompt, inputContext, systemPrompt string) string {
	if config.UseStdin && inputContext != "" {
		return inputContext
	}
	return ""
}

func (a *GenericAdapter) ParseOutput(output string) string {
	return strings.TrimSpace(output)
}

// ========== Adapter Runner ==========

// RunWithAdapter executes an AI agent using the appropriate adapter.
func RunWithAdapter(ctx context.Context, config Config, prompt, inputContext, systemPrompt string) (string, error) {
	adapter := GetAdapter(config.Agent)

	// Create command with timeout
	execCtx, cancel := context.WithTimeout(ctx, config.Timeout)
	defer cancel()

	cmd, err := adapter.BuildCommand(execCtx, config, prompt, inputContext, systemPrompt)
	if err != nil {
		return "", fmt.Errorf("failed to build command: %w", err)
	}

	if adapter.RequiresPTY() {
		return runWithPTY(execCtx, cmd, adapter, config, prompt, inputContext, systemPrompt)
	}
	return runWithPipe(execCtx, cmd, adapter, config, prompt, inputContext, systemPrompt)
}

func runWithPTY(ctx context.Context, cmd *exec.Cmd, adapter AgentAdapter, config Config, prompt, inputContext, systemPrompt string) (string, error) {
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return "", fmt.Errorf("failed to start PTY: %w", err)
	}
	defer ptmx.Close()

	// Write any stdin data if the adapter requires it
	stdinData := adapter.StdinData(config, prompt, inputContext, systemPrompt)
	if stdinData != "" {
		if _, err := ptmx.WriteString(stdinData); err != nil {
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

	// Wait for command to complete
	waitErr := cmd.Wait()

	// Wait for output reading to complete (with timeout for cleanup)
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
	}

	// Check for context timeout
	if ctx.Err() == context.DeadlineExceeded {
		return "", ErrTimeout
	}

	if waitErr != nil {
		if ctx.Err() != nil {
			return "", ErrTimeout
		}
		return "", fmt.Errorf("command failed: %w", waitErr)
	}

	return adapter.ParseOutput(output.String()), nil
}

func runWithPipe(ctx context.Context, cmd *exec.Cmd, adapter AgentAdapter, config Config, prompt, inputContext, systemPrompt string) (string, error) {
	// Setup stdin if adapter has data
	stdinData := adapter.StdinData(config, prompt, inputContext, systemPrompt)
	if stdinData != "" {
		cmd.Stdin = strings.NewReader(stdinData)
	}

	// Capture output
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Run command
	err := cmd.Run()

	// Check for context timeout
	if ctx.Err() == context.DeadlineExceeded {
		return "", ErrTimeout
	}

	if err != nil {
		errMsg := stderr.String()
		if errMsg != "" {
			return "", fmt.Errorf("command failed: %w: %s", err, strings.TrimSpace(errMsg))
		}
		return "", fmt.Errorf("command failed: %w", err)
	}

	return adapter.ParseOutput(stdout.String()), nil
}
