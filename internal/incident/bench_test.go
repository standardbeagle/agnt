package incident

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ── Bus: enqueue-only microbenchmarks ─────────────────────────────────────────
// These events carry no session ID, and the dispatcher discards a sessionless
// event before dedup or the inbox. They therefore time ONLY the producer-side
// channel send (and the drop path) — useful for the Fire hot-path budget, and
// useless as a statement about pipeline throughput. BenchmarkBus_EndToEnd below
// is the real workload.

// BenchmarkBus_EnqueueOnly_Microbench: target <500ns/op for the non-blocking
// send. Nothing downstream of the channel is exercised.
func BenchmarkBus_EnqueueOnly_Microbench(b *testing.B) {
	bus := NewMPSCBus(nil)
	defer bus.Close()

	ev := NewIncidentEvent(SourceBrowserJS, SeverityError, "TypeError", "bench error", Context{}, nil)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bus.Fire(&ev)
	}
}

// BenchmarkBus_EnqueueSaturated_Microbench measures Fire latency when the
// inbound channel is near-full (drop path exercised). This is the worst-case
// producer scenario; like the benchmark above it never reaches an inbox.
func BenchmarkBus_EnqueueSaturated_Microbench(b *testing.B) {
	if raceEnabled {
		b.Skip("latency benchmark not meaningful under race detector")
	}
	bus := NewMPSCBus(nil)
	defer bus.Close()

	ev := NewIncidentEvent(SourceBrowserJS, SeverityError, "TypeError", "bench", Context{}, nil)

	// Pre-fill inbound channel to force the drop path on most iterations.
	bus.pauseDispatch()
	for i := 0; i < busInboundCap-10; i++ {
		bus.inbound <- &ev
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bus.Fire(&ev)
	}
	bus.resumeDispatch()
}

// ── BenchmarkBus_EndToEnd ─────────────────────────────────────────────────────
// Session-scoped events through the whole pipeline: bus → dedup → inbox (and
// the session blob store for oversized messages). Four numbers are reported
// separately, because a single ns/op hides which one moved:
//
//	enqueue-ns/op     producer cost of Fire, measured around the send loop only
//	events/s          completed throughput: accepted events / wall time until the
//	                  dispatcher has ingested every one of them
//	drain-ns/batch    queue delay: time from the last Fire of a batch returning
//	                  to its last event being ingested (mean over batches)
//	max-drain-ns      the worst such batch
//	drops             events the bus refused (must stay 0, or the run is invalid)
//
// The reported ns/op is completed time per event (enqueue + queue + ingest).
// Delivery is verified against the pipeline's own ingest counter and inbox, so a
// run that silently discards events fails instead of reporting a fast number.

const benchSessionID = "bench-sess"

// benchBatch keeps each burst well inside the inbound channel so a healthy run
// never drops; the drop path has its own microbenchmark.
const benchBatch = busInboundCap / 4

func benchEvent(msg string, sev Severity) IncidentEvent {
	return NewIncidentEvent(SourceBrowserJS, sev, "TypeError", msg, Context{SessionID: benchSessionID}, nil)
}

// benchWaitIngested spins until the session pipeline has ingested want events.
func benchWaitIngested(b *testing.B, pl *sessionPipeline, want int64) {
	b.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for pl.ingestCount.Load() < want {
		if time.Now().After(deadline) {
			b.Fatalf("pipeline ingested %d of %d events within 30s", pl.ingestCount.Load(), want)
		}
		runtime.Gosched()
	}
}

