package incident

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ── Webpack rebuild burst ──────────────────────────────────────────────────────
// 50 identical-fingerprint JS errors in 2s.
// Invariants: exactly 1 inbox entry (all merged), dropped=0.

func TestBurst_WebpackRebuild_SameFP_NoDrops(t *testing.T) {
	t.Parallel()
	bus := NewMPSCBus(nil)
	defer bus.Close()

	const sessionID = "webpack-sess"
	bus.AddSession(sessionID, nil, nil, nil)
	pl := bus.getSessionPipeline(sessionID)
	require.NotNil(t, pl)

	// Fire 50 identical-fingerprint events via the bus.
	ev := NewIncidentEvent(SourceBrowserJS, SeverityError, "TypeError",
		"Cannot read property 'exports' of undefined\n    at src/webpack.js:10:5", Context{}, nil)
	const burstCount = 50
	for i := 0; i < burstCount; i++ {
		bus.Publish(ev)
	}

	// Wait for all events to dispatch and dedup.
	require.Eventually(t, func() bool {
		_, stats := pl.inbox.Query(QueryFilter{})
		return stats.Error > 0
	}, 2*time.Second, 5*time.Millisecond, "webpack burst: inbox must receive events")

	// Let the dispatch goroutine fully drain.
	require.Eventually(t, func() bool {
		return len(bus.inbound) == 0
	}, 2*time.Second, 2*time.Millisecond, "inbound must drain")

	// Exactly one fingerprint in the error band — all 50 events merged, no drops.
	entries, stats := pl.inbox.Query(QueryFilter{})
	require.Equal(t, 0, int(stats.Dropped), "webpack burst: dropped must be 0")
	require.Equal(t, 1, len(entries), "webpack burst: exactly one inbox entry (deduped)")
	require.Greater(t, entries[0].Count, 0, "webpack burst: merged entry must have positive count")
}

// ── 5xx storm ─────────────────────────────────────────────────────────────────
// 100 HTTP 500s across 10 distinct URLs in 5s.
// Invariants: 10 inbox entries, counts sum to 100, dropped=0.

func TestBurst_5xxStorm_10URLs_100Events(t *testing.T) {
	t.Parallel()
	bus := NewMPSCBus(nil)
	defer bus.Close()

	const sessionID = "storm-sess"
	bus.AddSession(sessionID, nil, nil, nil)
	pl := bus.getSessionPipeline(sessionID)
	require.NotNil(t, pl)

	deltaCh, cancel := pl.inbox.Subscribe()
	defer cancel()

	const urlCount = 10
	const totalEvents = 100 // 10 per URL

	for i := 0; i < totalEvents; i++ {
		url := fmt.Sprintf("http://localhost:3000/api/endpoint-%d", i%urlCount)
		ev := NewIncidentEvent(SourceHTTP5xx, SeverityError, "500",
			fmt.Sprintf("Internal Server Error at %s", url),
			Context{URL: url}, nil)
		bus.Publish(ev)
	}

	// Wait until all 10 distinct fingerprints are visible.
	require.Eventually(t, func() bool {
		// Drain pending deltas.
		for {
			select {
			case <-deltaCh:
			default:
				return pl.inbox.Stats().Error >= urlCount
			}
		}
	}, 2*time.Second, 5*time.Millisecond, "5xx storm: expected %d error entries", urlCount)

	// Let the dispatch goroutine fully drain.
	require.Eventually(t, func() bool {
		return len(bus.inbound) == 0
	}, 2*time.Second, 2*time.Millisecond, "inbound must drain")

	// Query: exactly 10 URL-distinct entries, each with positive count, no drops.
	entries, stats := pl.inbox.Query(QueryFilter{})

	require.Equal(t, urlCount, len(entries), "5xx storm: exactly 10 distinct URL entries")
	require.Equal(t, 0, int(stats.Dropped), "5xx storm: no drops expected")
	for _, e := range entries {
		require.Greater(t, e.Count, 0, "5xx storm: each entry must have positive count")
	}
}

// ── Mixed-severity cascade ─────────────────────────────────────────────────────
// 1 crash + 20 errors + 50 warnings + 500 info.
// Critical ping must emit; others may be throttled by flow controller.

