package main

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
	"github.com/standardbeagle/agnt/internal/daemon"
)

const appName = "agnt"

// appVersion can be overridden at build time with -ldflags="-X main.appVersion=x.y.z"
var appVersion = "0.7.5"

var rootCmd = &cobra.Command{
	Use:   appName,
	Short: "Development agent for AI-assisted coding",
	Long: `Agnt is a development tool that provides:
  - MCP server for AI coding assistants (Claude Code, etc.)
  - PTY wrapper to run any AI coding tool with overlay features
  - Reverse proxy with traffic logging and frontend instrumentation
  - Process management with output capture`,
	Version: appVersion,
	// Default behavior: if stdin is not a terminal, run as MCP server
	Run: func(cmd *cobra.Command, args []string) {
		if !isTerminal(os.Stdin) {
			// Running as MCP server (stdin is a pipe)
			runServe(cmd, args)
		} else {
			// Interactive terminal - show help
			cmd.Help()
		}
	},
}

func init() {
	// Global flags
	rootCmd.PersistentFlags().String("socket", "", "Socket path for daemon communication")

	// Add subcommands
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(daemonCmd)
	rootCmd.AddCommand(sessionCmd)

	// Custom version output that includes daemon version
	rootCmd.SetVersionTemplate(getVersionString())
}

func main() {
	// Load .env file if present (silently ignore if not found)
	_ = godotenv.Load()

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func isTerminal(f *os.File) bool {
	stat, err := f.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) != 0
}

// getVersionString returns the version string including daemon version if available
func getVersionString() string {
	version := fmt.Sprintf("%s v%s\n", appName, appVersion)

	// Try to get daemon version
	client := daemon.NewClient()
	if err := client.Connect(); err == nil {
		defer client.Close()
		if info, err := client.Info(); err == nil {
			version += fmt.Sprintf("daemon v%s (uptime: %s)\n", info.Version, info.Uptime)
		} else {
			version += fmt.Sprintf("daemon: error getting info (%v)\n", err)
		}
	} else {
		version += "daemon: not running\n"
	}

	return version
}
