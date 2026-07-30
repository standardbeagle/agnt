package daemon

// Mechanism-isolation stress harness for AutostartManager (G1).
//
// Scope: this file exercises the public AutostartManager API with synthetic
// AutostartStartFunc implementations only. It NEVER touches RunAutostart,
// ProcessManager, ProxyManager, sessions, sockets, or disk I/O. The goal is
// to surface races and contract violations in the producer/consumer machinery
// in run() that have been intermittently surfacing as flakes in the
// integration test TestSessionRegister_ProgressSnapshotIncludesHistory.
//
// Every test runs under -race and verifies goroutine cleanup with
// verifyNoLeaks(t) (per-test, baselined with IgnoreCurrent + os/exec-worker filters so we tolerate
// any pre-existing daemon background goroutines spawned by other tests in
// the same package).

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// synthStartFn returns an AutostartStartFunc that emits `events` progress
// events with optional sleep between each, and a synthetic failed event every
// `errorEvery` iterations (0 to disable). All sends are blocking — the test
// is verifying the AutostartManager's drain mechanism, not the production
// emitProgress non-blocking behavior. Honors ctx.Done at every step.
func synthStartFn(events int, sleepBetween time.Duration, errorEvery int) AutostartStartFunc {
	return func(ctx context.Context, progress chan<- AutostartProgress) *AutostartResult {
		for i := 0; i < events; i++ {
			select {
			case <-ctx.Done():
				return &AutostartResult{}
			case progress <- AutostartProgress{
				Phase:  PhaseScriptStarting,
				Script: fmt.Sprintf("s%d", i),
				Layer:  i,
			}:
			}
			if errorEvery > 0 && i > 0 && i%errorEvery == 0 {
				select {
				case <-ctx.Done():
					return &AutostartResult{}
				case progress <- AutostartProgress{
					Phase:  PhaseScriptFailed,
					Script: fmt.Sprintf("s%d", i),
					Layer:  i,
					Err:    errors.New("synth"),
				}:
				}
			}
			if sleepBetween > 0 {
				time.Sleep(sleepBetween)
			}
		}
		return &AutostartResult{Scripts: []string{"synth"}}
	}
}

// panicStartFn returns a startFn that emits one event then panics. The
// AutostartManager.run drain MUST recover so the daemon process is not torn
// down by a single bad startFn implementation.
func panicStartFn() AutostartStartFunc {
	return func(ctx context.Context, progress chan<- AutostartProgress) *AutostartResult {
		progress <- AutostartProgress{Phase: PhaseScriptStarting, Script: "boom"}
		panic("synth panic")
	}
}

// pathN returns a synthetic project path. We do NOT use t.TempDir() because
// the manager keys on normalizePath which calls filepath.Abs — pure synthetic
// paths normalize deterministically without touching the filesystem.
func pathN(i int) string {
	return fmt.Sprintf("/synth/path/%d", i)
}

// =====================================================================
// 1. HighThroughput — 100 distinct paths × 1000 events
// =====================================================================
func TestAutostartManager_HighThroughput(t *testing.T) {
	defer verifyNoLeaks(t)()

	const (
		paths      = 100
		perPath    = 1000
		expectPlus = 1 // +1 synthetic PhaseDone per handle
	)

	mgr := NewAutostartManager()
	var startCount atomic.Int32

	startFn := func(ctx context.Context, progress chan<- AutostartProgress) *AutostartResult {
		startCount.Add(1)
		fn := synthStartFn(perPath, 0, 0)
		return fn(ctx, progress)
	}

	handles := make([]*AutostartHandle, paths)
	t0 := time.Now()
	for i := 0; i < paths; i++ {
		handles[i] = mgr.GetOrCreate(pathN(i), startFn)
	}
	for i := 0; i < paths; i++ {
		<-handles[i].Done()
	}
	wall := time.Since(t0)

	assert.Equal(t, int32(paths), startCount.Load(), "startFn should run exactly once per path")

	totalEvents := 0
	for i := 0; i < paths; i++ {
		evs := handles[i].Progress()
		require.Len(t, evs, perPath+expectPlus, "path %d event count", i)
		// First event should be the first synthetic emit, last should be PhaseDone.
		assert.Equal(t, PhaseScriptStarting, evs[0].Phase, "path %d first phase", i)
		assert.Equal(t, 0, evs[0].Layer, "path %d first layer", i)
		assert.Equal(t, PhaseDone, evs[perPath].Phase, "path %d last phase", i)
		// Strict ordering of layer index for the script-emitting events.
		for j := 0; j < perPath; j++ {
			assert.Equal(t, j, evs[j].Layer, "path %d event %d ordering", i, j)
		}
		totalEvents += len(evs)
	}
	assert.Equal(t, paths*(perPath+expectPlus), totalEvents)
	t.Logf("processed %d events across %d paths in %s", totalEvents, paths, wall)
}

