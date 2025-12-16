package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/process"

	"github.com/spf13/cobra"
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Manage the background daemon",
	Long: `Manage the background daemon that maintains persistent state.

The daemon manages processes and proxies, and survives client disconnections.
It is automatically started when needed, but can be managed manually.`,
}

var daemonStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the daemon",
	Run:   runDaemonStart,
}

var daemonStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the daemon",
	Run:   runDaemonStop,
}

var daemonRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the daemon",
	Run:   runDaemonRestart,
}

var daemonStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check daemon status",
	Run:   runDaemonStatus,
}

var daemonInfoCmd = &cobra.Command{
	Use:   "info",
	Short: "Show daemon information",
	Run:   runDaemonInfo,
}

var daemonUpgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Upgrade the daemon to a new version",
	Long: `Gracefully upgrades the daemon to a new version.

The upgrade process:
  1. Connects to running daemon
  2. Requests graceful shutdown (stops all processes)
  3. Waits for daemon to exit
  4. Starts new daemon binary
  5. Verifies new version

Examples:
  agnt daemon upgrade                    # Upgrade to current binary version
  agnt daemon upgrade --timeout 60s      # Custom timeout
  agnt daemon upgrade --force            # Force upgrade even if version matches`,
	Run: runDaemonUpgrade,
}

func init() {
	daemonCmd.AddCommand(daemonStartCmd)
	daemonCmd.AddCommand(daemonStopCmd)
	daemonCmd.AddCommand(daemonRestartCmd)
	daemonCmd.AddCommand(daemonStatusCmd)
	daemonCmd.AddCommand(daemonInfoCmd)
	daemonCmd.AddCommand(daemonUpgradeCmd)

	// Upgrade command flags
	daemonUpgradeCmd.Flags().Duration("timeout", 30*time.Second, "Maximum time for upgrade")
	daemonUpgradeCmd.Flags().Bool("force", false, "Force upgrade even if versions match")
	daemonUpgradeCmd.Flags().Bool("verbose", false, "Enable verbose logging")
}

func getSocketPath(cmd *cobra.Command) string {
	socketPath, _ := cmd.Root().PersistentFlags().GetString("socket")
	if socketPath == "" {
		socketPath = daemon.DefaultSocketPath()
	}
	return socketPath
}

func runDaemonStart(cmd *cobra.Command, args []string) {
	socketPath := getSocketPath(cmd)

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

	log.Printf("Daemon started on %s", socketPath)

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

func runDaemonStop(cmd *cobra.Command, args []string) {
	socketPath := getSocketPath(cmd)

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

func runDaemonRestart(cmd *cobra.Command, args []string) {
	socketPath := getSocketPath(cmd)

	// Try to stop existing daemon
	client := daemon.NewClient(daemon.WithSocketPath(socketPath))
	if err := client.Connect(); err == nil {
		_ = client.Shutdown()
		client.Close()
		// Give it time to shut down
		time.Sleep(500 * time.Millisecond)
	}

	// Start new daemon
	runDaemonStart(cmd, args)
}

func runDaemonStatus(cmd *cobra.Command, args []string) {
	socketPath := getSocketPath(cmd)

	if daemon.IsRunning(socketPath) {
		fmt.Println("Daemon is running")
		fmt.Printf("Socket: %s\n", socketPath)
		os.Exit(0)
	} else {
		fmt.Println("Daemon is not running")
		os.Exit(1)
	}
}

func runDaemonInfo(cmd *cobra.Command, args []string) {
	socketPath := getSocketPath(cmd)

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
	if info.BuildTime != "" {
		fmt.Printf("Build Time: %s\n", info.BuildTime)
	}
	if info.GitCommit != "" {
		fmt.Printf("Git Commit: %s\n", info.GitCommit)
	}
	fmt.Printf("Socket: %s\n", info.SocketPath)
	fmt.Printf("Uptime: %s\n", info.Uptime.Round(time.Second))
	fmt.Printf("Clients: %d\n", info.ClientCount)
	fmt.Printf("Processes: %d active, %d total, %d failed\n",
		info.ProcessInfo.Active, info.ProcessInfo.TotalStarted, info.ProcessInfo.TotalFailed)
	fmt.Printf("Proxies: %d active, %d total\n",
		info.ProxyInfo.Active, info.ProxyInfo.TotalStarted)

	// Show update notification if available
	if info.UpdateInfo != nil {
		if info.UpdateInfo.Available {
			fmt.Printf("\n🎉 Update available: v%s → v%s\n",
				info.UpdateInfo.CurrentVersion, info.UpdateInfo.LatestVersion)
			fmt.Printf("   Run 'agnt daemon upgrade' to update\n")
			if info.UpdateInfo.ReleaseURL != "" {
				fmt.Printf("   Release notes: %s\n", info.UpdateInfo.ReleaseURL)
			}
		} else if info.UpdateInfo.CheckError != "" {
			fmt.Printf("\n⚠ Update check failed: %s\n", info.UpdateInfo.CheckError)
		} else if !info.UpdateInfo.LastChecked.IsZero() {
			fmt.Printf("\n✓ Up to date (last checked: %s ago)\n",
				time.Since(info.UpdateInfo.LastChecked).Round(time.Second))
		}
	}
}

func runDaemonUpgrade(cmd *cobra.Command, args []string) {
	socketPath := getSocketPath(cmd)

	// Parse flags
	timeout, _ := cmd.Flags().GetDuration("timeout")
	force, _ := cmd.Flags().GetBool("force")
	verbose, _ := cmd.Flags().GetBool("verbose")

	// Create upgrade config
	config := daemon.UpgradeConfig{
		SocketPath:      socketPath,
		Timeout:         timeout,
		GracefulTimeout: 5 * time.Second,
		Force:           force,
		Verbose:         verbose,
	}

	// Create upgrader
	upgrader := daemon.NewDaemonUpgrader(config)

	// Run upgrade with timeout context
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	fmt.Println("Starting daemon upgrade...")
	if err := upgrader.Upgrade(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Upgrade failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("✓ Upgrade complete!")
}
