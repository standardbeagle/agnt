//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/standardbeagle/agnt/internal/daemon"
	"golang.org/x/term"
)

func init() {
	runAttachTerminal = runAttachTerminalWindows
}

func runAttachTerminalWindows(client *daemon.Client, sessionID string, detachChord []byte) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return fmt.Errorf("agnt attach: stdin is not a Windows console; redirected input is unsupported")
	}
	consoleState, err := enterConsoleRawMode()
	if err != nil {
		return fmt.Errorf("agnt attach: failed to enter Windows console raw mode: %w", err)
	}
	var restoreOnce sync.Once
	restore := func() { restoreOnce.Do(consoleState.Restore) }
	defer restore()
	if attachRawModeEntered != nil {
		attachRawModeEntered()
	}
	return runAttachedSession(client, sessionID, detachChord, restore, attachWatchResizeWindows)
}

// attachWatchResizeWindows polls because Windows has no SIGWINCH. Cancellation
// owns the ticker and the returned stop joins the worker, so EOF/detach cannot
// leave a resize goroutine behind.
func attachWatchResizeWindows(ctx context.Context, onResize func(cols, rows int)) func() {
	stopped := pollAttachResize(ctx, 250*time.Millisecond,
		func() (int, int, error) { return term.GetSize(int(os.Stdin.Fd())) }, onResize)
	return func() { <-stopped }
}
