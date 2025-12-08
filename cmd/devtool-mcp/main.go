package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"devtool-mcp/internal/daemon"
	"devtool-mcp/internal/process"
	"devtool-mcp/internal/proxy"
	"devtool-mcp/internal/tools"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	serverName    = "devtool-mcp"
	serverVersion = "0.3.0"
)

func main() {
	// Parse command line arguments
	var (
		daemonMode  bool
		legacyMode  bool
		socketPath  string
		showHelp    bool
		showVersion bool
	)

	flag.BoolVar(&daemonMode, "daemon", false, "Run as background daemon")
	flag.BoolVar(&legacyMode, "legacy", false, "Run in legacy mode (no daemon, direct process management)")
	flag.StringVar(&socketPath, "socket", "", "Socket path for daemon communication")
	flag.BoolVar(&showHelp, "help", false, "Show help")
	flag.BoolVar(&showVersion, "version", false, "Show version")
	flag.Parse()

	// Handle help and version
	if showHelp {
		printHelp()
		return
	}
	if showVersion {
		fmt.Printf("%s v%s\n", serverName, serverVersion)
		return
	}

	// Set default socket path
	if socketPath == "" {
		socketPath = daemon.DefaultSocketPath()
	}

	// Check for "daemon" subcommand with potential sub-subcommand
	if len(os.Args) > 1 && os.Args[1] == "daemon" {
		// Parse daemon-specific arguments
		// Support both: "daemon start" and "daemon --socket path start"
		subCmd := ""
		for i := 2; i < len(os.Args); i++ {
			arg := os.Args[i]
			if arg == "--socket" && i+1 < len(os.Args) {
				socketPath = os.Args[i+1]
				i++ // Skip the socket path value
			} else if !strings.HasPrefix(arg, "-") {
				subCmd = arg
				break
			}
		}

		switch subCmd {
		case "status":
			runDaemonStatus(socketPath)
		case "start", "":
			// "daemon" or "daemon start" starts the daemon
			runDaemon(socketPath)
		case "stop":
			runDaemonStop(socketPath)
		case "restart":
			runDaemonRestart(socketPath)
		case "info":
			runDaemonInfo(socketPath)
		default:
			fmt.Fprintf(os.Stderr, "Unknown daemon subcommand: %s\n", subCmd)
			fmt.Fprintln(os.Stderr, "Valid subcommands: status, start, stop, restart, info")
			os.Exit(1)
		}
		return
	}

	if daemonMode {
		runDaemon(socketPath)
	} else if legacyMode {
		runLegacy()
	} else {
		runMCPClient(socketPath)
	}
}

func printHelp() {
	fmt.Printf(`%s v%s - Development tool MCP server

Usage:
  %s [options]
  %s daemon [subcommand]

Options:
  --legacy      Run in legacy mode (no daemon, direct process management)
  --socket PATH Socket path for daemon communication (default: auto)
  --help        Show this help
  --version     Show version

Daemon Subcommands:
  %s daemon          Start the daemon (foreground)
  %s daemon start    Start the daemon (foreground)
  %s daemon status   Check if daemon is running
  %s daemon stop     Stop the running daemon
  %s daemon restart  Restart the daemon
  %s daemon info     Show daemon information

Modes:
  Default (MCP Client):
    Runs as an MCP server that communicates with a background daemon.
    The daemon is auto-started if not already running.
    Process and proxy state persists across MCP client connections.

  Daemon:
    Runs as the background daemon process that manages state.
    Usually started automatically by the MCP client.

  Legacy:
    Runs in the original mode without a daemon.
    Process and proxy state is lost when the MCP server stops.

Examples:
  # Normal usage (MCP server with daemon backend)
  %s

  # Check daemon status
  %s daemon status

  # Start daemon manually
  %s daemon start

  # Use custom socket path
  %s --socket /tmp/my-devtool.sock

  # Legacy mode (no daemon)
  %s --legacy
`, serverName, serverVersion, serverName, serverName,
		serverName, serverName, serverName, serverName, serverName, serverName,
		serverName, serverName, serverName, serverName, serverName)
}

// runDaemonStatus checks if the daemon is running.
func runDaemonStatus(socketPath string) {
	if daemon.IsRunning(socketPath) {
		fmt.Println("Daemon is running")
		fmt.Printf("Socket: %s\n", socketPath)
		os.Exit(0)
	} else {
		fmt.Println("Daemon is not running")
		os.Exit(1)
	}
}

