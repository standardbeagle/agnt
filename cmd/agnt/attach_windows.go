//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/standardbeagle/agnt/internal/daemon"
	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

func init() {
	runAttachTerminal = runAttachTerminalWindows
}

func runAttachTerminalWindows(client *daemon.Client, sessionID string, detachChord []byte) error {
	fd := int(os.Stdin.Fd())
	return runPreparedConsole(func() bool { return term.IsTerminal(fd) }, func() (func(), error) {
		consoleState, err := enterConsoleRawMode()
		if err != nil {
			return nil, fmt.Errorf("agnt attach: failed to enter Windows console raw mode: %w", err)
		}
		var restoreOnce sync.Once
		return func() { restoreOnce.Do(consoleState.Restore) }, nil
	}, func(restore func()) error {
		if attachRawModeEntered != nil {
			attachRawModeEntered()
		}
		interruptInput := func() { _ = windows.CancelIoEx(windows.Handle(os.Stdin.Fd()), nil) }
		return runAttachedSession(client, sessionID, detachChord, restore, interruptInput, attachWatchResizeWindows)
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
