//go:build windows

// Windows support for `agnt ssh` (local named-pipe forwarding of the
// session-host PTY, port forwards, and daemon socket — see ssh.go's unix
// implementation) is an explicit, deferred gap: it is a substantial native
// feature (Windows has no unix-domain-socket-style local forwarding
// primitive; it needs named pipes end to end) that is not worth building
// until Windows-remote is prioritized. Per
// .claude/rules/daemon-architecture.md's Silent Failure Prohibition, the
// command must still exist and fail loud with an actionable message —
// never "unknown command" — rather than silently vanish from the Windows
// build. This file registers the same `ssh` command name/parent as the
// unix build (cmd/agnt/ssh.go) so cobra recognizes it on both platforms.
package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var sshCmd = &cobra.Command{
	Use:   "ssh <host>[:path]",
	Short: "Open a session-host PTY on a remote agnt daemon over SSH (not yet supported on Windows)",
	Args:  cobra.ExactArgs(1),
	RunE:  runSSH,
}

func init() {
	rootCmd.AddCommand(sshCmd)
}

// runSSH is a stub: native Windows local named-pipe forwarding for `agnt
// ssh` is deferred until Windows-remote is prioritized (see task 06a of the
// remote-ssh epic). This returns a clear, actionable error instead of
// silently no-oping.
func runSSH(cmd *cobra.Command, args []string) error {
	return fmt.Errorf("agnt ssh is not yet supported on Windows (local named-pipe forwarding unimplemented); track: epic 01KWMARXTVWKC33EPHZZJ43JT9 / task 06a. Use WSL as a workaround")
}
