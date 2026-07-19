//go:build soak

package incident

// Soak tests are excluded from the default test run.
// Run with: go test -tags=soak -run TestSoak ./internal/incident/ -v -timeout 25h

import (
	"fmt"
	"runtime"
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
func TestSoak_24h_StableMemory(t *testing.T) {
	bus := NewMPSCBus(nil)
	defer bus.Close()

	store := NewBlobStore(0)
	defer store.Close()

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
		used, max, _ := store.Stats()
		require.Less(t, used, max,
			"BlobStore over budget: used=%d max=%d at event %d", used, max, eventCount)

		// Inbox bands must never exceed capacity.
		stats := pl.inbox.Stats()
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
			ev := NewIncidentEvent(SourceBrowserJS, sev, "SoakError", msg, Context{}, store)
			bus.Publish(ev)
			eventCount++

		case <-healthTicker.C:
			checkHealth()
		}
	}
}
