package incident

import (
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/proxy"
)

// TestDeadBandMessageTailIsRetrievable pins the invariant that no byte of an
// incident message is discarded unless it is retrievable via detail:"full"
// (a committed blob). The regression it guards is the 200<len<=1024 "dead
// band": Summary caps at 200 bytes but the blob threshold used to be 1024, so a
// message in that band had its tail truncated with no blob to hydrate from.
//
// The shape is the ordinary React one: framework/vendor frames precede the
// application frame, so the 200-byte Summary cap cuts exactly the app frame the
// developer needs. Driving the real FromFrontendError adapter through the bus,
// the full message — including that app frame — must be reachable from the
// session blob store.
func TestDeadBandMessageTailIsRetrievable(t *testing.T) {
	fe := proxy.FrontendError{
		Message: "TypeError: Cannot read properties of undefined (reading 'items')",
		Stack: strings.Join([]string{
			"    at invokeGuardedCallback (node_modules/react-dom/cjs/react-dom.development.js:4277:31)",
			"    at beginWork$1 (node_modules/react-dom/cjs/react-dom.development.js:27451:7)",
			"    at performUnitOfWork (node_modules/react-dom/cjs/react-dom.development.js:27649:12)",
			"    at submitOrder (src/checkout.js:42:15)",
		}, "\n"),
		URL: "http://localhost:5173/checkout",
	}
	fullMsg := fe.Message + "\n" + fe.Stack

	// The message must actually land in the dead band, or this test proves
	// nothing. This is the load-bearing premise.
	if len(fullMsg) <= maxSummaryBytes || len(fullMsg) > 1024 {
		t.Fatalf("premise broken: message len %d is not in the 200<len<=1024 dead band", len(fullMsg))
	}
	// And the app frame must fall past the 200-byte Summary cap — the whole point
	// of the React shape (vendor frames first).
	appFrame := "src/checkout.js:42:15"
	if idx := strings.Index(fullMsg, appFrame); idx <= maxSummaryBytes {
		t.Fatalf("premise broken: app frame at byte %d is within the Summary cap; it must be past %d", idx, maxSummaryBytes)
	}

	bus := NewMPSCBus(nil)
	defer bus.Close()
	bus.AddSession("session", nil, nil, nil)
	pl := bus.getSessionPipeline("session")
	deltas, cancel := pl.inbox.Subscribe()
	defer cancel()

	ev := FromFrontendError(fe, "dev", "frame-1")
	// Production stamps the owning session (stampIncidentOwner) before Publish;
	// mirror that so the event routes to our session pipeline.
	ev.Ctx.SessionID = "session"
	bus.Publish(ev)

	select {
	case <-deltas:
	case <-time.After(2 * time.Second):
		t.Fatal("incident never became visible in the inbox")
	}

	entries, _ := bus.QuerySession("session", QueryFilter{})
	if len(entries) != 1 || entries[0].Sample == nil {
		t.Fatalf("expected one incident sample, got %#v", entries)
	}
	sample := entries[0].Sample

	// Summary alone must NOT carry the app frame — it was truncated by the cap.
	// This is what makes the blob load-bearing rather than incidental.
	if strings.Contains(sample.Summary, appFrame) {
		t.Fatalf("premise broken: Summary already contains the app frame %q, cap did not truncate it", appFrame)
	}

	// The invariant: the tail the Summary dropped must be retrievable via the
	// committed blob (detail:"full").
	if sample.PayloadRef == nil {
		t.Fatalf("dead-band regression: message len %d truncated to Summary with NO blob to hydrate the tail", len(fullMsg))
	}
	payload, _, err := bus.ReadSessionBlob("session", sample.PayloadRef.Hash)
	if err != nil {
		t.Fatalf("read committed blob: %v", err)
	}
	if string(payload) != fullMsg {
		t.Fatalf("blob payload mismatch: got %d bytes, want the full %d-byte message", len(payload), len(fullMsg))
	}
	if !strings.Contains(string(payload), appFrame) {
		t.Fatalf("hydrated payload is missing the app frame %q", appFrame)
	}
}
