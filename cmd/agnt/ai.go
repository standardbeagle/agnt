package main

import (
	"github.com/spf13/cobra"
)

// aiCmd is the parent command for all AI tool subcommands.
var aiCmd = &cobra.Command{
	Use:   "ai",
	Short: "Run AI coding tools with clean JSONL streaming output",
	Long: `Run AI coding tools with clean JSONL streaming output for programmatic consumption.

Unlike 'agnt run' which wraps tools in a PTY with overlay features and terminal
animations, 'agnt ai' provides direct JSON-RPC streaming over stdio - no spinners,
no ANSI escape sequences, no animation cruft. Output is machine-parseable JSONL.

Use this when you need to:
  - Integrate Claude Code into automated pipelines
  - Parse structured output from another process
  - Avoid terminal animation interference
  - Build tooling that consumes Claude Code output

Subcommands:
  claude    Run Claude Code with stream-json output`,
}

// Common flags shared by all AI subcommands
var (
	aiModel        string
	aiMaxTurns     int
	aiMaxBudget    float64
	aiSystemPrompt string
	aiVerbose      bool
)

func init() {
	// Register shared persistent flags on the ai parent command
	aiCmd.PersistentFlags().StringVar(&aiModel, "model", "", "Model to use (sonnet, opus, haiku)")
	aiCmd.PersistentFlags().IntVar(&aiMaxTurns, "max-turns", 0, "Maximum conversation turns (0 = unlimited)")
	aiCmd.PersistentFlags().Float64Var(&aiMaxBudget, "max-budget", 0, "Maximum budget in USD (0 = unlimited)")
	aiCmd.PersistentFlags().StringVar(&aiSystemPrompt, "system-prompt", "", "Additional system prompt to append")
	aiCmd.PersistentFlags().BoolVar(&aiVerbose, "verbose", false, "Enable verbose output")

	// Add subcommands
	aiCmd.AddCommand(aiClaudeCmd)
}