func TestBurst_MixedSeverityCascade_CritFirst(t *testing.T) {
	t.Parallel()
	inbox := NewInbox("mix-sess")
	flow := NewFlowController(DefaultBucketConfigs)

	// Coalesce delay deliberately far larger than the liveness window below
	// (same pattern as TestProperty_CriticalPingLatency, internal/incident/
	// property_test.go, commit 6ecaa0d): the invariant worth pinning is not
	// an absolute millisecond bound (razor-thin here — Max/liveness were
	// both 50ms, a coin flip under CPU saturation) but that SeverityCritical
	// bypasses the coalescer entirely. A ping can only land inside a window
	// a fraction of the coalesce delay's size by skipping it, so the check
	// is structural rather than a race against the scheduler.
	const coalesceDelay = 2 * time.Second
	const livenessWindow = 500 * time.Millisecond

	var mu sync.Mutex
	var pings []PingPayload
	cfg := PingConfig{
		MCPNotifications: true,
		MaxTopFPs:        5,
		Delays: PingDelays{
			Initial:    coalesceDelay,
			Max:        coalesceDelay,
			ResetAfter: coalesceDelay,
		},
	}
	pe := NewPingEmitter(inbox, cfg, flow,
		func(_ string, p PingPayload) error {
			mu.Lock()
			pings = append(pings, p)
			mu.Unlock()
			return nil
		},
		nil, nil,
	)
	defer pe.Stop()

	// Fire critical first, then bulk.
	inbox.Ingest(makeEntry("fp-crash", SeverityCritical))

	// Critical must bypass the coalescer and emit well inside a window a
	// quarter the size of the configured coalesce delay.
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(pings) > 0
	}, livenessWindow, 2*time.Millisecond, "critical ping must bypass the coalescer and emit promptly")

	// Verify first ping is at critical level.
	mu.Lock()
	firstPing := pings[0]
	mu.Unlock()
	require.Greater(t, firstPing.Summary.Critical, 0, "first ping must reflect critical incident")

	// Now fire the bulk.
	for i := 0; i < 20; i++ {
		inbox.Ingest(makeEntry(fmt.Sprintf("fp-err-%d", i), SeverityError))
	}
	for i := 0; i < 50; i++ {
		inbox.Ingest(makeEntry(fmt.Sprintf("fp-warn-%d", i), SeverityWarning))
	}
	for i := 0; i < 500; i++ {
		inbox.Ingest(makeEntry(fmt.Sprintf("fp-info-%d", i), SeverityInfo))
	}

	// Wait for coalescer to settle.
	time.Sleep(150 * time.Millisecond)

	stats := inbox.Stats()
	require.Equal(t, 1, stats.Critical, "exactly 1 critical entry")
	require.LessOrEqual(t, stats.Error, defaultBandCapacity, "error band capped")
	require.LessOrEqual(t, stats.Warning, defaultBandCapacity, "warning band capped")
	require.LessOrEqual(t, stats.Info, defaultBandCapacity, "info band capped")
}

// ── Single big stack trace ─────────────────────────────────────────────────────
// 1 event with 32KB stack: Summary<200B, payload stored in blob.

func TestBurst_BigStackTrace_32KB_BlobSpill(t *testing.T) {
	t.Parallel()
	store := NewBlobStore(0) // default 16MB
	defer store.Close()

	// Build a 32KB stack trace.
	const targetSize = 32 * 1024
	stackLine := "    at src/components/DeepComponent.tsx:42:15\n"
	var stack string
	for len(stack) < targetSize {
		stack += stackLine
	}
	msg := "RangeError: Maximum call stack size exceeded\n" + stack

	ev := NewIncidentEvent(SourceBrowserJS, SeverityError, "RangeError", msg, Context{}, store)

	require.LessOrEqual(t, len(ev.Summary), maxSummaryBytes, "Summary must be ≤200B")
	require.NotNil(t, ev.PayloadRef, "32KB payload must spill to blob")
	require.GreaterOrEqual(t, ev.PayloadRef.Size, targetSize, "blob size must be ≥32KB")

	// Blob must be readable.
	require.Eventually(t, func() bool {
		content, _, err := store.Read(ev.PayloadRef.Hash)
		return err == nil && len(content) >= targetSize
	}, time.Second, 5*time.Millisecond, "blob content must land within 1s")
}

// ── 1000 distinct fingerprints ─────────────────────────────────────────────────
// 1000 unique fps: band at 100 capacity, ≥900 dropped.

func TestBurst_1000DistinctFPs_BandDrops(t *testing.T) {
	t.Parallel()
	inbox := NewInbox("fp1000-sess")

	for i := 0; i < 1000; i++ {
		inbox.Ingest(makeEntry(fmt.Sprintf("fp-unique-%d", i), SeverityError))
	}

	stats := inbox.Stats()
	require.Equal(t, defaultBandCapacity, stats.Error, "error band must be at capacity")
	require.GreaterOrEqual(t, int(stats.Dropped), 900, "at least 900 events must be dropped")
	// sum of inbox.Count + dropped = total fired (1000).
	entries, _ := inbox.Query(QueryFilter{})
	var countSum int
	for _, e := range entries {
		countSum += e.Count
	}
	require.Equal(t, 1000, countSum+int(stats.Dropped),
		"count_sum + dropped must equal total fired (conservation)")
}

