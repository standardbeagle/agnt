//go:build windows

package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aymanbagabas/go-pty"
	"github.com/spf13/cobra"
	"github.com/standardbeagle/agnt/internal/aichannel"
	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/overlay"
	"github.com/standardbeagle/agnt/internal/protocol"
	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

var runCmd = &cobra.Command{
	Use:   "run <command> [args...]",
	Short: "Run an AI coding tool with overlay features",
	Long: `Run any AI coding tool (Claude, Gemini, Copilot, etc.) with overlay features.

The command is executed in a pseudo-terminal (ConPTY on Windows) that allows:
- Capturing and forwarding all input/output
- Injecting synthetic input from external sources (like devtool proxy events)
- Terminal resize handling
- Session management for programmatic message injection and scheduling

Flags:
  --session <code>      Session code for identifying this run (auto-generated if not set)
  --overlay-socket      Custom socket path for overlay server
  --hotkey <key>        Hotkey for overlay menu (default: CTRL+Y)
  --no-indicator        Disable the indicator bar
  --no-overlay          Disable terminal overlay entirely
  --no-autostart        Skip auto-starting scripts and proxies from .agnt.kdl

Examples:
  agnt run claude --dangerously-skip-permissions
  agnt run claude --session dev
  agnt run claude
  agnt run claude --no-autostart    # Skip .agnt.kdl autostart
  agnt run gemini
  agnt run copilot
  agnt run opencode

Overlay Features:
- CTRL+Y: Toggle overlay menu to view processes, proxies, and actions
- Status bar: Shows running services and proxy URLs for browser access
- Auto-start: Loads .agnt.kdl to auto-start configured dev scripts and proxies

The overlay listens on port 19191 for WebSocket connections from devtool-mcp
to receive events that can be injected as user input. Sessions can receive
scheduled messages via MCP tools, CLI commands, or the devtools API.`,
	DisableFlagParsing: true,
	Args:               cobra.MinimumNArgs(1),
	Run:                runCommand,
}

