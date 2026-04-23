package overlay

// Stress harness for AlertScanner deferred-activity logic (G9).
//
// Mechanism under test: internal/overlay/alerts.go — pattern-match buffer,
// deferral timer, retry interval, batch window, dedup window.
//
// Out of scope: real PTY, real ActivityMonitor, real terminal renderer,
// real alert sink.
//
// Stub clock design:
//   - stubClock controls clockNow so dedup window checks are deterministic.
//   - fakeTimerSet collects scheduled timers; tests fire them explicitly.
//
// Every test verifies goroutine cleanliness with goleak.IgnoreCurrent()
// scoped to the test so sibling tests with long-lived goroutines are not
// penalised. Run harness: go test -race -count=10 -run TestAlertScanner ./internal/overlay/

import (
	"fmt"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// ---- Stub helpers ----------------------------------------------------------

// stubClock is an injectable clock backed by an atomic time pointer.
type stubClock struct {
	mu  sync.Mutex
	now time.Time
}

func newStubClock(t time.Time) *stubClock { return &stubClock{now: t} }

func (c *stubClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stubClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeTimer represents a timer scheduled via fakeTimerSet.
type fakeTimer struct {
	deadline time.Duration // relative to creation
	fn       func()
	stopped  atomic.Bool
	timer    *time.Timer // underlying real timer (nil until Fire called)
}

// fakeTimerSet collects all timers created via afterFunc for controlled firing.
type fakeTimerSet struct {
	mu     sync.Mutex
	timers []*fakeTimer
}

// afterFunc returns an afterFunc-compatible function backed by this set.
func (fts *fakeTimerSet) afterFunc(d time.Duration, f func()) *time.Timer {
	ft := &fakeTimer{deadline: d, fn: f}
	fts.mu.Lock()
	fts.timers = append(fts.timers, ft)
	fts.mu.Unlock()
	// Return a real timer that will never fire naturally (1h deadline).
	// Tests call FireAll / FirePending to trigger callbacks.
	ft.timer = time.AfterFunc(time.Hour, func() {})
	return ft.timer
}

// FireAll fires all pending (non-stopped) timers immediately.
func (fts *fakeTimerSet) FireAll() {
	fts.mu.Lock()
	pending := make([]*fakeTimer, len(fts.timers))
	copy(pending, fts.timers)
	fts.timers = fts.timers[:0]
	fts.mu.Unlock()

	for _, ft := range pending {
		if ft.stopped.Load() {
			continue
		}
		if ft.timer != nil {
			ft.timer.Stop()
		}
		ft.fn()
	}
}

// Count returns the number of pending timers.
func (fts *fakeTimerSet) Count() int {
	fts.mu.Lock()
	defer fts.mu.Unlock()
	return len(fts.timers)
}

// drainTimers stops all real underlying timers to prevent leaks in tests.
func (fts *fakeTimerSet) drainTimers() {
	fts.mu.Lock()
	defer fts.mu.Unlock()
	for _, ft := range fts.timers {
		ft.stopped.Store(true)
		if ft.timer != nil {
			ft.timer.Stop()
		}
	}
	fts.timers = fts.timers[:0]
}

// ---- Error line helpers ----------------------------------------------------

// errorLine returns a line that matches the go-panic pattern.
func errorLine(i int) string { return fmt.Sprintf("panic: error %d runtime", i) }

// nonErrorLine returns a line that matches no pattern.
func nonErrorLine(i int) string { return fmt.Sprintf("INFO: progress step %d", i) }

// ---- Test 1: PatternFloodDuringActivity ------------------------------------

// TestAlertScanner_PatternFloodDuringActivity pushes 10 000 lines (mix of
// matching + non-matching) while activity is reported as Active. Verifies
// deferred lines accumulate without delivery, then fire once the fake timer
// is triggered and activity becomes idle.
func TestAlertScanner_PatternFloodDuringActivity(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	clk := newStubClock(time.Now())
	fts := &fakeTimerSet{}
	defer fts.drainTimers()

	var active atomic.Bool
	active.Store(true)

	var mu sync.Mutex
	var received []*AlertBatch

	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:   1 * time.Millisecond,
		DedupeWindow:  10 * time.Minute, // large so every unique line passes dedup
		RetryInterval: 1 * time.Millisecond,
		ActivityState: func() ActivityState {
			if active.Load() {
				return ActivityActive
			}
			return ActivityIdle
		},
		OnAlert: func(b *AlertBatch) {
			mu.Lock()
			received = append(received, b)
			mu.Unlock()
		},
	})
	scanner.clockNow = clk.Now
	scanner.afterFunc = fts.afterFunc
	defer scanner.Stop()

	// Push 10 000 lines: every 5th is a matching error line (unique index).
	const total = 10_000
	for i := 0; i < total; i++ {
		if i%5 == 0 {
			scanner.ProcessLine(errorLine(i), "flood")
		} else {
			scanner.ProcessLine(nonErrorLine(i), "flood")
		}
	}

	// While active, no delivery should have occurred.
	mu.Lock()
	countWhileActive := len(received)
	mu.Unlock()
	assert.Equal(t, 0, countWhileActive, "no alerts while activity is active")

	// Switch to idle and fire the retry timer.
	active.Store(false)
	fts.FireAll()

	// Allow any goroutine callbacks to complete.
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) > 0
	}, 2*time.Second, 5*time.Millisecond, "alerts must fire after idle + timer fire")

	mu.Lock()
	total_received := len(received)
	mu.Unlock()
	assert.Greater(t, total_received, 0, "at least one batch delivered")
}

