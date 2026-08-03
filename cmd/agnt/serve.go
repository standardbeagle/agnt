package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"

	"github.com/standardbeagle/agnt/internal/daemonclient"
	"github.com/standardbeagle/agnt/internal/license"
	"github.com/standardbeagle/agnt/internal/selflog"
	"github.com/standardbeagle/agnt/internal/snapshot"
	"github.com/standardbeagle/agnt/internal/tools"

	"github.com/spf13/cobra"
	"github.com/standardbeagle/go-sdk/mcp"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run as shared server",
	Long: `Run as a shared server that syncronizes processes and proxies across clients.

Uses a background daemon for persistent state across connections.`,
	Run: runServe,
}

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run as MCP server",
	Long: `Run as an MCP (Model Context Protocol) server for AI coding assistants.

This is the primary mode for integration with Claude Code, Claude Desktop, and other MCP clients.
Uses a background daemon for persistent state across connections.`,
	Run: runMCP,
}

var (
	mcpNoAttach bool
)

func init() {
	mcpCmd.Flags().BoolVar(&mcpNoAttach, "no-attach", false, "Don't auto-attach to existing session (operate globally)")
}

func runServe(cmd *cobra.Command, args []string) {
	socketPath, _ := cmd.Flags().GetString("socket")
	if socketPath == "" {
		socketPath = daemonclient.DefaultSocketPath()
	}

	runDaemonClient(socketPath)
}

func runMCP(cmd *cobra.Command, args []string) {
	socketPath, _ := cmd.Flags().GetString("socket")
	if socketPath == "" {
		socketPath = daemonclient.DefaultSocketPath()
	}

	runDaemonClient(socketPath, mcpNoAttach)
}