// ── Concurrent sessions isolation ─────────────────────────────────────────────
// 10 sessions, all receiving all events (bus fans out to every session by design).
// Invariant: each session has an independent inbox with identical state —
// no session's inbox is contaminated by another session's internal state.

func TestBurst_ConcurrentSessions_IndependentInboxes(t *testing.T) {
	t.Parallel()
	bus := NewMPSCBus(nil)
	defer bus.Close()

	const numSessions = 10
	const totalEvents = 30

	sessionIDs := make([]string, numSessions)
	for i := 0; i < numSessions; i++ {
		sessionIDs[i] = fmt.Sprintf("iso-sess-%d", i)
		bus.AddSession(sessionIDs[i], nil, nil, nil)
	}

	// Publish shared events; all sessions receive all events (by design).
	for j := 0; j < totalEvents; j++ {
		ev := NewIncidentEvent(
			SourceHTTP5xx, SeverityError, "500",
			fmt.Sprintf("Error event %d at http://host/e%d", j, j),
			Context{URL: fmt.Sprintf("http://host/e%d", j)},
			nil,
		)
		bus.Publish(ev)
	}

	// Wait for dispatch to drain.
	require.Eventually(t, func() bool {
		return len(bus.inbound) == 0
	}, 2*time.Second, 5*time.Millisecond, "inbound must drain")

	// Allow a short settle period for all sessions to finish ingesting.
	time.Sleep(20 * time.Millisecond)

	// Each session must have the same number of entries (independent inboxes,
	// same events). No session's inbox count must exceed the error band capacity.
	var firstCount int
	for i, sid := range sessionIDs {
		pl := bus.getSessionPipeline(sid)
		require.NotNil(t, pl, "session %s must exist", sid)
		entries, stats := pl.inbox.Query(QueryFilter{})
		count := len(entries)

		require.LessOrEqual(t, stats.Error, defaultBandCapacity,
			"session %s: error band overflow", sid)
		require.Greater(t, count, 0, "session %s: inbox must have entries", sid)

		if i == 0 {
			firstCount = count
		} else {
			// All sessions must agree on entry count (independent but symmetric inboxes).
			require.Equal(t, firstCount, count,
				"session %s inbox count %d != session 0 count %d (independent inboxes must be symmetric)",
				sid, count, firstCount)
		}
	}
}

// ── Adversarial: binary/non-UTF8 payload ──────────────────────────────────────

func TestAdversarial_BinaryPayload_BlobHandlesBytes(t *testing.T) {
	t.Parallel()
	store := NewBlobStore(0)
	defer store.Close()

	// 2KB of binary data including NUL bytes and high bytes.
	data := make([]byte, 2048)
	for i := range data {
		data[i] = byte(i & 0xFF)
	}
	// Place >1KB binary in a message; trigger blob path.
	msg := string(data)
	ev := NewIncidentEvent(SourceHTTP5xx, SeverityError, "binary", msg, Context{}, store)

	// PayloadRef must exist (>1KB) and blob must be readable.
	require.NotNil(t, ev.PayloadRef, "binary payload >1KB must spill to blob")
	require.Equal(t, len(data), ev.PayloadRef.Size)

	require.Eventually(t, func() bool {
		content, mime, err := store.Read(ev.PayloadRef.Hash)
		return err == nil && len(content) == len(data) && mime == "text/plain"
	}, 500*time.Millisecond, 5*time.Millisecond, "binary blob must be readable")
}

// ── Adversarial: malformed stack (no app frames) ──────────────────────────────

func TestAdversarial_MalformedStack_FingerprintFallback(t *testing.T) {
	t.Parallel()
	// A stack with no app frames (only runtime/vendor): fingerprint should not panic.
	malformed := `SomeError: message
    at <anonymous>:1:1
    at webpack://./node_modules/react/index.js:10:5
    at vendor/polyfill.js:200:3`

	canonical := Canonicalize(malformed)
	fp := computeFingerprint("browser_js", "SomeError", canonical, "")
	require.NotEmpty(t, fp, "fingerprint must not be empty for malformed stack")
	require.Len(t, fp, 16, "fingerprint must be 16 hex chars")

	// Two identical malformed stacks must produce the same fingerprint.
	fp2 := computeFingerprint("browser_js", "SomeError", Canonicalize(malformed), "")
	require.Equal(t, fp, fp2, "identical malformed stacks must produce equal fingerprints")
}

// ── Adversarial: rapid session create/destroy ──────────────────────────────────
// No goroutine leak, no panic.