// =====================================================================
// 2. ConcurrentGetOrCreate_SameKey — 1000 contenders, exactly 1 wins
// =====================================================================
func TestAutostartManager_ConcurrentGetOrCreate_SameKey(t *testing.T) {
	defer verifyNoLeaks(t)()

	mgr := NewAutostartManager()
	const N = 1000
	path := pathN(0)

	var startCount atomic.Int32
	startFn := func(ctx context.Context, progress chan<- AutostartProgress) *AutostartResult {
		startCount.Add(1)
		// Hold briefly so all contenders enter GetOrCreate while the worker
		// is still running.
		select {
		case <-ctx.Done():
		case <-time.After(50 * time.Millisecond):
		}
		return &AutostartResult{Scripts: []string{"only"}}
	}

	handles := make([]*AutostartHandle, N)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start // line everyone up at the gate
			handles[idx] = mgr.GetOrCreate(path, startFn)
		}(i)
	}
	close(start)
	wg.Wait()

	<-handles[0].Done()

	assert.Equal(t, int32(1), startCount.Load(), "exactly 1 startFn invocation across %d contenders", N)
	for i := 1; i < N; i++ {
		assert.Same(t, handles[0], handles[i], "contender %d must share the winning handle", i)
	}
	require.NotNil(t, handles[0].Result())
	assert.Equal(t, []string{"only"}, handles[0].Result().Scripts)
}

// =====================================================================
// 3. SlowDrain — backpressure observable, no producer panic, no event loss
// =====================================================================
func TestAutostartManager_SlowDrain(t *testing.T) {
	defer verifyNoLeaks(t)()

	const events = 500

	// The drain loop in run() is single-threaded per handle, so we cannot
	// "slow" it from outside. Instead we measure: when the producer emits
	// 500 events with no sleep through a buffer of 64, does the system stay
	// correct (no panic, no loss, monotonic ordering, all events recorded
	// via broadcast)?
	var (
		broadcastMu sync.Mutex
		broadcasts  []AutostartProgress
	)
	mgr := NewAutostartManagerWithBroadcast(func(_ string, ev AutostartProgress) {
		// Inject backpressure: this broadcast callback runs synchronously in
		// the drain loop (per autostart_manager.go contract). A slow callback
		// must not cause event loss because production startFn does blocking
		// sends.
		time.Sleep(50 * time.Microsecond)
		broadcastMu.Lock()
		broadcasts = append(broadcasts, ev)
		broadcastMu.Unlock()
	})

	var producerPanicked atomic.Bool
	startFn := func(ctx context.Context, progress chan<- AutostartProgress) *AutostartResult {
		defer func() {
			if r := recover(); r != nil {
				producerPanicked.Store(true)
			}
		}()
		return synthStartFn(events, 0, 0)(ctx, progress)
	}

	h := mgr.GetOrCreate(pathN(0), startFn)
	<-h.Done()

	assert.False(t, producerPanicked.Load(), "producer must not panic under slow drain")

	// All events recorded in handle history.
	rec := h.Progress()
	require.Len(t, rec, events+1, "handle should have all events + PhaseDone")

	// All events fanned out to broadcast.
	broadcastMu.Lock()
	defer broadcastMu.Unlock()
	require.Len(t, broadcasts, events+1, "broadcast should receive all events + PhaseDone")

	// Ordering preserved end-to-end.
	for i := 0; i < events; i++ {
		assert.Equal(t, i, broadcasts[i].Layer, "broadcast event %d", i)
		assert.Equal(t, i, rec[i].Layer, "handle event %d", i)
	}
	assert.Equal(t, PhaseDone, broadcasts[events].Phase)
	assert.Equal(t, PhaseDone, rec[events].Phase)
}

