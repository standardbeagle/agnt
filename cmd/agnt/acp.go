//go:build unix

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/spf13/cobra"
	"github.com/standardbeagle/agnt/internal/aichannel"
	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/overlay"
	"golang.org/x/term"
)

// acpCmd runs any Agent Client Protocol (ACP) agent over stdio.
//
// Unlike `agnt ai claude` — which is hard-wired to Claude's bespoke
// stream-json format via the claude-go library — `agnt acp` speaks the
// standardized ACP JSON-RPC protocol, so a single client drives any
// ACP-compatible agent (gemini, opencode, claude-code-acp, ...). The
// SDK (github.com/coder/acp-go-sdk) owns the JSON-RPC plumbing; this
// command spawns the agent subprocess, implements the acp.Client
// callbacks, and drives the initialize → session/new → session/prompt
// lifecycle.
//
// Daemon integration (autostart, session registration, agnt system
// prompt, overlay) is reused from the `ai` path. ACP has no
// system-prompt flag, so the agnt context rides in the first prompt
// turn (prepended to the user's first message).
var acpCmd = &cobra.Command{
	Use:   "acp <agent> [prompt]",
	Short: "Run any ACP-compatible agent (gemini, opencode, ...) over stdio",
	Long: `Run an Agent Client Protocol (ACP) agent with clean streaming output.

ACP is a standardized JSON-RPC protocol over stdio, so one client drives any
compatible agent. Contrast with 'agnt ai claude', which only speaks Claude's
private stream-json format, and 'agnt run', which wraps a tool in a PTY.

Agent resolution:
  agnt acp gemini                  known agent → launched in ACP mode (gemini --experimental-acp)
  agnt acp opencode "fix lint"     known agent (opencode acp), one-shot prompt
  agnt acp mycli -- acp --flag     args after -- are appended to the launch command
  agnt acp /path/to/agent          unknown name → run as-is (must speak ACP on stdio)

Prompt sources (priority): positional arg, -p/--prompt, stdin, else interactive REPL.

The agnt context (running processes, proxies, project state) is injected into the
first prompt turn, since ACP has no system-prompt channel. Tool-call permission
requests are auto-approved (lightest mode); use --no-auto-approve to be asked.`,
	Args: cobra.MinimumNArgs(1),
	Run:  runACP,
}

// acpAgentSpec describes how to launch a known agent in ACP stdio mode.
type acpAgentSpec struct {
	// base is the command plus the args that put it into ACP-over-stdio
	// mode. The first element is the executable.
	base []string
	// modelFlag is the CLI flag this agent uses to select a model
	// (e.g. "-m"). Empty means the agent has no model flag we set.
	modelFlag string
}

// acpAgents maps a friendly agent name to its ACP launch spec. Unknown
// names fall through to "run the name verbatim" so any ACP agent works
// without a registry entry (use -- to append the agent's ACP subcommand).
//
// Verified invocations (2026-06): gemini exposes ACP via --experimental-acp;
// opencode via the `opencode acp` subcommand; Claude Code via Zed's
// claude-code-acp adapter binary.
var acpAgents = map[string]acpAgentSpec{
	"gemini":   {base: []string{"gemini", "--experimental-acp"}, modelFlag: "-m"},
	"opencode": {base: []string{"opencode", "acp"}},
	"claude":   {base: []string{"claude-code-acp"}},
}

// ACP command flags.
var (
	acpModel         string
	acpPromptFlag    string
	acpSessionCode   string
	acpNoAgntPrompt  bool
	acpNoAutostart   bool
	acpNoSession     bool
	acpNoAutoApprove bool
	acpNoOverlay     bool
	acpNoIndicator   bool
	acpVerbose       bool
)

