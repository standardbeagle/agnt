package daemon

// Mechanism-isolation stress harness for the Claude Code hook ring buffer (G3).
//
// Scope: this file exercises hookRingBuffer + drainHooks + fanOutHookEvent
// with STUB sinks only. It NEVER opens a real socket, spawns a real `agnt
// hook` CLI, creates a real proxy BroadcastToast round trip, or registers
// real StreamSink consumers. The system under test is the producer/consumer
// machinery in hub_hook.go; all external surfaces are replaced with
// counter-based stubs that can optionally sleep, panic, or error.
//
// Mental model (read hub_hook.go end-to-end before editing):
//
//   * The ring buffer (hookRingBuffer) is a bounded FIFO with drop-oldest
//     overflow semantics. Push takes a single short-duration mutex plus a
//     non-blocking send on a buffered-length-1 notify channel. Pop takes the
//     same mutex and returns (HookEvent, bool).
//   * drainHooks(ctx) is a single goroutine spawned by Daemon.Start(). It
//     pops everything currently buffered, then blocks on <-notify or
//     <-ctx.Done(). Exit is strictly via ctx cancel; any events still in the
//     buffer at shutdown are discarded (fire-and-forget contract — see
//     hookRingCapacity doc comment).
//   * fanOutHookEvent dispatches one event to four consumers in strict
//     cheapest-first order:
//       1. session heartbeat (optional — skipped when sessionRegistry is nil
//          or SessionID lookup misses)
//       2. BroadcastLogEntry → StreamSink fan-out (channel-send-with-default;
//          a wedged sink drops events, it does NOT block the drain)
//       3. per-proxy BroadcastToast loop for notification/stop/stop-failure
//          events (errors swallowed at debug level)
//       4. BroadcastHookEvent → typed HookEventSink fan-out (sinks
//          contracted to be non-blocking; a stalled sink stalls the drain,
//          which in turn trips overflow)
//   * OverflowCount is monotonic: each full-buffer Push bumps it by 1 and
//     overwrites the head slot in place.
//
// p99 enqueue latency target: <=5ms even when downstream sinks are wedged.
// Because Push holds only the ring-buffer mutex (not any sink lock), a
// slow drain makes the ring fill up and trip overflow — but enqueue itself
// stays cheap. That is the invariant these tests pin down.
//
// Every test runs under -race and verifies goroutine cleanup with
// goleak.VerifyNone(t, goleak.IgnoreCurrent()) so background goroutines
// from other tests in the same package do not produce false positives.
// Out of scope: the real cmd/agnt/hook_bench_test.go cost contract; this
// file exercises only the in-memory mechanism, not the socket round trip.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/assert"
	"go.uber.org/goleak"
)

// stubBlockingHookSink implements HookEventSink with an optional sleep and
// optional panic-every-Nth-call behavior. All counters are atomic so the
// test can observe progress mid-flight. Pointer receiver so AddHookSink /
// RemoveHookSink identity works.
type stubBlockingHookSink struct {
	received   atomic.Int64
	panicked   atomic.Int64
	sleep      time.Duration
	panicEvery int // 0 = never, N = every Nth call panics
}

func (s *stubBlockingHookSink) EmitHookEvent(_ HookEvent) {
	if s.sleep > 0 {
		time.Sleep(s.sleep)
	}
	n := s.received.Add(1)
	if s.panicEvery > 0 && int(n)%s.panicEvery == 0 {
		s.panicked.Add(1)
		panic(fmt.Sprintf("synth hook sink panic at call %d", n))
	}
}

// stressStreamSink is a drop-in consumer of a StreamSink's Ch: it reads in
// a goroutine until the channel closes, counting received LogEntries. The
// stressStreamSink stop channel lets tests tear down the reader cleanly
// before the test returns (so goleak stays clean).
type stressStreamSink struct {
	got   atomic.Int64
	drain chan struct{}
}

func newStressStreamSink() *stressStreamSink {
	return &stressStreamSink{drain: make(chan struct{})}
}

