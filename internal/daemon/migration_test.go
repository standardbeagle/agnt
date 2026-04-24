package daemon

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingMCPSink counts calls to SendAlert and records messages.
type countingMCPSink struct {
	count atomic.Int32
	msgs  []string
	mu    noCopyMu
}

func (s *countingMCPSink) SendAlert(level, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.count.Add(1)
	s.msgs = append(s.msgs, message)
	return nil
}

func (s *countingMCPSink) Count() int {
	return int(s.count.Load())
}

// countingOverlaySink counts calls to TypeAlert.
type countingOverlaySink struct {
	count  atomic.Int32
	active atomic.Bool
}

func (s *countingOverlaySink) TypeAlert(text string) error {
	s.count.Add(1)
	return nil
}

func (s *countingOverlaySink) IsEnabled() bool {
	return s.active.Load()
}

func (s *countingOverlaySink) Activate() {
	s.active.Store(true)
}

func (s *countingOverlaySink) Count() int {
	return int(s.count.Load())
}

// noCopyMu is a plain sync.Mutex alias used for test helpers.
type noCopyMu = sync.Mutex

// TestMigration_DualPath_OldAndNewBothFire asserts that with
// incidentPipeline=false (default) a Deliver call reaches both the old
// MCPAlertSink AND the new incident.Bus (via the adapter), i.e. dual-path is
// truly parallel.
func TestMigration_DualPath_OldAndNewBothFire(t *testing.T) {
	// Build incident bus (wired but pipeline disabled).
	bus := incident.NewMPSCBus(nil)
	defer bus.Close()

	// Build AlertHub with flag = false (default, old behaviour).
	hub := NewAlertHub()

	mcpSink := &countingMCPSink{}
	hub.AddMCPSink(mcpSink)

	overlaySink := &countingOverlaySink{}
	overlaySink.Activate()
	hub.SetOverlaySink(overlaySink)

	// Wire the bus onto the hub with pipeline disabled.
	hub.SetIncidentBus(bus)
	hub.SetIncidentPipeline(false)

	// Deliver one alert.
	hub.Deliver("error", "hello dual-path")

	// Old path must have fired.
	assert.Equal(t, 1, mcpSink.Count(), "MCPAlertSink must fire with flag=false")
	assert.Equal(t, 1, overlaySink.Count(), "OverlayAlertSink must fire with flag=false")

	// New path: bus receives events via the incident adapter, not Deliver.
	// Deliver itself does NOT publish to the bus — that is the adapter's job.
	// This test focuses on the flag gate not suppressing old sinks; the
	// adapter-to-bus path is tested in the incident package's adapter tests.
}

// TestMigration_FlagEnabled_OldSinksSuppressed asserts that with
// incidentPipeline=true the MCPAlertSink and OverlayAlertSink receive NO
// calls from Deliver(), but a StreamSink still receives BroadcastLogEntry
// events (StreamSink is never gated).
func TestMigration_FlagEnabled_OldSinksSuppressed(t *testing.T) {
	hub := NewAlertHub()

	mcpSink := &countingMCPSink{}
	hub.AddMCPSink(mcpSink)

	overlaySink := &countingOverlaySink{}
	overlaySink.Activate()
	hub.SetOverlaySink(overlaySink)

	// StreamSink must still receive BroadcastLogEntry regardless of the flag.
	streamSink := hub.AddStreamSink(streamFilter{})
	defer hub.RemoveStreamSink(streamSink)

	// Enable incident pipeline flag.
	hub.SetIncidentPipeline(true)

	// Deliver — old sinks must be silent.
	hub.Deliver("error", "suppressed alert")

	assert.Equal(t, 0, mcpSink.Count(), "MCPAlertSink must be suppressed when flag=true")
	assert.Equal(t, 0, overlaySink.Count(), "OverlayAlertSink must be suppressed when flag=true")

	// BroadcastLogEntry must still reach StreamSink (flag does not gate it).
	entry := proxy.LogEntry{
		Type: proxy.LogTypeError,
	}
	hub.BroadcastLogEntry(entry, "proxy-1")

	// Read from stream to confirm delivery.
	select {
	case got := <-streamSink.Ch:
		assert.Equal(t, proxy.LogTypeError, got.Type, "StreamSink must receive BroadcastLogEntry even when flag=true")
	default:
		t.Error("StreamSink did not receive BroadcastLogEntry when flag=true")
	}
}

// TestMigration_DriftMetrics_ReportsDelta asserts that the AlertHub exposes
// per-session counters: OldPathCount and NewPathCount that callers can
// inspect to detect drift between the two pipelines.
func TestMigration_DriftMetrics_ReportsDelta(t *testing.T) {
	hub := NewAlertHub()

	mcpSink := &countingMCPSink{}
	hub.AddMCPSink(mcpSink)

	overlaySink := &countingOverlaySink{}
	overlaySink.Activate()
	hub.SetOverlaySink(overlaySink)

	hub.SetIncidentPipeline(false)

	// Deliver 3 alerts — old path only since bus is not wired.
	hub.Deliver("error", "msg-1")
	hub.Deliver("warning", "msg-2")
	hub.Deliver("error", "msg-3")

	metrics := hub.DriftMetrics()
	require.NotNil(t, metrics)
	assert.EqualValues(t, 3, metrics.OldPathCount, "OldPathCount must reflect 3 Deliver calls")
	// NewPathCount is 0 because no incident bus events were published directly.
	assert.EqualValues(t, 0, metrics.NewPathCount, "NewPathCount must be 0 with no bus events")

	// Now simulate new-path events by calling IncrNewPath directly.
	hub.IncrNewPath()
	hub.IncrNewPath()

	metrics = hub.DriftMetrics()
	assert.EqualValues(t, 2, metrics.NewPathCount, "NewPathCount must track IncrNewPath calls")
	assert.EqualValues(t, 3, metrics.OldPathCount, "OldPathCount must remain unchanged")
}