var (
	overlaySocketPath string
	overlayHotkey     byte = 0x19 // Ctrl+Y
	showIndicator     bool = true
	useTermOverlay    bool = true
	skipAutostart     bool = false
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
		case "--hotkey":
			if i+1 < len(args) {
				if hk := parseHotkey(args[i+1]); hk != 0 {
					overlayHotkey = hk
				}
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
		case "--no-autostart":
			skipAutostart = true
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
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer cancel()

	if err := runWithConPTY(ctx, commandArgs, overlaySocketPath); err != nil {
		log.Fatalf("Error: %v", err)
	}
}

// spinner displays a loading animation and returns a stop function.
func spinner(message string) func() {
	frames := []string{"|", "/", "-", "\\"}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		i := 0
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-done:
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

// runWithConPTY runs a command in a ConPTY with overlay support.
func runWithConPTY(ctx context.Context, args []string, socketPath string) error {
	// Find the command
	command := args[0]
	cmdArgs := args[1:]

	// Get project path for session registration and MCP directory filtering
	projectPath, _ := os.Getwd()

	// For Claude, inject system prompt with agnt context
	if isClaudeCommand(command) {
		if prompt := buildAgntSystemPrompt(socketPath); prompt != "" {
			cmdArgs = append(cmdArgs, "--append-system-prompt", prompt)
		}
	}

	// Get initial terminal size BEFORE any mode changes
	// Uses multiple fallback methods for VS Code and other embedded terminals
	width, height := getTerminalSize()

	// Reserve bottom row for indicator bar if enabled
	childHeight := height
	if useTermOverlay && showIndicator && height > 1 {
		childHeight = height - 1
	}

	// Set stdin in raw mode BEFORE creating ConPTY
	// This ensures ConPTY doesn't inherit/interfere with console mode
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("failed to set raw mode: %w", err)
	}
	defer func() {
		_ = term.Restore(int(os.Stdin.Fd()), oldState)
	}()

	// Enable Virtual Terminal Processing on stdout
	stdoutHandle, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE)
	if err == nil {
		var stdoutMode uint32
		if err := windows.GetConsoleMode(stdoutHandle, &stdoutMode); err == nil {
			newMode := stdoutMode | 0x0004 // ENABLE_VIRTUAL_TERMINAL_PROCESSING
			_ = windows.SetConsoleMode(stdoutHandle, newMode)
		}
	}

	// Show startup animation (after raw mode so spinner works)
	stopSpinner := spinner(fmt.Sprintf("Starting %s...", command))

	// Create PTY using aymanbagabas/go-pty (cross-platform PTY with ConPTY support)
	ptmx, err := pty.New()
	if err != nil {
		stopSpinner()
		return fmt.Errorf("failed to create PTY: %w", err)
	}
	defer ptmx.Close()

	// Set initial size
	if err := ptmx.Resize(width, childHeight); err != nil {
		log.Printf("warning: failed to set initial PTY size: %v", err)
	}

	// Find the command
	cmdPath, err := exec.LookPath(command)
	if err != nil {
		stopSpinner()
		return fmt.Errorf("command not found: %s", command)
	}

	// Create and start the command attached to the PTY
	cmd := ptmx.Command(cmdPath, cmdArgs...)
	// Pass project path to child process so MCP server can filter by correct directory
	cmd.Env = append(os.Environ(), "AGNT_PROJECT_PATH="+projectPath)
	if err := cmd.Start(); err != nil {
		stopSpinner()
		return fmt.Errorf("failed to start process: %w", err)
	}
	stopSpinner()

	// Disable win32-input-mode - ConPTY requests this by default which causes
	// Windows Terminal to send extended key event sequences instead of raw bytes.
	// We need standard VT input for our InputRouter to work correctly.
	// See: https://github.com/microsoft/terminal/blob/main/doc/specs/%234999%20-%20Improved%20keyboard%20handling%20in%20Conpty.md
	fmt.Fprint(os.Stdout, "\x1b[?9001l")

	// Clear screen before child starts outputting
	fmt.Fprint(os.Stdout, "\x1b[2J\x1b[H")

	// Create network overlay for receiving external events (from browser)
	netOverlay := newOverlay(socketPath, ptmx)
	_ = netOverlay.Start(ctx)
	defer netOverlay.Stop()

	// Register overlay endpoint with daemon
	var resilientClient *daemon.ResilientClient
	go func() {
		socketPath, _ := rootCmd.Flags().GetString("socket")

		overlayEndpoint := netOverlay.SocketPath()

		config := daemon.DefaultResilientClientConfig()
		if socketPath != "" {
			config.AutoStartConfig.SocketPath = socketPath
		}

		config.OnReconnect = func(client *daemon.Client) error {
			_, err := client.OverlaySet(overlayEndpoint)
			return err
		}

		resilientClient = daemon.NewResilientClient(config)
		if err := resilientClient.Connect(); err != nil {
			return
		}

		_, _ = resilientClient.OverlaySet(overlayEndpoint)
	}()

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

		outputGate = overlay.NewOutputGate(os.Stdout)
		termOverlay = overlay.New(ptmx, width, height, cfg)
		termOverlay.SetGate(outputGate)
		inputRouter = overlay.NewInputRouter(ptmx, termOverlay, overlayHotkey)

		// Set up daemon communication
		socketPath, _ := rootCmd.Flags().GetString("socket")
		bashRunner := overlay.NewDaemonBashRunner(socketPath)
		inputRouter.SetBashRunner(bashRunner)
		outputFetcher := overlay.NewDaemonOutputFetcher(socketPath)
		inputRouter.SetOutputFetcher(outputFetcher)
		daemonConnector := overlay.NewDaemonConnector(socketPath)
		inputRouter.SetDaemonConnector(daemonConnector)

		// Set up summarizer
		if agent := detectAIAgent(); agent != "" {
			summarizer := overlay.NewSummarizer(overlay.SummarizerConfig{
				SocketPath: socketPath,
				Agent:      aichannel.AgentType(agent),
				Timeout:    2 * time.Minute,
			})
			inputRouter.SetSummarizer(summarizer)
		}

		// Create output filter to protect indicator bar
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

		// Start status fetcher
		statusFetcher = overlay.NewStatusFetcher(socketPath, termOverlay, 2*time.Second)
		statusFetcher.Start(ctx)
		defer statusFetcher.Stop()

		inputRouter.SetStatusFetcher(statusFetcher)
	}

	done := make(chan struct{})
	var wg sync.WaitGroup

	// Draw initial indicator bar after delay
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
				if outputFilter != nil {
					outputFilter.EnforceScrollRegion()
				}
				termOverlay.Redraw()
			}
		}()
	}

	// Handle terminal resize (polling on Windows since no SIGWINCH)
	wg.Add(1)
	go func() {
		defer wg.Done()
		lastWidth, lastHeight := width, height
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				w, h := getTerminalSize()
				if w == lastWidth && h == lastHeight {
					continue
				}
				lastWidth, lastHeight = w, h

				// Reserve bottom row for indicator
				ch := h
				if termOverlay != nil && termOverlay.ShowIndicator() && h > 1 {
					ch = h - 1
				}
				if err := ptmx.Resize(w, ch); err != nil {
					log.Printf("error resizing PTY: %s", err)
				}
				if termOverlay != nil {
					termOverlay.SetSize(w, h)
				}
				if outputFilter != nil {
					outputFilter.SetSize(w, h)
				}
			}
		}
	}()

	// Handle stdin - use explicit console input reader on Windows
	wg.Add(1)
	go func() {
		defer wg.Done()
		if inputRouter != nil {
			inputRouter.Run()
		} else {
			// Direct copy from console to PTY
			buf := make([]byte, 256)
			for {
				n, err := os.Stdin.Read(buf)
				if err != nil {
					return
				}
				if n > 0 {
					ptmx.Write(buf[:n])
				}
			}
		}
	}()

	// Copy PTY output to stdout
	var activityMonitor *overlay.ActivityMonitor
	wg.Add(1)
	go func() {
		defer wg.Done()
		var outputDest io.Writer = os.Stdout
		if outputFilter != nil {
			outputDest = outputFilter
		} else if outputGate != nil {
			outputDest = outputGate
		}

		// Wrap with BrowserHelper to detect and open auth URLs
		// This helps when Claude tries to open browser for OAuth but ConPTY blocks it
		browserHelper := NewBrowserHelper(outputDest)

		// Activity monitor
		activityCfg := overlay.DefaultActivityMonitorConfig()
		activityCfg.OnStateChange = func(state overlay.ActivityState) {
			if resilientClient != nil {
				resilientClient.BroadcastActivity(state == overlay.ActivityActive)
			}
		}
		activityMonitor = overlay.NewActivityMonitor(browserHelper, activityCfg)

		_, _ = io.Copy(activityMonitor, ptmx)
		close(done)
	}()

	// Monitor process exit separately - close PTY when process exits
	// This ensures io.Copy returns even if PTY stays open on Windows
	processExited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(processExited)
		// Close PTY to unblock io.Copy if it's still running
		ptmx.Close()
	}()

	// Wait for context cancellation, PTY close, or process exit
	select {
	case <-ctx.Done():
		// Send interrupt to the process
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	case <-done:
		// PTY closed (process probably exited)
	case <-processExited:
		// Process exited but PTY might still be open
	}

	// Stop components
	if inputRouter != nil {
		inputRouter.Stop()
	}
	if outputFilter != nil {
		outputFilter.Stop()
	}
	if activityMonitor != nil {
		activityMonitor.Stop()
	}

	// Give process a moment to fully exit, force kill if needed
	select {
	case <-processExited:
		// Already exited
	case <-time.After(2 * time.Second):
		// Force kill if taking too long
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}

	// Clean up terminal state before returning
	// This prevents garbage output when Ctrl+C is pressed
	cleanupTerminal(height)

	return nil
}

