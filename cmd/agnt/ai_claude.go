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
	"sync"
	"syscall"
	"time"

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
	Long: `Run Claude Code with streaming output over stdio.

Prompt sources (in priority order):
  1. Positional argument: agnt ai claude "prompt"
  2. Flag: agnt ai claude -p "prompt"
  3. Stdin: echo "prompt" | agnt ai claude
  4. Interactive: agnt ai claude (opens REPL when no prompt and stdin is a terminal)

Interactive mode shows human-readable output: assistant text goes to stdout,
status indicators and tool activity appear on stderr. Multi-turn conversation
is maintained via session resumption. Exit with /exit, /quit, or Ctrl+D.

When piped or with --raw, output is JSONL (one JSON object per line):
  {"type":"system","subtype":"init","session_id":"..."}
  {"type":"assistant","uuid":"..."}
  {"type":"result","duration_ms":1450,"num_turns":1,"result":"..."}

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
	aiClaudeCmd.Flags().BoolVar(&claudeRawOutput, "raw", false, "Output compact JSON (JSONL) instead of interactive rendering")
	aiClaudeCmd.Flags().StringVarP(&claudePromptFlag, "prompt", "p", "", "Prompt (alternative to positional arg)")
}

func runAiClaude(cmd *cobra.Command, args []string) {
	// Set up context with signal handling
	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer cancel()

	// Determine the prompt from various sources
	prompt := getPrompt(args)
	if prompt == "" && isTerminal(os.Stdin) {
		// Interactive REPL mode
		if err := runAiClaudeInteractive(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if prompt == "" {
		fmt.Fprintln(os.Stderr, "Error: prompt is required")
		fmt.Fprintln(os.Stderr, "Usage: agnt ai claude [prompt] or agnt ai claude -p \"prompt\" or echo \"prompt\" | agnt ai claude")
		os.Exit(1)
	}

	// Build agent options
	opts := buildClaudeOptions()

	// Run the query (one-shot is never interactive)
	if _, err := runClaudeQuery(ctx, prompt, opts, false); err != nil {
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

// runAiClaudeInteractive runs an interactive REPL loop for multi-turn conversation.
func runAiClaudeInteractive(ctx context.Context) error {
	scanner := bufio.NewScanner(os.Stdin)
	opts := buildClaudeOptions()
	var sessionID string
	interactive := !claudeRawOutput

	// Welcome message
	if interactive {
		fmt.Fprintln(os.Stderr, "agnt ai claude - interactive mode")
		fmt.Fprintln(os.Stderr, "Type /exit or /quit to exit, Ctrl+D for EOF.")
		if opts.SystemPrompt != "" {
			fmt.Fprintln(os.Stderr, "[agnt context injected]")
		}
	} else {
		fmt.Fprintln(os.Stderr, "Interactive mode. Type /exit or /quit to exit, Ctrl+D for EOF.")
	}

	for {
		fmt.Fprint(os.Stderr, "> ")
		if !scanner.Scan() {
			// EOF or scan error
			fmt.Fprintln(os.Stderr)
			return scanner.Err()
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "/exit" || line == "/quit" {
			return nil
		}

		if sessionID != "" {
			opts.Resume = sessionID
		}

		sid, err := runClaudeQuery(ctx, line, opts, interactive)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			continue
		}
		if sid != "" {
			sessionID = sid
		}

		// Add blank line between responses in interactive mode
		if interactive {
			fmt.Fprintln(os.Stderr)
		}
	}
}

// runClaudeQuery executes the query using the claude-go library and streams output.
// Returns the session ID from the ResultMessage for multi-turn resumption.
func runClaudeQuery(ctx context.Context, prompt string, opts *claude.AgentOptions, interactive bool) (string, error) {
	iter, err := claude.NewQueryIterator(ctx, prompt, opts)
	if err != nil {
		return "", fmt.Errorf("failed to create query: %w", err)
	}
	defer iter.Close()

	if interactive {
		return streamInteractive(ctx, iter)
	}
	return streamJSON(ctx, iter)
}

// streamInteractive renders messages as human-readable output.
func streamInteractive(ctx context.Context, iter *claude.QueryIterator) (string, error) {
	msgCh := iter.Messages()
	errCh := iter.Errors()

	var spin *stderrSpinner
	textPrinted := false

	defer func() {
		if spin != nil {
			spin.Stop()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case err, ok := <-errCh:
			if !ok {
				continue
			}
			if err != nil {
				return "", fmt.Errorf("query error: %w", err)
			}
		case msg, ok := <-msgCh:
			if !ok {
				return "", nil
			}
			if msg == nil {
				continue
			}

			sid := renderMessageTo(msg, &spin, os.Stdout, os.Stderr, &textPrinted)
			if sid != "" {
				return sid, nil
			}
		}
	}
}

// streamJSON outputs each message as a JSON line (original behavior).
func streamJSON(ctx context.Context, iter *claude.QueryIterator) (string, error) {
	encoder := json.NewEncoder(os.Stdout)
	if !claudeRawOutput {
		encoder.SetIndent("", "  ")
	}

	msgCh := iter.Messages()
	errCh := iter.Errors()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case err, ok := <-errCh:
			if !ok {
				continue
			}
			if err != nil {
				return "", fmt.Errorf("query error: %w", err)
			}
		case msg, ok := <-msgCh:
			if !ok {
				return "", nil
			}
			if msg == nil {
				continue
			}

			if err := encoder.Encode(msg); err != nil {
				return "", fmt.Errorf("failed to encode message: %w", err)
			}

			if result, isResult := msg.(claude.ResultMessage); isResult {
				return result.SessionID, nil
			}
		}
	}
}

// renderMessage renders a single message in interactive mode (convenience wrapper).
func renderMessage(msg claude.MessageType, spin **stderrSpinner) string {
	var textPrinted bool
	return renderMessageTo(msg, spin, os.Stdout, os.Stderr, &textPrinted)
}

// renderMessageTo renders a message using the provided writers.
// textPrinted tracks whether any text was printed during this query (for fallback).
func renderMessageTo(msg claude.MessageType, spin **stderrSpinner, stdout, stderr io.Writer, textPrinted *bool) string {
	switch m := msg.(type) {
	case claude.SystemMessage:
		renderSystemMessage(m, spin, stderr)

	case claude.AssistantMessage:
		renderAssistantMessage(m, spin, stdout, stderr, textPrinted)

	case claude.ResultMessage:
		if *spin != nil {
			(*spin).Stop()
			*spin = nil
		}
		// Fallback: print result text if no streaming text was rendered
		if textPrinted != nil && !*textPrinted && m.Result != "" {
			fmt.Fprintln(stdout, m.Result)
		}
		renderResultSummary(m, stderr)
		return m.SessionID

	default:
		// StreamEvent, UserMessage — silent
	}
	return ""
}

func renderSystemMessage(m claude.SystemMessage, spin **stderrSpinner, stderr io.Writer) {
	switch m.Subtype {
	case "init":
		// Silent
	case "hook_started":
		if *spin != nil {
			(*spin).Stop()
		}
		*spin = newStderrSpinner("Hook running...", stderr)
	case "hook_response":
		if *spin != nil {
			(*spin).Stop()
			*spin = nil
		}
	default:
		if *spin != nil {
			(*spin).Stop()
		}
		*spin = newStderrSpinner(m.Subtype+"...", stderr)
	}
}

func renderAssistantMessage(m claude.AssistantMessage, spin **stderrSpinner, stdout, stderr io.Writer, textPrinted *bool) {
	for _, block := range m.Content {
		switch b := block.(type) {
		case claude.TextBlock:
			if *spin != nil {
				(*spin).Stop()
				*spin = nil
			}
			fmt.Fprintln(stdout, b.Text)
			if textPrinted != nil {
				*textPrinted = true
			}

		case claude.ToolUseBlock:
			if *spin != nil {
				(*spin).Stop()
			}
			fmt.Fprintf(stderr, "\r\033[K[tool: %s]\n", b.Name)
			*spin = newStderrSpinner("Working...", stderr)

		case claude.ThinkingBlock:
			if *spin != nil {
				(*spin).Stop()
			}
			*spin = newStderrSpinner("Thinking...", stderr)

		case claude.ToolResultBlock:
			if b.IsError {
				if *spin != nil {
					(*spin).Stop()
					*spin = nil
				}
				fmt.Fprintf(stderr, "\r\033[K[tool error]\n")
			}
			// Non-error results are silent
		}
	}
}

// renderResultSummary prints a compact summary line to stderr.
func renderResultSummary(m claude.ResultMessage, stderr io.Writer) {
	var parts []string

	// Duration
	if m.DurationMS > 0 {
		parts = append(parts, formatDuration(m.DurationMS))
	}

	// Token count
	if m.Usage != nil {
		total := m.Usage.InputTokens + m.Usage.OutputTokens
		if total > 0 {
			parts = append(parts, formatTokens(total))
		}
	}

	// Cost
	if m.TotalCostUSD > 0 {
		parts = append(parts, fmt.Sprintf("$%.2f", m.TotalCostUSD))
	}

	if len(parts) > 0 {
		fmt.Fprintf(stderr, "\r\033[K(%s)\n", strings.Join(parts, " · "))
	}
}

// formatDuration converts milliseconds to a human-readable duration string.
func formatDuration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	mins := int(d.Minutes())
	secs := int(d.Seconds()) % 60
	if secs == 0 {
		return fmt.Sprintf("%dm", mins)
	}
	return fmt.Sprintf("%dm %ds", mins, secs)
}

// formatTokens formats a token count with k/M suffixes.
func formatTokens(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM tokens", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fk tokens", float64(n)/1_000)
	}
	return fmt.Sprintf("%d tokens", n)
}

// stderrSpinner displays a braille spinner animation on a writer using \r overwrite.
type stderrSpinner struct {
	done chan struct{}
	wg   sync.WaitGroup
	w    io.Writer
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// newStderrSpinner starts a braille spinner with the given message.
func newStderrSpinner(message string, w io.Writer) *stderrSpinner {
	s := &stderrSpinner{
		done: make(chan struct{}),
		w:    w,
	}
	s.wg.Add(1)

	go func() {
		defer s.wg.Done()
		i := 0
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-s.done:
				fmt.Fprintf(s.w, "\r\033[K")
				return
			case <-ticker.C:
				fmt.Fprintf(s.w, "\r%s %s", spinnerFrames[i%len(spinnerFrames)], message)
				i++
			}
		}
	}()

	return s
}

// Stop halts the spinner and clears the line.
func (s *stderrSpinner) Stop() {
	select {
	case <-s.done:
		// Already stopped
	default:
		close(s.done)
	}
	s.wg.Wait()
}
