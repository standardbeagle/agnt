package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"

	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/license"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/snapshot"
	"github.com/standardbeagle/agnt/internal/tools"
	"github.com/standardbeagle/go-cli-server/process"

	"github.com/spf13/cobra"
	"github.com/standardbeagle/go-sdk/mcp"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run as shared server",
	Long: `Run as a shared server that syncronizes processes and proxies across clients.

By default, uses a background daemon for persistent state.
Use --legacy for direct process management (state lost on exit).`,
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
	serveLegacy bool
	mcpNoAttach bool
)

func init() {
	serveCmd.Flags().BoolVar(&serveLegacy, "legacy", false, "Run in legacy mode (no daemon)")
	mcpCmd.Flags().BoolVar(&mcpNoAttach, "no-attach", false, "Don't auto-attach to existing session (operate globally)")
}

func runServe(cmd *cobra.Command, args []string) {
	socketPath, _ := cmd.Flags().GetString("socket")
	if socketPath == "" {
		socketPath = daemon.DefaultSocketPath()
	}

	if serveLegacy {
		runLegacyServer()
	} else {
		runDaemonClient(socketPath)
	}
}

func runMCP(cmd *cobra.Command, args []string) {
	socketPath, _ := cmd.Flags().GetString("socket")
	if socketPath == "" {
		socketPath = daemon.DefaultSocketPath()
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
	daemonCfg := daemon.AutoStartConfig{
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
- get_errors: Unified error view across processes and proxies
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
	tools.RegisterResponsiveAuditTool(server, dt, nil)
	tools.RegisterAPIAuditTool(server, dt, nil)
	tools.RegisterLoadingAuditTool(server, dt, nil)
	tools.RegisterGetIncidentsTool(server, dt)

	// Register the replaytest tool (Pro: advanced_testing). The license manager
	// validates the installed license once at load.
	licMgr := license.NewManager()
	_ = licMgr.Load()
	tools.RegisterReplaytestTool(server, licMgr)

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
		if ctx.Err() == nil {
			log.Fatalf("Server error: %v", err)
		}
	}

	debug.Log("mcp", "MCP client shutdown complete")
}

// runLegacyServer runs in the original mode without a daemon.
func runLegacyServer() {
	// Create root context with signal cancellation
	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer cancel()

	// Initialize process manager with default config
	pm := process.NewProcessManager(process.ManagerConfig{
		DefaultTimeout:    0,
		MaxOutputBuffer:   process.DefaultBufferSize,
		GracefulTimeout:   5 * time.Second,
		HealthCheckPeriod: 10 * time.Second,
	})

	// Initialize proxy manager
	proxym := proxy.NewProxyManager()

	// Create MCP server
	legacyOpts := &mcp.ServerOptions{
		HasTools:     true,
		Instructions: "Development tool server for project detection, process management, and reverse proxy with traffic logging. Running in legacy mode - state will be lost when server stops.",
	}

	// Load .agnt.kdl and apply channel capability when enabled.
	agntCfg, cfgErr := config.LoadAgntConfig(".")
	if cfgErr != nil {
		agntCfg = config.DefaultAgntConfig()
	}
	legacyOpts = tools.ChannelServerOptions(legacyOpts, agntCfg.Channel)

	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    appName,
			Version: appVersion,
		},
		legacyOpts,
	)

	// Register legacy tools (direct process management)
	tools.RegisterProcessTools(server, pm)
	tools.RegisterProjectTools(server)
	tools.RegisterProxyTools(server, proxym)
	tools.RegisterGetErrorsTool(server, nil, proxym)
	tools.RegisterResponsiveAuditTool(server, nil, proxym)
	tools.RegisterAPIAuditTool(server, nil, proxym)
	tools.RegisterLoadingAuditTool(server, nil, proxym)

	// Register snapshot tools (visual regression testing)
	snapshotManager, err := snapshot.NewManager("", 0.01)
	if err != nil {
		log.Printf("Warning: Failed to initialize snapshot manager: %v", err)
	} else {
		tools.RegisterSnapshotTools(server, snapshotManager, nil)
	}

	// Register the replaytest tool (Pro: advanced_testing).
	licMgr := license.NewManager()
	_ = licMgr.Load()
	tools.RegisterReplaytestTool(server, licMgr)

	// Handle shutdown in background
	go func() {
		<-ctx.Done()
		debug.Log("mcp", "Shutdown signal received, stopping all processes and proxies")

		shutdownCtx, shutdownCancel := context.WithTimeout(
			context.Background(),
			2*time.Second,
		)
		defer shutdownCancel()

		if err := pm.Shutdown(shutdownCtx); err != nil {
			debug.Error("mcp", "Process manager shutdown error: %v", err)
		}

		if err := proxym.Shutdown(shutdownCtx); err != nil {
			debug.Error("mcp", "Proxy manager shutdown error: %v", err)
		}
	}()

	// Run server over stdio
	log.SetOutput(os.Stderr)
	debug.Log("mcp", "Starting %s v%s (legacy mode)", appName, appVersion)

	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		if ctx.Err() == nil {
			log.Fatalf("Server error: %v", err)
		}
	}

	debug.Log("mcp", "Server shutdown complete")
}

// runAutostartFunc is the signature for triggering a non-interactive autostart.
type runAutostartFunc func(projectDir string) (map[string]interface{}, error)

// mcpAutostartHandler returns an InitializedHandler that triggers project
// autostart for the current working directory. Safe to run alongside agnt run:
// AutostartManager.GetOrCreate ensures at-most-one autostart per project.
func mcpAutostartHandler(runAutostart runAutostartFunc) func(context.Context, *mcp.InitializedRequest) {
	return func(ctx context.Context, req *mcp.InitializedRequest) {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[agnt] autostart: failed to get working directory: %v\n", err)
			return
		}

		result, err := runAutostart(cwd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[agnt] autostart failed: %v\n", err)
			return
		}

		if scripts, ok := result["scripts"]; ok {
			fmt.Fprintf(os.Stderr, "[agnt] autostart completed: scripts=%v\n", scripts)
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
