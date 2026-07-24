package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/daemonclient"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/hookrules"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/shims"
)

// shimCmd implements the shell-wrapper entry points. `shim exec` is what
// every generated script in <project>/.agnt/bin invokes; `shim watch` is
// the detached cleanup watcher spawned by the daemon. Both are hidden —
// users never call them directly.
var shimCmd = &cobra.Command{
	Use:    "shim",
	Hidden: true,
	Short:  "Shell shim internals (used by .agnt/bin wrappers)",
}

var shimExecCmd = &cobra.Command{
	Use:                "exec <command> [args...]",
	Hidden:             true,
	DisableFlagParsing: true,
	Args:               cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		os.Exit(runShimExec(args[0], args[1:]))
	},
}

var shimWatchSocket string

var shimWatchCmd = &cobra.Command{
	Use:    "watch",
	Hidden: true,
	Short:  "Watch daemon liveness and clean stale shim dirs",
	Run: func(cmd *cobra.Command, args []string) {
		os.Exit(runShimWatch(shimWatchSocket))
	},
}

func init() {
	shimWatchCmd.Flags().StringVar(&shimWatchSocket, "socket", "", "Daemon socket path to watch")
	shimCmd.AddCommand(shimExecCmd)
	shimCmd.AddCommand(shimWatchCmd)
	rootCmd.AddCommand(shimCmd)
}

// runShimExec routes one shimmed command. Exit-code contract:
//
//   - handled:   daemon's exit code (0 for reuse/ignore, build result for one-shots)
//   - blocked:   daemon's exit code (2)
//   - passthrough / any error: exec the real binary and propagate its code
//
// The function is fail-open at every step: an unreachable daemon, missing
// session, or transport error NEVER surfaces to the user as a shim failure.
func runShimExec(command string, args []string) int {
	projectPath := shimProjectPath()
	if projectPath == "" {
		return shimPassthrough(command, args)
	}

	socketPath := daemonclient.DefaultSocketPath()
	if !daemonclient.IsRunning(socketPath) {
		return shimPassthrough(command, args)
	}

	client := daemonclient.NewClient(daemonclient.WithSocketPath(socketPath), daemonclient.WithTimeout(daemonclient.ShimExecTimeout))
	if err := client.Connect(); err != nil {
		debug.Log("shim", "connect failed: %v (passthrough)", err)
		return shimPassthrough(command, args)
	}
	defer client.Close()

	cwd, _ := os.Getwd()
	resp, err := client.ShimExec(protocol.ShimExecRequest{
		ProjectPath: projectPath,
		SessionCode: os.Getenv("AGNT_SESSION_CODE"),
		Command:     command,
		Args:        args,
		Cwd:         cwd,
	})
	if err != nil || resp == nil {
		debug.Log("shim", "exec failed: %v (passthrough)", err)
		return shimPassthrough(command, args)
	}

	switch resp.Action {
	case "passthrough":
		return shimPassthrough(command, args)
	case "blocked":
		printShimFeedback(resp)
		if resp.ExitCode == 0 {
			return 2
		}
		return resp.ExitCode
	default: // "handled"
		printShimFeedback(resp)
		return resp.ExitCode
	}
}

// printShimFeedback renders the daemon's feedback: message + tool hint to
// stderr (so piped stdout stays clean), captured output to stdout.
func printShimFeedback(resp *protocol.ShimExecResponse) {
	if resp.Message != "" {
		fmt.Fprintln(os.Stderr, resp.Message)
	}
	if resp.ToolHint != "" {
		fmt.Fprintf(os.Stderr, ">>> you can do this directly using the agnt MCP tool >>> %s\n", resp.ToolHint)
	}
	if resp.Output != "" {
		fmt.Fprintln(os.Stdout, resp.Output)
	}
}

// shimProjectPath resolves the project root for this invocation. Empty
// means "not a managed context" → passthrough. A managed `agnt run` shell
// stamps AGNT_PROJECT_PATH; for other contexts we require a .agnt.kdl at
// or above cwd (same scope guard as the hook interceptor) so stray shim
// dirs outside managed shells stay inert.
func shimProjectPath() string {
	if p := os.Getenv("AGNT_PROJECT_PATH"); p != "" {
		return p
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	if !hookrules.ScopeGuardActive(cwd) {
		return ""
	}
	cfg, err := config.LoadAgntConfig(cwd)
	if err != nil || cfg == nil || !cfg.ShimsEnabled() {
		return ""
	}
	return cwd
}

// shimPassthrough execs the real binary, skipping shim dirs on PATH.
// Missing binary reproduces the shell's command-not-found (exit 127).
func shimPassthrough(command string, args []string) int {
	real := shims.ResolveRealBinary(command, os.Environ())
	if real == "" {
		fmt.Fprintf(os.Stderr, "%s: command not found\n", command)
		return 127
	}
	return execRealBinary(real, command, args)
}

// runShimWatch is the detached cleanup watcher. Poll cadence is 5s; a dead
// daemon gets a 30s grace window so the auto-restart path
// (ResilientClient.EnsureHubRunning) wins the race. Exits when the
// manifest is empty (graceful Stop already cleaned up) or after removing
// every registered bin dir itself.
func runShimWatch(socketPath string) int {
	if socketPath == "" {
		socketPath = daemonclient.DefaultSocketPath()
	}
	const (
		pollInterval = 5 * time.Second
		deadGrace    = 30 * time.Second
	)
	var deadSince time.Time
	for {
		alive := daemonclient.IsRunning(socketPath)
		if alive {
			deadSince = time.Time{}
		} else if deadSince.IsZero() {
			deadSince = time.Now()
		}

		m := shims.LoadManifest()
		if len(m.Projects) == 0 {
			// Nothing to guard: either graceful Stop cleaned up, or no
			// project ever installed shims. The daemon respawns a watcher
			// on the next SHIM REGISTER.
			return 0
		}
		if m.WatcherPID != 0 && m.WatcherPID != os.Getpid() {
			// Superseded by a newer watcher.
			return 0
		}

		if !deadSince.IsZero() && time.Since(deadSince) > deadGrace {
			// Final probe before tearing down: a daemon that came back
			// during the grace window resets the clock instead.
			if daemonclient.IsRunning(socketPath) {
				deadSince = time.Time{}
				continue
			}
			for projectPath := range m.Projects {
				shims.Remove(projectPath)
			}
			if err := shims.SaveManifest(&shims.Manifest{Projects: map[string]*shims.ManifestEntry{}}); err != nil {
				fmt.Fprintf(os.Stderr, "agnt shim watch: save manifest: %v\n", err)
				return 1
			}
			return 0
		}

		time.Sleep(pollInterval)
	}
}
