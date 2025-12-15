package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/standardbeagle/agnt/internal/daemon"

	"github.com/spf13/cobra"
)

var upCmd = &cobra.Command{
	Use:   "up",
	Short: "Start the daemon in the background",
	Long: `Start the daemon in the background and exit.

The daemon will continue running after this command exits.
Use 'agnt daemon status' to check if it's running.
Use 'agnt daemon stop' to stop it.`,
	Run: runUp,
}

func init() {
	rootCmd.AddCommand(upCmd)
}

func runUp(cmd *cobra.Command, args []string) {
	socketPath := getSocketPath(cmd)

	// Check if daemon is already running
	if daemon.IsRunning(socketPath) {
		fmt.Println("Daemon is already running")
		return
	}

	// Find the daemon binary
	execPath, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get executable path: %v\n", err)
		os.Exit(1)
	}

	// Try the dedicated daemon binary first (avoids fork restrictions in sandboxes)
	daemonPath := execPath + "-daemon"
	if _, err := os.Stat(daemonPath); err != nil {
		// Fall back to self
		daemonPath = execPath
	}

	// Start daemon in background
	daemonCmd := exec.Command(daemonPath, "daemon", "start", "--socket", socketPath)
	daemonCmd.Stdin = nil
	daemonCmd.Stdout = nil
	daemonCmd.Stderr = nil

	// Detach from parent process group
	setSysProcAttr(daemonCmd)

	if err := daemonCmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start daemon: %v\n", err)
		os.Exit(1)
	}

	// Don't wait - let daemon run independently
	go daemonCmd.Wait() //nolint:errcheck

	// Wait briefly for daemon to be ready
	config := daemon.DefaultAutoStartConfig()
	config.SocketPath = socketPath
	client := daemon.NewAutoStartClient(config)

	if err := client.Connect(); err != nil {
		fmt.Fprintf(os.Stderr, "Daemon started but failed to connect: %v\n", err)
		os.Exit(1)
	}
	client.Close()

	fmt.Printf("Daemon started (socket: %s)\n", socketPath)
}
