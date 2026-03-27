//go:build unix

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

	"github.com/charmbracelet/glamour"
	"github.com/spf13/cobra"
	"github.com/standardbeagle/agnt/internal/aichannel"
	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/overlay"
	"github.com/standardbeagle/agnt/internal/protocol"
	claude "github.com/standardbeagle/claude-go"
	"golang.org/x/term"
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
	claudeSessionCode       string
	claudeNoAutostart       bool
	claudeNoSession         bool
	claudeNoOverlay         bool
	claudeNoIndicator       bool
)

// mdRenderer lazily initializes a glamour markdown renderer.
var mdRenderer *glamour.TermRenderer

func getMarkdownRenderer() *glamour.TermRenderer {
	if mdRenderer == nil {
		width := 80
		if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
			width = w
		}
		r, err := glamour.NewTermRenderer(
			glamour.WithAutoStyle(),
			glamour.WithWordWrap(width),
		)
		if err == nil {
			mdRenderer = r
		}
	}
	return mdRenderer
}

// renderMarkdown renders markdown text to ANSI. Falls back to plain text on error.
func renderMarkdown(text string) string {
	r := getMarkdownRenderer()
	if r == nil {
		return text
	}
	rendered, err := r.Render(text)
	if err != nil {
		return text
	}
	// glamour adds a trailing newline; trim it since callers add their own
	return strings.TrimRight(rendered, "\n")
}

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
	aiClaudeCmd.Flags().StringVar(&claudeSessionCode, "session", "", "Session code for daemon integration (auto-generated if empty)")
	aiClaudeCmd.Flags().BoolVar(&claudeNoAutostart, "no-autostart", false, "Skip auto-starting scripts and proxies from .agnt.kdl")
	aiClaudeCmd.Flags().BoolVar(&claudeNoSession, "no-session", false, "Disable daemon integration entirely")
	aiClaudeCmd.Flags().BoolVar(&claudeNoOverlay, "no-overlay", false, "Disable terminal overlay (status bar, menus)")
	aiClaudeCmd.Flags().BoolVar(&claudeNoIndicator, "no-indicator", false, "Disable indicator bar (keep overlay menus)")
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

	// Build agent options (without system prompt — added after daemon registration)
	opts := buildClaudeOptions()

	// One-shot with optional daemon autostart
	var daemonHandle *daemonSessionHandle
	if !claudeNoSession {
		code := claudeSessionCode
		if code == "" {
			code = generateSessionCode("ai-claude")
		}
		projectPath, _ := os.Getwd()
		daemonSocketPath, _ := rootCmd.Flags().GetString("socket")
		daemonHandle = startDaemonSession(ctx, daemonSessionConfig{
			SessionCode:   code,
			ProjectPath:   projectPath,
			Command:       "agnt",
			CmdArgs:       []string{"ai", "claude"},
			SocketPath:    daemonSocketPath,
			SkipAutostart: claudeNoAutostart,
		})
		defer daemonHandle.Close()
		// Wait for autostart to complete so system prompt includes runtime state
		daemonHandle.WaitRegistered(2 * time.Second)
		printDaemonStatus(daemonHandle)
	}

	// Apply system prompt after daemon registration for runtime-aware context
	applyAgntSystemPrompt(opts)

	// Run the query (one-shot is never interactive)
	if _, err := runClaudeQuery(ctx, prompt, opts, false, nil); err != nil {
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

// buildClaudeOptions constructs AgentOptions from flags (without system prompt).
// Call applyAgntSystemPrompt after daemon registration to add runtime-aware context.
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

// applyAgntSystemPrompt builds and sets the system prompt on the given options.
// Called after daemon registration so the prompt includes runtime state (running processes/proxies).
func applyAgntSystemPrompt(opts *claude.AgentOptions) {
	var systemPrompt string
	if !claudeNoAgntPrompt {
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
}

// runAiClaudeInteractive runs an interactive REPL loop for multi-turn conversation.
// When daemon integration is enabled, it starts an AI overlay server and registers
// a daemon session, allowing message injection via `agnt session send/schedule`
// and browser events.
//
// When the terminal overlay is enabled (default), the REPL runs in raw mode with
// the same overlay infrastructure as `agnt run`: status bar, Ctrl+Y menu, and
// process viewer. The LineEditor handles input in raw mode.
func runAiClaudeInteractive(ctx context.Context) error {
	opts := buildClaudeOptions()
	interactive := !claudeRawOutput
	useOverlay := interactive && !claudeNoOverlay && isTerminal(os.Stdin)

	// Enable markdown rendering for interactive modes
	useMarkdown = interactive

	// Daemon integration
	var aiOverlay *AIOverlay
	var daemonHandle *daemonSessionHandle
	var msgCh <-chan string

	if !claudeNoSession {
		code := claudeSessionCode
		if code == "" {
			code = generateSessionCode("ai-claude")
		}

		// Start AI overlay server
		aiOverlay = newAIOverlay(code)
		if err := aiOverlay.Start(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "[agnt] overlay start failed: %v\n", err)
		} else {
			defer aiOverlay.Stop()
			msgCh = aiOverlay.Messages()
		}

		// Register daemon session (triggers .agnt.kdl autostart)
		projectPath, _ := os.Getwd()
		daemonSocketPath, _ := rootCmd.Flags().GetString("socket")
		overlayEndpoint := ""
		if aiOverlay != nil {
			overlayEndpoint = aiOverlay.SocketPath()
		}
		daemonHandle = startDaemonSession(ctx, daemonSessionConfig{
			SessionCode:     code,
			OverlayEndpoint: overlayEndpoint,
			ProjectPath:     projectPath,
			Command:         "agnt",
			CmdArgs:         []string{"ai", "claude"},
			SocketPath:      daemonSocketPath,
			SkipAutostart:   claudeNoAutostart,
		})
		defer daemonHandle.Close()

		// Wait for registration to complete before building system prompt
		daemonHandle.WaitRegistered(5 * time.Second)
	}

	// Build system prompt AFTER daemon registration so it includes runtime state
	applyAgntSystemPrompt(opts)

	if useOverlay {
		return runAiClaudeOverlay(ctx, opts, daemonHandle, msgCh)
	}
	return runAiClaudeLegacy(ctx, opts, daemonHandle, msgCh)
}

// runAiClaudeLegacy is the original cooked-mode REPL (--no-overlay or non-terminal).
func runAiClaudeLegacy(ctx context.Context, opts *claude.AgentOptions, daemonHandle *daemonSessionHandle, msgCh <-chan string) error {
	var sessionID string
	interactive := !claudeRawOutput

	// Welcome message
	if interactive {
		fmt.Fprintln(os.Stderr, "agnt ai claude - interactive mode")
		fmt.Fprintln(os.Stderr, "Type /exit or /quit to exit, Ctrl+D for EOF.")
		if opts.SystemPrompt != "" {
			fmt.Fprintln(os.Stderr, "[agnt context injected]")
		}
		printDaemonStatus(daemonHandle)
	} else {
		fmt.Fprintln(os.Stderr, "Interactive mode. Type /exit or /quit to exit, Ctrl+D for EOF.")
	}

	// Wrap blocking stdin scanner in a goroutine → channel
	stdinCh := make(chan string)
	go func() {
		defer close(stdinCh)
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			stdinCh <- scanner.Text()
		}
	}()

	// REPL loop: multiplex stdin and message queue
	for {
		fmt.Fprint(os.Stderr, "> ")

		var prompt string
		var fromMessage bool

		if msgCh != nil {
			select {
			case line, ok := <-stdinCh:
				if !ok {
					fmt.Fprintln(os.Stderr)
					return nil
				}
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				if line == "/exit" || line == "/quit" {
					return nil
				}
				prompt = line
			case msg := <-msgCh:
				fmt.Fprint(os.Stderr, "\r\033[K")
				fmt.Fprintf(os.Stderr, "[message] %s\n", msg)
				prompt = msg
				fromMessage = true
			case <-ctx.Done():
				return ctx.Err()
			}
		} else {
			select {
			case line, ok := <-stdinCh:
				if !ok {
					fmt.Fprintln(os.Stderr)
					return nil
				}
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				if line == "/exit" || line == "/quit" {
					return nil
				}
				prompt = line
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		if sessionID != "" {
			opts.Resume = sessionID
		}

		var spin *stderrSpinner
		if interactive {
			label := "Starting..."
			if fromMessage {
				label = "Processing message..."
			}
			spin = newStderrSpinner(label, os.Stderr)
		}

		sid, err := runClaudeQuery(ctx, prompt, opts, interactive, spin)
		if err != nil {
			if spin != nil {
				spin.Stop()
			}
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			continue
		}
		if sid != "" {
			sessionID = sid
		}

		if interactive {
			fmt.Fprintln(os.Stderr)
		}

		// Drain queued messages
		if msgCh != nil {
			for {
				select {
				case msg := <-msgCh:
					fmt.Fprintf(os.Stderr, "[queued message] %s\n", msg)
					opts.Resume = sessionID
					if interactive {
						spin = newStderrSpinner("Processing message...", os.Stderr)
					}
					sid, err := runClaudeQuery(ctx, msg, opts, interactive, spin)
					if err != nil {
						if spin != nil {
							spin.Stop()
						}
						fmt.Fprintf(os.Stderr, "Error: %v\n", err)
						continue
					}
					if sid != "" {
						sessionID = sid
					}
					if interactive {
						fmt.Fprintln(os.Stderr)
					}
				default:
					goto nextPrompt
				}
			}
		nextPrompt:
		}
	}
}

// runAiClaudeOverlay runs the REPL with full overlay support (raw mode, status bar, menus).
func runAiClaudeOverlay(ctx context.Context, opts *claude.AgentOptions, daemonHandle *daemonSessionHandle, msgCh <-chan string) error {
	var sessionID string

	// Get terminal size
	width, height := 80, 24
	if w, h, err := term.GetSize(int(os.Stdin.Fd())); err == nil {
		width, height = w, h
	}

	// Enter raw mode
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("failed to set raw mode: %w", err)
	}
	defer func() {
		_ = term.Restore(int(os.Stdin.Fd()), oldState)
	}()

	showIndicator := !claudeNoIndicator

	// Build overlay component chain (same pattern as run.go)
	// OutputGate → ProtectedWriter → overlayWriter (for REPL output)
	outputGate := overlay.NewOutputGate(os.Stdout)

	// Use a pointer so OnRedraw closure can capture it before overlay exists
	var termOverlay *overlay.Overlay

	var outputWriter io.Writer = outputGate
	var outputFilter *overlay.ProtectedWriter

	if showIndicator {
		filterCfg := overlay.FilterConfig{
			ProtectBottomRows: 1,
			RedrawInterval:    200 * time.Millisecond,
			OnRedraw: func() {
				if termOverlay != nil {
					termOverlay.Redraw()
				}
			},
		}
		outputFilter = overlay.NewProtectedWriter(outputGate, width, height, filterCfg)
		outputWriter = outputFilter
	}

	// Wrap with \n → \r\n translation for raw mode
	rawWriter := newRawModeWriter(outputWriter)

	lineEditor := NewLineEditor(rawWriter, "> ")
	replAdapter := NewREPLAdapter(lineEditor)

	// Use a pointer so OnAction closure can capture statusFetcher before it's created
	var statusFetcher *overlay.StatusFetcher

	cfg := overlay.DefaultConfig()
	cfg.ShowIndicator = showIndicator
	cfg.Version = appVersion
	cfg.OnAction = func(action overlay.Action) error {
		if action == overlay.ActionRefreshStatus && statusFetcher != nil {
			statusFetcher.Refresh()
		}
		return nil
	}

	// Create overlay (uses replAdapter as "ptmx")
	termOverlay = overlay.New(replAdapter, width, height, cfg)
	termOverlay.SetGate(outputGate)

	// InputRouter: routes stdin between overlay and line editor
	inputRouter := overlay.NewInputRouter(replAdapter, termOverlay, cfg.Hotkey)

	// OutputGate callbacks for menu open/close
	outputGate.SetCallbacks(nil, func() {
		if outputFilter != nil {
			outputFilter.EnforceScrollRegion()
		}
		lineEditor.Redraw()
	})

	// Set up shared daemon connection for overlay components
	daemonSocketPath, _ := rootCmd.Flags().GetString("socket")
	daemonConn := daemon.NewConn(daemonSocketPath)
	defer daemonConn.Close()

	bashRunner := overlay.NewDaemonBashRunner(daemonConn)
	inputRouter.SetBashRunner(bashRunner)
	outputFetcher := overlay.NewDaemonOutputFetcher(daemonConn)
	inputRouter.SetOutputFetcher(outputFetcher)
	daemonConnector := overlay.NewDaemonConnector(daemonConn)
	inputRouter.SetDaemonConnector(daemonConnector)
	scriptController := overlay.NewDaemonScriptController(daemonConn)
	inputRouter.SetScriptController(scriptController)

	// Set up summarizer
	if agent := detectAIAgent(); agent != "" {
		projectPath, _ := os.Getwd()
		summarizer := overlay.NewSummarizer(daemonConn, overlay.SummarizerConfig{
			Agent:       aichannel.AgentType(agent),
			Timeout:     2 * time.Minute,
			ProjectPath: projectPath,
		})
		inputRouter.SetSummarizer(summarizer)
	}

	// Start status fetcher
	statusFetcher = overlay.NewStatusFetcher(daemonConn, termOverlay, 2*time.Second)
	statusFetcher.Start(ctx)
	defer statusFetcher.Stop()

	inputRouter.SetStatusFetcher(statusFetcher)

	// SIGWINCH handler
	sizeCh := make(chan os.Signal, 1)
	signal.Notify(sizeCh, syscall.SIGWINCH)
	defer signal.Stop(sizeCh)

	go func() {
		for range sizeCh {
			w, h, err := term.GetSize(int(os.Stdin.Fd()))
			if err != nil {
				continue
			}
			termOverlay.SetSize(w, h)
			if outputFilter != nil {
				outputFilter.SetSize(w, h)
				outputFilter.EnforceScrollRegion()
			}
			lineEditor.Redraw()
		}
	}()

	// Start input router in background
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		inputRouter.Run()
	}()

	// Clear screen and set up scroll region before any output
	fmt.Fprint(os.Stdout, "\x1b[2J\x1b[H")
	if showIndicator && outputFilter != nil {
		outputFilter.EnforceScrollRegion()
		termOverlay.Redraw()
	}

	// Welcome message through raw mode writer (above cursor)
	fmt.Fprintln(rawWriter, "agnt ai claude - interactive mode")
	fmt.Fprintln(rawWriter, "Type /exit or /quit to exit, Ctrl+D for EOF. Ctrl+Y for dashboard.")
	if opts.SystemPrompt != "" {
		fmt.Fprintln(rawWriter, "[agnt context injected]")
	}
	printDaemonStatusTo(daemonHandle, rawWriter)

	// Show initial prompt
	lineEditor.ShowPrompt()

	// Main REPL loop
	for {
		var prompt string
		var fromMessage bool

		select {
		case line, ok := <-lineEditor.Lines():
			if !ok {
				goto cleanup
			}
			line = strings.TrimSpace(line)
			if line == "" {
				lineEditor.ShowPrompt()
				continue
			}
			if line == "/exit" || line == "/quit" {
				goto cleanup
			}
			prompt = line

		case <-lineEditor.EOF():
			goto cleanup

		case msg := <-func() <-chan string {
			if msgCh != nil {
				return msgCh
			}
			return nil
		}():
			fmt.Fprintf(rawWriter, "\r\033[K[message] %s\n", msg)
			prompt = msg
			fromMessage = true

		case <-ctx.Done():
			goto cleanup
		}

		if sessionID != "" {
			opts.Resume = sessionID
		}

		// Disable line editor during streaming
		lineEditor.SetActive(false)

		// Show spinner
		var spin *stderrSpinner
		label := "Starting..."
		if fromMessage {
			label = "Processing message..."
		}
		spin = newStderrSpinner(label, rawWriter)

		sid, err := runClaudeQueryWith(ctx, prompt, opts, true, spin, rawWriter, rawWriter)
		if err != nil {
			if spin != nil {
				spin.Stop()
			}
			fmt.Fprintf(rawWriter, "Error: %v\n", err)
			lineEditor.SetActive(true)
			lineEditor.ShowPrompt()
			continue
		}
		if sid != "" {
			sessionID = sid
		}

		fmt.Fprintln(rawWriter)

		// Drain queued messages
		if msgCh != nil {
			draining := true
			for draining {
				select {
				case msg := <-msgCh:
					fmt.Fprintf(rawWriter, "[queued message] %s\n", msg)
					opts.Resume = sessionID
					spin = newStderrSpinner("Processing message...", rawWriter)
					sid, err := runClaudeQueryWith(ctx, msg, opts, true, spin, rawWriter, rawWriter)
					if err != nil {
						if spin != nil {
							spin.Stop()
						}
						fmt.Fprintf(rawWriter, "Error: %v\n", err)
						continue
					}
					if sid != "" {
						sessionID = sid
					}
					fmt.Fprintln(rawWriter)
				default:
					draining = false
				}
			}
		}

		// Re-enable line editor and show prompt
		lineEditor.SetActive(true)
		lineEditor.ShowPrompt()
	}

cleanup:
	// Shutdown sequence
	inputRouter.Stop()
	if outputFilter != nil {
		outputFilter.Stop()
	}

	// Wait for input router goroutine
	wg.Wait()

	// Clean up terminal
	cleanupTerminal(height)

	return nil
}

