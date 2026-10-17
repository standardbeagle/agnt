//go:build soak

package incident

// Soak tests are excluded from the default test run.
// Run with: go test -tags=soak -run TestSoak ./internal/incident/ -v -timeout 25h

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	soakDuration     = 24 * time.Hour
	soakTickRate     = 10 * time.Second // 1 event per 10s
	soakMaxMemoryMB  = 200              // MB heap upper bound
	soakHealthPeriod = 5 * time.Minute
)

// TestSoak_24h_StableMemory runs a low-rate stream for 24 hours and verifies
// that memory, goroutine count, and blob store usage remain stable.
//
// Invariants verified every 5 minutes:
//   - Heap allocation < 200MB
//   - Goroutine count within ±20 of baseline
//   - BlobStore bytes < defaultBudgetBytes (16MB)
//   - Inbox band counts never exceed defaultBandCapacity
//   - Every published event was ingested by the session pipeline (events carry
//     the session ID, since the bus discards sessionless events), the inbox is
//     populated, and oversized messages reach the session blob store
func TestSoak_24h_StableMemory(t *testing.T) {
	bus := NewMPSCBus(nil)
	defer bus.Close()

	sessionID := "soak-sess"
	bus.AddSession(sessionID, nil, nil, nil)
	pl := bus.getSessionPipeline(sessionID)
	require.NotNil(t, pl)

	// Capture baseline goroutine count.
	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)
	baselineGoroutines := runtime.NumGoroutine()

	ticker := time.NewTicker(soakTickRate)
	defer ticker.Stop()
	healthTicker := time.NewTicker(soakHealthPeriod)
	defer healthTicker.Stop()
	deadline := time.After(soakDuration)

	severities := []Severity{SeverityCritical, SeverityError, SeverityWarning, SeverityInfo}
	var eventCount int

	checkHealth := func() {
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)

		heapMB := ms.HeapAlloc / (1024 * 1024)
		goroutines := runtime.NumGoroutine()

		t.Logf("soak check at event %d: heap=%dMB goroutines=%d", eventCount, heapMB, goroutines)

		require.Less(t, heapMB, uint64(soakMaxMemoryMB),
			"heap %dMB exceeds %dMB limit at event %d", heapMB, soakMaxMemoryMB, eventCount)

		// Goroutine count must not grow unboundedly (allow +20 over baseline for timers).
		require.Less(t, goroutines, baselineGoroutines+20,
			"goroutine leak: %d goroutines (baseline=%d) at event %d",
			goroutines, baselineGoroutines, eventCount)

		// BlobStore must not exceed its budget.
		used, max, _ := pl.blobs.Stats()
		require.Less(t, used, max,
			"BlobStore over budget: used=%d max=%d at event %d", used, max, eventCount)

		// Delivery: sessionless events are discarded before the inbox, so a soak
		// that skipped this check could pass without exercising dedup, the inbox
		// or the blob store at all.
		require.Eventually(t, func() bool { return pl.ingestCount.Load() == int64(eventCount) },
			10*time.Second, 10*time.Millisecond,
			"pipeline ingested %d of %d published events", pl.ingestCount.Load(), eventCount)
		require.Zero(t, bus.Dropped(), "bus dropped events at event %d", eventCount)

		// Inbox bands must never exceed capacity.
		stats := pl.inbox.Stats()
		if eventCount > 0 {
			require.Positive(t, stats.Critical+stats.Error+stats.Warning+stats.Info,
				"inbox empty after %d published events", eventCount)
			_, _, blobs := pl.blobs.Stats()
			require.Positive(t, blobs, "session blob store empty after %d published events", eventCount)
		}
		require.LessOrEqual(t, stats.Critical, defaultBandCapacity)
		require.LessOrEqual(t, stats.Error, defaultBandCapacity)
		require.LessOrEqual(t, stats.Warning, defaultBandCapacity)
		require.LessOrEqual(t, stats.Info, defaultBandCapacity)
	}

	for {
		select {
		case <-deadline:
			t.Log("soak complete: all health checks passed")
			return

		case <-ticker.C:
			sev := severities[eventCount%len(severities)]
			msg := fmt.Sprintf("soak event %d", eventCount)
			if eventCount%8 == 0 {
				// Oversized: the bus spills it into the session's own blob store.
				msg += " " + strings.Repeat("padding ", 64)
			}
			// The session ID is what routes the event to the pipeline: the bus
			// discards sessionless events. A nil store lets the bus own the spill,
			// as for production adapters.
			ev := NewIncidentEvent(SourceBrowserJS, sev, "SoakError", msg, Context{SessionID: sessionID}, nil)
			bus.Publish(ev)
			eventCount++

		case <-healthTicker.C:
			checkHealth()
		}
	}
}
