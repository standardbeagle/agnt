//go:build windows

package main

import "github.com/spf13/cobra"

// aiClaudeCmd is a stub on Windows — the interactive Claude REPL
// requires PTY features (SIGWINCH) not available on Windows.
var aiClaudeCmd = &cobra.Command{
	Use:   "claude",
	Short: "Run Claude Code with streaming output (not supported on Windows)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}