// printDaemonStatusTo prints daemon status through a specific writer.
// Expects the writer to handle \n → \r\n translation if needed (e.g., rawModeWriter).
func printDaemonStatusTo(h *daemonSessionHandle, w io.Writer) {
	if h == nil {
		return
	}
	if !h.sessionRegistered {
		fmt.Fprintln(w, "[daemon: not available]")
		return
	}
	fmt.Fprintln(w, "[daemon session active]")
	started := append(h.autostartScripts, h.autostartProxies...)
	if len(started) > 0 {
		fmt.Fprintf(w, "[autostart: %s]\n", strings.Join(started, ", "))
	}
	for _, e := range h.autostartErrors {
		fmt.Fprintf(w, "[autostart error: %s]\n", e)
	}

	if h.client == nil || !h.IsConnected() {
		return
	}

	cwd, _ := os.Getwd()
	dirFilter := protocol.DirectoryFilter{Directory: cwd}

	if proxies, err := h.client.ProxyList(dirFilter); err == nil {
		if proxyList, ok := proxies["proxies"].([]interface{}); ok {
			for _, p := range proxyList {
				if pm, ok := p.(map[string]interface{}); ok {
					id := getString(pm, "id")
					listenAddr := getString(pm, "listen_addr")
					if id != "" && listenAddr != "" {
						fmt.Fprintf(w, "[proxy: %s] http://%s\n", id, overlay.NormalizeListenAddr(listenAddr))
					}
				}
			}
		}
	}

	if procs, err := h.client.ProcList(dirFilter); err == nil {
		if processes, ok := procs["processes"].([]interface{}); ok {
			for _, p := range processes {
				if pm, ok := p.(map[string]interface{}); ok {
					id := getString(pm, "id")
					state := getString(pm, "state")
					if state != "running" {
						continue
					}
					if urls, ok := pm["urls"].([]interface{}); ok {
						for _, u := range urls {
							if urlStr, ok := u.(string); ok {
								fmt.Fprintf(w, "[script: %s] %s\n", id, urlStr)
							}
						}
					}
				}
			}
		}
	}
}

