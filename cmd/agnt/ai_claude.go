package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	claude "github.com/standardbeagle/claude-go"
)

// Claude-specific flags
var (
	claudeBypassPermissions bool
	claudeNoAgntPrompt      bool
	claudeAllowedTools      []string
	claudeDisallowedTools   []string
	claudeMCPConfig         string
	claudeRawOutput         bool
	claudePromptFlag        string
)

var aiClaudeCmd = &cobra.Command{
	Use:   "claude [prompt]",
	Short: "Run Claude Code with clean JSONL streaming output",
	Long: `Run Claude Code with clean JSONL streaming output over stdio.

Provides direct JSON-RPC streaming without PTY wrapping - no spinners, no ANSI
escape sequences, no terminal animation cruft. Each message is a single JSON
object on its own line, suitable for parsing by other processes.

Prompt sources (in priority order):
  1. Positional argument: agnt ai claude "prompt"
  2. Flag: agnt ai claude -p "prompt"
  3. Stdin: echo "prompt" | agnt ai claude

Output format (JSONL - one JSON object per line):
  {"type":"system","subtype":"init","session_id":"..."}
  {"type":"assistant","uuid":"..."}
  {"type":"result","duration_ms":1450,"num_turns":1,"result":"..."}

The --raw flag outputs compact JSON (no indentation) for efficient parsing.
By default, output is pretty-printed for human readability.

Integration example (parse result with jq):
  agnt ai claude --raw "Fix the lint errors" | jq -r 'select(.type=="result") | .result'`,
	Args: cobra.MaximumNArgs(1),
	Run:  runAiClaude,
}

func init() {
	// Claude-specific flags
	aiClaudeCmd.Flags().BoolVar(&claudeBypassPermissions, "bypass-permissions", true, "Bypass permission checks")
	aiClaudeCmd.Flags().BoolVar(&claudeNoAgntPrompt, "no-agnt-prompt", false, "Skip agnt system prompt injection")
	aiClaudeCmd.Flags().StringSliceVar(&claudeAllowedTools, "allowed-tools", nil, "Tools to allow (comma-separated)")
	aiClaudeCmd.Flags().StringSliceVar(&claudeDisallowedTools, "disallowed-tools", nil, "Tools to disallow (comma-separated)")
	aiClaudeCmd.Flags().StringVar(&claudeMCPConfig, "mcp-config", "", "Path to MCP config file")
	aiClaudeCmd.Flags().BoolVar(&claudeRawOutput, "raw", false, "Output raw JSON without formatting")
	aiClaudeCmd.Flags().StringVarP(&claudePromptFlag, "prompt", "p", "", "Prompt (alternative to positional arg)")
}

func runAiClaude(cmd *cobra.Command, args []string) {
	// Determine the prompt from various sources
	prompt := getPrompt(args)
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "Error: prompt is required")
		fmt.Fprintln(os.Stderr, "Usage: agnt ai claude [prompt] or agnt ai claude -p \"prompt\" or echo \"prompt\" | agnt ai claude")
		os.Exit(1)
	}

	// Set up context with signal handling
	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer cancel()

	// Build agent options
	opts := buildClaudeOptions()

	// Run the query
	if err := runClaudeQuery(ctx, prompt, opts); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// getPrompt determines the prompt from positional args, flags, or stdin.
func getPrompt(args []string) string {
	// Priority 1: Positional argument
	if len(args) > 0 && args[0] != "" {
		return args[0]
	}

	// Priority 2: -p/--prompt flag
	if claudePromptFlag != "" {
		return claudePromptFlag
	}

	// Priority 3: Stdin (only if not a terminal)
	if !isTerminal(os.Stdin) {
		reader := bufio.NewReader(os.Stdin)
		var lines []string
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					if line != "" {
						lines = append(lines, line)
					}
					break
				}
				return ""
			}
			lines = append(lines, strings.TrimSuffix(line, "\n"))
		}
		return strings.Join(lines, "\n")
	}

	return ""
}

// buildClaudeOptions constructs AgentOptions from flags.
func buildClaudeOptions() *claude.AgentOptions {
	opts := &claude.AgentOptions{
		OutputFormat: "stream-json",
		Verbose:      aiVerbose,
	}

	// Model selection (from shared ai flags)
	if aiModel != "" {
		opts.Model = aiModel
	}

	// Resource limits
	if aiMaxTurns > 0 {
		opts.MaxTurns = aiMaxTurns
	}
	if aiMaxBudget > 0 {
		opts.MaxBudgetUSD = aiMaxBudget
	}

	// System prompt: combine agnt context with user-provided prompt
	var systemPrompt string
	if !claudeNoAgntPrompt {
		// Get agnt system prompt with running services context
		socketPath, _ := rootCmd.Flags().GetString("socket")
		systemPrompt = buildAgntSystemPrompt(socketPath)
	}
	if aiSystemPrompt != "" {
		if systemPrompt != "" {
			systemPrompt = systemPrompt + "\n\n" + aiSystemPrompt
		} else {
			systemPrompt = aiSystemPrompt
		}
	}
	if systemPrompt != "" {
		opts.SystemPrompt = systemPrompt
	}

	// Permission handling
	if claudeBypassPermissions {
		opts.PermissionMode = claude.PermissionModeBypassPermission
	}

	// Tool configuration
	if len(claudeAllowedTools) > 0 {
		opts.AllowedTools = claudeAllowedTools
	}
	if len(claudeDisallowedTools) > 0 {
		opts.DisallowedTools = claudeDisallowedTools
	}

	// MCP configuration
	if claudeMCPConfig != "" {
		opts.MCPConfigPath = claudeMCPConfig
	}

	// Working directory
	if cwd, err := os.Getwd(); err == nil {
		opts.WorkingDirectory = cwd
	}

	return opts
}

// runClaudeQuery executes the query using the claude-go library and streams output.
func runClaudeQuery(ctx context.Context, prompt string, opts *claude.AgentOptions) error {
	// Create the iterator for streaming messages
	iter, err := claude.NewQueryIterator(ctx, prompt, opts)
	if err != nil {
		return fmt.Errorf("failed to create query: %w", err)
	}
	defer iter.Close()

	// Create encoder for JSON output
	encoder := json.NewEncoder(os.Stdout)
	if !claudeRawOutput {
		encoder.SetIndent("", "  ")
	}

	// Stream messages using channels for reliable handling
	msgCh := iter.Messages()
	errCh := iter.Errors()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err, ok := <-errCh:
			if !ok {
				// Error channel closed, check if messages are also done
				continue
			}
			if err != nil {
				return fmt.Errorf("query error: %w", err)
			}
		case msg, ok := <-msgCh:
			if !ok {
				// Message channel closed, we're done
				return nil
			}
			if msg == nil {
				continue
			}

			// Output the message as JSON
			if err := encoder.Encode(msg); err != nil {
				return fmt.Errorf("failed to encode message: %w", err)
			}

			// Check if this is a result message (end of query)
			if _, isResult := msg.(claude.ResultMessage); isResult {
				return nil
			}
		}
	}
}
