//go:build windows

package main

import (
	"fmt"

	"github.com/standardbeagle/agnt/internal/daemon"
)

func init() {
	runAttachTerminal = runAttachTerminalWindows
}

// runAttachTerminalWindows is a stub: native Windows local attach (a full
// ConPTY relay) is deferred to a later task — `agnt ssh` (task 04) covers
// Windows clients attaching to a Unix remote in the meantime. This returns a
// clear, loud error rather than silently no-oping, per the daemon
// architecture's Silent Failure Prohibition.
func runAttachTerminalWindows(client *daemon.Client, sessionID string, detachChord []byte) error {
	return fmt.Errorf("agnt attach: native Windows local attach is not yet supported (deferred; use `agnt ssh` to attach to a Unix remote, or attach from WSL)")
}