// runClaudeQuery executes the query using the claude-go library and streams output.
// Returns the session ID from the ResultMessage for multi-turn resumption.
// The spin parameter allows passing a pre-started spinner for immediate feedback.
func runClaudeQuery(ctx context.Context, prompt string, opts *claude.AgentOptions, interactive bool, spin *stderrSpinner) (string, error) {
	return runClaudeQueryWith(ctx, prompt, opts, interactive, spin, os.Stdout, os.Stderr)
}

// runClaudeQueryWith is like runClaudeQuery but writes through the given writers.
func runClaudeQueryWith(ctx context.Context, prompt string, opts *claude.AgentOptions, interactive bool, spin *stderrSpinner, stdout, stderr io.Writer) (string, error) {
	iter, err := claude.NewQueryIterator(ctx, prompt, opts)
	if err != nil {
		return "", fmt.Errorf("failed to create query: %w", err)
	}
	defer iter.Close()

	if interactive {
		return streamInteractiveWith(ctx, iter, spin, stdout, stderr)
	}
	if spin != nil {
		spin.Stop()
	}
	return streamJSON(ctx, iter)
}

// streamInteractive renders messages as human-readable output.
// The initialSpin parameter allows passing a pre-started spinner for immediate feedback.
func streamInteractive(ctx context.Context, iter *claude.QueryIterator, initialSpin *stderrSpinner) (string, error) {
	return streamInteractiveWith(ctx, iter, initialSpin, os.Stdout, os.Stderr)
}