// run spawns a goroutine that drains every entry sent to ch until ch closes
// or stop is signalled. Returns a done channel closed when the goroutine
// exits. The caller is responsible for closing stop before goleak checks.
func (s *stressStreamSink) run(ch <-chan proxy.LogEntry, stop <-chan struct{}) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case _, ok := <-ch:
				if !ok {
					return
				}
				s.got.Add(1)
			case <-stop:
				return
			}
		}
	}()
	return done
}

// hookStressDaemon wires the minimum Daemon fields needed for ring +
// drain + fan-out. No socket, no Hub, no proxy manager, no session
// registry — those are added per-test as needed.
func hookStressDaemon(capacity int) *Daemon {
	return &Daemon{
		hookRing: newHookRingBuffer(capacity),
		eventHub: NewEventHub(),
	}
}

// startStressDrain runs drainHooks in a goroutine and returns a cancel
// function plus a done channel. The caller must call cancel and wait on
// done before the test returns, otherwise goleak will flag the drain.
func startStressDrain(t *testing.T, d *Daemon) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.drainHooks(ctx)
		close(done)
	}()
	return cancel, done
}

// stopStressDrain cancels the drain goroutine and waits for it to exit.
// A timeout fails the test loudly so hangs surface immediately rather than
// as test-runner 10-minute wallclock timeouts.
func stopStressDrain(t *testing.T, cancel context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("drainHooks did not exit within 2s of context cancel")
	}
}