// benchEndToEnd drives b.N events built by mk through a one-session bus and
// returns the pipeline for post-run delivery assertions.
func benchEndToEnd(b *testing.B, mk func(i int) IncidentEvent) *sessionPipeline {
	b.Helper()
	bus := NewMPSCBus(nil)
	b.Cleanup(bus.Close)
	bus.AddSession(benchSessionID, nil, nil, nil)
	pl := bus.getSessionPipeline(benchSessionID)
	if pl == nil {
		b.Fatal("session pipeline missing after AddSession")
	}

	var (
		accepted              int64
		enqueue, drain, worst time.Duration
		batches               int
		events                = make([]IncidentEvent, 0, benchBatch)
	)

	b.ReportAllocs()
	b.ResetTimer()
	for done := 0; done < b.N; {
		n := min(benchBatch, b.N-done)

		b.StopTimer()
		events = events[:0]
		for j := 0; j < n; j++ {
			events = append(events, mk(done+j))
		}
		b.StartTimer()

		t0 := time.Now()
		for j := range events {
			if bus.fire(&events[j]) {
				accepted++
			}
		}
		enqueued := time.Now()
		benchWaitIngested(b, pl, accepted)
		d := time.Since(enqueued)

		enqueue += enqueued.Sub(t0)
		drain += d
		worst = max(worst, d)
		batches++
		done += n
	}
	elapsed := b.Elapsed()

	if dropped := bus.Dropped(); dropped != 0 || accepted != int64(b.N) {
		b.Fatalf("bus dropped %d events, accepted %d of %d: delivered counts are not comparable", dropped, accepted, b.N)
	}
	if got := pl.ingestCount.Load(); got != int64(b.N) {
		b.Fatalf("pipeline ingested %d events, published %d", got, b.N)
	}

	b.ReportMetric(float64(enqueue.Nanoseconds())/float64(b.N), "enqueue-ns/op")
	b.ReportMetric(float64(b.N)/elapsed.Seconds(), "events/s")
	b.ReportMetric(float64(drain.Nanoseconds())/float64(batches), "drain-ns/batch")
	b.ReportMetric(float64(worst.Nanoseconds()), "max-drain-ns")
	b.ReportMetric(float64(bus.Dropped()), "drops")
	return pl
}

func BenchmarkBus_EndToEnd(b *testing.B) {
	// Every event is a new fingerprint: dedup insert + inbox insert + band eviction.
	b.Run("unique", func(b *testing.B) {
		seen := make(map[string]struct{}, benchBatch)
		pl := benchEndToEnd(b, func(i int) IncidentEvent {
			ev := benchEvent("bench error "+strconv.FormatInt(int64(i), 36)+"x", SeverityError)
			if i < benchBatch {
				seen[ev.Fingerprint] = struct{}{}
			}
			return ev
		})
		if want := min(b.N, benchBatch); len(seen) != want {
			b.Fatalf("messages collapsed to %d fingerprints, want %d: not a unique-entry workload", len(seen), want)
		}
		if got, want := pl.inbox.Stats().Error, min(b.N, defaultBandCapacity); got != want {
			b.Fatalf("error band holds %d entries, want %d", got, want)
		}
	})

	// One fingerprint repeated: the dedup/inbox merge path.
	b.Run("repeat", func(b *testing.B) {
		ev := benchEvent("bench repeat error", SeverityError)
		pl := benchEndToEnd(b, func(int) IncidentEvent { return ev })
		entry := pl.inbox.FindByFingerprint(ev.Fingerprint)
		if entry == nil || entry.Count != b.N {
			b.Fatalf("merged entry = %+v, want count %d", entry, b.N)
		}
	})

	// Oversized unique messages: adds the per-session blob store write.
	b.Run("payload_1KB", func(b *testing.B) {
		pad := strings.Repeat("stack frame padding ", 52) // ~1KB
		pl := benchEndToEnd(b, func(i int) IncidentEvent {
			return benchEvent("bench payload "+strconv.FormatInt(int64(i), 36)+"x "+pad, SeverityError)
		})
		if _, _, count := pl.blobs.Stats(); count == 0 {
			b.Fatal("blob store empty: payloads were not stored")
		}
	})
}

// ── BenchmarkDedup_Merge ──────────────────────────────────────────────────────
// Target: <2µs/op for a merge on an existing entry.

func BenchmarkDedup_Merge(b *testing.B) {
	dedup := NewDeduplicator(30 * time.Second)
	ev := NewIncidentEvent(SourceBrowserJS, SeverityError, "TypeError", "dedup bench", Context{}, nil)

	// Warm-up: insert once so subsequent calls hit the merge path.
	dedup.Ingest("sess", ev)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dedup.Ingest("sess", ev)
	}
}

// BenchmarkDedup_NewEntry measures the new-entry path (cold fingerprint).
func BenchmarkDedup_NewEntry(b *testing.B) {
	dedup := NewDeduplicator(30 * time.Second)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ev := NewIncidentEvent(SourceBrowserJS, SeverityError, "TypeError",
			fmt.Sprintf("unique error %d", i), Context{}, nil)
		dedup.Ingest("sess", ev)
	}
}

// ── BenchmarkPing_BuildPayload ────────────────────────────────────────────────
// Target: <5µs/op for a 5-fingerprint payload build.

