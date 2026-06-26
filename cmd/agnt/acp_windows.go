//go:build windows

package main

import "github.com/spf13/cobra"

// acpCmd is a stub on Windows — the ACP client REPL relies on Unix stdio
// pipe plumbing and PTY-adjacent helpers not yet ported to Windows.
var acpCmd = &cobra.Command{
	Use:   "acp <agent> [prompt]",
	Short: "Run an ACP-compatible agent (not supported on Windows)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}
