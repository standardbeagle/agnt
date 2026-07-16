package incident

import (
	"strings"
	"testing"
	"time"
)

func TestBusDoesNotPublishPayloadRefBeforeBlobIsReadable(t *testing.T) {
	bus := NewMPSCBus(nil)
	defer bus.Close()
	bus.AddSession("session", nil, nil, nil)
	pl := bus.getSessionPipeline("session")
	deltas, cancel := pl.inbox.Subscribe()
	defer cancel()
	pl.blobs.pauseDrain()

	fullText := strings.Repeat("delayed-drain-payload-", 100)
	bus.Publish(NewIncidentEvent(SourceBrowserJS, SeverityError, "TypeError", fullText,
		Context{SessionID: "session"}, nil))

	// Wait until dispatch has enqueued the acknowledged write. The worker has
	// already confirmed it is parked, so the request cannot be committed yet.
	select {
	case <-pl.blobs.pausedWriteQueued():
	case <-time.After(time.Second):
		t.Fatal("dispatch never reached delayed blob write")
	}
	if entries, _ := bus.QuerySession("session", QueryFilter{}); len(entries) != 0 {
		t.Fatalf("inbox exposed %d entry before blob commit", len(entries))
	}

	pl.blobs.resumeDrain()
	select {
	case <-deltas:
	case <-time.After(time.Second):
		t.Fatal("committed incident did not become visible")
	}

	entries, _ := bus.QuerySession("session", QueryFilter{})
	if len(entries) != 1 || entries[0].Sample == nil || entries[0].Sample.PayloadRef == nil {
		t.Fatalf("expected one incident with payload ref, got %#v", entries)
	}
	payload, _, err := bus.ReadSessionBlob("session", entries[0].Sample.PayloadRef.Hash)
	if err != nil {
		t.Fatalf("read committed blob: %v", err)
	}
	if string(payload) != fullText {
		t.Fatal("committed blob payload mismatch")
	}
}
