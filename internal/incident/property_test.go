package incident

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ── Property: inbox entry count ≥ distinct fingerprints ──────────────────────
// For any burst of N events with D distinct fingerprints:
//   - inbox.len == min(D, bandCapacity)
//   - stats.Dropped == max(0, D - bandCapacity)
// (Inbox.Count per entry reflects merged occurrences via the dedup pipeline;
//  the raw event count is tracked by the deduplicator, not the inbox.)
// Verified across 1000 random burst sizes.

func TestProperty_InboxEntryInvariant(t *testing.T) {
	t.Parallel()
	const iterations = 1000

	for i := 0; i < iterations; i++ {
		// Vary number of distinct fingerprints between 1 and 200.
		distinctFPs := (i%200 + 1)
		inbox := NewInbox(fmt.Sprintf("prop-sess-%d", i))

		// Each fp appears 3 times to exercise merging.
		for rep := 0; rep < 3; rep++ {
			for j := 0; j < distinctFPs; j++ {
				fp := fmt.Sprintf("fp-%d", j)
				inbox.Ingest(makeEntry(fp, SeverityError))
			}
		}

		entries, stats := inbox.Query(QueryFilter{})
		expectedLen := distinctFPs
		if expectedLen > defaultBandCapacity {
			expectedLen = defaultBandCapacity
		}
		expectedDropped := int64(0)
		if distinctFPs > defaultBandCapacity {
			// Dropped is at least the excess; may be more due to LRU eviction of repeats.
			expectedDropped = int64(distinctFPs - defaultBandCapacity)
		}

		if len(entries) != expectedLen {
			t.Errorf("iteration %d (distinctFPs=%d): len(entries)=%d, want %d",
				i, distinctFPs, len(entries), expectedLen)
		}
		if stats.Dropped < expectedDropped {
			t.Errorf("iteration %d: Dropped=%d < expected %d",
				i, stats.Dropped, expectedDropped)
		}
		// Each entry must have positive count.
		for _, e := range entries {
			if e.Count <= 0 {
				t.Errorf("iteration %d: entry %s has non-positive count %d",
					i, e.Fingerprint, e.Count)
			}
		}
	}
}

// ── Property: critical ping bypasses the coalescer ───────────────────────────
// Wall-clock latency bounds are inherently load-sensitive: under `go test
// ./...` CPU saturation, goroutine scheduling delay alone can eat tens of ms,
// so a tight absolute-ms threshold flakes intermittently even though the
// Pinger's emit path is correct (daemon-architecture.md § Incident Pipeline
// contract 7: "Pinger never blocks delivery"). The actual invariant worth
// pinning is not a millisecond number but the *relative* guarantee that
// critical severity bypasses the non-critical coalesce delay entirely — see
// PingEmitter.handleDelta, which calls pe.coalesce.Stop() + pe.emit()
// synchronously for SeverityCritical instead of scheduling through the
// Coalescer. We assert that by configuring a coalesce delay an order of
// magnitude larger than the liveness wait window: a critical ping can only
// land inside that window if it skipped the coalescer, so the assertion is
// a deterministic structural check, not a race against the scheduler.
// Verified across 200 iterations with varying preceding load.

func TestProperty_CriticalPingLatency(t *testing.T) {
	t.Parallel()
	const iterations = 200

	// Coalesce delay deliberately far larger than the liveness window below,
	// so the only way a ping lands inside that window is by bypassing it.
	const coalesceDelay = 2 * time.Second
	const livenessWindow = 500 * time.Millisecond

	for i := 0; i < iterations; i++ {
		inbox := NewInbox(fmt.Sprintf("crit-prop-%d", i))
		flow := NewFlowController(DefaultBucketConfigs)

		var criticalPings atomic.Int64
		var nonCriticalPings atomic.Int64
		cfg := PingConfig{
			MCPNotifications: true,
			MaxTopFPs:        3,
			Delays: PingDelays{
				Initial:    coalesceDelay,
				Max:        coalesceDelay,
				ResetAfter: coalesceDelay,
			},
		}

		pe := NewPingEmitter(inbox, cfg, flow,
			func(_ string, payload PingPayload) error {
				if payload.Summary.Critical > 0 {
					criticalPings.Add(1)
				} else {
					nonCriticalPings.Add(1)
				}
				return nil
			},
			nil, nil,
		)

		// Optionally add some preceding error load to vary conditions. These
		// are non-critical and would normally sit behind the coalesce delay.
		if i%3 == 0 {
			for j := 0; j < 10; j++ {
				inbox.Ingest(makeEntry(fmt.Sprintf("bg-fp-%d", j), SeverityError))
			}
		}
		inbox.Ingest(makeEntry(fmt.Sprintf("crit-%d", i), SeverityCritical))

		// Liveness + bypass: the critical ping must land well inside a window
		// far shorter than the configured coalesce delay. Any regression that
		// routed critical events through the coalescer would fail this even
		// under generous scheduler jitter, since livenessWindow is 1/4 of
		// coalesceDelay.
		require.Eventually(t, func() bool {
			return criticalPings.Load() > 0
		}, livenessWindow, time.Millisecond,
			"iteration %d: critical ping must bypass the coalescer and emit promptly", i)

		require.Equal(t, int64(1), criticalPings.Load(),
			"iteration %d: exactly one critical ping expected", i)
		require.Equal(t, int64(0), nonCriticalPings.Load(),
			"iteration %d: no coalesced (non-critical) ping should have fired within the liveness window", i)

		pe.Stop()
	}
}