// =====================================================================
// 4. CancelMidFlight — ctx.Done propagation, clean drain
// =====================================================================
func TestAutostartManager_CancelMidFlight(t *testing.T) {
	defer verifyNoLeaks(t)()

	mgr := NewAutostartManager()

	emittedBeforeCancel := make(chan struct{})
	var ctxObservedCancel atomic.Bool

	startFn := func(ctx context.Context, progress chan<- AutostartProgress) *AutostartResult {
		// Emit a few events immediately so the test has something to wait on.
		for i := 0; i < 5; i++ {
			select {
			case <-ctx.Done():
				ctxObservedCancel.Store(true)
				return &AutostartResult{}
			case progress <- AutostartProgress{
				Phase: PhaseScriptStarting, Script: "s", Layer: i,
			}:
			}
		}
		close(emittedBeforeCancel)

		// Now sleep on ctx.Done forever.
		<-ctx.Done()
		ctxObservedCancel.Store(true)
		return &AutostartResult{}
	}

	h := mgr.GetOrCreate(pathN(0), startFn)

	select {
	case <-emittedBeforeCancel:
	case <-time.After(2 * time.Second):
		t.Fatal("startFn never reached the steady-state await")
	}

	mgr.Cancel(pathN(0))

	select {
	case <-h.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("handle.Done() never fired after Cancel")
	}

	assert.True(t, ctxObservedCancel.Load(), "startFn must observe ctx.Done after Cancel")

	// Late history snapshot includes the 5 pre-cancel events + PhaseDone.
	rec := h.Progress()
	require.GreaterOrEqual(t, len(rec), 6)
	assert.Equal(t, PhaseDone, rec[len(rec)-1].Phase, "last event must be PhaseDone even on cancel")
}

// =====================================================================
// 5. RemoveDuringRun — Remove doesn't cancel; new GetOrCreate creates fresh
// =====================================================================
func TestAutostartManager_RemoveDuringRun(t *testing.T) {
	defer verifyNoLeaks(t)()

	mgr := NewAutostartManager()

	var firstStarted atomic.Bool
	releaseFirst := make(chan struct{})
	startFn1 := func(ctx context.Context, progress chan<- AutostartProgress) *AutostartResult {
		firstStarted.Store(true)
		select {
		case <-ctx.Done():
		case <-releaseFirst:
		}
		progress <- AutostartProgress{Phase: PhaseScriptStarted, Script: "first"}
		return &AutostartResult{Scripts: []string{"first"}}
	}

	h1 := mgr.GetOrCreate(pathN(0), startFn1)

	// Wait for first to be running.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !firstStarted.Load() {
		time.Sleep(5 * time.Millisecond)
	}
	require.True(t, firstStarted.Load(), "first startFn never started")

	// Remove while still running. Per autostart_manager.go contract, Remove
	// does NOT cancel — it only deletes the registry entry. h1 keeps running.
	mgr.Remove(pathN(0))

	// h1 should NOT be done yet (Remove must not cancel).
	select {
	case <-h1.Done():
		t.Fatal("h1.Done fired prematurely — Remove must not cancel")
	case <-time.After(50 * time.Millisecond):
	}

	// Second GetOrCreate with the same key creates a fresh handle distinct
	// from h1 because the registry slot was vacated.
	var secondStarted atomic.Bool
	startFn2 := func(ctx context.Context, progress chan<- AutostartProgress) *AutostartResult {
		secondStarted.Store(true)
		progress <- AutostartProgress{Phase: PhaseScriptStarted, Script: "second"}
		return &AutostartResult{Scripts: []string{"second"}}
	}
	h2 := mgr.GetOrCreate(pathN(0), startFn2)
	assert.NotSame(t, h1, h2, "after Remove, GetOrCreate must produce a fresh handle")

	<-h2.Done()
	assert.True(t, secondStarted.Load(), "second startFn must have run")
	require.NotNil(t, h2.Result())
	assert.Equal(t, []string{"second"}, h2.Result().Scripts)

	// Now release the first run and let it finish too.
	close(releaseFirst)
	select {
	case <-h1.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("h1 never completed after release")
	}
	require.NotNil(t, h1.Result())
	assert.Equal(t, []string{"first"}, h1.Result().Scripts)
}