// ---- Test 2: RetryIntervalRespected ----------------------------------------

// TestAlertScanner_RetryIntervalRespected verifies that while the activity
// state remains Active, the scanner schedules exactly one retry per flush
// call (up to maxRetries=5). Each FireAll advances one retry cycle.
func TestAlertScanner_RetryIntervalRespected(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	fts := &fakeTimerSet{}
	defer fts.drainTimers()

	var active atomic.Bool
	active.Store(true)

	var mu sync.Mutex
	var received []*AlertBatch

	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:   1 * time.Millisecond,
		DedupeWindow:  10 * time.Minute,
		RetryInterval: 1 * time.Millisecond,
		ActivityState: func() ActivityState {
			if active.Load() {
				return ActivityActive
			}
			return ActivityIdle
		},
		OnAlert: func(b *AlertBatch) {
			mu.Lock()
			received = append(received, b)
			mu.Unlock()
		},
	})
	scanner.afterFunc = fts.afterFunc
	defer scanner.Stop()

	scanner.ProcessLine("panic: something failed", "svc")

	// Batch timer fires -> flush sees Active -> schedules retry (retry 1).
	fts.FireAll() // fires batch timer; flush defers, schedules retry timer
	require.Equal(t, 1, fts.Count(), "retry timer scheduled after first defer")

	// Retry 1 fires -> still Active -> retry 2.
	fts.FireAll()
	require.Equal(t, 1, fts.Count(), "retry timer rescheduled")

	// Retry 2 -> still Active -> retry 3.
	fts.FireAll()
	require.Equal(t, 1, fts.Count(), "retry timer rescheduled again")

	// No delivery yet.
	mu.Lock()
	countBeforeIdle := len(received)
	mu.Unlock()
	assert.Equal(t, 0, countBeforeIdle, "no delivery while active")

	// Transition idle; fire the pending retry.
	active.Store(false)
	fts.FireAll()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) > 0
	}, 2*time.Second, 5*time.Millisecond, "alert delivered after idle")
}

// ---- Test 3: BatchWindowGrouping -------------------------------------------

// TestAlertScanner_BatchWindowGrouping verifies that lines processed within
// one batch window end up in the same OnAlert call, while lines added after
// the timer fires end up in a separate batch.
func TestAlertScanner_BatchWindowGrouping(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	fts := &fakeTimerSet{}
	defer fts.drainTimers()

	var mu sync.Mutex
	var received []*AlertBatch

	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:  1 * time.Millisecond,
		DedupeWindow: 10 * time.Minute,
		OnAlert: func(b *AlertBatch) {
			mu.Lock()
			received = append(received, b)
			mu.Unlock()
		},
	})
	scanner.afterFunc = fts.afterFunc
	defer scanner.Stop()

	// First group: three unique error lines, all within one batch window.
	scanner.ProcessLine("panic: alpha error", "svc")
	scanner.ProcessLine("panic: beta error", "svc")
	scanner.ProcessLine("panic: gamma error", "svc")

	// Fire the batch timer — all three should be delivered together.
	fts.FireAll()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) >= 1
	}, 2*time.Second, 5*time.Millisecond, "first batch delivered")

	mu.Lock()
	firstBatch := received[0]
	mu.Unlock()
	assert.Len(t, firstBatch.Matches, 3, "first batch contains all three lines")

	// Second group: one new line after the timer fired.
	scanner.ProcessLine("panic: delta error", "svc")
	fts.FireAll()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) >= 2
	}, 2*time.Second, 5*time.Millisecond, "second batch delivered")

	mu.Lock()
	secondBatch := received[1]
	mu.Unlock()
	assert.Len(t, secondBatch.Matches, 1, "second batch contains only the later line")
}

// ---- Test 4: ActivityDeferralCancelOnIdle ----------------------------------

