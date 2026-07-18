package incident

import (
	"testing"
	"time"
)

func makeProcessEntry(fp, processID string, sev Severity, lastSeen time.Time) *InboxEntry {
	ev := NewIncidentEvent(SourceProcessAlert, sev, "go", "boom "+fp, Context{ProcessID: processID}, nil)
	return &InboxEntry{
		Fingerprint: fp,
		FirstSeenAt: lastSeen,
		LastSeenAt:  lastSeen,
		Count:       1,
		Sample:      &ev,
		Severity:    sev,
	}
}

func TestInbox_ClearProcessBefore(t *testing.T) {
	t.Parallel()
	inbox := NewInbox("sess")
	base := time.Now()

	inbox.Ingest(makeProcessEntry("fp-old", "web", SeverityError, base.Add(-time.Minute)))
	inbox.Ingest(makeProcessEntry("fp-warn-old", "web", SeverityWarning, base.Add(-time.Minute)))
	inbox.Ingest(makeProcessEntry("fp-other", "api", SeverityError, base.Add(-time.Minute)))
	inbox.Ingest(makeProcessEntry("fp-recurred", "web", SeverityError, base.Add(time.Second)))
	// No-process entry (e.g. browser JS) must never be swept by a process clear.
	inbox.Ingest(makeEntry("fp-browser", SeverityError))

	removed := inbox.ClearProcessBefore("web", base)
	if removed != 2 {
		t.Fatalf("removed=%d, want 2 (old error + old warning)", removed)
	}
	if inbox.FindByFingerprint("fp-old") != nil {
		t.Error("fp-old should be cleared")
	}
	if inbox.FindByFingerprint("fp-warn-old") != nil {
		t.Error("fp-warn-old should be cleared")
	}
	for _, keep := range []string{"fp-other", "fp-recurred", "fp-browser"} {
		if inbox.FindByFingerprint(keep) == nil {
			t.Errorf("%s should survive the clear", keep)
		}
	}
	if got := inbox.ClearProcessBefore("", base); got != 0 {
		t.Errorf("empty process id must be a no-op, cleared %d", got)
	}
}

func TestBus_ClearSessionBefore_OnlyTargetSession(t *testing.T) {
	t.Parallel()
	bus := NewMPSCBus(nil)
	defer bus.Close()
	bus.AddSession("sess-a", nil, nil, nil)
	bus.AddSession("sess-b", nil, nil, nil)

	ev := NewIncidentEvent(SourceBrowserJS, SeverityError, "TypeError", "boom in both", Context{}, nil)
	bus.Fire(&ev)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && bus.FindFingerprintSession("sess-b", ev.Fingerprint) == nil {
		time.Sleep(5 * time.Millisecond)
	}

	bus.ClearSessionBefore("sess-a", time.Now())

	for time.Now().Before(deadline) {
		if bus.FindFingerprintSession("sess-a", ev.Fingerprint) == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if bus.FindFingerprintSession("sess-a", ev.Fingerprint) != nil {
		t.Error("sess-a inbox should be swept")
	}
	if bus.FindFingerprintSession("sess-b", ev.Fingerprint) == nil {
		t.Error("sess-b inbox must be untouched by sess-a's clear")
	}
}

func TestBus_ClearProcessBefore_FIFOWithEvents(t *testing.T) {
	t.Parallel()
	bus := NewMPSCBus(nil)
	defer bus.Close()
	bus.AddSession("sess", nil, nil, nil)

	before := NewIncidentEvent(SourceProcessAlert, SeverityError, "go", "stale compile error", Context{ProcessID: "web"}, nil)
	before.ReceivedAt = time.Now().Add(-time.Minute)
	bus.Fire(&before)

	// The clear rides the same inbound channel: it must land AFTER the stale
	// event (clearing it) and BEFORE the fresh event fired next.
	bus.ClearProcessBefore("web", time.Now())

	after := NewIncidentEvent(SourceProcessAlert, SeverityError, "go", "fresh error after build", Context{ProcessID: "web"}, nil)
	bus.Fire(&after)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if bus.FindFingerprintSession("sess", after.Fingerprint) != nil &&
			bus.FindFingerprintSession("sess", before.Fingerprint) == nil {
			return // exactly the desired end state
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("stale cleared=%v fresh present=%v; want true/true",
		bus.FindFingerprintSession("sess", before.Fingerprint) == nil,
		bus.FindFingerprintSession("sess", after.Fingerprint) != nil)
}
