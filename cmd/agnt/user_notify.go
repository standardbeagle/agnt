package main

import (
	"fmt"
	"os"
	"sync/atomic"

	"github.com/standardbeagle/agnt/internal/overlay"
	"github.com/standardbeagle/agnt/internal/selflog"
)

// userNotifier is the single sink for user-facing messages in this binary.
// Before a PTY session it writes plain lines to stderr; while the terminal
// overlay runs it is the overlay's notification stack (cursor-safe, TTL,
// dedup); in raw passthrough it writes CRLF lines; after teardown it is
// stderr again. Writing to stdout/stderr directly during a session tears
// the screen, so nothing else in cmd/agnt should print to the user.
var userNotifier atomic.Pointer[notifierBox]

type notifierBox struct{ n overlay.Notifier }

func init() {
	userNotifier.Store(&notifierBox{n: overlay.NewLineNotifier(os.Stderr, false)})
}

// setUserNotifier swaps the sink and returns a func that restores the
// previous one. Sessions pair the two around their lifetime.
func setUserNotifier(n overlay.Notifier) (restore func()) {
	prev := userNotifier.Swap(&notifierBox{n: n})
	return func() { userNotifier.Store(prev) }
}

// notifyUser routes a formatted message to the current sink.
func notifyUser(level overlay.Level, format string, args ...any) {
	userNotifier.Load().n.Notify(overlay.Notification{Level: level, Text: fmt.Sprintf(format, args...)})
}

// notifyUserID is notifyUser with a dedup key: a repeat while the first is
// still visible bumps a counter instead of stacking.
func notifyUserID(id string, level overlay.Level, format string, args ...any) {
	userNotifier.Load().n.Notify(overlay.Notification{ID: id, Level: level, Text: fmt.Sprintf(format, args...)})
}

// reportAgentWriteError surfaces the first failed write to the agent's
// terminal of a streak: what was typed or injected did not arrive.
func reportAgentWriteError(source string, err error) {
	selflog.Record("agent-tty", "%s write to the agent failed: %v", source, err)
	notifyUserID("agent-tty-write", overlay.LevelError, "Input not delivered to the agent (%s): %v", source, err)
}
