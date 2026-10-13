package incident

import (
	"strings"
	"testing"
	"time"
)

// A repeat of an incident that is still unread changes nothing a ping reports,
// so it must not schedule one: an error recurring every few seconds re-sent the
// identical digest for as long as it kept happening. Once the agent reads the
// entry, the next recurrence is news again and does ping.
func TestPing_UnreadMergeDoesNotSchedule(t *testing.T) {
	t.Parallel()
	// The emitter is subscribed to an inbox that never ingests, so the only
	// deltas it sees are the ones this test hands to handleDelta.
	emitted := 0
	pe := NewPingEmitter(NewInbox("idle"), PingConfig{MCPNotifications: true},
		NewFlowController(DefaultBucketConfigs),
		func(string, PingPayload) error { emitted++; return nil }, nil, nil)
	defer pe.Stop()
	scheduled := 0
	pe.coalesce.afterFunc = func(time.Duration, func()) coalesceTimer {
		scheduled++
		return fakeTimer{}
	}

	src := NewInbox("src")
	ingest := func(sev Severity) InboxDelta {
		ev := NewIncidentEvent(SourceBrowserJS, sev, "TypeError", "boom", Context{}, nil)
		ev.Fingerprint = "fp-poll"
		return src.Ingest(&InboxEntry{Fingerprint: ev.Fingerprint, FirstSeenAt: ev.ReceivedAt,
			LastSeenAt: ev.ReceivedAt, Count: 1, Sample: &ev, Severity: sev})
	}

	pe.handleDelta(ingest(SeverityWarning))
	if scheduled != 1 {
		t.Fatalf("first occurrence scheduled %d pings, want 1", scheduled)
	}
	for i := 0; i < 20; i++ {
		d := ingest(SeverityWarning)
		if d.IsNew || !d.WasUnread {
			t.Fatalf("setup: repeat %d should be an unread merge, got %+v", i, d)
		}
		pe.handleDelta(d)
	}
	if scheduled != 1 {
		t.Fatalf("20 repeats of a still-unread incident scheduled %d pings, want 1", scheduled)
	}

	if emitted != 0 {
		t.Fatalf("setup: nothing should have emitted yet (timers are faked), got %d", emitted)
	}

	// Escalation on the same fingerprint is news even while unread: it emits
	// at once instead of waiting on the pending timer.
	pe.handleDelta(ingest(SeverityError))
	if emitted != 1 {
		t.Fatalf("escalation emitted %d pings, want 1 immediate", emitted)
	}

	// Read, then recur: unread stats change, so it pings again.
	src.MarkRead([]string{"fp-poll"}, true)
	d := ingest(SeverityError)
	if d.WasUnread {
		t.Fatal("setup: a recurrence after mark-read must report WasUnread=false")
	}
	pe.handleDelta(d)
	if scheduled != 2 {
		t.Fatalf("recurrence after read scheduled %d pings total, want 2", scheduled)
	}
}

// The PTY hint is typed into an agent's input; the call it names must be one
// get_incidents accepts. It used to say `since=cursor`, which the hub rejects.
func TestPing_PTYHintIsAnAcceptedCall(t *testing.T) {
	t.Parallel()
	line := ptyLine(PingPayload{Summary: PingStats{Error: 1, New: 1}})
	if strings.Contains(line, "since=cursor") {
		t.Errorf("PTY hint still names the rejected literal cursor: %q", line)
	}
	if !strings.Contains(line, "get_incidents") || !strings.Contains(line, "mark_read") {
		t.Errorf("PTY hint should name a get_incidents pull that advances the cursor: %q", line)
	}
	if strings.Count(line, "\n") != 1 || !strings.HasSuffix(line, "\n") {
		t.Errorf("PTY hint must stay a single line: %q", line)
	}
}