// cleanupTerminal resets the terminal state to prevent display corruption on exit.
// This is especially important on Windows where ConPTY can leave the terminal in
// a bad state when the child process is killed.
func cleanupTerminal(height int) {
	// Disable extended keyboard/input modes that the child process may have enabled
	// These cause garbage output (semicolons, escape sequences) if left enabled
	fmt.Fprint(os.Stdout, "\x1b[?9001l") // Disable win32-input-mode (extended key reporting)
	fmt.Fprint(os.Stdout, "\x1b[?1004l") // Disable focus event reporting ([I and [O sequences)
	fmt.Fprint(os.Stdout, "\x1b[?2004l") // Disable bracketed paste mode
	fmt.Fprint(os.Stdout, "\x1b[?1l")    // Disable application cursor keys (DECCKM)
	fmt.Fprint(os.Stdout, "\x1b[?25h")   // Show cursor (might have been hidden)

	// Reset scroll region to full screen (removes protected area)
	fmt.Fprint(os.Stdout, "\x1b[r")

	// Reset all text attributes
	fmt.Fprint(os.Stdout, "\x1b[0m")

	// Move to the bottom row and clear it (remove status bar remnants)
	fmt.Fprintf(os.Stdout, "\x1b[%d;1H\x1b[2K", height)

	// Move cursor to a reasonable position (bottom-left)
	fmt.Fprintf(os.Stdout, "\x1b[%d;1H", height)
}

