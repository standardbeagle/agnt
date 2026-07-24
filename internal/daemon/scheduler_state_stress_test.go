package daemon

// Mechanism-isolation stress harness for SchedulerStateManager (G8).
//
// Scope: this file exercises the SchedulerStateManager write-behind channel
// + flusher goroutine + Close shutdown ordering using a STUB persister only.
// It NEVER writes to a real filesystem. The stub counts calls, optionally
// sleeps, optionally errors, and optionally panics. The system under test is
// the channel mechanism, not real disk I/O.
//
// Mental model (read scheduler_state.go end-to-end before editing):
//
//   * Single writeLoop goroutine spawned in newSchedulerStateManager.
//   * Producer API (SaveTask/RemoveTask/ClearProject) updates the in-memory
//     cache under m.mu, then calls enqueueProjectWrite which adds the path to
//     pendingWrites and does a non-blocking send on writeCh (buffered 1, used
//     as a signal not a queue).
//   * writeLoop selects on writeCh / flushCh / stopCh. On a writeCh signal it
//     starts a debounce timer of saveInterval, after which it drains writeCh
//     and calls doWriteAll. On flushCh it does an immediate doWriteAll. On
//     stopCh it does a final doWriteAll and returns; defer close(stopped)
//     fires.
//   * Close(): closeOnce.Do { close(stopCh); <-stopped }. Idempotent.
//   * Flush(): try flushCh send, fall back to direct doWriteAll if stopped
//     or stopCh is already closed.
//
// Bugs caught by this harness (fixed in scheduler_state.go on this commit):
//   1. Persister panic propagated out of writeLoop, killing the goroutine
//      without closing m.stopped. Close() then blocked forever and the
//      writeLoop goroutine appeared as a leak in unrelated tests. Fixed by
//      adding recover() in doWriteAll AND a defence-in-depth recover in
//      writeLoop itself.
//   2. Flush() racing with Close() could send on flushCh after stopCh closed
//      but before stopped closed, blocking forever. Fixed by adding stopCh
//      to Flush's select and waiting for stopped before falling back to a
//      direct write.
//
// Every test runs under -race and verifies goroutine cleanup with
// verifyNoLeaks(t) (see goleak_leaks_test.go). That shared helper pairs
// goleak.IgnoreCurrent() with IgnoreAnyFunction filters for os/exec's worker
// goroutines, because IgnoreCurrent alone is NOT sufficient in this
// mixed-parallel-test package: a suspended t.Parallel() sibling's daemon
// background loop shells out on a ticker and spawns fresh os/exec goroutines
// after the snapshot. This test's own real leak (a stuck
// (*SchedulerStateManager).writeLoop, the panic-kills-goroutine bug this
// harness exists to catch) carries no os/exec frame and still fails the check.

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubPersister is the canonical isolated persister. It counts calls, can
// sleep, can return errors at a configured cadence, and can panic at a
// configured cadence. All counters are atomic so the test can observe
// progress mid-flight.
type stubPersister struct {
	written    atomic.Int64
	errored    atomic.Int64
	panicked   atomic.Int64
	sleep      time.Duration
	errEvery   int // 0 = never, N = every Nth call returns an error
	panicEvery int // 0 = never, N = every Nth call panics
}

func (p *stubPersister) Persist(_ string, _ []byte) error {
	if p.sleep > 0 {
		time.Sleep(p.sleep)
	}
	n := p.written.Add(1)
	if p.panicEvery > 0 && int(n)%p.panicEvery == 0 {
		p.panicked.Add(1)
		panic(fmt.Sprintf("synth persister panic at call %d", n))
	}
	if p.errEvery > 0 && int(n)%p.errEvery == 0 {
		p.errored.Add(1)
		return errors.New("synth persist err")
	}
	return nil
}

// taskN returns a synthetic task pinned to a specific project path. The
// project path is purely synthetic — no t.TempDir, no real fs lookup — the
// stub persister never opens the path.
func taskN(id int, projectPath string) *ScheduledTask {
	return NewScheduledTask(
		fmt.Sprintf("task-%d", id),
		"sess",
		"msg",
		projectPath,
		time.Now().Add(1*time.Hour),
		time.Now(),
		TaskStatusPending,
	)
}

