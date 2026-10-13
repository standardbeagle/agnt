//go:build windows

package main

import (
	"errors"

	"github.com/spf13/cobra"
)

// errAiClaudeUnsupportedOnWindows is returned by `agnt ai claude` on native
// Windows.
var errAiClaudeUnsupportedOnWindows = errors.New(
	"agnt ai claude is not supported on native Windows: the streaming client and its overlay REPL rely on Unix terminal handling (raw mode, SIGWINCH). Run it from WSL, or use `agnt run claude` here")

// aiClaudeCmd is a loud stub on Windows — the Claude streaming client and its
// overlay REPL are built on Unix terminal plumbing that is not ported. It
// fails with a non-zero exit rather than printing help and reporting success,
// so a script or agent that invokes it can tell nothing ran. Flag parsing is
// disabled so every invocation shape (`-p "…"`, `--raw`, a positional prompt)
// reaches the same message instead of an "unknown flag" error.
var aiClaudeCmd = &cobra.Command{
	Use:                "claude [prompt]",
	Short:              "Run Claude Code with streaming output (not supported on Windows; use WSL)",
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return errAiClaudeUnsupportedOnWindows
	},
}
