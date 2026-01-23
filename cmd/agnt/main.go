package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/debug"
)

const appName = "agnt"

// appVersion can be overridden at build time with -ldflags="-X main.appVersion=x.y.z"
var appVersion = "0.9.0"

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

var debugMode bool
var debugLogFile string

func init() {
	// Global flags
	rootCmd.PersistentFlags().String("socket", "", "Socket path for daemon communication")
	rootCmd.PersistentFlags().BoolVarP(&debugMode, "debug", "d", false, "Enable debug logging (also: AGNT_DEBUG=1)")
	rootCmd.PersistentFlags().StringVar(&debugLogFile, "debug-log", "", "Write debug logs to file (in ~/.cache/agnt/logs/)")

	// Initialize debug mode from flags before command execution
	cobra.OnInitialize(initDebug)

	// Add subcommands
	rootCmd.AddCommand(mcpCmd)
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(daemonCmd)
	rootCmd.AddCommand(sessionCmd)

	// Custom version output that includes daemon version
	rootCmd.SetVersionTemplate(getVersionString())
}

func initDebug() {
	// Enable debug if flag is set (env var is checked in debug.init())
	if debugMode {
		debug.Enable()
	}

	// Set up log file if specified
	if debugLogFile != "" {
		if err := debug.SetLogFile(debugLogFile); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to set debug log file: %v\n", err)
		} else if debug.IsEnabled() {
			debug.Log("main", "Debug logging to file: %s", debug.GetLogFilePath())
		}
	}

	if debug.IsEnabled() {
		debug.Log("main", "Debug mode enabled (version: %s)", appVersion)
	}
}

func main() {
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