// runDaemonClient runs the MCP server that communicates with the daemon.
func runDaemonClient(socketPath string, noAttach ...bool) {
	// Create root context with signal cancellation
	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer cancel()

	// Configure daemon tools with auto-start
	daemonCfg := daemonclient.AutoStartConfig{
		SocketPath:    socketPath,
		StartTimeout:  5 * time.Second,
		RetryInterval: 100 * time.Millisecond,
		MaxRetries:    50,
	}

	dt := tools.NewDaemonTools(daemonCfg, appVersion)
	defer dt.Close()

	// Disable auto-attach if requested
	if len(noAttach) > 0 && noAttach[0] {
		dt.SetNoAutoAttach(true)
	}

	// Create MCP server
	serverOpts := &mcp.ServerOptions{
		HasTools: true,
		Instructions: `Development tool server for project detection, process management, and reverse proxy with traffic logging.

Uses a background daemon for persistent state across connections:
- Processes and proxies survive client disconnections
- State is shared across multiple MCP clients
- Auto-starts daemon if not running

Available tools:
- detect: Detect project type and available scripts
- run: Run scripts or raw commands (background/foreground modes)
- proc: Manage processes (status, output, stop, list, cleanup_port)
- proxy: Reverse proxy with traffic logging and JS instrumentation
- proxylog: Query proxy traffic logs
- currentpage: View active page sessions
- get_incidents: Unified incident view across processes and proxies
- responsive_audit: Responsive design audits across multiple viewport sizes
- snapshot: Visual regression testing (baseline/compare screenshots)
- daemon: Manage the background daemon service`,
	}

	// Load .agnt.kdl and apply channel capability when enabled.
	agntCfg, cfgErr := config.LoadAgntConfig(".")
	if cfgErr != nil {
		agntCfg = config.DefaultAgntConfig()
	}
	serverOpts = tools.ChannelServerOptions(serverOpts, agntCfg.Channel)

	// Always trigger autostart on MCP initialized. In agnt run mode,
	// SESSION REGISTER drives autostart; StartScriptExplicit skips scripts
	// already in Running/Starting state, so both paths are safe to coexist.
	serverOpts.InitializedHandler = mcpAutostartHandler(dt.RunAutostart)

	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    appName,
			Version: appVersion,
		},
		serverOpts,
	)

	// Register daemon-aware tools
	tools.RegisterDaemonTools(server, dt)
	tools.RegisterDaemonManagementTool(server, dt)
	tools.RegisterTunnelTool(server, dt)
	tools.RegisterBrowserTool(server, dt)
	tools.RegisterAutomationTool(server, dt)
	tools.RegisterResponsiveAuditTool(server, dt)
	tools.RegisterAPIAuditTool(server, dt)
	tools.RegisterLoadingAuditTool(server, dt)
	tools.RegisterGetIncidentsTool(server, dt)

	// Register the replaytest tool (Pro: advanced_testing). The license manager
	// validates the installed license once at load.
	licMgr := license.NewManager()
	// Best-effort: a missing/invalid license is the normal free-tier state, so
	// the tool still registers (gated at call time). Record why for diagnosis.
	if err := licMgr.Load(); err != nil {
		debug.Log("license", "license load failed, continuing on free tier: %v", err)
	}
	tools.RegisterReplaytestTool(server, licMgr, dt.ReplaytestLogClient)

	// Register channel_reply tool when channel mode is enabled and reply-tool is on.
	if agntCfg.Channel != nil && agntCfg.Channel.IsEnabled() && agntCfg.Channel.ReplyToolEnabled() {
		tools.RegisterChannelReplyTool(server, dt)
	}

	// Register snapshot tools (visual regression testing)
	snapshotManager, err := snapshot.NewManager("", 0.01) // Default path and 1% threshold
	if err != nil {
		log.Printf("Warning: Failed to initialize snapshot manager: %v", err)
	} else {
		tools.RegisterSnapshotTools(server, snapshotManager, dt)
	}

	// Register walkthrough tool (live-demo step list; daemon mode only).
	tools.RegisterWalkthroughTool(server, dt)

	// Subscribe an always-on incident-digest sink so the unified inbox's
	// periodic digest reaches the agent via MCP Log notifications, independent
	// of channel mode. Rides the STREAM-EVENTS transport.
	digestCancel := dt.StartIncidentDigestSink(server)
	defer digestCancel()

	// Resolve working directory once — used for both channel sink scoping and
	// session registration so both operate on the same project path.
	cwd, cwdErr := os.Getwd()
	if cwdErr != nil {
		cwd = "."
	}

	// When channel mode is active, subscribe a ChannelSink to the daemon's
	// event stream. Events that pass severity/type filtering and deduplication
	// are forwarded as notifications/claude/channel notifications to all
	// connected MCP sessions. Project path scopes the subscription so two
	// simultaneous MCP clients don't receive each other's proxy events.
	channelCancel := dt.StartChannelSink(server, agntCfg.Channel, cwd)
	defer channelCancel()

	// When channel mode is active, register a daemon session so the MCP
	// process is tracked like an agnt run session. SessionPGID=0 because
	// there is no PTY child (no pgid containment needed). Cleanup
	// (unregister + heartbeat stop) runs when the handle is closed.
	var channelSession *tools.ChannelSessionHandle
	channelSession = dt.RegisterChannelSession(ctx, agntCfg.Channel, cwd)
	defer channelSession.Close()

	// Handle context cancellation
	go func() {
		<-ctx.Done()
		debug.Log("mcp", "MCP client shutdown signal received")
	}()

	// Run server over stdio
	log.SetOutput(os.Stderr)
	debug.Log("mcp", "Starting %s v%s (daemon mode)", appName, appVersion)

	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		// A closed stdio pipe surfaces as io.EOF / ErrConnectionClosed
		// ("server is closing: EOF"). This is the normal teardown path:
		// MCP clients such as Claude Code close stdin instead of sending
		// SIGINT/SIGTERM, so ctx is never cancelled. Treating that as a
		// fatal error made the client log "Server error" and exit non-zero,
		// which looks like a crash. Only a genuinely unexpected error is fatal.
		if ctx.Err() == nil && !isClientDisconnect(err) {
			// Persist the reason so it survives the process exit and surfaces
			// via `agnt hook log` and the overlay ⚠ notice on the next run.
			selflog.Record("mcp", "MCP server exited with error: %v", err)
			log.Fatalf("Server error: %v", err)
		}
		debug.Log("mcp", "MCP server stopped: %v", err)
	}

	debug.Log("mcp", "MCP client shutdown complete")
}

// isClientDisconnect reports whether err is the benign "the MCP client closed
// the stdio pipe" condition rather than a real server fault. Clients tear the
// server down by closing stdin (EOF) instead of signalling, so this must not be
// treated as fatal.
func isClientDisconnect(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, mcp.ErrConnectionClosed) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "EOF") ||
		strings.Contains(msg, "connection closed") ||
		strings.Contains(msg, "server is closing") ||
		strings.Contains(msg, "file already closed")
}

// runAutostartFunc is the signature for triggering a non-interactive autostart.
type runAutostartFunc func(projectDir string) (map[string]interface{}, error)

