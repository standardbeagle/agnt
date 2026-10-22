package daemonclient

import (
	"errors"
	"os"
	"testing"

	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

// A hook deadline that fires after part of the ack was read surfaces as a
// PartialFrameError wrapping the timeout. It must still map to ErrHookDeadline
// so the hook CLI exits silently instead of printing an error to Claude.
func TestMapHookSendError_WrappedTimeoutIsDeadline(t *testing.T) {
	wrapped := &hubproto.PartialFrameError{Err: os.ErrDeadlineExceeded}
	if !errors.Is(mapHookSendError(wrapped), ErrHookDeadline) {
		t.Fatalf("wrapped timeout mapped to %v, want ErrHookDeadline", mapHookSendError(wrapped))
	}
}