// =====================================================================
// 6. StartFnPanic — drain recovers, sets Result.Errors, closes Done
//
// BUG FOUND: AutostartManager.run does not recover panics from the worker
// goroutine. A single misbehaving startFn implementation tears down the whole
// daemon process. This test asserts the contract that the manager MUST
// recover and surface the panic as a populated Result with an error string,
// so observers see PhaseDone and handle.Result() != nil.
// =====================================================================
func TestAutostartManager_StartFnPanic(t *testing.T) {
	defer verifyNoLeaks(t)()

	mgr := NewAutostartManager()
	h := mgr.GetOrCreate(pathN(0), panicStartFn())

	select {
	case <-h.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("handle.Done never fired after startFn panic")
	}

	// Result must be non-nil after Done fires (contract: Result is populated
	// once Done is closed, even on panic).
	res := h.Result()
	require.NotNil(t, res, "Result() must be populated after panic, not nil")
	require.NotEmpty(t, res.Errors, "Result.Errors must contain the recovered panic")
	assert.Contains(t, res.Errors[0], "synth panic", "panic message should be surfaced")

	// Progress should still include the one event emitted before the panic
	// plus the synthetic PhaseDone.
	evs := h.Progress()
	require.GreaterOrEqual(t, len(evs), 2, "must have pre-panic event + PhaseDone")
	assert.Equal(t, "boom", evs[0].Script)
	assert.Equal(t, PhaseDone, evs[len(evs)-1].Phase)
}

// =====================================================================
// 7. ProgressBufferOverflow — 10000 events, slow drain, no event loss
// =====================================================================
func TestAutostartManager_ProgressBufferOverflow(t *testing.T) {
	defer verifyNoLeaks(t)()

	const events = 10000

	// Slow drain via broadcast callback. Producer uses BLOCKING send, so
	// it should back-pressure on the 64-slot buffer rather than drop.
	//
	// The yield here is runtime.Gosched(), NOT a sleep. This callback runs
	// synchronously in the drain loop, so yielding the drain's timeslice lets
	// the producer outrun it and fill the 64-slot buffer, which is what puts
	// the producer's blocking send under back-pressure. That is the condition
	// this test needs, and it is a scheduling property, not a timing one.
	//
	// It previously slept 10µs per event on the reasoning that "10k events at
	// ~10µs each is ~100ms of drain wall time". Go cannot sleep 10µs: a timer
	// park costs ~0.5ms+, so 10k iterations cost ~5s, not ~100ms -- 5s of gate
	// time bought nothing, because wall-clock duration was never the property
	// under test. What is asserted below is completeness (no event lost),
	// ordering (strict layer sequence, no reordering inside the drain), and
	// termination (PhaseDone last). All three are deterministic and need no
	// clock, so the back-pressure is now simulated rather than timed.
	mgr := NewAutostartManagerWithBroadcast(func(_ string, _ AutostartProgress) {
		runtime.Gosched()
	})

	// Count how often the producer found the buffer full. This makes the
	// back-pressure an asserted observable rather than an assumption: without
	// it, deleting the yield above would leave a fast drain that never
	// saturates the buffer, and the test would still pass while no longer
	// exercising the thing it is named for.
	var blocked atomic.Int64
	startFn := func(ctx context.Context, progress chan<- AutostartProgress) *AutostartResult {
		for i := 0; i < events; i++ {
			ev := AutostartProgress{
				Phase:  PhaseScriptStarting,
				Script: fmt.Sprintf("s%d", i),
				Layer:  i,
			}
			select {
			case progress <- ev:
			default:
				// Buffer full — the drain is behind. Record it, then commit to
				// the blocking send the production emitters use.
				blocked.Add(1)
				select {
				case <-ctx.Done():
					return &AutostartResult{}
				case progress <- ev:
				}
			}
		}
		return &AutostartResult{Scripts: []string{"synth"}}
	}

	h := mgr.GetOrCreate(pathN(0), startFn)
	<-h.Done()

	// Measured on a 6-core dev box: ~8000 of 10000 sends block with the yield in
	// place, versus ~150 with it removed. Back-pressure does arise without the
	// yield, but only incidentally (~1.5%) and could reach zero on another
	// scheduler; the yield is what makes saturation the sustained norm this test
	// is named for. Logged rather than bounded, because the exact count is
	// scheduler-dependent and only its presence is a real invariant.
	t.Logf("producer blocked on a full buffer %d of %d sends", blocked.Load(), events)
	assert.Positive(t, blocked.Load(),
		"producer never found the 64-slot buffer full, so no back-pressure was exercised: "+
			"%d events drained without the producer ever blocking", events)

	rec := h.Progress()
	require.Len(t, rec, events+1, "all %d events + PhaseDone must be recorded", events)

	// Strict layer ordering — proves no reordering inside the drain.
	for i := 0; i < events; i++ {
		assert.Equal(t, i, rec[i].Layer, "event %d layer", i)
	}
	assert.Equal(t, PhaseDone, rec[events].Phase)
}