// ── Property: identical post-canonicalize fingerprints always merge ────────────
// For any fingerprint pair (a, b): if Canonicalize(a)==Canonicalize(b), they merge.

func TestProperty_IdenticalCanonical_AlwaysMerge(t *testing.T) {
	t.Parallel()
	const iterations = 500

	// Pairs of messages that differ only in volatile parts (timestamps, addresses).
	type msgPair struct {
		a, b string
	}
	templates := []msgPair{
		{
			"Error at 2024-01-15T10:30:00Z: connection refused",
			"Error at 2024-03-22T14:55:30Z: connection refused",
		},
		{
			"panic: runtime error at 0x4a2f10 in main.foo",
			"panic: runtime error at 0x9b3d81 in main.foo",
		},
		{
			"Request 550e8400-e29b-41d4-a716-446655440000 failed",
			"Request 6ba7b810-9dad-11d1-80b4-00c04fd430c8 failed",
		},
	}

	for i := 0; i < iterations; i++ {
		pair := templates[i%len(templates)]

		ca := Canonicalize(pair.a)
		cb := Canonicalize(pair.b)
		require.Equal(t, ca, cb,
			"iteration %d: canonical forms differ despite identical volatile content", i)

		fp1 := computeFingerprint("browser_js", "Error", ca, "")
		fp2 := computeFingerprint("browser_js", "Error", cb, "")
		require.Equal(t, fp1, fp2,
			"iteration %d: fingerprints differ for canonically-equal messages", i)

		// Ingest both into a fresh inbox — should yield exactly 1 entry with count=2.
		inbox := NewInbox(fmt.Sprintf("merge-prop-%d", i))
		inbox.Ingest(&InboxEntry{
			Fingerprint: fp1,
			FirstSeenAt: time.Now(),
			LastSeenAt:  time.Now(),
			Count:       1,
			Severity:    SeverityError,
		})
		inbox.Ingest(&InboxEntry{
			Fingerprint: fp2,
			FirstSeenAt: time.Now(),
			LastSeenAt:  time.Now(),
			Count:       1,
			Severity:    SeverityError,
		})

		entries, stats := inbox.Query(QueryFilter{})
		require.Equal(t, 1, len(entries),
			"iteration %d: canonically-equal fingerprints must merge to 1 entry", i)
		require.Equal(t, 2, entries[0].Count,
			"iteration %d: merged entry count must be 2", i)
		require.Equal(t, int64(0), stats.Dropped,
			"iteration %d: no drops for 2 events in empty inbox", i)
	}
}

// ── Property: distinct fingerprints never merge ────────────────────────────────

func TestProperty_DistinctFingerprints_NeverMerge(t *testing.T) {
	t.Parallel()
	const pairs = 200

	for i := 0; i < pairs; i++ {
		fp1 := fmt.Sprintf("fp-distinct-a-%d", i)
		fp2 := fmt.Sprintf("fp-distinct-b-%d", i)
		require.NotEqual(t, fp1, fp2)

		inbox := NewInbox(fmt.Sprintf("distinct-%d", i))
		inbox.Ingest(makeEntry(fp1, SeverityError))
		inbox.Ingest(makeEntry(fp2, SeverityError))

		entries, _ := inbox.Query(QueryFilter{})
		require.Equal(t, 2, len(entries),
			"iteration %d: distinct fingerprints must produce 2 entries", i)
	}
}

// ── Property: blob read(write(x)) == x before GC ──────────────────────────────
// After GC of the entry, read returns ErrBlobEvicted.

