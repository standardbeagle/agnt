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
	pl.blobs.pauseDrain()

	fullText := strings.Repeat("delayed-drain-payload-", 100)
	bus.Publish(NewIncidentEvent(SourceBrowserJS, SeverityError, "TypeError", fullText,
		Context{SessionID: "session"}, nil))

	// Wait until dispatch has enqueued the acknowledged write. The paused drain
	// makes this state deterministic; no scheduler-duration assumption is used.
	queuedDeadline := time.Now().Add(time.Second)
	for len(pl.blobs.writeCh) == 0 && time.Now().Before(queuedDeadline) {
		time.Sleep(time.Millisecond)
	}
	if len(pl.blobs.writeCh) == 0 {
		t.Fatal("dispatch never reached delayed blob write")
	}
	if entries, _ := bus.QuerySession("session", QueryFilter{}); len(entries) != 0 {
		t.Fatalf("inbox exposed %d entry before blob commit", len(entries))
	}

	pl.blobs.resumeDrain()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		entries, _ := bus.QuerySession("session", QueryFilter{})
		if len(entries) == 1 && entries[0].Sample != nil && entries[0].Sample.PayloadRef != nil {
			payload, _, err := bus.ReadSessionBlob("session", entries[0].Sample.PayloadRef.Hash)
			if err == nil && string(payload) == fullText {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("committed blob and incident did not become visible together")
}
