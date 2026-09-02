package main

import (
	"bytes"
	"testing"

	"github.com/standardbeagle/agnt/internal/overlay"
)

// The default sink is a plain stderr line writer; a session swaps in its own
// sink and the returned restore puts the previous one back, so a message
// after teardown never lands on a torn-down overlay.
func TestNotifyUser_SwapAndRestore(t *testing.T) {
	var before bytes.Buffer
	restoreDefault := setUserNotifier(overlay.NewLineNotifier(&before, false))
	defer restoreDefault()

	notifyUser(overlay.LevelInfo, "hello %d", 1)
	if before.String() != "agnt: hello 1\n" {
		t.Fatalf("line notifier output = %q", before.String())
	}

	rec := &recNotifier{}
	restore := setUserNotifier(rec)
	notifyUserID("k", overlay.LevelWarn, "in session")
	if len(rec.got) != 1 || rec.got[0].ID != "k" || rec.got[0].Level != overlay.LevelWarn {
		t.Fatalf("session notifier did not receive the message: %+v", rec.got)
	}
	restore()

	notifyUser(overlay.LevelError, "after")
	if len(rec.got) != 1 {
		t.Fatalf("message after restore reached the session sink: %+v", rec.got)
	}
	if before.String() != "agnt: hello 1\nagnt: error: after\n" {
		t.Fatalf("restored line notifier output = %q", before.String())
	}
}