// TestAlertScanner_ActivityDeferralCancelOnIdle verifies the full deferral
// lifecycle: lines arrive while Active, retries exhaust up to maxRetries
// without delivery, then activity transitions to Idle and the next retry
// delivers the pending batch.
func TestAlertScanner_ActivityDeferralCancelOnIdle(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	fts := &fakeTimerSet{}
	defer fts.drainTimers()

	var active atomic.Bool
	active.Store(true)

	var mu sync.Mutex
	var received []*AlertBatch

	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:   1 * time.Millisecond,
		DedupeWindow:  10 * time.Minute,
		RetryInterval: 1 * time.Millisecond,
		ActivityState: func() ActivityState {
			if active.Load() {
				return ActivityActive
			}
			return ActivityIdle
		},
		OnAlert: func(b *AlertBatch) {
			mu.Lock()
			received = append(received, b)
			mu.Unlock()
		},
	})
	scanner.afterFunc = fts.afterFunc
	defer scanner.Stop()

	scanner.ProcessLine("panic: runtime error deferred", "app")

	// Fire batch timer + 4 retries while still active.
	// maxRetries = 5, so 5 fires should all defer.
	for i := 0; i < 5; i++ {
		fts.FireAll()
		// Brief yield to allow flush goroutine (it runs inline in AfterFunc) to complete.
		time.Sleep(2 * time.Millisecond)
	}

	mu.Lock()
	countBefore := len(received)
	mu.Unlock()
	assert.Equal(t, 0, countBefore, "no delivery before idle; maxRetries not yet exhausted or still active")

	// Transition to idle; fire the remaining retry.
	active.Store(false)
	fts.FireAll()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) > 0
	}, 2*time.Second, 5*time.Millisecond, "alert delivered after idle transition")

	mu.Lock()
	assert.Greater(t, len(received), 0, "pending alerts flushed on idle")
	mu.Unlock()
}

// ---- Test 5: ConcurrentLineFlood -------------------------------------------

// TestAlertScanner_ConcurrentLineFlood has 8 producers pushing lines into
// AlertScanner.ProcessLine concurrently. Verifies no data race (run with
// -race -count=10) and that at least one alert batch is delivered.
func TestAlertScanner_ConcurrentLineFlood(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	const producers = 8
	const linesPerProducer = 500

	var delivered atomic.Int64

	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:  5 * time.Millisecond,
		DedupeWindow: 1 * time.Millisecond, // short so unique lines pass dedup repeatedly
		OnAlert: func(b *AlertBatch) {
			delivered.Add(int64(len(b.Matches)))
		},
	})
	defer scanner.Stop()

	var wg sync.WaitGroup
	wg.Add(producers)
	for p := 0; p < producers; p++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < linesPerProducer; i++ {
				// Mix matching and non-matching; use unique content to defeat dedup.
				if i%3 == 0 {
					scanner.ProcessLine(
						fmt.Sprintf("panic: producer %d line %d", id, i),
						fmt.Sprintf("svc%d", id),
					)
				} else {
					scanner.ProcessLine(
						fmt.Sprintf("INFO: producer %d step %d", id, i),
						fmt.Sprintf("svc%d", id),
					)
				}
			}
		}(p)
	}
	wg.Wait()

	// Wait for batch window to flush naturally.
	require.Eventually(t, func() bool {
		return delivered.Load() > 0
	}, 2*time.Second, 10*time.Millisecond, "at least one alert delivered")
}

// ---- Test 6: StopWithPendingAlerts -----------------------------------------

// TestAlertScanner_StopWithPendingAlerts verifies that Stop() flushes any
// pending alerts synchronously (per the documented contract in Stop →
// deliverPending) and that no goroutines leak after Stop returns.
func TestAlertScanner_StopWithPendingAlerts(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	fts := &fakeTimerSet{}
	// Do NOT defer fts.drainTimers — scanner.Stop will call batchTimer.Stop().

	var active atomic.Bool
	active.Store(true) // Keep active so batch timer doesn't auto-flush.

	var mu sync.Mutex
	var received []*AlertBatch

	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:   10 * time.Second, // Very long — timer should NOT fire.
		DedupeWindow:  10 * time.Minute,
		RetryInterval: 10 * time.Second,
		ActivityState: func() ActivityState {
			if active.Load() {
				return ActivityActive
			}
			return ActivityIdle
		},
		OnAlert: func(b *AlertBatch) {
			mu.Lock()
			received = append(received, b)
			mu.Unlock()
		},
	})
	scanner.afterFunc = fts.afterFunc
	// Note: do NOT defer scanner.Stop(); we call it explicitly below.

	// Inject a custom pattern so we control exact match.
	scanner.AddPattern(&AlertPattern{
		ID:       "stop-test-err",
		Pattern:  regexp.MustCompile(`STOP_TEST_ERROR`),
		Severity: AlertSeverityError,
		Category: "test",
	})

	scanner.ProcessLine("STOP_TEST_ERROR: pending at stop time", "stopper")

	// Verify nothing delivered yet (batch timer is 10s and fake).
	mu.Lock()
	countBefore := len(received)
	mu.Unlock()
	assert.Equal(t, 0, countBefore, "no alert before Stop")

	// Stop should call deliverPending synchronously.
	scanner.Stop()

	// Stop is synchronous per the contract — deliverPending called before return.
	mu.Lock()
	countAfter := len(received)
	mu.Unlock()
	assert.Equal(t, 1, countAfter, "Stop must flush pending alerts synchronously")

	if countAfter > 0 {
		mu.Lock()
		batch := received[0]
		mu.Unlock()
		assert.Equal(t, "stopper", batch.ScriptID)
		require.Len(t, batch.Matches, 1)
		assert.Equal(t, "stop-test-err", batch.Matches[0].Pattern.ID)
	}

	// Drain fake timers to prevent 1h real timers from leaking.
	fts.drainTimers()
}