func BenchmarkPing_BuildPayload(b *testing.B) {
	inbox := NewInbox("bench-sess")
	for i := 0; i < 5; i++ {
		e := makeEntry(fmt.Sprintf("fp-%d", i), SeverityError)
		ev := NewIncidentEvent(SourceBrowserJS, SeverityError, "TypeError",
			fmt.Sprintf("bench error %d", i), Context{}, nil)
		e.Sample = &ev
		inbox.Ingest(e)
	}

	flow := NewFlowController(DefaultBucketConfigs)
	pe := &PingEmitter{
		inbox:  inbox,
		config: PingConfig{MaxTopFPs: 5, IncludeSummary: true},
		flow:   flow,
	}
	entries, stats := inbox.Query(QueryFilter{})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = pe.buildPayload(stats, entries)
	}
}

// ── BenchmarkBlob_WriteAsync ──────────────────────────────────────────────────
// Target: <1µs/op producer-side (hash computation + channel send).
// 64KB payload representative of real stack traces.

func BenchmarkBlob_WriteAsync_64KB(b *testing.B) {
	store := NewBlobStore(0)
	defer store.Close()

	data := make([]byte, 64*1024)
	for i := range data {
		data[i] = byte(i & 0xFF)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = store.WriteAsync(data, "text/plain")
	}
}

// BenchmarkBlob_WriteAsync_1KB is the small-payload baseline.
func BenchmarkBlob_WriteAsync_1KB(b *testing.B) {
	store := NewBlobStore(0)
	defer store.Close()

	data := make([]byte, 1024)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = store.WriteAsync(data, "text/plain")
	}
}

// BenchmarkBlob_WriteAsync_ChannelOnly measures the channel-send overhead of
// WriteAsync with a pre-computed hash (minimal data, hash dominates for large payloads).
// This isolates the "producer side" cost excluding SHA256 computation.
func BenchmarkBlob_WriteAsync_ChannelOnly(b *testing.B) {
	store := NewBlobStore(0)
	defer store.Close()

	// Tiny 1-byte payload: SHA256 is negligible, channel overhead dominates.
	data := []byte{0x42}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = store.WriteAsync(data, "text/plain")
	}
}

// ── BenchmarkFingerprint_Canonicalize ────────────────────────────────────────
// Target: <10µs/op for a typical multi-line JS stack trace.

func BenchmarkFingerprint_Canonicalize(b *testing.B) {
	input := `TypeError: Cannot read properties of null (reading 'map')
    at ProductList (src/components/List.tsx:42:15)
    at renderWithHooks (node_modules/react-dom/cjs/react-dom.development.js:14985:18)
    at mountIndeterminateComponent (node_modules/react-dom/cjs/react-dom.development.js:17811:13)
    at beginWork (node_modules/react-dom/cjs/react-dom.development.js:19044:16)
    at performUnitOfWork (node_modules/react-dom/cjs/react-dom.development.js:23231:12)`

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Canonicalize(input)
	}
}

// BenchmarkFingerprint_ComputeFingerprint measures the hash step alone.
func BenchmarkFingerprint_ComputeFingerprint(b *testing.B) {
	canonical := Canonicalize(`TypeError: Cannot read properties of null (reading 'map')
    at ProductList (src/components/List.tsx:42:15)`)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = computeFingerprint("browser_js", "TypeError", canonical, "http://localhost:3000")
	}
}

// ── BenchmarkInbox_Ingest ─────────────────────────────────────────────────────

func BenchmarkInbox_Ingest_NewEntry(b *testing.B) {
	inbox := NewInbox("bench-sess")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		inbox.Ingest(makeEntry(fmt.Sprintf("fp-%d", i), SeverityError))
	}
}

func BenchmarkInbox_Ingest_MergeExisting(b *testing.B) {
	inbox := NewInbox("bench-sess")
	entry := makeEntry("fp-merge", SeverityError)
	inbox.Ingest(entry)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		inbox.Ingest(entry)
	}
}

// ── BenchmarkFlowController_TryPing ──────────────────────────────────────────

func BenchmarkFlowController_TryPing_Critical(b *testing.B) {
	fc := NewFlowController(DefaultBucketConfigs)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fc.TryPing(SeverityCritical)
	}
}

func BenchmarkFlowController_TryPing_Error(b *testing.B) {
	fc := NewFlowController(DefaultBucketConfigs)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fc.TryPing(SeverityError)
	}
}
