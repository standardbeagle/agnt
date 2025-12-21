package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/process"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/snapshot"
	"github.com/standardbeagle/agnt/internal/tools"

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
	config := daemon.AutoStartConfig{
		SocketPath:    socketPath,
		StartTimeout:  5 * time.Second,
		RetryInterval: 100 * time.Millisecond,
		MaxRetries:    50,
	}

	dt := tools.NewDaemonTools(config, appVersion)
	defer dt.Close()

	// Disable auto-attach if requested
	if len(noAttach) > 0 && noAttach[0] {
		dt.SetNoAutoAttach(true)
	}

	// Create MCP server
	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    appName,
			Version: appVersion,
		},
		&mcp.ServerOptions{
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
- snapshot: Visual regression testing (baseline/compare screenshots)
- daemon: Manage the background daemon service`,
		},
	)

	// Register daemon-aware tools
	tools.RegisterDaemonTools(server, dt)
	tools.RegisterDaemonManagementTool(server, dt)
	tools.RegisterTunnelTool(server, dt)

	// Register snapshot tools (visual regression testing)
	snapshotManager, err := snapshot.NewManager("", 0.01) // Default path and 1% threshold
	if err != nil {
		log.Printf("Warning: Failed to initialize snapshot manager: %v", err)
	} else {
		tools.RegisterSnapshotTools(server, snapshotManager)
	}

	// Handle context cancellation
	go func() {
		<-ctx.Done()
		log.Println("MCP client shutdown signal received...")
	}()

	// Run server over stdio
	log.SetOutput(os.Stderr)
	log.Printf("Starting %s v%s (daemon mode)", appName, appVersion)

	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		if ctx.Err() == nil {
			log.Fatalf("Server error: %v", err)
		}
	}

	log.Println("MCP client shutdown complete")
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
	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    appName,
			Version: appVersion,
		},
		&mcp.ServerOptions{
			HasTools:     true,
			Instructions: "Development tool server for project detection, process management, and reverse proxy with traffic logging. Running in legacy mode - state will be lost when server stops.",
		},
	)

	// Register legacy tools (direct process management)
	tools.RegisterProcessTools(server, pm)
	tools.RegisterProjectTools(server)
	tools.RegisterProxyTools(server, proxym)

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
		log.Println("Shutdown signal received, stopping all processes and proxies...")

		shutdownCtx, shutdownCancel := context.WithTimeout(
			context.Background(),
			2*time.Second,
		)
		defer shutdownCancel()

		if err := pm.Shutdown(shutdownCtx); err != nil {
			log.Printf("Process manager shutdown error: %v", err)
		}

		if err := proxym.Shutdown(shutdownCtx); err != nil {
			log.Printf("Proxy manager shutdown error: %v", err)
		}
	}()

	// Run server over stdio
	log.SetOutput(os.Stderr)
	log.Printf("Starting %s v%s (legacy mode)", appName, appVersion)

	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		if ctx.Err() == nil {
			log.Fatalf("Server error: %v", err)
		}
	}

	log.Println("Server shutdown complete")
}