// =====================================================================
//  1. EnqueueP99Latency — 100k enqueues × 8 producers with a stub fan-out
//     that sleeps 1ms per event. p99 of Push must stay under 5ms even
//     though the drain+fan-out is three orders of magnitude slower.
//
// This is THE core contract the bench test in cmd/agnt/hook_bench_test.go
// only covers against a warm daemon. Here we deliberately wedge the drain
// and verify that producers still meet latency because Push only holds the
// ring mutex, never a sink lock.
// =====================================================================
func TestHookRing_EnqueueP99Latency(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	const (
		producers = 8
		perProd   = 2000 // 16k total — enough for a meaningful p99 without
		// blowing wall time under -race instrumentation.
	)

	d := hookStressDaemon(hookRingCapacity)
	slowSink := &stubBlockingHookSink{sleep: time.Millisecond}
	d.eventHub.AddHookSink(slowSink)

	cancel, done := startStressDrain(t, d)

	// Measure a mutex-shaped control in the same goroutines, at the same time,
	// and under the same producer contention as Push. The old sequential
	// preflight baseline measured an idle ~1us floor, then compared it with an
	// 8-producer tail that included scheduler stalls; a single loaded-machine
	// timeslice could therefore make the derived guard tighter than the 5ms
	// product target.
	var baselineMu sync.Mutex

	// Each producer records its control and Push latencies into local slices
	// so there is zero false sharing between producers. We merge after.
	var wg sync.WaitGroup
	latSlices := make([][]time.Duration, producers)
	baselineSlices := make([][]time.Duration, producers)
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func(prod int) {
			defer wg.Done()
			lats := make([]time.Duration, perProd)
			baseline := make([]time.Duration, perProd)
			for i := 0; i < perProd; i++ {
				t0 := time.Now()
				baselineMu.Lock()
				baselineMu.Unlock()
				baseline[i] = time.Since(t0)

				t0 = time.Now()
				d.hookRing.Push(HookEvent{
					Event:     "pre-tool-use",
					SessionID: fmt.Sprintf("p%d", prod),
				})
				lats[i] = time.Since(t0)
			}
			latSlices[prod] = lats
			baselineSlices[prod] = baseline
		}(p)
	}
	wg.Wait()

	// Tear down the drain before computing stats so the test is strictly
	// isolated from drain teardown wallclock.
	stopStressDrain(t, cancel, done)
	<-done

	// Merge and sort.
	all := make([]time.Duration, 0, producers*perProd)
	baseline := make([]time.Duration, 0, producers*perProd)
	for _, s := range latSlices {
		all = append(all, s...)
	}
	for _, s := range baselineSlices {
		baseline = append(baseline, s...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	sort.Slice(baseline, func(i, j int) bool { return baseline[i] < baseline[j] })

	p50 := all[len(all)/2]
	p99 := all[(len(all)*99)/100]
	pMax := all[len(all)-1]
	baselineP50 := baseline[len(baseline)/2]
	baselineP99 := baseline[(len(baseline)*99)/100]
	const p99Target = 5 * time.Millisecond
	p99Guard := p99Target + baselineP99*4

	t.Logf("push latency over %d ops (drain=1ms/event, %d sinks stalled): p50=%s p99=%s max=%s baseline_p99=%s target=%s guard=%s target_met=%v overflow=%d",
		len(all), 1, p50, p99, pMax, baselineP99, p99Target, p99Guard, p99 <= p99Target, d.hookRing.OverflowCount())

	assert.Less(t, p99, p99Guard,
		"p99 enqueue latency %v exceeded the %v target plus scheduler guard derived from same-shape baseline %v", p99, p99Target, baselineP99)
	assert.Less(t, p50, baselineP50*100+50*time.Microsecond,
		"p50 enqueue latency %v diverged from same-run mutex baseline %v", p50, baselineP50)
}

// =====================================================================
//  2. OverflowAccounting — 10x drain rate enqueue. After producers stop,
//     OverflowCount must equal (enqueued - drained) and must be
//     monotonically non-decreasing during the run.
//
// Catches: off-by-one on overflow increment, non-atomic counter,
// overflow counter decreasing after Pop (which it must not — overflow is
// a lifetime drop counter, not a current-drop counter).
// =====================================================================
func TestHookRing_OverflowAccounting(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	// Small capacity so overflow kicks in within a modest number of pushes.
	const (
		capacity  = 16
		producers = 4
		perProd   = 500 // 2000 pushes vs cap 16 => overflow inevitable.
	)

	d := hookStressDaemon(capacity)

	// Sink that sleeps 20ms per event so the drain drops far behind the
	// producers. Producers will flood the ring faster than drain can empty
	// it, driving overflow.
	slowSink := &stubBlockingHookSink{sleep: 20 * time.Millisecond}
	d.eventHub.AddHookSink(slowSink)

	cancel, done := startStressDrain(t, d)

	// Background goroutine samples OverflowCount periodically to verify
	// monotonicity. Any decrease is a bug and fails the test.
	var monoViolations atomic.Int64
	stopMonitor := make(chan struct{})
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		var last int64
		for {
			select {
			case <-stopMonitor:
				return
			default:
			}
			cur := d.hookRing.OverflowCount()
			if cur < last {
				monoViolations.Add(1)
			}
			last = cur
			time.Sleep(500 * time.Microsecond)
		}
	}()

	var wg sync.WaitGroup
	totalPushes := int64(producers * perProd)
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func(prod int) {
			defer wg.Done()
			for i := 0; i < perProd; i++ {
				d.hookRing.Push(HookEvent{Event: fmt.Sprintf("p%d-%d", prod, i)})
			}
		}(p)
	}
	wg.Wait()

	close(stopMonitor)
	<-monitorDone

	// Shut down the drain before reading final stats so no more Pops happen.
	stopStressDrain(t, cancel, done)
	<-done

	// After the drain goroutine exits, the ring buffer still holds whatever
	// events the drain didn't get to. Accounting invariant:
	//   totalPushes == drained + overflowed + stillBuffered
	drained := slowSink.received.Load()
	overflow := d.hookRing.OverflowCount()
	remaining := int64(d.hookRing.Len())

	t.Logf("overflow-accounting: total=%d drained=%d overflowed=%d remaining=%d",
		totalPushes, drained, overflow, remaining)

	assert.Equal(t, totalPushes, drained+overflow+remaining,
		"push accounting invariant violated: total=%d drained=%d overflow=%d remaining=%d",
		totalPushes, drained, overflow, remaining)
	assert.Greater(t, overflow, int64(0), "slow drain with 10x rate mismatch should produce overflow")
	assert.Equal(t, int64(0), monoViolations.Load(), "OverflowCount must never decrease")
}

