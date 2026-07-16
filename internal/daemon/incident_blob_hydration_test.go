package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/protocol"
)

func TestIncidentDetailFullHydratesSessionOwnedPayload(t *testing.T) {
	bus := incident.NewMPSCBus(nil)
	defer bus.Close()
	bus.AddSession("owner", nil, nil, nil)
	bus.AddSession("other", nil, nil, nil)

	fullText := strings.Repeat("full-production-detail-", 100)
	event := incident.NewIncidentEvent(incident.SourceBrowserJS, incident.SeverityError,
		"TypeError", fullText, incident.Context{SessionID: "owner"}, nil)
	bus.Publish(event)

	var entries []incident.InboxEntry
	var refHash string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		entries, _ = bus.QuerySession("owner", incident.QueryFilter{})
		if len(entries) == 1 && entries[0].Sample != nil && entries[0].Sample.PayloadRef != nil {
			refHash = entries[0].Sample.PayloadRef.Hash
			if payload, _, err := bus.ReadSessionBlob("owner", refHash); err == nil && string(payload) == fullText {
				break
			}
		}
		time.Sleep(time.Millisecond)
	}
	if refHash == "" {
		t.Fatal("production event never received a session-owned PayloadRef")
	}

	hydrate := func(hash string) ([]byte, error) {
		payload, _, err := bus.ReadSessionBlob("owner", hash)
		return payload, err
	}
	summary := buildIncidentQueryResultWithHydrator(entries, incident.Stats{}, protocol.IncidentQueryFilter{Detail: "summary"}, hydrate)
	full := buildIncidentQueryResultWithHydrator(entries, incident.Stats{}, protocol.IncidentQueryFilter{Detail: "full"}, hydrate)
	if summary.Incidents[0].Payload != nil {
		t.Fatal("summary detail unexpectedly included payload")
	}
	if full.Incidents[0].Payload == nil || *full.Incidents[0].Payload != fullText {
		t.Fatal("full detail did not hydrate the original payload")
	}
	if _, _, err := bus.ReadSessionBlob("other", refHash); err == nil {
		t.Fatal("another session read the owner's content-addressed blob")
	}
	bus.RemoveSession("owner")
	if _, _, err := bus.ReadSessionBlob("owner", refHash); err == nil {
		t.Fatal("removed session retained readable blob state")
	}
}