// runDaemonStop stops a running daemon.
func runDaemonStop(socketPath string) {
	client := daemon.NewClient(daemon.WithSocketPath(socketPath))
	if err := client.Connect(); err != nil {
		fmt.Fprintf(os.Stderr, "Daemon is not running: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	if err := client.Shutdown(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to stop daemon: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Daemon stopped")
}

// runDaemonRestart restarts the daemon.
func runDaemonRestart(socketPath string) {
	// Try to stop existing daemon
	client := daemon.NewClient(daemon.WithSocketPath(socketPath))
	if err := client.Connect(); err == nil {
		_ = client.Shutdown()
		client.Close()
		// Give it time to shut down
		time.Sleep(500 * time.Millisecond)
	}

	// Start new daemon
	runDaemon(socketPath)
}

// runDaemonInfo shows daemon information.
func runDaemonInfo(socketPath string) {
	client := daemon.NewClient(daemon.WithSocketPath(socketPath))
	if err := client.Connect(); err != nil {
		fmt.Fprintf(os.Stderr, "Daemon is not running: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	info, err := client.Info()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get daemon info: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Daemon v%s\n", info.Version)
	fmt.Printf("Socket: %s\n", info.SocketPath)
	fmt.Printf("Uptime: %s\n", info.Uptime.Round(time.Second))
	fmt.Printf("Clients: %d\n", info.ClientCount)
	fmt.Printf("Processes: %d active, %d total, %d failed\n",
		info.ProcessInfo.Active, info.ProcessInfo.TotalStarted, info.ProcessInfo.TotalFailed)
	fmt.Printf("Proxies: %d active, %d total\n",
		info.ProxyInfo.Active, info.ProxyInfo.TotalStarted)
}

// runDaemon runs as the background daemon process.
func runDaemon(socketPath string) {
	// Create root context with signal cancellation
	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer cancel()

	// Configure and create daemon
	config := daemon.DaemonConfig{
		SocketPath: socketPath,
		ProcessConfig: process.ManagerConfig{
			DefaultTimeout:    0,
			MaxOutputBuffer:   process.DefaultBufferSize,
			GracefulTimeout:   5 * time.Second,
			HealthCheckPeriod: 10 * time.Second,
		},
		MaxClients:   100,
		WriteTimeout: 30 * time.Second,
	}

	d := daemon.New(config)

	// Start daemon
	if err := d.Start(); err != nil {
		log.Fatalf("Failed to start daemon: %v", err)
	}

	// Wait for shutdown signal
	<-ctx.Done()
	log.Println("Daemon shutdown signal received...")

	// Graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer shutdownCancel()

	if err := d.Stop(shutdownCtx); err != nil {
		log.Printf("Daemon shutdown error: %v", err)
	}

	log.Println("Daemon shutdown complete")
}

// runMCPClient runs the MCP server that communicates with the daemon.
func runMCPClient(socketPath string) {
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

	dt := tools.NewDaemonTools(config)
	defer dt.Close()

	// Create MCP server
	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    serverName,
			Version: serverVersion,
		},
		&mcp.ServerOptions{
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
- daemon: Manage the background daemon service`,
		},
	)

	// Register daemon-aware tools
	tools.RegisterDaemonTools(server, dt)
	tools.RegisterDaemonManagementTool(server, dt)

	// Handle context cancellation
	go func() {
		<-ctx.Done()
		log.Println("MCP client shutdown signal received...")
		// Daemon continues running in background
	}()

	// Run server over stdio
	log.SetOutput(os.Stderr)
	log.Printf("Starting %s v%s (daemon mode)", serverName, serverVersion)

	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		if ctx.Err() == nil {
			log.Fatalf("Server error: %v", err)
		}
	}

	log.Println("MCP client shutdown complete")
}

// runLegacy runs in the original mode without a daemon.
func runLegacy() {
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
			Name:    serverName,
			Version: serverVersion,
		},
		&mcp.ServerOptions{
			Instructions: "Development tool server for project detection, process management, and reverse proxy with traffic logging. Running in legacy mode - state will be lost when server stops.",
		},
	)

	// Register legacy tools (direct process management)
	tools.RegisterProcessTools(server, pm)
	tools.RegisterProjectTools(server)
	tools.RegisterProxyTools(server, proxym)

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
	log.Printf("Starting %s v%s (legacy mode)", serverName, serverVersion)

	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		if ctx.Err() == nil {
			log.Fatalf("Server error: %v", err)
		}
	}

	log.Println("Server shutdown complete")
}
