//go:build windows

package main

import (
	"errors"

	"github.com/spf13/cobra"
)

// errACPUnsupportedOnWindows is returned by `agnt acp` on native Windows.
var errACPUnsupportedOnWindows = errors.New(
	"agnt acp is not supported on native Windows: the ACP client relies on Unix process groups and terminal signals. Run it from WSL instead")

// acpCmd is a loud stub on Windows — the ACP client REPL relies on Unix stdio
// pipe plumbing, process groups and SIGWINCH, none of which are ported. It
// fails with a non-zero exit rather than printing help and reporting success,
// so a script or agent that invokes it can tell nothing ran.
var acpCmd = &cobra.Command{
	Use:   "acp <agent> [prompt]",
	Short: "Run an ACP-compatible agent (not supported on Windows; use WSL)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return errACPUnsupportedOnWindows
	},
}
