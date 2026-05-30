package main

import "github.com/spf13/cobra"

// initCmd is the explicit, on-demand counterpart to the first-run setup gate
// baked into `agnt run`. It launches a coding agent (default: claude) in
// one-time SETUP MODE — the agent configures the project and writes
// `.agnt.kdl` — then exits without relaunching. On success it records a
// permanent first-run marker so a subsequent `agnt run` skips the setup nudge.
//
// Use `agnt init` when you want to configure a project up front; use
// `agnt run <agent>` for the normal flow (which auto-runs setup once on a
// project that has no `.agnt.kdl`, then relaunches into a coding session).
var initCmd = &cobra.Command{
	Use:   "init [agent]",
	Short: "Configure agnt for this project (setup only, no relaunch)",
	Long: `Configure agnt for the current project.

init launches a coding agent (default: claude) in one-time SETUP MODE: the
agent detects the project type, registers dev-server scripts and proxies, and
writes a .agnt.kdl. Unlike "agnt run", it does NOT relaunch into a coding
session afterward — it sets the project up and exits.

On success a permanent first-run marker is recorded so a later "agnt run"
skips the setup nudge.

Examples:
  agnt init            # configure with claude
  agnt init gemini     # configure with gemini
  agnt init claude --dangerously-skip-permissions`,
	DisableFlagParsing: true,
	Run: func(cmd *cobra.Command, args []string) {
		// setupOnlyMode makes the shared firstRunOrCoding dispatcher run a
		// single setup phase (no first-run gate, no relaunch).
		setupOnlyMode = true
		if len(args) == 0 {
			args = []string{"claude"}
		}
		runCommand(cmd, args)
	},
}