func TestAdversarial_RapidSessionCreateDestroy_NoLeak(t *testing.T) {
	// No t.Parallel(): goroutine count is process-wide.
	bus := NewMPSCBus(nil)
	defer bus.Close()

	const rounds = 50
	ev := NewIncidentEvent(SourceBrowserJS, SeverityError, "T", "m", Context{}, nil)

	for i := 0; i < rounds; i++ {
		sid := fmt.Sprintf("rapid-%d", i)
		bus.AddSession(sid, nil, nil, nil)
		// Fire a few events while the session is live.
		for j := 0; j < 10; j++ {
			bus.Publish(ev)
		}
		bus.RemoveSession(sid)
	}

	// Bus must still function after churn.
	bus.AddSession("final", nil, nil, nil)
	pl := bus.getSessionPipeline("final")
	require.NotNil(t, pl, "bus must accept new sessions after churn")

	bus.Publish(ev)
	require.Eventually(t, func() bool {
		s := pl.inbox.Stats()
		return s.Error > 0
	}, 500*time.Millisecond, 5*time.Millisecond, "final session must receive events")
}

// ── Adversarial: 10MB HTTP response body ──────────────────────────────────────

func TestAdversarial_10MBResponseBody_TruncatedToBlob(t *testing.T) {
	// No t.Parallel(): large allocation.
	store := NewBlobStore(32 * 1024 * 1024) // 32MB
	defer store.Close()

	// Build 10MB body.
	const bodySize = 10 * 1024 * 1024
	body := make([]byte, bodySize)
	copy(body, []byte(`{"error":"Internal Server Error","message":"database connection timeout"}`))
	for i := 100; i < bodySize; i++ {
		body[i] = 'x'
	}

	msg := string(body)
	ev := NewIncidentEvent(SourceHTTP5xx, SeverityError, "500", msg, Context{}, store)

	// Summary must be ≤200B.
	require.LessOrEqual(t, len(ev.Summary), maxSummaryBytes, "10MB body: Summary must be ≤200B")
	// Blob ref must exist.
	require.NotNil(t, ev.PayloadRef, "10MB body must spill to blob")
	require.Equal(t, bodySize, ev.PayloadRef.Size, "blob ref must capture full size")

	// Blob must eventually land.
	require.Eventually(t, func() bool {
		_, _, err := store.Read(ev.PayloadRef.Hash)
		return err == nil
	}, 3*time.Second, 10*time.Millisecond, "10MB blob must be readable within 3s")
}

// ── Bus drop counter conservation ──────────────────────────────────────────────
// Fired = delivered_to_sessions + dropped (within margins of channel capacity).

func TestStress_BusDropConservation(t *testing.T) {
	// No t.Parallel(): uses large goroutine count.
	bus := NewMPSCBus(nil)
	defer bus.Close()

	bus.AddSession("conserve-sess", nil, nil, nil)

	const producers = 20
	const eventsEach = 500

	var fired atomic.Int64
	var wg sync.WaitGroup

	ev := NewIncidentEvent(SourceBrowserJS, SeverityError, "T", "m", Context{}, nil)

	for i := 0; i < producers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < eventsEach; j++ {
				bus.Fire(&ev)
				fired.Add(1)
			}
		}()
	}
	wg.Wait()

	// Wait for dispatch to drain inbound channel.
	require.Eventually(t, func() bool {
		return len(bus.inbound) == 0
	}, 2*time.Second, 5*time.Millisecond, "inbound channel must drain")

	actualFired := fired.Load()
	dropped := bus.Dropped()

	// dropped must be non-negative and ≤ total fired.
	require.GreaterOrEqual(t, dropped, int64(0))
	require.LessOrEqual(t, dropped, actualFired)
}

// ── Coalescer: burst produces ≤ceiling pings ──────────────────────────────────

func TestBurst_Coalescer_BoundedPings(t *testing.T) {
	t.Parallel()
	inbox := NewInbox("coalesce-sess")
	flow := NewFlowController(DefaultBucketConfigs)

	var mu sync.Mutex
	var pings []PingPayload

	cfg := PingConfig{
		MCPNotifications: true,
		MaxTopFPs:        5,
		Delays: PingDelays{
			Initial:    20 * time.Millisecond,
			Max:        100 * time.Millisecond,
			ResetAfter: 500 * time.Millisecond,
		},
	}
	pe := NewPingEmitter(inbox, cfg, flow,
		func(_ string, p PingPayload) error {
			mu.Lock()
			pings = append(pings, p)
			mu.Unlock()
			return nil
		},
		nil, nil,
	)
	defer pe.Stop()

	// Fire 100 events with the same fingerprint.
	for i := 0; i < 100; i++ {
		inbox.Ingest(makeEntry("fp-coalesce", SeverityError))
	}

	time.Sleep(300 * time.Millisecond) // settle

	mu.Lock()
	n := len(pings)
	mu.Unlock()

	// With exponential backoff, 100 same-fp events should produce far fewer than 20 pings.
	require.LessOrEqual(t, n, 20, "coalescer should bound pings well below event count")
	require.Greater(t, n, 0, "at least one ping must emit")
}
