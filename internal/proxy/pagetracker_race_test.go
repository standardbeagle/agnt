package proxy

// Tier 2 — concurrency invariants for the PageTracker actor model. Run under
// -race. Validates: lossless backpressure (no dropped Track* under contention),
// snapshot isolation (handed-out copies cannot mutate tracker state), and
// safe teardown while producers/consumers are in flight.

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTracker_ConcurrentTracking_Lossless hammers one session from many
// producers. Because send() applies blocking backpressure (not drop) and the
// ops channel is FIFO, a query enqueued after every producer has returned must
// observe EVERY increment — the final count is exact, not approximate.
func TestTracker_ConcurrentTracking_Lossless(t *testing.T) {
	pt, _ := newTrackerWithDoc(t, 10, "/p", "b1")

	const goroutines = 16
	const perG = 200
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				pt.TrackInteraction(InteractionEvent{EventType: "click", URL: "/p"}, "b1")
				pt.TrackMutation(MutationEvent{MutationType: "added", URL: "/p"}, "b1")
			}
		}()
	}
	// Concurrent readers race the writers — must never panic or trip -race.
	stop := make(chan struct{})
	var rwg sync.WaitGroup
	for r := 0; r < 4; r++ {
		rwg.Add(1)
		go func() {
			defer rwg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = pt.GetActiveSessions()
					_ = pt.GetActiveSessionSummaries()
				}
			}
		}()
	}

	wg.Wait()
	close(stop)
	rwg.Wait()

	s := pt.GetActiveSessions()
	require.Len(t, s, 1)
	want := goroutines * perG
	assert.Equal(t, want, s[0].InteractionCount, "every concurrent interaction counted (lossless)")
	assert.Equal(t, want, s[0].MutationCount, "every concurrent mutation counted (lossless)")
}

// TestTracker_SnapshotIsolation proves GetSession/GetActiveSessions hand out
// deep-enough copies that callers mutating the returned slices cannot corrupt
// tracker-owned state.
func TestTracker_SnapshotIsolation(t *testing.T) {
	pt, id := newTrackerWithDoc(t, 10, "/p", "b1")
	pt.TrackInteraction(InteractionEvent{EventType: "click", URL: "/p"}, "b1")
	pt.TrackError(FrontendError{Message: "e", URL: "/p"}, "b1")
	require.Eventually(t, func() bool {
		s, ok := pt.GetSession(id)
		return ok && len(s.Interactions) == 1 && len(s.Errors) == 1
	}, time.Second, 5*time.Millisecond)

	snap, ok := pt.GetSession(id)
	require.True(t, ok)
	// Vandalize the snapshot's slices.
	snap.Interactions = append(snap.Interactions, InteractionEvent{EventType: "FORGED"})
	snap.Errors = append(snap.Errors, FrontendError{Message: "FORGED"})
	snap.Resources = append(snap.Resources, HTTPLogEntry{URL: "/forged"})
	snap.URL = "/HACKED"

	fresh, ok := pt.GetSession(id)
	require.True(t, ok)
	assert.Len(t, fresh.Interactions, 1, "snapshot append must not reach tracker state")
	assert.Len(t, fresh.Errors, 1, "snapshot error append isolated")
	assert.Equal(t, "/p", fresh.URL, "snapshot field mutation isolated")
}

// TestTracker_StopUnderLoad spins producers and consumers, then Stops the
// tracker out from under them. The contract: no panic, no send-on-closed,
// no -race failure; post-stop queries return zero.
func TestTracker_StopUnderLoad(t *testing.T) {
	pt, _ := newTrackerWithDoc(t, 10, "/p", "b1")

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				pt.TrackInteraction(InteractionEvent{EventType: "click", URL: "/p"}, "b1")
				_ = pt.GetActiveSessions()
			}
		}()
	}

	time.Sleep(5 * time.Millisecond) // let some work land, then yank the rug
	assert.NotPanics(t, pt.Stop, "Stop under concurrent load must not panic")
	wg.Wait()

	assert.Nil(t, pt.GetActiveSessions(), "queries return zero after Stop")
	assert.NotPanics(t, func() {
		pt.TrackInteraction(InteractionEvent{EventType: "click", URL: "/p"}, "b1")
	}, "post-Stop track is a safe no-op")
}
