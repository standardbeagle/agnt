package overlay

// Regression tests for alert delivery serialization and the protected-only
// batch filter. Reuses the fake timer harness from alert_scanner_stress_test.go.

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// TestAlertScanner_FlushDeliveriesSerialized pins that flush() routes batches
// through the single delivery goroutine instead of calling onAlert directly.
// A direct call would let two overlapping flushes (a retry timer racing a
// fresh batch timer) run onAlert concurrently and interleave PTY injection.
func TestAlertScanner_FlushDeliveriesSerialized(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	fts := &fakeTimerSet{}
	defer fts.drainTimers()

	var inFlight, maxInFlight, delivered atomic.Int32
	release := make(chan struct{})

	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:  1 * time.Millisecond,
		DedupeWindow: 10 * time.Minute,
		OnAlert: func(b *AlertBatch) {
			cur := inFlight.Add(1)
			for {
				m := maxInFlight.Load()
				if cur <= m || maxInFlight.CompareAndSwap(m, cur) {
					break
				}
			}
			<-release
			inFlight.Add(-1)
			delivered.Add(1)
		},
	})
	scanner.afterFunc = fts.afterFunc
	defer scanner.Stop()

	// First flush: the delivery goroutine picks up the batch and blocks in
	// onAlert. Second flush while the first is still in flight must queue
	// behind it, never run concurrently.
	scanner.Inject(errMatch(1))
	fts.FireAll()
	scanner.Inject(errMatch(2))
	fts.FireAll()

	// Give an incorrect concurrent delivery time to show up.
	require.Eventually(t, func() bool { return inFlight.Load() == 1 },
		2*time.Second, 5*time.Millisecond, "first batch reaches onAlert")
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(1), maxInFlight.Load(), "onAlert never concurrent")

	close(release)
	require.Eventually(t, func() bool { return delivered.Load() == 2 },
		2*time.Second, 5*time.Millisecond, "both batches delivered in turn")
}
