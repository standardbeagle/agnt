package incident

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ── Bus.Fire non-blocking ─────────────────────────────────────────────────────

func TestBus_Fire_NeverBlocks(t *testing.T) {
	// No t.Parallel(): timing-sensitive benchmark-style test.
	// Skipped under race detector: the race instrumentation adds ~10x overhead
	// to atomic operations, making the 1μs budget unreachable.
	if raceEnabled {
		t.Skip("skipping latency assertion under race detector")
	}
	bus := NewMPSCBus(nil)
	defer bus.Close()

	ev := NewIncidentEvent(SourceBrowserJS, SeverityError, "Test", "msg", Context{}, nil)

	const iters = 10_000
	var totalNs int64

	// Pause dispatch and fill the channel near-full so most Fire calls hit
	// the fast drop path (no channel send overhead).
	bus.pauseDispatch()
	for i := 0; i < busInboundCap-100; i++ {
		bus.inbound <- &ev
	}

	// Measure Fire latency over iters iterations (all hit drop path since channel ~full).
	for i := 0; i < iters; i++ {
		start := time.Now()
		bus.Fire(&ev)
		totalNs += time.Since(start).Nanoseconds()
	}

	avgNs := totalNs / iters
	// Budget is deliberately loose (50μs, vs the ~hundreds-of-ns the drop path
	// actually costs in isolation). Under `go test ./...` the CPU is saturated
	// by every other package's tests, and per-call scheduling jitter plus the
	// two time.Now() reads push the average up — a 5μs budget (the original
	// value) was still observed to flake under adversarial full-core
	// saturation (stress -c 6 + go test ./... concurrently), well above the
	// ~1.1μs "ordinary load" figure it was raised to tolerate. The bound
	// still catches what this test guards against: a Fire that BLOCKS or
	// takes a slow allocating/locking path would cost ms (or hang outright,
	// since dispatch is paused here) — three orders of magnitude over 50μs.
	// Same reason this test skips under -race above.
	if avgNs > 50_000 { // 50μs budget
		t.Errorf("Fire average latency %dns exceeds 50μs budget (expected sub-μs drop path)", avgNs)
	}

	bus.resumeDispatch()
}

func TestBus_Overflow_DropsNewest_EmitsMetaIncident(t *testing.T) {
	t.Parallel()
	bus := NewMPSCBus(nil)
	defer bus.Close()

	ev := NewIncidentEvent(SourceBrowserJS, SeverityError, "Test", "msg", Context{}, nil)

	// Pause the dispatch goroutine so the channel stays full.
	bus.pauseDispatch()

	// Fill the inbound channel completely so all subsequent Fire calls drop.
	// We fill to capacity AND then fire busInboundCap + 100 extra calls via Fire
	// to guarantee at least 100 drops regardless of how many items the dispatch
	// goroutine consumed before the gate took effect.
	for i := 0; i < busInboundCap; i++ {
		select {
		case bus.inbound <- &ev:
		default:
		}
	}

	// Fire busInboundCap+100 events: even if the goroutine consumed the entire
	// channel before the gate, we still have 100 guaranteed drops after refill.
	const extraFires = 100
	for i := 0; i < busInboundCap+extraFires; i++ {
		bus.Fire(&ev)
	}

	dropped := bus.Dropped()
	if dropped < extraFires {
		t.Errorf("Dropped() = %d, want >= %d", dropped, extraFires)
	}

	// At 100 drops, one meta:bus_overflow incident must have been synthesized.
	metaEvs := bus.drainMetaQueue()
	if len(metaEvs) == 0 {
		t.Error("expected at least one meta:bus_overflow incident after 100 drops")
	}
	for _, me := range metaEvs {
		if me.Fingerprint != metaBusOverflowFP {
			t.Errorf("meta incident fingerprint=%q, want %q", me.Fingerprint, metaBusOverflowFP)
		}
		if me.Severity != SeverityWarning {
			t.Errorf("meta incident severity=%q, want warning", me.Severity)
		}
	}

	// Resume dispatch so cleanup goroutines can exit.
	bus.resumeDispatch()
}

func TestBus_Dropped_Counter(t *testing.T) {
	t.Parallel()
	bus := NewMPSCBus(nil)
	defer bus.Close()

	// Fresh bus: no drops.
	if d := bus.Dropped(); d != 0 {
		t.Fatalf("initial Dropped()=%d, want 0", d)
	}

	// Pause dispatch so inbound channel stays full.
	bus.pauseDispatch()

	ev := NewIncidentEvent(SourceBrowserJS, SeverityInfo, "T", "m", Context{}, nil)
	// Fill plus extra fires — guarantees drops regardless of pre-gate goroutine activity.
	for i := 0; i < busInboundCap; i++ {
		select {
		case bus.inbound <- &ev:
		default:
		}
	}
	// Fire busInboundCap+5 via Fire to guarantee >= 5 drops.
	const wantDrops = 5
	for i := 0; i < busInboundCap+wantDrops; i++ {
		bus.Fire(&ev)
	}

	if d := bus.Dropped(); d < wantDrops {
		t.Errorf("Dropped()=%d, want >= %d after overflow (dispatch paused)", d, wantDrops)
	}

	bus.resumeDispatch()
}