// isClaudeCommand checks if the command appears to be Claude Code.
func isClaudeCommand(command string) bool {
	if command == "claude" || command == "claude.exe" {
		return true
	}
	if strings.HasSuffix(command, "\\claude") || strings.HasSuffix(command, "\\claude.exe") {
		return true
	}
	if strings.HasSuffix(command, "/claude") || strings.HasSuffix(command, "/claude.exe") {
		return true
	}
	return false
}

// buildAgntSystemPrompt queries the daemon for running services and builds
// a system prompt to inject into Claude with context about agnt.
func buildAgntSystemPrompt(socketPath string) string {
	if socketPath == "" {
		socketPath = daemon.DefaultSocketPath()
	}

	client := daemon.NewClient(daemon.WithSocketPath(socketPath))
	if err := client.Connect(); err != nil {
		return "You are running inside agnt, a tool that gives AI coding agents browser superpowers. The agnt MCP tools (proxy, proc, proxylog, etc.) are available for browser debugging, screenshots, and dev server management."
	}
	defer client.Close()

	var sb strings.Builder
	sb.WriteString("You are running inside agnt, a tool that gives AI coding agents browser superpowers.\n\n")

	procFilter := protocol.DirectoryFilter{Global: true}
	procs, err := client.ProcList(procFilter)
	if err == nil {
		if processes, ok := procs["processes"].([]interface{}); ok && len(processes) > 0 {
			sb.WriteString("**Running processes (auto-started by agnt):**\n")
			for _, p := range processes {
				if pm, ok := p.(map[string]interface{}); ok {
					id := pm["id"]
					state := pm["state"]
					cmd := pm["command"]
					sb.WriteString(fmt.Sprintf("- %s: %s (state: %s)\n", id, cmd, state))
				}
			}
			sb.WriteString("\n")
		}
	}

	proxyFilter := protocol.DirectoryFilter{Global: true}
	proxies, err := client.ProxyList(proxyFilter)
	if err == nil {
		if proxyList, ok := proxies["proxies"].([]interface{}); ok && len(proxyList) > 0 {
			sb.WriteString("**Running proxies (auto-started by agnt):**\n")
			for _, p := range proxyList {
				if pm, ok := p.(map[string]interface{}); ok {
					id := pm["id"]
					target := pm["target_url"]
					listen := pm["listen_addr"]
					sb.WriteString(fmt.Sprintf("- %s: %s -> %s\n", id, listen, target))
				}
			}
			sb.WriteString("\n")
		}
	}

	sb.WriteString("Use agnt MCP tools (proxy, proc, proxylog, currentpage) for browser debugging, screenshots, JavaScript execution, and dev server management. Do NOT try to start processes or proxies that are already running.")

	return sb.String()
}

// detectAIAgent detects the first available AI agent in PATH.
func detectAIAgent() string {
	agents := aichannel.DetectAvailableAgents()
	if len(agents) > 0 {
		return string(agents[0])
	}
	return ""
}

// getTerminalSize tries multiple methods to get terminal size.
// VS Code and other embedded terminals may not report size correctly on stdin.
func getTerminalSize() (width, height int) {
	// Method 1: Try stdin
	if w, h, err := term.GetSize(int(os.Stdin.Fd())); err == nil && w > 0 && h > 0 {
		return w, h
	}
	// Method 2: Try stdout
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 && h > 0 {
		return w, h
	}
	// Method 3: Windows Console API
	if w, h, err := getConsoleSize(); err == nil && w > 0 && h > 0 {
		return w, h
	}
	// Fallback
	return 80, 24
}

