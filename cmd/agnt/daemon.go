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

func init() {
	daemonCmd.AddCommand(daemonStartCmd)
	daemonCmd.AddCommand(daemonStopCmd)
	daemonCmd.AddCommand(daemonRestartCmd)
	daemonCmd.AddCommand(daemonStatusCmd)
	daemonCmd.AddCommand(daemonInfoCmd)
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
	fmt.Printf("Socket: %s\n", info.SocketPath)
	fmt.Printf("Uptime: %s\n", info.Uptime.Round(time.Second))
	fmt.Printf("Clients: %d\n", info.ClientCount)
	fmt.Printf("Processes: %d active, %d total, %d failed\n",
		info.ProcessInfo.Active, info.ProcessInfo.TotalStarted, info.ProcessInfo.TotalFailed)
	fmt.Printf("Proxies: %d active, %d total\n",
		info.ProxyInfo.Active, info.ProxyInfo.TotalStarted)
}
