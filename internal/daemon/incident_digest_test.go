package daemon

import (
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
	sink := d.alertHub.AddStreamSink(streamFilter{})
	defer d.alertHub.RemoveStreamSink(sink)

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
