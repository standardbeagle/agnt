//go:build unix

package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"devtool-mcp/internal/aichannel"
	"devtool-mcp/internal/daemon"
	"devtool-mcp/internal/overlay"

	"github.com/creack/pty"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var runCmd = &cobra.Command{
	Use:   "run <command> [args...]",
	Short: "Run an AI coding tool with overlay features",
	Long: `Run any AI coding tool (Claude, Gemini, Copilot, etc.) with overlay features.

The command is executed in a pseudo-terminal (PTY) that allows:
- Capturing and forwarding all input/output
- Injecting synthetic input from external sources (like devtool proxy events)
- Terminal resize handling

Examples:
  agnt run claude --dangerously-skip-permissions
  agnt run claude
  agnt run gemini
  agnt run copilot
  agnt run opencode

The overlay listens on port 19191 for WebSocket connections from devtool-mcp
to receive events that can be injected as user input.`,
	DisableFlagParsing: true,
	Args:               cobra.MinimumNArgs(1),
	Run:                runCommand,
}

var (
	overlaySocketPath string
	overlayHotkey     byte = 0x0f // Ctrl+O
	showIndicator     bool = true
	useTermOverlay    bool = true
)

func init() {
	// We use DisableFlagParsing so flags are parsed manually
	// to allow passing all flags to the wrapped command
}

func runCommand(cmd *cobra.Command, args []string) {
	// Handle help flag manually since we disabled flag parsing
	for _, arg := range args {
		if arg == "-h" || arg == "--help" || arg == "help" {
			cmd.Help()
			return
		}
	}

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Error: command is required")
		cmd.Help()
		os.Exit(1)
	}

	// Parse our own flags from args
	overlaySocketPath = "" // will use default
	commandArgs := args

	// Look for our flags
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--overlay-socket":
			if i+1 < len(args) {
				overlaySocketPath = args[i+1]
				commandArgs = append(args[:i], args[i+2:]...)
				continue
			}
		case "--no-indicator":
			showIndicator = false
			commandArgs = append(args[:i], args[i+1:]...)
			continue
		case "--no-overlay":
			useTermOverlay = false
			commandArgs = append(args[:i], args[i+1:]...)
			continue
		}
		i++
	}

	if len(commandArgs) == 0 {
		fmt.Fprintln(os.Stderr, "Error: command is required")
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer cancel()

	if err := runWithPTY(ctx, commandArgs, overlaySocketPath); err != nil {
		log.Fatalf("Error: %v", err)
	}
}

// spinner displays a loading animation and returns a stop function.
func spinner(message string) func() {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		i := 0
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-done:
				// No need to clear - screen clear after PTY start handles it
				return
			case <-ticker.C:
				fmt.Printf("\r%s %s", frames[i%len(frames)], message)
				i++
			}
		}
	}()

	return func() {
		close(done)
		wg.Wait()
	}
}

