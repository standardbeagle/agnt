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
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/snapshot"
	"github.com/standardbeagle/agnt/internal/tools"
	"github.com/standardbeagle/go-cli-server/process"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
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

// mcpAlertSink implements daemon.MCPAlertSink to deliver alerts via MCP Log notifications.
type mcpAlertSink struct {
	server *mcp.Server
}

func (s *mcpAlertSink) SendAlert(level string, message string) error {
	mcpLevel := mcp.LoggingLevel(level)
	for session := range s.server.Sessions() {
		_ = session.Log(context.Background(), &mcp.LoggingMessageParams{
			Level:  mcpLevel,
			Logger: "agnt-alerts",
			Data:   message,
		})
	}
	return nil
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

	// When channel mode is active, trigger project autostart on MCP
	// initialized. In channel mode there is no PTY session to drive autostart
	// through SESSION REGISTER, so the InitializedHandler kicks it off instead.
	serverOpts.InitializedHandler = channelAutostartHandler(agntCfg.Channel, dt.RunAutostart)

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

	// Register channel_reply tool when channel mode is enabled and reply-tool is on.
	if agntCfg.Channel != nil && agntCfg.Channel.IsEnabled() && agntCfg.Channel.ReplyToolEnabled() {
		tools.RegisterChannelReplyTool(server, dt)
	}

	// Register snapshot tools (visual regression testing)
	snapshotManager, err := snapshot.NewManager("", 0.01) // Default path and 1% threshold
	if err != nil {
		log.Printf("Warning: Failed to initialize snapshot manager: %v", err)
	} else {
		tools.RegisterSnapshotTools(server, snapshotManager)
	}

	// Set up MCP alert sink for process output alerts.
	// The AlertHub in the daemon can route alerts to these sessions.
	// For now, alerts from process output are delivered via the PTY overlay path
	// in agnt run. This MCP sink enables future daemon-side alert routing.
	alertSink := &mcpAlertSink{server: server}
	_ = alertSink // Available for daemon alert hub integration
	dt.SetAlertSink(alertSink)

	// When channel mode is active, subscribe a ChannelSink to the daemon's
	// event stream. Events that pass severity/type filtering and deduplication
	// are forwarded as notifications/claude/channel notifications to all
	// connected MCP sessions.
	channelCancel := dt.StartChannelSink(server, agntCfg.Channel)
	defer channelCancel()

	// When channel mode is active, register a daemon session so the MCP
	// process is tracked like an agnt run session. SessionPGID=0 because
	// there is no PTY child (no pgid containment needed). Cleanup
	// (unregister + heartbeat stop) runs when the handle is closed.
	var channelSession *tools.ChannelSessionHandle
	if cwd, err := os.Getwd(); err == nil {
		channelSession = dt.RegisterChannelSession(ctx, agntCfg.Channel, cwd)
	} else {
		channelSession = dt.RegisterChannelSession(ctx, agntCfg.Channel, ".")
	}
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

	// Register snapshot tools (visual regression testing)
	snapshotManager, err := snapshot.NewManager("", 0.01)
	if err != nil {
		log.Printf("Warning: Failed to initialize snapshot manager: %v", err)
	} else {
		tools.RegisterSnapshotTools(server, snapshotManager)
	}

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

// channelAutostartHandler returns an InitializedHandler that triggers project
// autostart when channel mode is enabled, or nil when disabled. The handler
// calls the provided runAutostart function with the current working directory.
func channelAutostartHandler(cfg *config.ChannelConfig, runAutostart runAutostartFunc) func(context.Context, *mcp.InitializedRequest) {
	if cfg == nil || !cfg.IsEnabled() {
		return nil
	}
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