// streamInteractiveWith renders messages using the provided writers.
func streamInteractiveWith(ctx context.Context, iter *claude.QueryIterator, initialSpin *stderrSpinner, stdout, stderr io.Writer) (string, error) {
	msgCh := iter.Messages()
	errCh := iter.Errors()

	spin := initialSpin
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
				errCh = nil // nil channel blocks in select, prevents busy-loop
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

			sid := renderMessageTo(msg, &spin, stdout, stderr, &textPrinted)
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
				errCh = nil // nil channel blocks in select, prevents busy-loop
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
// useMarkdown controls whether text is rendered through glamour.
var useMarkdown bool

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
			if useMarkdown {
				fmt.Fprintln(stdout, renderMarkdown(m.Result))
			} else {
				fmt.Fprintln(stdout, m.Result)
			}
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
			if useMarkdown {
				fmt.Fprintln(stdout, renderMarkdown(b.Text))
			} else {
				fmt.Fprintln(stdout, b.Text)
			}
			if textPrinted != nil {
				*textPrinted = true
			}

		case claude.ToolUseBlock:
			if *spin != nil {
				(*spin).Stop()
			}
			*spin = newStderrSpinner(fmt.Sprintf("[tool: %s]", b.Name), stderr)

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

// printDaemonStatus prints daemon session and autostart status to stderr.
// Queries daemon for running processes/proxies to display URLs.
func printDaemonStatus(h *daemonSessionHandle) {
	if h == nil {
		return
	}
	if !h.sessionRegistered {
		fmt.Fprintln(os.Stderr, "[daemon: not available]")
		return
	}
	fmt.Fprintln(os.Stderr, "[daemon session active]")
	started := append(h.autostartScripts, h.autostartProxies...)
	if len(started) > 0 {
		fmt.Fprintf(os.Stderr, "[autostart: %s]\n", strings.Join(started, ", "))
	}
	for _, e := range h.autostartErrors {
		fmt.Fprintf(os.Stderr, "[autostart error: %s]\n", e)
	}

	// Query daemon for running processes and proxies to show URLs
	if h.client == nil || !h.IsConnected() {
		return
	}

	cwd, _ := os.Getwd()
	dirFilter := protocol.DirectoryFilter{Directory: cwd}

	// Fetch proxies
	if proxies, err := h.client.ProxyList(dirFilter); err == nil {
		if proxyList, ok := proxies["proxies"].([]interface{}); ok {
			for _, p := range proxyList {
				if pm, ok := p.(map[string]interface{}); ok {
					id := getString(pm, "id")
					listenAddr := getString(pm, "listen_addr")
					if id != "" && listenAddr != "" {
						fmt.Fprintf(os.Stderr, "[proxy: %s] http://%s\n", id, overlay.NormalizeListenAddr(listenAddr))
					}
				}
			}
		}
	}

	// Fetch processes with URLs
	if procs, err := h.client.ProcList(dirFilter); err == nil {
		if processes, ok := procs["processes"].([]interface{}); ok {
			for _, p := range processes {
				if pm, ok := p.(map[string]interface{}); ok {
					id := getString(pm, "id")
					state := getString(pm, "state")
					if state != "running" {
						continue
					}
					if urls, ok := pm["urls"].([]interface{}); ok {
						for _, u := range urls {
							if urlStr, ok := u.(string); ok {
								fmt.Fprintf(os.Stderr, "[script: %s] %s\n", id, urlStr)
							}
						}
					}
				}
			}
		}
	}
}

// stderrSpinner displays a braille spinner animation on a writer using \r overwrite.
// Shows elapsed time after 5 seconds of spinning.
type stderrSpinner struct {
	done      chan struct{}
	wg        sync.WaitGroup
	w         io.Writer
	mu        sync.RWMutex
	message   string
	startTime time.Time
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// newStderrSpinner starts a braille spinner with the given message.
func newStderrSpinner(message string, w io.Writer) *stderrSpinner {
	s := &stderrSpinner{
		done:      make(chan struct{}),
		w:         w,
		message:   message,
		startTime: time.Now(),
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
				s.mu.RLock()
				msg := s.message
				start := s.startTime
				s.mu.RUnlock()

				elapsed := time.Since(start)
				frame := spinnerFrames[i%len(spinnerFrames)]

				fmt.Fprintf(s.w, "\r\033[K%s %s (%s)", frame, msg, formatElapsed(elapsed))
				i++
			}
		}
	}()

	return s
}

// UpdateLabel changes the spinner message.
func (s *stderrSpinner) UpdateLabel(msg string) {
	s.mu.Lock()
	s.message = msg
	s.mu.Unlock()
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

// formatElapsed formats a duration as compact elapsed time (e.g. "12s", "1m 23s").
func formatElapsed(d time.Duration) string {
	secs := int(d.Truncate(time.Second).Seconds())
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	mins := secs / 60
	rem := secs % 60
	if rem == 0 {
		return fmt.Sprintf("%dm", mins)
	}
	return fmt.Sprintf("%dm %ds", mins, rem)
}