// runWithPTY runs a command in a PTY with overlay support.
func runWithPTY(ctx context.Context, args []string, socketPath string) error {
	// Find the command
	command := args[0]
	cmdArgs := args[1:]

	// Show startup animation
	stopSpinner := spinner(fmt.Sprintf("Starting %s...", command))

	// Create the command
	c := commandWithArgs(command, cmdArgs...)
	c.Env = os.Environ()

	// Start the command with a pty
	ptmx, err := pty.Start(c)
	stopSpinner() // Stop spinner once PTY starts
	if err != nil {
		return fmt.Errorf("failed to start pty: %w", err)
	}

	// Clear screen before child starts outputting to prevent visual artifacts
	// from previous terminal content showing through
	fmt.Fprint(os.Stdout, "\x1b[2J\x1b[H") // Clear screen + move cursor home

	defer func() {
		_ = ptmx.Close()
	}()

	// Get initial terminal size
	width, height := 80, 24
	if w, h, err := term.GetSize(int(os.Stdin.Fd())); err == nil {
		width, height = w, h
	}

	// Handle pty size changes
	sizeCh := make(chan os.Signal, 1)
	signal.Notify(sizeCh, syscall.SIGWINCH)
	defer signal.Stop(sizeCh)

	// Reserve bottom row for indicator bar by telling child the terminal is 1 row shorter.
	// This prevents the child from drawing in our indicator area.
	childHeight := height
	if useTermOverlay && showIndicator && height > 1 {
		childHeight = height - 1
	}
	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(childHeight), Cols: uint16(width)}); err != nil {
		log.Printf("error setting pty size: %s", err)
	}

	// Set stdin in raw mode
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("failed to set raw mode: %w", err)
	}
	defer func() {
		_ = term.Restore(int(os.Stdin.Fd()), oldState)
	}()

	// Create network overlay for receiving external events (from browser)
	netOverlay := newOverlay(socketPath, ptmx)
	_ = netOverlay.Start(ctx) // Best-effort, non-critical for PTY operation
	defer netOverlay.Stop()

	// Register overlay endpoint with daemon so proxies forward events to us
	// Use ResilientClient for automatic reconnection with overlay re-registration
	var resilientClient *daemon.ResilientClient
	go func() {
		socketPath, _ := rootCmd.Flags().GetString("socket")

		overlayEndpoint := netOverlay.SocketPath()

		config := daemon.DefaultResilientClientConfig()
		if socketPath != "" {
			config.AutoStartConfig.SocketPath = socketPath
		}

		// Re-register overlay when connection is restored after daemon restart
		config.OnReconnect = func(client *daemon.Client) error {
			_, err := client.OverlaySet(overlayEndpoint)
			return err
		}

		// OnDisconnect and OnReconnectFailed are left nil since we don't want
		// to pollute the terminal output with daemon connection status messages

		resilientClient = daemon.NewResilientClient(config)
		if err := resilientClient.Connect(); err != nil {
			return // Daemon connection is best-effort, non-critical
		}

		// Register overlay endpoint on initial connect (best-effort)
		_, _ = resilientClient.OverlaySet(overlayEndpoint)
	}()

	// Clean up resilient client when PTY session ends
	defer func() {
		if resilientClient != nil {
			resilientClient.Close()
		}
	}()

	// Create terminal overlay (indicator bar and menus)
	var termOverlay *overlay.Overlay
	var inputRouter *overlay.InputRouter
	var statusFetcher *overlay.StatusFetcher
	var outputFilter *overlay.ProtectedWriter
	var outputGate *overlay.OutputGate

	if useTermOverlay {
		cfg := overlay.DefaultConfig()
		cfg.ShowIndicator = showIndicator
		cfg.Hotkey = overlayHotkey
		cfg.OnAction = func(action overlay.Action) error {
			switch action {
			case overlay.ActionRefreshStatus:
				if statusFetcher != nil {
					statusFetcher.Refresh()
				}
			}
			return nil
		}

		// Create output gate first - it's the final stage before stdout
		// When menu is open, gate freezes (discards) PTY output to prevent corruption
		outputGate = overlay.NewOutputGate(os.Stdout)

		termOverlay = overlay.New(ptmx, width, height, cfg)
		termOverlay.SetGate(outputGate) // Give overlay control of the gate
		inputRouter = overlay.NewInputRouter(ptmx, termOverlay, overlayHotkey)

		// Set up bash runner, output fetcher, and daemon connector for daemon communication
		socketPath, _ := rootCmd.Flags().GetString("socket")
		bashRunner := overlay.NewDaemonBashRunner(socketPath)
		inputRouter.SetBashRunner(bashRunner)
		outputFetcher := overlay.NewDaemonOutputFetcher(socketPath)
		inputRouter.SetOutputFetcher(outputFetcher)
		daemonConnector := overlay.NewDaemonConnector(socketPath)
		inputRouter.SetDaemonConnector(daemonConnector)

		// Set up summarizer - detect first available AI agent
		if agent := detectAIAgent(); agent != "" {
			summarizer := overlay.NewSummarizer(overlay.SummarizerConfig{
				SocketPath: socketPath,
				Agent:      aichannel.AgentType(agent),
				Timeout:    2 * time.Minute,
			})
			inputRouter.SetSummarizer(summarizer)
		}

		// Create output filter to protect the indicator bar from being overwritten
		// Filter writes to gate (not directly to stdout)
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
		}

		// Start status fetcher to update the indicator (reusing socketPath from above)
		statusFetcher = overlay.NewStatusFetcher(socketPath, termOverlay, 2*time.Second)
		statusFetcher.Start(ctx)
		defer statusFetcher.Stop()

		// Set status fetcher on input router so it can refresh after daemon connection
		inputRouter.SetStatusFetcher(statusFetcher)
	}

	// Create a channel for signaling completion
	done := make(chan struct{})

	var wg sync.WaitGroup

	// Enforce scroll region and draw initial indicator bar after a brief delay for child to start
	if termOverlay != nil && showIndicator {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-time.After(50 * time.Millisecond):
				// Enforce scroll region before drawing indicator
				if outputFilter != nil {
					outputFilter.EnforceScrollRegion()
				}
				termOverlay.Redraw()
			}
		}()
	}

	// Handle terminal resize
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-sizeCh:
				w, h, err := term.GetSize(int(os.Stdin.Fd()))
				if err != nil {
					continue
				}
				// Reserve bottom row for indicator if enabled
				ch := h
				if termOverlay != nil && termOverlay.ShowIndicator() && h > 1 {
					ch = h - 1
				}
				if err := pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(ch), Cols: uint16(w)}); err != nil {
					log.Printf("error resizing pty: %s", err)
				}
				// Update overlay with full terminal dimensions (it draws in the reserved row)
				if termOverlay != nil {
					termOverlay.SetSize(w, h)
				}
				// Update output filter with new dimensions
				if outputFilter != nil {
					outputFilter.SetSize(w, h)
				}
			}
		}
	}()

	// Handle stdin - either through input router or direct copy
	wg.Add(1)
	go func() {
		defer wg.Done()
		if inputRouter != nil {
			inputRouter.Run()
		} else {
			_, _ = io.Copy(ptmx, os.Stdin)
		}
	}()

	// Copy pty output to stdout (through filter and gate if enabled)
	// Chain: PTY -> Filter (protects indicator) -> Gate (freezes for menu) -> Stdout
	wg.Add(1)
	go func() {
		defer wg.Done()
		var outputDest io.Writer = os.Stdout
		if outputFilter != nil {
			// Filter -> Gate -> Stdout (filter already wraps gate)
			outputDest = outputFilter
		} else if outputGate != nil {
			// Gate -> Stdout (no indicator, but still need freeze for menu)
			outputDest = outputGate
		}
		_, _ = io.Copy(outputDest, ptmx)
		close(done)
	}()

	// Wait for context cancellation or process exit
	select {
	case <-ctx.Done():
		// Send interrupt to the process
		if c.Process != nil {
			_ = c.Process.Signal(syscall.SIGINT)
		}
	case <-done:
		// Process exited normally
	}

	// Stop input router if running
	if inputRouter != nil {
		inputRouter.Stop()
	}

	// Stop output filter if running
	if outputFilter != nil {
		outputFilter.Stop()
	}

	// Wait for the process
	_ = c.Wait()

	return nil
}