// getConsoleSize gets the current console size on Windows.
func getConsoleSize() (width, height int, err error) {
	handle, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE)
	if err != nil {
		return 80, 24, err
	}

	var info windows.ConsoleScreenBufferInfo
	err = windows.GetConsoleScreenBufferInfo(handle, &info)
	if err != nil {
		return 80, 24, err
	}

	width = int(info.Window.Right - info.Window.Left + 1)
	height = int(info.Window.Bottom - info.Window.Top + 1)
	return width, height, nil
}

// BrowserHelper wraps an io.Writer and detects URLs in the output,
// automatically opening them in the default browser. This helps with
// OAuth flows where the child process tries to open a browser but
// the ConPTY environment may prevent it from working correctly.
type BrowserHelper struct {
	dest       io.Writer
	buffer     []byte
	urlPattern *regexp.Regexp
	opened     map[string]bool // Track URLs we've already opened
	mu         sync.Mutex
}

// NewBrowserHelper creates a new BrowserHelper that wraps the given writer.
func NewBrowserHelper(dest io.Writer) *BrowserHelper {
	return &BrowserHelper{
		dest: dest,
		// Match URLs that look like OAuth/auth URLs
		urlPattern: regexp.MustCompile(`https?://[^\s\x00-\x1f"'<>|\x7f]+`),
		opened:     make(map[string]bool),
	}
}

// Write implements io.Writer, scanning for URLs and opening them.
func (b *BrowserHelper) Write(p []byte) (n int, err error) {
	// Always write through to destination first
	n, err = b.dest.Write(p)

	// Scan for URLs in the output
	b.mu.Lock()
	defer b.mu.Unlock()

	// Append to buffer for URL detection (keep last 4KB)
	b.buffer = append(b.buffer, p...)
	if len(b.buffer) > 4096 {
		b.buffer = b.buffer[len(b.buffer)-4096:]
	}

	// Find URLs
	matches := b.urlPattern.FindAll(b.buffer, -1)
	for _, match := range matches {
		url := string(match)
		// Clean up URL (remove trailing punctuation that might have been captured)
		url = strings.TrimRight(url, ".,;:!?)]}>\"'")

		// Only open auth-related URLs we haven't opened yet
		if !b.opened[url] && isAuthURL(url) {
			b.opened[url] = true
			go openBrowser(url)
		}
	}

	return n, err
}

// isAuthURL checks if a URL looks like an authentication/OAuth URL.
func isAuthURL(url string) bool {
	authPatterns := []string{
		"oauth",
		"auth",
		"login",
		"signin",
		"sign-in",
		"callback",
		"authorize",
		"console.anthropic.com",
		"accounts.google.com",
		"github.com/login",
		"login.microsoftonline.com",
	}
	urlLower := strings.ToLower(url)
	for _, pattern := range authPatterns {
		if strings.Contains(urlLower, pattern) {
			return true
		}
	}
	return false
}

// openBrowser opens a URL in the default browser on Windows.
func openBrowser(url string) {
	// Use cmd /c start to open URL in default browser
	cmd := exec.Command("cmd", "/c", "start", "", url)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
	}
	_ = cmd.Run()
}

// parseHotkey parses a hotkey string like "ctrl+l", "ctrl+g", "l", "p" into a byte.
// Returns 0 if invalid.
func parseHotkey(s string) byte {
	s = strings.ToLower(strings.TrimSpace(s))

	// Handle "ctrl+X" format
	if strings.HasPrefix(s, "ctrl+") {
		letter := strings.TrimPrefix(s, "ctrl+")
		if len(letter) == 1 && letter[0] >= 'a' && letter[0] <= 'z' {
			// Ctrl+A = 0x01, Ctrl+B = 0x02, etc.
			return letter[0] - 'a' + 1
		}
		return 0
	}

	// Handle "^X" format (e.g., "^L")
	if strings.HasPrefix(s, "^") {
		letter := strings.TrimPrefix(s, "^")
		if len(letter) == 1 && letter[0] >= 'a' && letter[0] <= 'z' {
			return letter[0] - 'a' + 1
		}
		return 0
	}

	// Handle single letter (assume ctrl+letter)
	if len(s) == 1 && s[0] >= 'a' && s[0] <= 'z' {
		return s[0] - 'a' + 1
	}

	// Handle hex format like "0x0c"
	if strings.HasPrefix(s, "0x") {
		var b byte
		if _, err := fmt.Sscanf(s, "0x%x", &b); err == nil {
			return b
		}
	}

	return 0
}