// =====================================================================
// 8. LateJoinerSeesHistory — full history available after run completes
// =====================================================================
func TestAutostartManager_LateJoinerSeesHistory(t *testing.T) {
	defer verifyNoLeaks(t)()

	const events = 200

	mgr := NewAutostartManager()
	h := mgr.GetOrCreate(pathN(0), synthStartFn(events, 0, 0))
	<-h.Done()

	// Late joiner — exact same path key, post-completion.
	hLate := mgr.Get(pathN(0))
	require.NotNil(t, hLate, "Get must return the completed handle")
	assert.Same(t, h, hLate, "Get must return the same handle, not a new one")

	evs := hLate.Progress()
	require.Len(t, evs, events+1, "late joiner must see full history + PhaseDone")
	assert.Equal(t, PhaseDone, evs[events].Phase)

	// Manager-level Progress() lookup matches.
	mgrEvs := mgr.Progress(pathN(0))
	require.Len(t, mgrEvs, events+1)
	for i := range evs {
		assert.Equal(t, evs[i].Layer, mgrEvs[i].Layer, "late joiner event %d", i)
	}

	// Done channel remains closed (late callers never block on it).
	select {
	case <-hLate.Done():
	default:
		t.Fatal("Done() must remain closed for completed handle")
	}
}

// =====================================================================
// 9. ManyHandlesShutdown — 50 handles, random-order Cancel, all clean
// =====================================================================
func TestAutostartManager_ManyHandlesShutdown(t *testing.T) {
	defer verifyNoLeaks(t)()

	const N = 50

	mgr := NewAutostartManager()
	startFn := func(ctx context.Context, progress chan<- AutostartProgress) *AutostartResult {
		// Long-blocking; only exits via ctx cancellation.
		for i := 0; ; i++ {
			select {
			case <-ctx.Done():
				return &AutostartResult{}
			case progress <- AutostartProgress{Phase: PhaseScriptStarting, Layer: i}:
			}
		}
	}

	handles := make([]*AutostartHandle, N)
	for i := 0; i < N; i++ {
		handles[i] = mgr.GetOrCreate(pathN(i), startFn)
	}

	// Random-order cancel.
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	order := r.Perm(N)
	for _, idx := range order {
		mgr.Cancel(pathN(idx))
	}

	// All handles must close cleanly within a generous deadline.
	deadline := time.After(10 * time.Second)
	for i := 0; i < N; i++ {
		select {
		case <-handles[i].Done():
		case <-deadline:
			t.Fatalf("handle %d never closed after Cancel", i)
		}
	}

	// Result on every handle is non-nil and last event is PhaseDone.
	for i := 0; i < N; i++ {
		require.NotNil(t, handles[i].Result(), "handle %d Result must be populated", i)
		evs := handles[i].Progress()
		require.NotEmpty(t, evs, "handle %d must have at least PhaseDone", i)
		assert.Equal(t, PhaseDone, evs[len(evs)-1].Phase, "handle %d last event", i)
	}

	// goleak.VerifyNone (deferred) catches any leaked drain or worker goroutines.
}