// commandWithArgs creates an exec.Cmd based on the platform.
// If the command is not found in PATH, it wraps the command in the user's
// shell to support aliases and shell functions.
func commandWithArgs(name string, args ...string) *execCmd {
	// Check if command exists in PATH
	if _, err := exec.LookPath(name); err == nil {
		// Command found in PATH, run directly
		return newExecCmd(name, args...)
	}

	// Command not in PATH - wrap in user's shell to support aliases/functions
	return wrapInShell(name, args...)
}

// wrapInShell wraps a command in the user's login shell with interactive mode.
// This enables shell aliases and functions defined in shell config files.
func wrapInShell(name string, args ...string) *execCmd {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	// Build the full command string with proper quoting
	var cmdParts []string
	cmdParts = append(cmdParts, shellQuote(name))
	for _, arg := range args {
		cmdParts = append(cmdParts, shellQuote(arg))
	}
	fullCmd := strings.Join(cmdParts, " ")

	// Use -i for interactive mode (loads rc files with aliases)
	// Use -c to execute a command string
	return newExecCmd(shell, "-ic", fullCmd)
}

// shellQuote quotes a string for safe use in shell commands.
// It uses single quotes and handles embedded single quotes.
func shellQuote(s string) string {
	// If string contains no special chars, return as-is
	if !strings.ContainsAny(s, " \t\n'\"\\$`!*?[]{}();<>&|") {
		return s
	}
	// Use single quotes, escaping any embedded single quotes
	// 'foo'\''bar' -> foo'bar
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// detectAIAgent detects the first available AI agent in PATH.
// Returns empty string if none found.
func detectAIAgent() string {
	agents := aichannel.DetectAvailableAgents()
	if len(agents) > 0 {
		return string(agents[0])
	}
	return ""
}
