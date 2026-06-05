package daemon

import (
	"fmt"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// broadcastIncidentDigest must fan a synthetic incident_digest LogEntry out to
// STREAM-EVENTS subscribers (the cross-process transport for the unified inbox).
func TestBroadcastIncidentDigest_ReachesStreamSink(t *testing.T) {
	d := NewForTest(t, DaemonConfig{})
	sink := d.eventHub.AddStreamSink(streamFilter{})
	defer d.eventHub.RemoveStreamSink(sink)

	payload := incident.PingPayload{
		Type:    "agnt.incident_ping",
		Version: 1,
		Summary: incident.PingStats{Error: 3, Warning: 1, New: 4},
	}
	d.broadcastIncidentDigest("error", payload)

	select {
	case entry := <-sink.Ch:
		assert.Equal(t, proxy.LogTypeIncidentDigest, entry.Type)
		require.NotNil(t, entry.Custom)
		assert.Equal(t, "error", entry.Custom.Level)
		assert.Contains(t, entry.Custom.Message, "err=3")
		assert.Contains(t, entry.Custom.Message, "get_incidents")
	case <-time.After(time.Second):
		t.Fatal("digest never reached the stream sink")
	}
}

// Full chain: a registered session's pinger must turn an HTTP 5xx storm on the
// incident bus into a digest LogEntry delivered over the stream — exercising the
// real production goroutines (bus dispatch, inbox, pinger coalescer, broadcast).
func TestIncidentDigest_EndToEnd_StormToStream(t *testing.T) {
	d := NewForTest(t, DaemonConfig{})
	require.NotNil(t, d.incidentBus, "incident bus must exist")

	sink := d.eventHub.AddStreamSink(streamFilter{})
	defer d.eventHub.RemoveStreamSink(sink)

	d.addIncidentSession("sess-e2e")

	// Two distinct-URL 5xx collapse into one storm entry (count 2). A small
	// burst keeps the coalescer's per-occurrence backoff short (~1s); a large
	// burst would push the first event-driven ping out toward the 10s ceiling —
	// that promptness gap is what the Phase 4 heartbeat digest closes.
	for i := 0; i < 2; i++ {
		ev, ok := incident.FromHTTPEntry(
			proxy.HTTPLogEntry{Method: "GET", URL: fmt.Sprintf("/api/e%d", i), StatusCode: 500},
			"dev")
		require.True(t, ok)
		d.incidentBus.Publish(ev)
	}

	// The pinger coalesces non-critical pings. Read the stream until a digest
	// arrives or we time out.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case entry := <-sink.Ch:
			if entry.Type != proxy.LogTypeIncidentDigest {
				continue
			}
			require.NotNil(t, entry.Custom)
			assert.Contains(t, entry.Custom.Message, "err=")
			return
		case <-deadline:
			t.Fatal("no incident digest reached the stream within 4s")
		}
	}
}