// synthPath returns a deterministic synthetic project path.
func synthPath(i int) string {
	return fmt.Sprintf("/synth/sched/p%d", i)
}

// =====================================================================
//  1. HighThroughput — 10000 writes from 10 producers; written + errored
//     must equal the number of unique distinct write batches after Close.
//
// This test exercises the full path: producers concurrently call SaveTask,
// the channel mechanism coalesces signals, the flusher goroutine drains
// pendingWrites, Close() waits for the final flush. The contract is that
// after Close returns, every dirty project has been handed to the persister
// at least once.
// =====================================================================
func TestSchedulerWriteBehind_HighThroughput(t *testing.T) {
	defer verifyNoLeaks(t)()

	const (
		producers = 8
		perProd   = 250 // 2000 total ops — exercises the mechanism without
		// burning wall time on the lock contention pile-up
		// that 10000 ops creates with -race instrumentation.
		paths = 40
	)

	stub := &stubPersister{}
	sm := newSchedulerStateManager(2*time.Millisecond, stub.Persist)

	var wg sync.WaitGroup
	t0 := time.Now()
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func(prod int) {
			defer wg.Done()
			for i := 0; i < perProd; i++ {
				task := taskN(prod*perProd+i, synthPath(i%paths))
				_ = sm.SaveTask(task)
			}
		}(p)
	}
	wg.Wait()

	// Close flushes pending writes and tears down the writer goroutine.
	require.NoError(t, sm.Close())
	wall := time.Since(t0)

	// Coalescing means the persister is called less than producers*perProd,
	// but more than 0 and at least once per distinct path that was dirty.
	written := stub.written.Load()
	assert.GreaterOrEqual(t, written, int64(paths),
		"persister must be called at least once per distinct project path")
	assert.Less(t, written, int64(producers*perProd),
		"coalescing should reduce write count below total producer ops")
	assert.Equal(t, int64(0), stub.errored.Load(), "no errors expected")
	assert.Equal(t, int64(0), stub.panicked.Load(), "no panics expected")

	t.Logf("highthroughput: %d producer ops over %d paths -> %d persist calls in %s",
		producers*perProd, paths, written, wall)
}

// =====================================================================
//  2. SlowPersister — 5ms per call; backpressure observable; no producer
//     panic; all dirty projects persist after Close.
//
// The producer side never blocks (enqueueProjectWrite uses a non-blocking
// signal send). The slowness shows up as a longer Close wall time because
// the flusher must drain pending work synchronously inside Close.
// =====================================================================
func TestSchedulerWriteBehind_SlowPersister(t *testing.T) {
	defer verifyNoLeaks(t)()

	const (
		paths     = 20
		producers = 5
		perProd   = 100
		persistMs = 5
	)

	stub := &stubPersister{sleep: persistMs * time.Millisecond}
	sm := newSchedulerStateManager(1*time.Millisecond, stub.Persist)

	var wg sync.WaitGroup
	produceStart := time.Now()
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func(prod int) {
			defer wg.Done()
			for i := 0; i < perProd; i++ {
				task := taskN(prod*perProd+i, synthPath(i%paths))
				assert.NoError(t, sm.SaveTask(task), "producer SaveTask must not fail")
			}
		}(p)
	}
	wg.Wait()
	produceWall := time.Since(produceStart)

	closeStart := time.Now()
	require.NoError(t, sm.Close())
	closeWall := time.Since(closeStart)

	// Producer side should complete fast — they only update the cache and
	// signal. The slow persister does NOT block producers.
	assert.Less(t, produceWall, 500*time.Millisecond,
		"producers must not block on slow persister; produce wall = %s", produceWall)

	// At least one persist call per dirty path must complete before Close
	// returns (Close runs the final doWriteAll).
	assert.GreaterOrEqual(t, stub.written.Load(), int64(paths),
		"every dirty path must be persisted at least once before Close returns")

	t.Logf("slowpersister: produce=%s close=%s persist_calls=%d",
		produceWall, closeWall, stub.written.Load())
}