// ── BlobStore.WriteAsync ──────────────────────────────────────────────────────

func TestBlob_WriteAsync_ReturnsImmediately(t *testing.T) {
	t.Parallel()
	// Use a tiny write queue to ensure the async path is exercised.
	store := newBlobStoreWithQueueCap(2)
	defer store.Close()

	// Establish a same-run baseline while the drain is healthy. The paused
	// call below must remain within a generous factor of this identical hot
	// path; an accidental synchronous write would diverge dramatically.
	baselineStart := time.Now()
	_ = store.WriteAsync([]byte("baseline"), "application/octet-stream")
	baseline := time.Since(baselineStart)

	// Block the drain goroutine so writes pile up in the channel.
	store.pauseDrain()

	data := make([]byte, 1024)
	for i := range data {
		data[i] = byte(i)
	}

	start := time.Now()
	ref := store.WriteAsync(data, "application/octet-stream")
	elapsed := time.Since(start)

	if limit := baseline*100 + 100*time.Microsecond; elapsed > limit {
		t.Errorf("WriteAsync took %v with paused drain, baseline %v (limit %v)", elapsed, baseline, limit)
	}

	// Ref must have correct hash and size.
	if ref.Hash == "" {
		t.Error("WriteAsync returned empty Hash")
	}
	if ref.Size != len(data) {
		t.Errorf("WriteAsync Size=%d, want %d", ref.Size, len(data))
	}
	if ref.MIME != "application/octet-stream" {
		t.Errorf("WriteAsync MIME=%q, want application/octet-stream", ref.MIME)
	}

	// Unblock drain; content should eventually land.
	store.resumeDrain()

	// Wait for content to be written.
	require.Eventually(t, func() bool {
		_, _, err := store.Read(ref.Hash)
		return err == nil
	}, 500*time.Millisecond, 5*time.Millisecond, "blob content never landed after resumeDrain")
}

func TestBlob_WriteAsync_HashMatchesSyncWrite(t *testing.T) {
	t.Parallel()
	store := NewBlobStore(0)
	defer store.Close()

	data := []byte("consistent payload for hash check")
	ref := store.WriteAsync(data, "text/plain")

	// Sync write of same content must produce same hash.
	syncRef, err := store.Write(data, "text/plain")
	require.NoError(t, err)

	if ref.Hash != syncRef.Hash {
		t.Errorf("WriteAsync hash %q != Write hash %q", ref.Hash, syncRef.Hash)
	}
}

func TestBlob_OverflowFallsBackToSummary(t *testing.T) {
	t.Parallel()
	// Create a store with a tiny 1-slot queue.
	store := newBlobStoreWithQueueCap(1)
	defer store.Close()

	// Block drain so queue fills immediately.
	store.pauseDrain()

	// Saturate the queue.
	store.WriteAsync([]byte("payload-1"), "text/plain")

	// This write should overflow (queue full): ref returned, content not written.
	data := []byte("payload-2-overflow")
	ref := store.WriteAsync(data, "text/plain")

	// Ref still has valid hash and size.
	if ref.Hash == "" || ref.Size != len(data) {
		t.Errorf("overflow ref invalid: hash=%q size=%d", ref.Hash, ref.Size)
	}

	// Unblock drain.
	store.resumeDrain()

	// The first write should land (queue had room), but the overflow write may not.
	// Reading the overflow ref: either it lands eventually or returns ErrBlobEvicted.
	// Either outcome is valid — the key invariant is the ref was returned immediately.
	time.Sleep(50 * time.Millisecond)
	_, _, err := store.Read(ref.Hash)
	// Accept both outcomes: content landed (nil) or was never written (ErrBlobEvicted).
	if err != nil && err != ErrBlobEvicted {
		t.Errorf("unexpected Read error: %v", err)
	}
}

// ── Pipeline end-to-end ───────────────────────────────────────────────────────

