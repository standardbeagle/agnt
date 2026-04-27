package incident

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── QuerySession ──────────────────────────────────────────────────────────────

func TestBus_QuerySession_NoPipeline_ReturnsEmpty(t *testing.T) {
	t.Parallel()
	bus := NewMPSCBus(nil)
	defer bus.Close()

	entries, stats := bus.QuerySession("nonexistent", QueryFilter{})
	assert.Nil(t, entries)
	assert.Equal(t, Stats{}, stats)
}

func TestBus_QuerySession_EmptyInbox(t *testing.T) {
	t.Parallel()
	bus := NewMPSCBus(nil)
	defer bus.Close()

	bus.AddSession("s1", nil, nil, nil)
	entries, stats := bus.QuerySession("s1", QueryFilter{})
	assert.Empty(t, entries)
	assert.Equal(t, 0, stats.Error)
}

func TestBus_QuerySession_ReturnsPublishedEvents(t *testing.T) {
	t.Parallel()
	bus := NewMPSCBus(nil)
	defer bus.Close()

	bus.AddSession("s2", nil, nil, nil)
	pl := bus.getSessionPipeline("s2")
	require.NotNil(t, pl)
	deltaCh, cancelSub := pl.inbox.Subscribe()
	defer cancelSub()

	ev := NewIncidentEvent(SourceBrowserJS, SeverityError, "TypeError", "test query", Context{}, nil)
	bus.Publish(ev)

	// Wait for event to land in inbox.
	select {
	case <-deltaCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for inbox delta")
	}

	entries, stats := bus.QuerySession("s2", QueryFilter{})
	require.Len(t, entries, 1)
	assert.Equal(t, ev.Fingerprint, entries[0].Fingerprint)
	assert.Equal(t, SeverityError, entries[0].Severity)
	assert.Equal(t, 1, stats.Error)
}

func TestBus_QuerySession_SeverityFilter(t *testing.T) {
	t.Parallel()
	bus := NewMPSCBus(nil)
	defer bus.Close()

	bus.AddSession("s3", nil, nil, nil)
	pl := bus.getSessionPipeline("s3")
	require.NotNil(t, pl)
	deltaCh, cancelSub := pl.inbox.Subscribe()
	defer cancelSub()

	errEv := NewIncidentEvent(SourceBrowserJS, SeverityError, "Err", "error event", Context{}, nil)
	warnEv := NewIncidentEvent(SourceHTTP4xx, SeverityWarning, "Warn", "warn event", Context{}, nil)
	bus.Publish(errEv)
	bus.Publish(warnEv)

	for i := 0; i < 2; i++ {
		select {
		case <-deltaCh:
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("timed out waiting for delta %d", i)
		}
	}

	// Filter to errors only.
	entries, _ := bus.QuerySession("s3", QueryFilter{Severities: []Severity{SeverityError}})
	require.Len(t, entries, 1)
	assert.Equal(t, SeverityError, entries[0].Severity)

	// Filter to warnings only.
	entries, _ = bus.QuerySession("s3", QueryFilter{Severities: []Severity{SeverityWarning}})
	require.Len(t, entries, 1)
	assert.Equal(t, SeverityWarning, entries[0].Severity)
}

func TestBus_QuerySession_LimitApplied(t *testing.T) {
	t.Parallel()
	bus := NewMPSCBus(nil)
	defer bus.Close()

	bus.AddSession("s4", nil, nil, nil)
	pl := bus.getSessionPipeline("s4")
	require.NotNil(t, pl)
	deltaCh, cancelSub := pl.inbox.Subscribe()
	defer cancelSub()

	// Publish 5 distinct events.
	categories := []string{"A", "B", "C", "D", "E"}
	for _, cat := range categories {
		ev := NewIncidentEvent(SourceBrowserJS, SeverityWarning, cat, cat+" msg", Context{}, nil)
		bus.Publish(ev)
	}
	for i := range categories {
		select {
		case <-deltaCh:
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("timed out waiting for delta %d", i)
		}
	}

	entries, _ := bus.QuerySession("s4", QueryFilter{Limit: 3})
	assert.Len(t, entries, 3)
}

// ── MarkReadSession ───────────────────────────────────────────────────────────

func TestBus_MarkReadSession_NoPipeline_NoOp(t *testing.T) {
	t.Parallel()
	bus := NewMPSCBus(nil)
	defer bus.Close()
	// Must not panic.
	bus.MarkReadSession("nonexistent", []string{"fp1"}, true)
}

func TestBus_MarkReadSession_MarksEntriesRead(t *testing.T) {
	t.Parallel()
	bus := NewMPSCBus(nil)
	defer bus.Close()

	bus.AddSession("s5", nil, nil, nil)
	pl := bus.getSessionPipeline("s5")
	require.NotNil(t, pl)
	deltaCh, cancelSub := pl.inbox.Subscribe()
	defer cancelSub()

	ev := NewIncidentEvent(SourceBrowserJS, SeverityError, "Type", "mark read test", Context{}, nil)
	bus.Publish(ev)
	select {
	case <-deltaCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out")
	}

	// Verify unread before mark.
	entries, _ := bus.QuerySession("s5", QueryFilter{UnreadOnly: true})
	require.Len(t, entries, 1)

	bus.MarkReadSession("s5", []string{ev.Fingerprint}, false)

	// Unread query returns nothing after mark.
	entries, _ = bus.QuerySession("s5", QueryFilter{UnreadOnly: true})
	assert.Empty(t, entries)
}