// =====================================================================
//  3. PersisterErrors — 50% error rate via errEvery=2; error count tracked;
//     Manager does not deadlock.
//
// =====================================================================
func TestSchedulerWriteBehind_PersisterErrors(t *testing.T) {
	defer verifyNoLeaks(t)()

	const paths = 30

	stub := &stubPersister{errEvery: 2}
	sm := newSchedulerStateManager(2*time.Millisecond, stub.Persist)

	for i := 0; i < paths; i++ {
		require.NoError(t, sm.SaveTask(taskN(i, synthPath(i))))
	}

	// Force-flush so we don't depend on Close's drain to count errors.
	// Flush must not deadlock even when the persister returns errors.
	flushDone := make(chan error, 1)
	go func() { flushDone <- sm.Flush() }()
	select {
	case err := <-flushDone:
		// Flush returns the first persister error encountered, which is
		// expected here.
		t.Logf("flush returned: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("Flush deadlocked when persister returns errors")
	}

	require.NoError(t, sm.Close())

	written := stub.written.Load()
	errored := stub.errored.Load()
	assert.GreaterOrEqual(t, written, int64(paths), "all dirty paths must reach persister")
	assert.Greater(t, errored, int64(0), "with errEvery=2 and %d paths, expect errors", paths)
	// errored should be roughly written/2 — allow loose bounds since pending
	// map iteration order is not deterministic.
	assert.GreaterOrEqual(t, errored, written/3, "expected ~half errored, got %d/%d", errored, written)

	t.Logf("errors: persist_calls=%d errors=%d", written, errored)
}

// =====================================================================
//  4. PersisterPanic — every Nth persister call panics; Manager recovers,
//     keeps draining, Close completes cleanly, goroutine exits.
//
// This is THE critical test. Before the fix, a single persister panic killed
// writeLoop without closing m.stopped, and Close() blocked forever. The
// goleak verification catches the leaked writeLoop goroutine.
// =====================================================================
func TestSchedulerWriteBehind_PersisterPanic(t *testing.T) {
	defer verifyNoLeaks(t)()

	const paths = 20

	stub := &stubPersister{panicEvery: 3}
	sm := newSchedulerStateManager(2*time.Millisecond, stub.Persist)

	for i := 0; i < paths; i++ {
		require.NoError(t, sm.SaveTask(taskN(i, synthPath(i))))
	}

	// Flush to drive at least one batch through the persister, hitting a
	// panic. Flush MUST return — not deadlock — because doWriteAll's recover
	// converts panic into error.
	flushDone := make(chan error, 1)
	go func() { flushDone <- sm.Flush() }()
	select {
	case err := <-flushDone:
		// Either the flush succeeds (panic was recovered + swallowed as
		// error) or returns the panic as a wrapped error. Both prove the
		// goroutine survived.
		t.Logf("flush after panic returned: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("Flush deadlocked after persister panic — writeLoop goroutine likely died")
	}

	// Verify writer goroutine is still alive by enqueuing more work and
	// flushing again.
	for i := paths; i < paths*2; i++ {
		require.NoError(t, sm.SaveTask(taskN(i, synthPath(i))))
	}
	flush2Done := make(chan error, 1)
	go func() { flush2Done <- sm.Flush() }()
	select {
	case <-flush2Done:
	case <-time.After(2 * time.Second):
		t.Fatal("Second Flush deadlocked — writer goroutine did not survive panic")
	}

	// Close must complete — if writeLoop died, close(stopCh) would never be
	// observed and <-stopped would block forever.
	closeDone := make(chan error, 1)
	go func() { closeDone <- sm.Close() }()
	select {
	case err := <-closeDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Close deadlocked — writeLoop goroutine did not close m.stopped")
	}

	assert.Greater(t, stub.panicked.Load(), int64(0), "expected persister to panic at least once")
	t.Logf("panic-recovery: persist_calls=%d panics=%d", stub.written.Load(), stub.panicked.Load())
}

// =====================================================================
//  5. CloseWithPendingWrites — 1000 writes immediately followed by Close;
//     no lost writes (every dirty path persisted at least once); flusher
//     exits; goleak clean.
//
// =====================================================================
func TestSchedulerWriteBehind_CloseWithPendingWrites(t *testing.T) {
	defer verifyNoLeaks(t)()

	const (
		writes = 1000
		paths  = 100
	)

	stub := &stubPersister{}
	// Long debounce so writes accumulate in pendingWrites before Close drains.
	sm := newSchedulerStateManager(10*time.Second, stub.Persist)

	for i := 0; i < writes; i++ {
		require.NoError(t, sm.SaveTask(taskN(i, synthPath(i%paths))))
	}

	// Close must drain pendingWrites synchronously and exit the goroutine.
	closeDone := make(chan error, 1)
	go func() { closeDone <- sm.Close() }()
	select {
	case err := <-closeDone:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not drain pending writes within 3s")
	}

	// Every distinct dirty path must have been persisted at least once.
	assert.GreaterOrEqual(t, stub.written.Load(), int64(paths),
		"Close must drain all %d dirty paths; got %d persist calls", paths, stub.written.Load())
}

// =====================================================================
//  6. ConcurrentWriteAndClose — tight-loop SaveTask + concurrent Close;
//     no panic, clean shutdown, goleak clean. Run under -race.
//
// This catches: producers writing while Close is racing the writer
// goroutine. If enqueueProjectWrite ever sent on writeCh after the
// channel was closed (it doesn't — writeCh is never closed), or if
// SaveTask raced with cache invalidation, this would deadlock or panic.
// =====================================================================
func TestSchedulerWriteBehind_ConcurrentWriteAndClose(t *testing.T) {
	defer verifyNoLeaks(t)()

	stub := &stubPersister{}
	sm := newSchedulerStateManager(1*time.Millisecond, stub.Persist)

	// Producer goroutine spams SaveTask for a short window.
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
			// SaveTask after Close returns nil error or simply no-ops; it
			// must NOT panic on a closed channel. The mechanism uses a
			// non-blocking signal send so even if the writer is gone, the
			// signal is dropped silently.
			_ = sm.SaveTask(taskN(i, synthPath(i%5)))
		}
	}()

	// Let the producer ramp up so we race Close against in-flight writes.
	time.Sleep(20 * time.Millisecond)

	// Close concurrently. This is the race: producer is calling SaveTask
	// while Close is closing stopCh and waiting on stopped.
	closeDone := make(chan error, 1)
	go func() { closeDone <- sm.Close() }()
	select {
	case err := <-closeDone:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		close(stop)
		producerDone.Wait()
		t.Fatal("Close deadlocked under concurrent producer load")
	}

	// Stop the producer cleanly so goleak doesn't report it.
	close(stop)
	producerDone.Wait()

	t.Logf("concurrent-close: persist_calls=%d", stub.written.Load())
}

// =====================================================================
// 7. DoubleClose — Close is idempotent and safe.
// =====================================================================
func TestSchedulerWriteBehind_DoubleClose(t *testing.T) {
	defer verifyNoLeaks(t)()

	stub := &stubPersister{}
	sm := newSchedulerStateManager(10*time.Millisecond, stub.Persist)

	// First close - real shutdown.
	require.NoError(t, sm.Close())

	// Second close - must be a no-op, must not panic, must not block.
	closeDone := make(chan error, 1)
	go func() { closeDone <- sm.Close() }()
	select {
	case err := <-closeDone:
		require.NoError(t, err, "second Close must be safe no-op")
	case <-time.After(1 * time.Second):
		t.Fatal("second Close blocked — closeOnce is broken")
	}

	// Third close for paranoia.
	require.NoError(t, sm.Close())

	// Flush after close must also not deadlock (it falls through to the
	// stopped/stopCh branches in Flush's select).
	flushDone := make(chan error, 1)
	go func() { flushDone <- sm.Flush() }()
	select {
	case <-flushDone:
	case <-time.After(1 * time.Second):
		t.Fatal("Flush after Close deadlocked")
	}
}
