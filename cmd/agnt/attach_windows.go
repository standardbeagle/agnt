//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/standardbeagle/agnt/internal/daemonclient"
	"golang.org/x/term"
)

func init() {
	runAttachTerminal = runAttachTerminalWindows
}

func runAttachTerminalWindows(client *daemonclient.Client, sessionID string, detachChord []byte) error {
	fd := int(os.Stdin.Fd())
	return runPreparedConsole(func() bool { return term.IsTerminal(fd) }, func() (func(), error) {
		consoleState, err := enterConsoleRawMode()
		if err != nil {
			return nil, fmt.Errorf("agnt attach: failed to enter Windows console raw mode: %w", err)
		}
		var restoreOnce sync.Once
		return func() { restoreOnce.Do(consoleState.Restore) }, nil
	}, func(restore func()) error {
		input, err := openWindowsAttachInput()
		if err != nil {
			return fmt.Errorf("agnt attach: %w", err)
		}
		defer input.Close()
		if attachRawModeEntered != nil {
			attachRawModeEntered()
		}
		return runAttachedSession(client, sessionID, detachChord, input, restore, input.Interrupt, attachWatchResizeWindows)
	})
}

// attachWatchResizeWindows polls because Windows has no SIGWINCH. Cancellation
// owns the ticker and the returned stop joins the worker, so EOF/detach cannot
// leave a resize goroutine behind.
func attachWatchResizeWindows(ctx context.Context, onResize func(cols, rows int)) func() {
	stopped := pollAttachResize(ctx, 250*time.Millisecond,
		func() (int, int, error) { return term.GetSize(int(os.Stdin.Fd())) }, onResize)
	return func() { <-stopped }
}