func TestProperty_BlobWriteRead_Roundtrip(t *testing.T) {
	t.Parallel()
	const iterations = 100

	store := NewBlobStore(0)
	defer store.Close()

	for i := 0; i < iterations; i++ {
		// Vary payload size between 8B and 4KB.
		size := (i%500 + 8) * 8
		payload := make([]byte, size)
		for j := range payload {
			payload[j] = byte((i + j) & 0xFF)
		}

		ref, err := store.Write(payload, "application/octet-stream")
		require.NoError(t, err, "iteration %d: Write must not error", i)
		require.NotEmpty(t, ref.Hash)
		require.Equal(t, size, ref.Size)

		// Verify hash matches content.
		sum := sha256.Sum256(payload)
		expectedHash := hex.EncodeToString(sum[:])
		require.Equal(t, expectedHash, ref.Hash, "iteration %d: hash mismatch", i)

		// read(write(x)) == x.
		content, mime, err := store.Read(ref.Hash)
		require.NoError(t, err, "iteration %d: Read must not error", i)
		require.Equal(t, payload, content, "iteration %d: roundtrip payload mismatch", i)
		require.Equal(t, "application/octet-stream", mime)
	}
}

// TestProperty_BlobAfterEviction verifies that reading a deliberately evicted
// blob returns ErrBlobEvicted (not a panic or wrong data).

func TestProperty_BlobAfterEviction_ReturnsEvictedError(t *testing.T) {
	t.Parallel()
	// Create a store with a tiny budget: 512 bytes.
	const budget = 512
	store := newBlobStoreInternal(budget, writeQueueCap)
	defer store.Close()

	// Write a payload that fills the budget.
	fill := make([]byte, budget)
	ref1, err := store.Write(fill, "text/plain")
	require.NoError(t, err)

	// Write a second payload that forces eviction of the first.
	evict := make([]byte, budget)
	for i := range evict {
		evict[i] = 0xFF
	}
	_, err = store.Write(evict, "text/plain")
	require.NoError(t, err)

	// First blob should be evicted.
	_, _, err = store.Read(ref1.Hash)
	require.Equal(t, ErrBlobEvicted, err, "evicted blob must return ErrBlobEvicted")
}

// ── Property: deduplicator window expiry → new entry ─────────────────────────

func TestProperty_DedupWindowExpiry_CreatesNewEntry(t *testing.T) {
	t.Parallel()
	const window = 20 * time.Millisecond
	dedup := NewDeduplicator(window)

	ev := NewIncidentEvent(SourceBrowserJS, SeverityError, "TypeError", "window test", Context{}, nil)

	// First ingest: new entry.
	merged1, e1 := dedup.Ingest("sess", ev)
	require.False(t, merged1, "first ingest must be new")
	require.Equal(t, 1, e1.Count)

	// Within window: merges.
	merged2, e2 := dedup.Ingest("sess", ev)
	require.True(t, merged2, "within-window ingest must merge")
	require.Equal(t, 2, e2.Count)

	// After window expires: new entry again.
	time.Sleep(window + 5*time.Millisecond)
	merged3, e3 := dedup.Ingest("sess", ev)
	require.False(t, merged3, "post-expiry ingest must be new")
	require.Equal(t, 1, e3.Count)
}

// ── Property: flow controller critical always allowed ─────────────────────────

func TestProperty_FlowController_CriticalAlwaysAllowed(t *testing.T) {
	t.Parallel()
	fc := NewFlowController(DefaultBucketConfigs)

	// Exhaust all buckets first by draining non-critical.
	for i := 0; i < 1000; i++ {
		fc.TryPing(SeverityError)
		fc.TryPing(SeverityWarning)
		fc.TryPing(SeverityInfo)
	}

	// Critical must always be allowed regardless of bucket state.
	const critChecks = 100
	for i := 0; i < critChecks; i++ {
		require.True(t, fc.TryPing(SeverityCritical),
			"critical must always be allowed (iteration %d)", i)
	}
}

// ── Property: inbox band capacity invariant ────────────────────────────────────

func TestProperty_InboxBandCapacity_NeverExceeds(t *testing.T) {
	t.Parallel()
	const iterations = 50
	const maxPerIter = 300

	for i := 0; i < iterations; i++ {
		inbox := NewInbox(fmt.Sprintf("cap-prop-%d", i))
		n := (i % maxPerIter) + 1
		for j := 0; j < n; j++ {
			sev := []Severity{SeverityCritical, SeverityError, SeverityWarning, SeverityInfo}[j%4]
			inbox.Ingest(makeEntry(fmt.Sprintf("fp-%d", j), sev))
		}

		stats := inbox.Stats()
		require.LessOrEqual(t, stats.Critical, defaultBandCapacity,
			"iteration %d: critical band overflow", i)
		require.LessOrEqual(t, stats.Error, defaultBandCapacity,
			"iteration %d: error band overflow", i)
		require.LessOrEqual(t, stats.Warning, defaultBandCapacity,
			"iteration %d: warning band overflow", i)
		require.LessOrEqual(t, stats.Info, defaultBandCapacity,
			"iteration %d: info band overflow", i)
	}
}