// mcpAutostartHandler returns an InitializedHandler that triggers project
// autostart for the current working directory. Safe to run alongside agnt run:
// AutostartManager.GetOrCreate ensures at-most-one autostart per project.
//
// The autostart runs on its own goroutine and the handler returns immediately.
// This is load-bearing, not a latency optimisation: the MCP SDK dispatches
// notifications *synchronously* on the JSON-RPC handler queue (only calls get
// jsonrpc2.Async), so anything this handler blocks on blocks every subsequent
// request on the session — including tools/list, which needs no daemon at all.
// Autostart reaches the daemon and can legitimately take a long time (a
// depends-on wait is bounded only by the caller's context, per
// .claude/rules/daemon-lifecycle.md), so blocking here turns a slow project
// start into a permanently silent MCP server. Nothing consumes the result but
// the diagnostics below, so detaching loses nothing.
func mcpAutostartHandler(runAutostart runAutostartFunc) func(context.Context, *mcp.InitializedRequest) {
	return func(ctx context.Context, req *mcp.InitializedRequest) {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[agnt] autostart: failed to get working directory: %v\n", err)
			return
		}

		go func() {
			done := make(chan struct{})
			defer close(done)
			// A stalled autostart used to be indistinguishable from a healthy
			// idle server: no output, no error, nothing to search for. Name it
			// out loud, and say explicitly that tools still work, so a slow
			// project start is not misread as the MCP server having died.
			go watchAutostartStall(os.Stderr, cwd, autostartStallNotice, done)

			result, err := runAutostart(cwd)
			if err != nil {
				// Persist as well as print: a stalled or failed autostart is
				// exactly the condition a developer goes looking for later,
				// and MCP clients routinely discard the server's stderr.
				selflog.Record("mcp", "autostart failed for %s: %v", cwd, err)
				fmt.Fprintf(os.Stderr, "[agnt] autostart failed for %s: %v\n", cwd, err)
				fmt.Fprintf(os.Stderr, "[agnt]   MCP tools are unaffected — the daemon is queried per call.\n")
				fmt.Fprintf(os.Stderr, "[agnt]   Diagnose with: agnt doctor   (details: agnt hook log)\n")
				return
			}

			if scripts, ok := result["scripts"]; ok {
				fmt.Fprintf(os.Stderr, "[agnt] autostart completed: scripts=%v\n", scripts)
			}
		}()
	}
}

// autostartStallNotice is how long autostart may run before it is reported as
// still in flight. A project whose dev server takes longer than this to signal
// ready is normal (dotnet cold start, NuGet restore); a project that never
// signals is not, and the two are indistinguishable from the outside without a
// message. Long enough not to fire on an ordinary start, short enough to beat
// a developer's "is this thing hung?" instinct.
//
// watchAutostartStall takes the period as a parameter rather than reading this
// directly, so a test can assert the notice's shape in milliseconds without
// mutating package state that a still-running watcher may be reading.
const autostartStallNotice = 20 * time.Second

// watchAutostartStall reports an autostart that is still running when the
// notice window elapses, then repeats on the same period so an indefinite
// depends-on wait (bounded only by the daemon's context — see
// daemon.waitForSingleDependency) leaves a trail instead of silence. Returns as
// soon as done is closed.
// The writer is a parameter rather than a hardcoded os.Stderr so tests can
// capture the notice without swapping the process-global stderr, which would
// race every other parallel test in this package.
func watchAutostartStall(w io.Writer, projectDir string, every time.Duration, done <-chan struct{}) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	waited := time.Duration(0)
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			waited += every
			msg := fmt.Sprintf("autostart still running after %s for %s", waited, projectDir)
			selflog.Record("mcp", "%s", msg)
			fmt.Fprintf(w, "[agnt] %s\n", msg)
			fmt.Fprintf(w, "[agnt]   MCP tools are NOT blocked by this — calls are served normally.\n")
			fmt.Fprintf(w, "[agnt]   Most likely a script's depends-on target has not signalled ready;\n")
			fmt.Fprintf(w, "[agnt]   that wait is unbounded unless the dependency declares timeout=N in .agnt.kdl.\n")
			fmt.Fprintf(w, "[agnt]   Inspect with: agnt doctor  |  agnt errors  |  proc list\n")
		}
	}
}

// channelAutostartHandler is kept for tests that reference it by name.
// New code should call mcpAutostartHandler directly.
func channelAutostartHandler(cfg *config.ChannelConfig, runAutostart runAutostartFunc) func(context.Context, *mcp.InitializedRequest) {
	if cfg == nil || !cfg.IsEnabled() {
		return nil
	}
	return mcpAutostartHandler(runAutostart)
}