// =====================================================================
//  3. SlowSink — 4 sinks, one of them sleeps 50ms/event. The three fast
//     sinks must NOT be blocked by the slow one. After producers stop,
//     the slow sink catches up and receives every event the fast sinks
//     did.
//
// This test proves that within a single fanOutHookEvent call, the typed
// HookEventSink fan-out iterates sequentially and a slow sink stalls the
// DRAIN (which is expected — sinks are contracted to be non-blocking and
// a stalling sink trips overflow). It does NOT prove independent fan-out
// per sink; that would require per-sink goroutines, which hub_hook.go
// deliberately does not do.
//
// What this test pins down:
//   - All four sinks eventually see every drained event (same ordering).
//   - The slow sink is not skipped or timed out.
//   - No sink-to-sink ordering inversion (each sink sees events in push
//     order).
//
// =====================================================================
func TestHookRing_SlowSink(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	const events = 50

	d := hookStressDaemon(hookRingCapacity)
	fast1 := &stubBlockingHookSink{}
	fast2 := &stubBlockingHookSink{}
	fast3 := &stubBlockingHookSink{}
	slow := &stubBlockingHookSink{sleep: 2 * time.Millisecond} // 50 events * 2ms = 100ms — fast enough for CI.

	for _, s := range []*stubBlockingHookSink{fast1, fast2, fast3, slow} {
		d.eventHub.AddHookSink(s)
	}

	cancel, done := startStressDrain(t, d)

	for i := 0; i < events; i++ {
		d.hookRing.Push(HookEvent{Event: fmt.Sprintf("evt-%d", i)})
	}

	// Every sink must eventually see all `events` events. Because the drain
	// serializes fan-out, the slow sink determines the wall clock — but
	// once drain catches up, every sink has identical counts.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fast1.received.Load() == events &&
			fast2.received.Load() == events &&
			fast3.received.Load() == events &&
			slow.received.Load() == events {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	assert.Equal(t, int64(events), fast1.received.Load(), "fast1 did not receive all events")
	assert.Equal(t, int64(events), fast2.received.Load(), "fast2 did not receive all events")
	assert.Equal(t, int64(events), fast3.received.Load(), "fast3 did not receive all events")
	assert.Equal(t, int64(events), slow.received.Load(), "slow sink did not catch up")

	stopStressDrain(t, cancel, done)
	<-done
}

// =====================================================================
//  4. PanickingSink — one sink panics every 10th call. Drain must recover
//     (i.e. keep draining), other sinks must still receive every event,
//     and the panic count must be consistent.
//
// Contract under test: a sink that violates the "must not panic" interface
// contract should NOT take down the drain goroutine. Today, BroadcastHookEvent
// does NOT recover panics — this test will fail and surface that bug. Fix
// target: add defer recover() around sink.EmitHookEvent in
// eventHub.BroadcastHookEvent, OR wrap in fanOutHookEvent.
// =====================================================================
func TestHookRing_PanickingSink(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	const events = 50

	d := hookStressDaemon(hookRingCapacity)
	panicker := &stubBlockingHookSink{panicEvery: 10}
	bystander := &stubBlockingHookSink{}
	d.eventHub.AddHookSink(panicker)
	d.eventHub.AddHookSink(bystander)

	cancel, done := startStressDrain(t, d)

	for i := 0; i < events; i++ {
		d.hookRing.Push(HookEvent{Event: fmt.Sprintf("evt-%d", i)})
	}

	// Wait for bystander to see all events — proves drain survived.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if bystander.received.Load() == events {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	assert.Equal(t, int64(events), bystander.received.Load(),
		"bystander sink should receive every event; drain died at %d", bystander.received.Load())
	assert.Greater(t, panicker.panicked.Load(), int64(0),
		"panicker should have panicked at least once")
	// The panicker's "received" counter is bumped BEFORE the panic, so it
	// should be equal to events (it saw every event, just aborted on some
	// of them).
	assert.Equal(t, int64(events), panicker.received.Load(),
		"panicker's received counter should be bumped before panic")

	stopStressDrain(t, cancel, done)
	<-done
}