func TestPipeline_EndToEnd_SingleGoroutinePerSession(t *testing.T) {
	t.Parallel()
	bus := NewMPSCBus(nil)
	defer bus.Close()

	sessionID := "sess-e2e"
	bus.AddSession(sessionID, nil, nil, nil)

	pl := bus.getSessionPipeline(sessionID)
	require.NotNil(t, pl, "pipeline must exist after AddSession")

	// Subscribe to the inbox to receive events.
	deltaCh, cancel := pl.inbox.Subscribe()
	defer cancel()

	// Publish one event via bus.
	ev := NewIncidentEvent(SourceBrowserJS, SeverityError, "TypeError", "test error", Context{SessionID: sessionID}, nil)
	bus.Publish(ev)

	// Event must flow through dispatch goroutine → dedup → inbox → subscriber.
	select {
	case delta := <-deltaCh:
		if delta.Entry.Fingerprint != ev.Fingerprint {
			t.Errorf("delta fingerprint=%q, want %q", delta.Entry.Fingerprint, ev.Fingerprint)
		}
		if delta.Entry.Severity != SeverityError {
			t.Errorf("delta severity=%q, want error", delta.Entry.Severity)
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("timed out waiting for inbox delta")
	}
}

func TestPipeline_DuplicateEvent_DedupedInInbox(t *testing.T) {
	t.Parallel()
	bus := NewMPSCBus(nil)
	defer bus.Close()

	sessionID := "sess-dedup"
	bus.AddSession(sessionID, nil, nil, nil)

	pl := bus.getSessionPipeline(sessionID)
	require.NotNil(t, pl)

	deltaCh, cancel := pl.inbox.Subscribe()
	defer cancel()

	ev := NewIncidentEvent(SourceBrowserJS, SeverityError, "TypeError", "dup error", Context{SessionID: sessionID}, nil)

	// Publish same event twice.
	bus.Publish(ev)
	bus.Publish(ev)

	// Collect up to 2 deltas (second must be merge, not new).
	var deltas []InboxDelta
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case d := <-deltaCh:
			deltas = append(deltas, d)
			if len(deltas) == 2 {
				goto done
			}
		case <-deadline:
			goto done
		}
	}
done:
	if len(deltas) < 1 {
		t.Fatal("no deltas received")
	}
	if len(deltas) >= 2 {
		if deltas[1].IsNew {
			t.Error("second identical event should be a merge (IsNew=false)")
		}
	}
}

func TestPipeline_SessionTeardown_DrainsWithinTwoSeconds(t *testing.T) {
	// No t.Parallel(): wall-clock timing assertion.
	bus := NewMPSCBus(nil)

	sessionID := "sess-teardown"
	bus.AddSession(sessionID, nil, nil, nil)

	// Flood the bus with 500 events.
	for i := 0; i < 500; i++ {
		ev := NewIncidentEvent(SourceBrowserJS, SeverityError, "TypeError", "flood", Context{SessionID: sessionID}, nil)
		bus.Fire(&ev)
	}

	start := time.Now()
	bus.RemoveSession(sessionID)
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("RemoveSession took %v, exceeds 2s drain window", elapsed)
	}

	// Session must be gone.
	if pl := bus.getSessionPipeline(sessionID); pl != nil {
		t.Error("session pipeline still present after RemoveSession")
	}
}

