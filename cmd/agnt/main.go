package main

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
)

const (
	appName    = "agnt"
	appVersion = "0.6.5"
)

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

	// Version template
	rootCmd.SetVersionTemplate(fmt.Sprintf("%s v%s\n", appName, appVersion))
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