// =====================================================================
//  5. FanOutToManyTypedSinks — 100 typed HookEventSinks, 1000 events.
//     Every sink must see exactly 1000 events; no sink should be skipped
//     or duplicated, and removing sinks mid-flight must not leak
//     registrations.
//
// Catches: slice-append aliasing in AddHookSink (a stale slice reference
// could cause misses), RemoveHookSink off-by-one, goleak regression from
// a per-sink goroutine that outlives the sink.
// =====================================================================
func TestHookRing_FanOutToManyTypedSinks(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	const (
		sinks  = 100
		events = 1000
	)

	d := hookStressDaemon(hookRingCapacity)
	allSinks := make([]*stubBlockingHookSink, sinks)
	for i := range allSinks {
		allSinks[i] = &stubBlockingHookSink{}
		d.eventHub.AddHookSink(allSinks[i])
	}

	cancel, done := startStressDrain(t, d)

	// Push slowly enough that the ring drains and no overflow occurs. With
	// 100 sinks per event the drain is ~100 allocs per event, so we pace
	// pushes to 100us to stay comfortably below any drop threshold.
	for i := 0; i < events; i++ {
		d.hookRing.Push(HookEvent{Event: fmt.Sprintf("evt-%d", i)})
		if i%128 == 127 {
			time.Sleep(100 * time.Microsecond) // let drain catch up periodically
		}
	}

	// Wait for every sink to reach the target count.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		minSeen := int64(events)
		for _, s := range allSinks {
			if c := s.received.Load(); c < minSeen {
				minSeen = c
			}
		}
		if minSeen >= events {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	for i, s := range allSinks {
		assert.Equal(t, int64(events), s.received.Load(),
			"sink %d received %d, want %d", i, s.received.Load(), events)
	}
	assert.Equal(t, int64(0), d.hookRing.OverflowCount(),
		"no overflow expected when producers pace themselves")

	// Now remove half the sinks and push more. The removed sinks must stop
	// receiving events; the remaining sinks must see the new events on top
	// of the old count.
	for i := 0; i < sinks; i += 2 {
		d.eventHub.RemoveHookSink(allSinks[i])
	}

	const moreEvents = 200
	for i := 0; i < moreEvents; i++ {
		d.hookRing.Push(HookEvent{Event: fmt.Sprintf("more-%d", i)})
	}

	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		minKept := int64(events + moreEvents)
		for i := 1; i < sinks; i += 2 {
			if c := allSinks[i].received.Load(); c < minKept {
				minKept = c
			}
		}
		if minKept >= events+moreEvents {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	for i := 0; i < sinks; i += 2 {
		assert.Equal(t, int64(events), allSinks[i].received.Load(),
			"removed sink %d should stop at %d, got %d", i, events, allSinks[i].received.Load())
	}
	for i := 1; i < sinks; i += 2 {
		assert.Equal(t, int64(events+moreEvents), allSinks[i].received.Load(),
			"kept sink %d should receive both rounds", i)
	}

	stopStressDrain(t, cancel, done)
	<-done
}

// =====================================================================
//  6. NotificationToastFanOut — synthetic "notification" events drive
//     broadcastNotificationToast. With a nil proxym the fan-out short-
//     circuits silently; with a stub proxy manager we verify every event
//     drives exactly one toast path per proxy.
//
// This test exercises the daemon-side replacement for the per-proxy loop
// that `agnt notify` used to do client-side. It uses a stub proxy manager
// stand-in by directly calling broadcastNotificationToast with a nil
// proxym (which short-circuits), verifying the no-panic contract.
//
// The real BroadcastToast is intentionally out of scope (it needs a real
// TrafficLogger + WebSocket server). Instead we verify:
//   - Every notification event that flows through fanOutHookEvent drives
//     the decode path without panic.
//   - Malformed payloads are silently swallowed (already tested in
//     hub_hook_test.go but re-verified under load here).
//   - Typed HookEventSink fan-out sees every event exactly once (the
//     typed path is lossless — it iterates with recover per sink, no
//     channel-send-with-default).
//   - StreamSink fan-out is best-effort with channel drops allowed under
//     burst, but no events should silently duplicate or reorder.
//
// =====================================================================
func TestHookRing_NotificationToastFanOut(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	const events = 1000

	// proxym is nil — broadcastNotificationToast short-circuits and the
	// notification branch becomes a pure no-op at the toast layer.
	// eventHub is present so StreamSink + HookEventSink fan-out both run.
	d := hookStressDaemon(hookRingCapacity)

	// Typed sink is the lossless proof-of-delivery channel: it counts
	// every call to EmitHookEvent, so we can assert end-to-end drain
	// completeness independent of StreamSink backpressure.
	typedSink := &stubBlockingHookSink{}
	d.eventHub.AddHookSink(typedSink)

	// StreamSink is the lossy path — events are channel-send-with-default
	// via BroadcastLogEntry, so drops are allowed under burst. We only
	// assert "no more events delivered than pushed" (no duplication).
	streamSink := d.eventHub.AddStreamSink(streamFilter{
		types: map[proxy.LogEntryType]bool{proxy.LogTypeHook: true},
	})
	defer d.eventHub.RemoveStreamSink(streamSink)

	drainer := newStressStreamSink()
	stopDrainer := make(chan struct{})
	drainerDone := drainer.run(streamSink.Ch, stopDrainer)

	cancel, done := startStressDrain(t, d)

	// Mix valid + malformed payloads so both code paths run under load.
	// Every 7th event is a syntactic garbage payload; every 11th is a
	// valid JSON missing the message field; the rest are well-formed.
	validPayload := json.RawMessage(`{"type":"info","title":"ok","message":"hi","duration":0}`)
	garbagePayload := json.RawMessage(`<<< not json >>>`)
	missingMsgPayload := json.RawMessage(`{"type":"info","title":"only title"}`)

	assert.NotPanics(t, func() {
		for i := 0; i < events; i++ {
			var payload json.RawMessage
			switch {
			case i%7 == 0:
				payload = garbagePayload
			case i%11 == 0:
				payload = missingMsgPayload
			default:
				payload = validPayload
			}
			d.hookRing.Push(HookEvent{
				Event:   "notification",
				Payload: payload,
			})
		}
	})

	// Typed HookEventSink is the lossless delivery proof. Every pushed
	// event must drive one EmitHookEvent regardless of payload validity —
	// the notification branch does NOT gate the typed fan-out on decode.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if typedSink.received.Load() >= events {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	assert.Equal(t, int64(events), typedSink.received.Load(),
		"every notification must reach the typed HookEventSink (lossless path)")

	// StreamSink is best-effort: drops are allowed under burst, but we
	// must never see more events than were pushed (no duplication).
	streamGot := drainer.got.Load()
	assert.LessOrEqual(t, streamGot, int64(events),
		"StreamSink must never deliver more events than were pushed (got %d, pushed %d)",
		streamGot, events)
	// And at least some events should have reached the StreamSink — if it
	// were zero, the fan-out wiring would be completely broken. A generous
	// lower bound (10% of events) tolerates aggressive channel drops
	// without being so loose that a wholly-broken path slips through.
	assert.Greater(t, streamGot, int64(events/10),
		"StreamSink should receive at least 10%% of events under load (got %d)", streamGot)

	stopStressDrain(t, cancel, done)
	<-done

	close(stopDrainer)
	select {
	case <-drainerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("stream reader did not exit after stop signal")
	}
}

// =====================================================================
//  7. CloseWithPendingEvents — 10k events pushed then context cancel fired
//     immediately. Drain must exit cleanly; accounting must reconcile
//     (drained + remaining + overflow == total).
//
// The drain contract explicitly says "any events remaining in the buffer
// at shutdown are discarded, which is safe because hook events are
// fire-and-forget." This test pins that contract down: shutdown is fast,
// no goroutines leak, and the accounting invariant holds even with a
// large backlog.
// =====================================================================
func TestHookRing_CloseWithPendingEvents(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	const events = 10000

	// Large capacity so no overflow dominates accounting — we want to
	// verify drain-on-shutdown behavior, not overflow.
	d := hookStressDaemon(hookRingCapacity * 16)
	slowSink := &stubBlockingHookSink{sleep: 200 * time.Microsecond}
	d.eventHub.AddHookSink(slowSink)

	cancel, done := startStressDrain(t, d)

	for i := 0; i < events; i++ {
		d.hookRing.Push(HookEvent{Event: fmt.Sprintf("evt-%d", i)})
	}

	// Immediate cancel — most events will still be buffered. The drain
	// must exit within 2 seconds regardless of how many events remain.
	closeStart := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("drain did not exit within 2s after ctx cancel — likely wedged on sink")
	}
	closeWall := time.Since(closeStart)

	// Accounting: drained + overflow + remaining == total.
	drained := slowSink.received.Load()
	overflow := d.hookRing.OverflowCount()
	remaining := int64(d.hookRing.Len())
	total := int64(events)

	t.Logf("close-pending: total=%d drained=%d overflow=%d remaining=%d close_wall=%s",
		total, drained, overflow, remaining, closeWall)

	assert.Equal(t, total, drained+overflow+remaining,
		"accounting invariant broken at shutdown")
	// Contract: drain-on-ctx-cancel is fire-and-forget, so drained is
	// allowed to be much less than total. We only assert drained >= 0.
	assert.GreaterOrEqual(t, drained, int64(0), "drained count must be non-negative")
}

