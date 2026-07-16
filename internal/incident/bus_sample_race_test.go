package incident

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestBusDuplicateStormConcurrentQueryOwnsSampleSnapshot(t *testing.T) {
	bus := NewMPSCBus(nil)
	defer bus.Close()
	bus.AddSession("race-session", nil, nil, nil)

	stopQuery := make(chan struct{})
	var queryWG sync.WaitGroup
	for i := 0; i < 4; i++ {
		queryWG.Add(1)
		go func() {
			defer queryWG.Done()
			for {
				select {
				case <-stopQuery:
					return
				default:
				}
				entries, _ := bus.QuerySession("race-session", QueryFilter{})
				if len(entries) == 0 || entries[0].Sample == nil {
					continue
				}
				sample := entries[0].Sample
				_ = sample.Summary
				if nested, ok := sample.Remediation.PrimaryArgs["nested"].(map[string]any); ok {
					_ = nested["sequence"]
				}
			}
		}()
	}

	const duplicates = 2000
	for i := 0; i < duplicates; i++ {
		bus.Publish(IncidentEvent{
			ID:          fmt.Sprintf("event-%d", i),
			Fingerprint: "same-fingerprint",
			ReceivedAt:  time.Now(),
			Severity:    SeverityError,
			Summary:     fmt.Sprintf("duplicate-%d", i),
			Ctx:         Context{SessionID: "race-session"},
			Remediation: Remediation{PrimaryArgs: map[string]any{
				"nested": map[string]any{"sequence": i},
			}},
		})
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		entries, _ := bus.QuerySession("race-session", QueryFilter{})
		if len(entries) == 1 && entries[0].Count == duplicates {
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(stopQuery)
	queryWG.Wait()
	entries, _ := bus.QuerySession("race-session", QueryFilter{})
	if len(entries) != 1 {
		t.Fatalf("entries=%d, want one", len(entries))
	}
	if entries[0].Count != duplicates {
		t.Fatalf("count=%d, want %d", entries[0].Count, duplicates)
	}
}

func TestDedupSnapshotDeepCopiesMutableEventFields(t *testing.T) {
	dedup := NewDeduplicator(time.Minute)
	payload := &BlobRef{Hash: "original"}
	nested := map[string]any{"slice": []any{"original"}}
	event := IncidentEvent{
		Fingerprint: "deep-copy",
		PayloadRef:  payload,
		Remediation: Remediation{PrimaryArgs: map[string]any{"nested": nested}},
	}
	_, snapshot := dedup.Ingest("session", event)

	payload.Hash = "mutated"
	nested["slice"].([]any)[0] = "mutated"
	if snapshot.Last.PayloadRef.Hash != "original" {
		t.Fatalf("payload hash=%q, want detached original", snapshot.Last.PayloadRef.Hash)
	}
	got := snapshot.Last.Remediation.PrimaryArgs["nested"].(map[string]any)["slice"].([]any)[0]
	if got != "original" {
		t.Fatalf("nested value=%v, want detached original", got)
	}
}
