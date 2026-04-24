package incident

import (
	"fmt"
	"testing"
	"time"
)

// ── BenchmarkBus_Fire ─────────────────────────────────────────────────────────
// Target: <500ns/op. Measures the non-blocking fire path under real dispatch.

func BenchmarkBus_Fire(b *testing.B) {
	bus := NewMPSCBus(nil)
	defer bus.Close()
	bus.AddSession("bench-sess", nil, nil, nil)

	ev := NewIncidentEvent(SourceBrowserJS, SeverityError, "TypeError", "bench error", Context{}, nil)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bus.Fire(&ev)
	}
}

// BenchmarkBus_Fire_Saturated measures Fire latency when the inbound channel is
// near-full (drop path exercised). This is the worst-case producer scenario.
func BenchmarkBus_Fire_Saturated(b *testing.B) {
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