func init() {
	acpCmd.Flags().StringVar(&acpModel, "model", "", "Model to pass to the agent (if it supports a model flag)")
	acpCmd.Flags().StringVarP(&acpPromptFlag, "prompt", "p", "", "Prompt (alternative to positional arg)")
	acpCmd.Flags().StringVar(&acpSessionCode, "session", "", "Session code for daemon integration (auto-generated if empty)")
	acpCmd.Flags().BoolVar(&acpNoAgntPrompt, "no-agnt-prompt", false, "Skip agnt context injection into the first turn")
	acpCmd.Flags().BoolVar(&acpNoAutostart, "no-autostart", false, "Skip auto-starting scripts and proxies from .agnt.kdl")
	acpCmd.Flags().BoolVar(&acpNoSession, "no-session", false, "Disable daemon integration entirely")
	acpCmd.Flags().BoolVar(&acpNoAutoApprove, "no-auto-approve", false, "Prompt for tool-call permission instead of auto-approving")
	acpCmd.Flags().BoolVar(&acpNoOverlay, "no-overlay", false, "Disable terminal overlay (status bar, menus) in interactive mode")
	acpCmd.Flags().BoolVar(&acpNoIndicator, "no-indicator", false, "Disable the indicator bar (keep overlay menus)")
	acpCmd.Flags().BoolVar(&acpVerbose, "verbose", false, "Log ACP JSON-RPC traffic to stderr")
	acpCmd.Flags().StringVar(&configOverride, "config", "", "Path to .agnt.kdl (default: <cwd>/.agnt.kdl, no walk-up)")
}

// resolveACPLaunch turns the positional agent name and any post-dash args
// into the exec argv used to start the agent in ACP mode.
func resolveACPLaunch(agent string, extraArgs []string) []string {
	var argv []string
	if spec, ok := acpAgents[agent]; ok {
		argv = append(argv, spec.base...)
		if acpModel != "" && spec.modelFlag != "" {
			argv = append(argv, spec.modelFlag, acpModel)
		}
	} else {
		// Unknown agent: run the name verbatim. The user is responsible
		// for it speaking ACP on stdio (typically via -- extra args).
		argv = append(argv, agent)
	}
	argv = append(argv, extraArgs...)
	return argv
}

// acpArgs splits cobra args into (agent, prompt-positional, extraArgs).
// Everything before -- is normal positional parsing; everything after --
// is appended to the agent launch command.
func acpArgs(cmd *cobra.Command, args []string) (agent, promptArg string, extra []string) {
	dash := cmd.ArgsLenAtDash()
	pre := args
	if dash >= 0 {
		pre = args[:dash]
		extra = args[dash:]
	}
	if len(pre) > 0 {
		agent = pre[0]
	}
	if len(pre) > 1 {
		promptArg = pre[1]
	}
	return agent, promptArg, extra
}

