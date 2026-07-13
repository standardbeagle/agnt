package daemon

import (
	"sync/atomic"
	"testing"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/assert"
)

// stubProxyBroadcaster is a test double for ProxyBroadcaster.
type stubProxyBroadcaster struct {
	calls       atomic.Int32
	diagnostics atomic.Int32
	// lastArgs records the last call's arguments for assertion.
	lastType    atomic.Value // string
	lastTitle   atomic.Value // string
	lastMessage atomic.Value // string
}

func (s *stubProxyBroadcaster) BroadcastAlertToast(toastType, title, message string) {
	s.calls.Add(1)
	s.lastType.Store(toastType)
	s.lastTitle.Store(title)
	s.lastMessage.Store(message)
}
func (s *stubProxyBroadcaster) DeliverDeveloperEvent(event protocol.DeveloperEvent) {
	s.BroadcastAlertToast(event.Severity, event.Title, event.Message)
	s.diagnostics.Add(1)
}

func TestEventHubDeveloperEventRoutesAreIndependentAndExactOnce(t *testing.T) {
	hub := NewEventHub()
	pb := &stubProxyBroadcaster{}
	hub.SetProxyBroadcaster(pb)
	event := protocol.DeveloperEvent{Kind: "file_arrived", ProxyID: "web", ProjectPath: "/project", Severity: "info", Title: "File arrived", Message: "fixture.png"}

	hub.DeliverDeveloperEvent(event)

	assert.Equal(t, int32(1), pb.calls.Load())
	assert.Equal(t, int32(1), pb.diagnostics.Load())
	// The paired operation emits each surface once without mirroring.
	assert.Equal(t, "fixture.png", pb.lastMessage.Load())
}

// TestEventHub_Deliver_BroadcastsToProxy verifies that an error-severity
// Deliver call reaches the registered ProxyBroadcaster.
func TestEventHub_Deliver_BroadcastsToProxy(t *testing.T) {
	t.Parallel()

	hub := NewEventHub()
	pb := &stubProxyBroadcaster{}
	hub.SetProxyBroadcaster(pb)

	hub.Deliver("error", "panic: runtime error: index out of range [proc=api]")

	assert.Equal(t, int32(1), pb.calls.Load(), "BroadcastAlertToast must be called once")
	assert.Equal(t, "error", pb.lastType.Load().(string))
	assert.Contains(t, pb.lastTitle.Load().(string), "Process Error")
	assert.Contains(t, pb.lastMessage.Load().(string), "panic: runtime error")
}

// TestEventHub_Deliver_NoProxyBroadcast_WhenNoProxy verifies that Deliver
// does not panic when no ProxyBroadcaster is registered (nil case).
func TestEventHub_Deliver_NoProxyBroadcast_WhenNoProxy(t *testing.T) {
	t.Parallel()

	hub := NewEventHub()
	// No proxy broadcaster set — must not panic.
	assert.NotPanics(t, func() {
		hub.Deliver("error", "panic: nil pointer dereference")
	})
}

// TestEventHub_Deliver_SkipsInfo_ForProxyBroadcast verifies that info-level
// alerts do NOT reach the ProxyBroadcaster.
func TestEventHub_Deliver_SkipsInfo_ForProxyBroadcast(t *testing.T) {
	t.Parallel()

	hub := NewEventHub()
	pb := &stubProxyBroadcaster{}
	hub.SetProxyBroadcaster(pb)

	hub.Deliver("info", "some informational message")

	assert.Equal(t, int32(0), pb.calls.Load(), "info-level alert must NOT reach ProxyBroadcaster")
}

// TestEventHub_Deliver_BroadcastsWarning verifies that warning-severity
// alerts also reach the ProxyBroadcaster (>= warning threshold).
func TestEventHub_Deliver_BroadcastsWarning(t *testing.T) {
	t.Parallel()

	hub := NewEventHub()
	pb := &stubProxyBroadcaster{}
	hub.SetProxyBroadcaster(pb)

	hub.Deliver("warning", "build warning detected")

	assert.Equal(t, int32(1), pb.calls.Load(), "warning-level alert must reach ProxyBroadcaster")
	assert.Equal(t, "warning", pb.lastType.Load().(string))
}