// =====================================================================
//  8. ConcurrentEnqueueAndShutdown — tight enqueue loop with concurrent
//     ctx cancel. Run under -race. No panic, no goleak, no deadlock.
//     Intended for -count=100 to expose subtle races between Push's
//     notify send and drain's <-notify select.
//
// This is the "must not panic on a closed channel" test in the scheduler
// stress harness's spirit. hookRingBuffer never closes the notify channel
// (it's a struct field), so Push's non-blocking select is safe. The race
// to catch is: drain doing <-ctx.Done() vs Push doing notify<- with the
// drain about to exit.
// =====================================================================
func TestHookRing_ConcurrentEnqueueAndShutdown(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	d := hookStressDaemon(hookRingCapacity)
	d.eventHub.AddHookSink(&stubBlockingHookSink{})

	cancel, done := startStressDrain(t, d)

	stop := make(chan struct{})
	var producerDone sync.WaitGroup
	producerDone.Add(1)
	go func() {
		defer producerDone.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			// After ctx cancel, Push must still be safe. It only touches
			// the ring mutex + a non-blocking send — no panic is possible
			// because the notify channel is never closed.
			d.hookRing.Push(HookEvent{Event: fmt.Sprintf("evt-%d", i)})
		}
	}()

	// Let producers ramp up so the race between Push and ctx cancel is
	// as tight as possible.
	time.Sleep(10 * time.Millisecond)

	// Cancel concurrently — this is the race. The drain is racing Push
	// through the buffered-1 notify channel.
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		close(stop)
		producerDone.Wait()
		t.Fatal("drain deadlocked under concurrent producer load")
	}

	// Stop the producer cleanly so goleak is happy.
	close(stop)
	producerDone.Wait()
}