func runACP(cmd *cobra.Command, args []string) {
	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	agent, promptArg, extra := acpArgs(cmd, args)
	if agent == "" {
		fmt.Fprintln(os.Stderr, "Error: agent is required (e.g. agnt acp gemini)")
		os.Exit(1)
	}

	// Determine prompt: positional[1] wins, then -p, then stdin. Empty
	// prompt with a TTY opens the interactive REPL.
	prompt := promptArg
	if prompt == "" && acpPromptFlag != "" {
		prompt = acpPromptFlag
	}
	if prompt == "" && !isTerminal(os.Stdin) {
		prompt = readAllStdin()
	}
	interactive := prompt == "" && isTerminal(os.Stdin)

	argv := resolveACPLaunch(agent, extra)

	var err error
	if interactive {
		err = runACPInteractive(ctx, agent, argv)
	} else {
		err = runACPOneShot(ctx, agent, argv, prompt)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// startACPDaemonSession registers a daemon session for autostart + agnt
// context, optionally wiring an overlay endpoint for message injection.
// Returns nil when --no-session is set.
func startACPDaemonSession(ctx context.Context, agent, code, overlayEndpoint string) *daemonSessionHandle {
	if acpNoSession {
		return nil
	}
	if code == "" {
		code = generateSessionCode("acp-" + sanitizeAgentName(agent))
	}
	projectPath, _ := os.Getwd()
	socketPath, _ := rootCmd.Flags().GetString("socket")
	return startDaemonSession(ctx, daemonSessionConfig{
		SessionCode:     code,
		OverlayEndpoint: overlayEndpoint,
		ProjectPath:     projectPath,
		Command:         "agnt",
		CmdArgs:         []string{"acp", agent},
		SocketPath:      socketPath,
		SkipAutostart:   acpNoAutostart,
	})
}

// runACPOneShot runs a single prompt turn (no overlay, no message queue).
func runACPOneShot(ctx context.Context, agent string, argv []string, prompt string) error {
	if prompt == "" {
		return fmt.Errorf("prompt is required (positional, -p, or stdin)")
	}
	daemonHandle := startACPDaemonSession(ctx, agent, acpSessionCode, "")
	if daemonHandle != nil {
		defer daemonHandle.Close()
		daemonHandle.WaitRegistered(2 * time.Second)
		printDaemonStatus(daemonHandle)
	}

	systemContext := buildACPSystemContext(false)

	conn, client, sid, cleanup, err := dialACPAgent(ctx, argv)
	if err != nil {
		return err
	}
	defer cleanup()

	return acpPromptTurn(ctx, conn, client, sid, prompt, systemContext, true)
}

// runACPInteractive opens a multi-turn REPL. When the daemon and a TTY are
// available it runs the full overlay (status bar, panels, message injection);
// otherwise it falls back to a cooked-mode loop.
func runACPInteractive(ctx context.Context, agent string, argv []string) error {
	code := acpSessionCode
	if code == "" {
		code = generateSessionCode("acp-" + sanitizeAgentName(agent))
	}

	// Start the AI overlay server (message injection source) before daemon
	// registration so the daemon learns its endpoint.
	var aiOverlay *AIOverlay
	var msgCh <-chan string
	var overlayEndpoint string
	if !acpNoSession {
		aiOverlay = newAIOverlay(code)
		if err := aiOverlay.Start(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "[agnt] overlay start failed: %v\n", err)
			aiOverlay = nil
		} else {
			defer aiOverlay.Stop()
			msgCh = aiOverlay.Messages()
			overlayEndpoint = aiOverlay.SocketPath()
		}
	}

	daemonHandle := startACPDaemonSession(ctx, agent, code, overlayEndpoint)
	if daemonHandle != nil {
		defer daemonHandle.Close()
		daemonHandle.WaitRegistered(5 * time.Second)
	}

	// First-run setup gate applies to the interactive REPL (verb-driven).
	systemContext := buildACPSystemContext(true)

	conn, client, sid, cleanup, err := dialACPAgent(ctx, argv)
	if err != nil {
		return err
	}
	defer cleanup()

	// Gate AIOverlay events through the single dedup/batch/activity queue
	// before they become prompt turns. The REPL pulls only while idle, which
	// is the deterministic ACP activity gate.
	var gate *acpAlertGate
	if msgCh != nil {
		gate = newACPAlertGate(msgCh)
		go gate.Run(ctx)
	}

	useOverlay := !acpNoOverlay && isTerminal(os.Stdin)
	if useOverlay {
		return runACPOverlay(ctx, conn, client, sid, systemContext, gate)
	}
	return runACPRepl(ctx, conn, client, sid, systemContext, gate)
}

// buildACPSystemContext returns the agnt context string to prepend to the
// first prompt turn, honoring --no-agnt-prompt and the first-run setup gate.
func buildACPSystemContext(allowSetup bool) string {
	if acpNoAgntPrompt {
		return ""
	}
	if allowSetup {
		if cwd, err := os.Getwd(); err == nil {
			if setup := aiSetupSystemPrompt(cwd); setup != "" {
				return setup
			}
		}
	}
	socketPath, _ := rootCmd.Flags().GetString("socket")
	return buildAgntSystemPrompt(socketPath)
}

// acpHandshakeTimeout bounds the initialize + new-session handshake. These
// involve no LLM call, so they are normally sub-second; a generous ceiling
// turns a genuinely unresponsive agent into a loud failure instead of an
// indefinite hang. The prompt turn itself is NOT bounded here — model
// latency is legitimately unbounded and is cancellable via SIGINT.
const acpHandshakeTimeout = 60 * time.Second

// dialACPAgent spawns the agent subprocess, runs the ACP handshake
// (initialize + new session), and returns the connection, client, session
// id, and a cleanup func that kills the process and releases terminals. A
// "Connecting…" spinner runs during the handshake so a slow agent launch
// does not look like a hang, and the handshake is bounded by
// acpHandshakeTimeout so a dead agent fails loud.
func dialACPAgent(ctx context.Context, argv []string) (*acp.ClientSideConnection, *acpClient, acp.SessionId, func(), error) {
	if len(argv) == 0 {
		return nil, nil, "", nil, fmt.Errorf("empty agent command")
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, "", nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, "", nil, fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, "", nil, fmt.Errorf("start agent %q: %w", argv[0], err)
	}

	client := newACPClient(!acpNoAutoApprove)
	cleanup := func() {
		client.releaseAllTerminals()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}

	conn := acp.NewClientSideConnection(client, stdin, stdout)
	// The SDK logs at INFO to stderr by default; silence it unless asked.
	if acpVerbose {
		conn.SetLogger(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	} else {
		conn.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	}

	// Feedback + bound for the handshake so a slow or dead agent launch is
	// visible and eventually fails loud rather than hanging silently.
	hctx, hcancel := context.WithTimeout(ctx, acpHandshakeTimeout)
	defer hcancel()
	spin := newStderrSpinner(fmt.Sprintf("Connecting to %s (ACP)...", sanitizeAgentName(argv[0])), os.Stderr)
	stopSpin := func() {
		if spin != nil {
			spin.Stop()
			spin = nil
		}
	}

	// Advertise filesystem read/write and terminal so the agent can edit
	// files and run commands through us.
	if _, err := conn.Initialize(hctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientCapabilities: acp.ClientCapabilities{
			Fs:       acp.FileSystemCapabilities{ReadTextFile: true, WriteTextFile: true},
			Terminal: true,
		},
	}); err != nil {
		stopSpin()
		cleanup()
		return nil, nil, "", nil, acpHandshakeError("initialize", argv[0], hctx, ctx, err)
	}

	cwd, _ := os.Getwd()
	if cwd == "" {
		cwd = "."
	}
	sess, err := conn.NewSession(hctx, acp.NewSessionRequest{
		Cwd:        cwd,
		McpServers: []acp.McpServer{},
	})
	stopSpin()
	if err != nil {
		cleanup()
		return nil, nil, "", nil, acpHandshakeError("new session", argv[0], hctx, ctx, err)
	}

	return conn, client, sess.SessionId, cleanup, nil
}

// acpHandshakeError wraps a handshake failure, distinguishing a timeout (the
// agent did not respond) from a parent-context cancel (user interrupt) and a
// genuine protocol error.
func acpHandshakeError(phase, agent string, hctx, parent context.Context, err error) error {
	if hctx.Err() == context.DeadlineExceeded && parent.Err() == nil {
		return fmt.Errorf("agent %q did not complete ACP %s within %s — is it installed and responsive? (try: %s)",
			sanitizeAgentName(agent), phase, acpHandshakeTimeout, agent)
	}
	return fmt.Errorf("acp %s: %w", phase, err)
}

// acpPromptTurn sends one prompt turn and blocks until the agent ends it.
// When systemContext is non-empty it is prepended as a separate text block
// before the user's message (ACP has no system-prompt channel). The spinner
// is rendered through the client's status writer so it works under the
// overlay's raw-mode writer too.
func acpPromptTurn(ctx context.Context, conn *acp.ClientSideConnection, client *acpClient, sessionID acp.SessionId, prompt, systemContext string, withSpinner bool) error {
	blocks := make([]acp.ContentBlock, 0, 2)
	if systemContext != "" {
		blocks = append(blocks, acp.TextBlock(systemContext))
	}
	blocks = append(blocks, acp.TextBlock(prompt))

	var spin *stderrSpinner
	if withSpinner {
		spin = newStderrSpinner("Starting...", client.errWriter())
	}
	client.setSpinner(spin)

	resp, err := conn.Prompt(ctx, acp.PromptRequest{
		SessionId: sessionID,
		Prompt:    blocks,
	})
	if spin != nil {
		spin.Stop()
	}
	if err != nil {
		return fmt.Errorf("acp prompt: %w", err)
	}
	if resp.StopReason != "" && resp.StopReason != acp.StopReasonEndTurn {
		fmt.Fprintf(client.errWriter(), "\r\033[K(stopped: %s)\n", resp.StopReason)
	}
	return nil
}

// sanitizeAgentName makes an agent string safe for a session-code suffix.
func sanitizeAgentName(agent string) string {
	base := filepath.Base(agent)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	if base == "" {
		return "agent"
	}
	return base
}

// runACPOverlay runs the interactive REPL with the full terminal overlay
// (raw mode, status bar, Ctrl+←/→ panels, message injection). It mirrors
// runAiClaudeOverlay, swapping the per-turn query for an ACP prompt turn:
// the acpClient renders streamed updates straight into the raw-mode writer.
func runACPOverlay(ctx context.Context, conn *acp.ClientSideConnection, client *acpClient, sid acp.SessionId, systemContext string, gate *acpAlertGate) error {
	width, height := 80, 24
	if w, h, err := term.GetSize(int(os.Stdin.Fd())); err == nil {
		width, height = w, h
	}

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("failed to set raw mode: %w", err)
	}
	defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }()

	showIndicator := !acpNoIndicator

	outputGate := overlay.NewOutputGate(os.Stdout)

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

	rawWriter := newRawModeWriter(outputWriter)
	// Route the ACP client's streamed output through the raw-mode writer so
	// it lands above the protected indicator row.
	client.setWriters(rawWriter, rawWriter)

	lineEditor := NewLineEditor(rawWriter, "> ")
	replAdapter := NewREPLAdapter(lineEditor)

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

	termOverlay = overlay.New(replAdapter, width, height, cfg)
	termOverlay.SetGate(outputGate)

	inputRouter := overlay.NewInputRouter(replAdapter, termOverlay)

	outputGate.SetCallbacks(nil, func() {
		if outputFilter != nil {
			outputFilter.EnforceScrollRegion()
		}
		lineEditor.Redraw()
	})

	daemonSocketPath, _ := rootCmd.Flags().GetString("socket")
	daemonConn := daemon.NewConn(daemonSocketPath)
	defer daemonConn.Close()

	daemonClient := newDaemonClientAdapter(daemonConn)
	outputFetcher := overlay.NewDaemonOutputFetcher(daemonClient)
	inputRouter.SetOutputFetcher(outputFetcher)
	daemonConnector := newDaemonConnector(daemonConn)
	inputRouter.SetDaemonConnector(daemonConnector)
	scriptController := overlay.NewDaemonScriptController(daemonClient)
	inputRouter.SetScriptController(scriptController)

	if agent := detectAIAgent(); agent != "" {
		projectPath, _ := os.Getwd()
		summarizer := overlay.NewSummarizer(daemonClient, overlay.SummarizerConfig{
			Agent:       aichannel.AgentType(agent),
			Timeout:     2 * time.Minute,
			ProjectPath: projectPath,
		})
		inputRouter.SetSummarizer(summarizer)
	}

	statusFetcher = overlay.NewStatusFetcher(daemonClient, termOverlay, 2*time.Second)
	statusFetcher.Start(ctx)
	defer statusFetcher.Stop()
	inputRouter.SetStatusFetcher(statusFetcher)

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

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		inputRouter.Run()
	}()

	fmt.Fprint(os.Stdout, "\x1b[2J\x1b[H")
	if showIndicator && outputFilter != nil {
		outputFilter.EnforceScrollRegion()
		termOverlay.Redraw()
	}

	fmt.Fprintln(rawWriter, "agnt acp - interactive mode")
	fmt.Fprintln(rawWriter, "Type /exit or /quit to exit, Ctrl+D for EOF. Ctrl+←/→ for panels.")
	if systemContext != "" {
		fmt.Fprintln(rawWriter, "[agnt context injected on first turn]")
	}

	lineEditor.ShowPrompt()

	firstTurn := true
	runTurn := func(text string) {
		turnContext := ""
		if firstTurn {
			turnContext = systemContext
			firstTurn = false
		}
		lineEditor.SetActive(false)
		if err := acpPromptTurn(ctx, conn, client, sid, text, turnContext, true); err != nil {
			fmt.Fprintf(rawWriter, "Error: %v\n", err)
		}
		fmt.Fprintln(rawWriter)
		lineEditor.SetActive(true)
		lineEditor.ShowPrompt()
	}

	for {
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
			runTurn(line)

		case <-lineEditor.EOF():
			goto cleanup

		case <-gate.Ready():
			// Idle between turns: pull the coalesced, deduped batch and inject
			// it as one turn. Alerts that arrived during the previous turn are
			// already batched here.
			if msg, ok := gate.Pull(); ok {
				fmt.Fprintf(rawWriter, "\r\033[K[alerts]\n%s\n", msg)
				runTurn(msg)
			}

		case <-ctx.Done():
			goto cleanup
		}
	}

cleanup:
	inputRouter.Stop()
	if outputFilter != nil {
		outputFilter.Stop()
	}
	wg.Wait()
	cleanupTerminal(height)
	return nil
}