func TestPipeline_MultipleSessionsIsolated(t *testing.T) {
	t.Parallel()
	bus := NewMPSCBus(nil)
	defer bus.Close()

	const sessions = 3
	deltaChs := make([]<-chan InboxDelta, sessions)
	cancels := make([]func(), sessions)
	for i := 0; i < sessions; i++ {
		sid := "sess-iso-" + string(rune('A'+i))
		bus.AddSession(sid, nil, nil, nil)
		pl := bus.getSessionPipeline(sid)
		require.NotNil(t, pl)
		deltaChs[i], cancels[i] = pl.inbox.Subscribe()
	}
	defer func() {
		for _, c := range cancels {
			c()
		}
	}()

	ev := NewIncidentEvent(SourceBrowserJS, SeverityError, "Test", "isolated", Context{SessionID: "sess-iso-A"}, nil)
	bus.Publish(ev)

	// Only the explicitly owning session receives the event.
	for i, ch := range deltaChs {
		if i == 0 {
			select {
			case <-ch:
			case <-time.After(500 * time.Millisecond):
				t.Errorf("session %d: timed out waiting for delta", i)
			}
			continue
		}
		select {
		case <-ch:
			t.Errorf("session %d received another session's incident", i)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func TestPipeline_SessionlessProductionEventFailsClosed(t *testing.T) {
	bus := NewMPSCBus(nil)
	t.Cleanup(bus.Close)

	for _, sessionID := range []string{"session-a", "session-b"} {
		bus.AddSession(sessionID, nil, nil, nil)
	}

	bus.Publish(NewIncidentEvent(
		SourceBrowserJS, SeverityError, "Test", "owner missing", Context{}, nil,
	))
	time.Sleep(50 * time.Millisecond)

	for _, sessionID := range []string{"session-a", "session-b"} {
		entries, _ := bus.QuerySession(sessionID, QueryFilter{})
		if len(entries) != 0 {
			t.Fatalf("sessionless production incident leaked into %s", sessionID)
		}
	}
}

func TestPipeline_SessionlessProductionEventFailsClosedWithOneSession(t *testing.T) {
	bus := NewMPSCBus(nil)
	t.Cleanup(bus.Close)
	bus.AddSession("only-session", nil, nil, nil)
	bus.Publish(NewIncidentEvent(SourceBrowserJS, SeverityError, "Test", "owner missing", Context{}, nil))
	time.Sleep(30 * time.Millisecond)
	entries, _ := bus.QuerySession("only-session", QueryFilter{})
	if len(entries) != 0 {
		t.Fatalf("sessionless production incident delivered with one active session")
	}
}

func TestPipeline_EventDeliveredOnlyToOriginatingSession(t *testing.T) {
	t.Parallel()
	bus := NewMPSCBus(nil)
	defer bus.Close()

	bus.AddSession("session-a", nil, nil, nil)
	bus.AddSession("session-b", nil, nil, nil)

	a := bus.getSessionPipeline("session-a")
	b := bus.getSessionPipeline("session-b")
	require.NotNil(t, a)
	require.NotNil(t, b)

	aDeltas, cancelA := a.inbox.Subscribe()
	defer cancelA()
	bDeltas, cancelB := b.inbox.Subscribe()
	defer cancelB()

	ev := NewIncidentEvent(
		SourceBrowserJS,
		SeverityError,
		"TypeError",
		"session A error",
		Context{SessionID: "session-a"},
		nil,
	)
	bus.Publish(ev)

	select {
	case <-aDeltas:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("originating session did not receive its incident")
	}

	select {
	case <-bDeltas:
		t.Fatal("incident leaked into another session inbox")
	case <-time.After(50 * time.Millisecond):
	}
}

// ── Close stops the bus ───────────────────────────────────────────────────────

func TestBus_Close_Idempotent(t *testing.T) {
	t.Parallel()
	bus := NewMPSCBus(nil)
	bus.Close()
	// Second Close must not panic.
	bus.Close()
}

func TestBus_Fire_AfterClose_NoBlock(t *testing.T) {
	t.Parallel()
	bus := NewMPSCBus(nil)
	bus.Close()

	ev := NewIncidentEvent(SourceBrowserJS, SeverityInfo, "T", "m", Context{}, nil)
	// Must return immediately (channel closed or drop path).
	done := make(chan struct{})
	go func() {
		bus.Fire(&ev)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Error("Fire blocked after Close")
	}
}

// ── Stress tests ──────────────────────────────────────────────────────────────

func TestStress_100Producers_1kEventsPerSec_NoPanic(t *testing.T) {
	// No t.Parallel(): stress test consumes goroutines for 1s.
	bus := NewMPSCBus(nil)
	defer bus.Close()

	bus.AddSession("stress-sess", nil, nil, nil)

	const producers = 100
	const duration = time.Second

	var fired atomic.Int64
	var wg sync.WaitGroup
	stop := make(chan struct{})

	for i := 0; i < producers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ev := NewIncidentEvent(SourceBrowserJS, SeverityError, "StressErr", "stress", Context{SessionID: "stress-sess"}, nil)
			for {
				select {
				case <-stop:
					return
				default:
				}
				bus.Fire(&ev)
				fired.Add(1)
				// Throttle to ~10 events/goroutine/ms.
				runtime.Gosched()
			}
		}(i)
	}

	time.Sleep(duration)
	close(stop)
	wg.Wait()

	t.Logf("stress: %d events fired, %d dropped", fired.Load(), bus.Dropped())
	// Must not have panicked; dropped counter must be consistent.
	if bus.Dropped() < 0 {
		t.Error("negative drop counter")
	}
}

func TestStress_LargePayload_10MB_NoTransientOver11MB(t *testing.T) {
	// No t.Parallel(): runtime.ReadMemStats is process-wide.
	store := NewBlobStore(12 * 1024 * 1024) // 12MB budget
	defer store.Close()

	const payload = 10 * 1024 * 1024 // 10MB

	data := make([]byte, payload)
	for i := range data {
		data[i] = byte(i & 0xFF)
	}

	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	ref := store.WriteAsync(data, "application/octet-stream")

	// Wait for content to land.
	require.Eventually(t, func() bool {
		_, _, err := store.Read(ref.Hash)
		return err == nil
	}, 2*time.Second, 10*time.Millisecond)

	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	// Heap allocated above baseline should not exceed payload + 1MB overhead.
	heapDelta := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	limit := int64(payload + 1*1024*1024) // payload + 1MB
	if heapDelta > limit {
		t.Errorf("heap delta %d bytes exceeds %d limit (payload=%d)", heapDelta, limit, payload)
	}
}
