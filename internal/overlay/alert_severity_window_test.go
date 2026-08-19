package overlay

// Severity-scaled batch-window tests.
//
// The highest-severity signal in the system (an error-level alert such as a
// dying autostart script / `fatal:` line) must reach the agent promptly — it
// is precisely when the agent most needs to steer. Warnings/info keep the full
// anti-churn window so ordinary log chatter does not churn the agent's input.
//
// The anti-churn property must survive the shorter error window: an error
// storm must still COALESCE (dedup + one batch) rather than flood the agent.
// These tests assert on the scheduled window value, the delivered batch count,
// and ordering — never on wall-clock latency (a busy suite outruns any fixed
// sleep; see .claude/rules/testing-timing-assertion-flakes.md).
//
// Reuses the stub clock + fake timer harness from alert_scanner_stress_test.go.

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// warnMatch builds a pre-classified warning-severity match with a unique line.
func warnMatch(i int) *AlertMatch {
	return &AlertMatch{
		Pattern:  &AlertPattern{ID: "warn-test", Severity: AlertSeverityWarning},
		Line:     fmt.Sprintf("warning number %d slow", i),
		ScriptID: "svc",
	}
}

// TestAlertScanner_ErrorSeverityUsesShortWindow proves an error-level alert
// schedules its flush at the short error window, NOT the default 3s anti-churn
// window. The assertion reads the scheduled timer's window directly (injected
// timer), so it is immune to scheduler jitter.
func TestAlertScanner_ErrorSeverityUsesShortWindow(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	fts := &fakeTimerSet{}
	defer fts.drainTimers()

	// No BatchWindow set → default (3s) applies; the error window must win.
	scanner := NewAlertScanner(AlertScannerConfig{
		DedupeWindow: 10 * time.Minute,
		OnAlert:      func(*AlertBatch) {},
	})
	scanner.afterFunc = fts.afterFunc
	defer scanner.Stop()

	scanner.Inject(errMatch(0))

	require.Equal(t, 1, fts.Count(), "one batch timer scheduled")
	assert.Equal(t, errorBatchWindow, fts.timers[0].deadline,
		"error-severity alert flushes at the short window, not the 3s default")
	assert.Less(t, errorBatchWindow, defaultBatchWindow,
		"error window must be strictly shorter than the anti-churn default")
}

// TestAlertScanner_WarningKeepsFullWindow proves a warning-level alert keeps
// the full anti-churn window — the fast path is reserved for the highest
// severity, ordinary chatter still coalesces over 3s.
func TestAlertScanner_WarningKeepsFullWindow(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	fts := &fakeTimerSet{}
	defer fts.drainTimers()

	scanner := NewAlertScanner(AlertScannerConfig{
		DedupeWindow: 10 * time.Minute,
		OnAlert:      func(*AlertBatch) {},
	})
	scanner.afterFunc = fts.afterFunc
	defer scanner.Stop()

	scanner.Inject(warnMatch(0))

	require.Equal(t, 1, fts.Count(), "one batch timer scheduled")
	assert.Equal(t, defaultBatchWindow, fts.timers[0].deadline,
		"warning-severity alert keeps the full anti-churn window")
}

// TestAlertScanner_ErrorStormStillCoalesces is the anti-churn guarantee: a
// burst of distinct error lines within one window is delivered as ONE
// coalesced batch (not one OnAlert per line), preserving arrival order. Judged
// by delivered count + ordering, never wall time.
func TestAlertScanner_ErrorStormStillCoalesces(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	fts := &fakeTimerSet{}
	defer fts.drainTimers()

	var mu sync.Mutex
	var received []*AlertBatch

	// Storm size below MaxPending (50) so the throttle does not evict — this
	// isolates the coalescing property from the overload cap.
	const storm = 40

	scanner := NewAlertScanner(AlertScannerConfig{
		DedupeWindow: 10 * time.Minute, // every unique line survives dedup
		OnAlert: func(b *AlertBatch) {
			mu.Lock()
			received = append(received, b)
			mu.Unlock()
		},
	})
	scanner.afterFunc = fts.afterFunc
	defer scanner.Stop()

	for i := 0; i < storm; i++ {
		scanner.Inject(errMatch(i))
	}

	// The whole storm shares ONE batch timer — it did not schedule 40 flushes.
	assert.Equal(t, 1, fts.Count(), "error storm coalesces onto a single flush")

	fts.FireAll()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) > 0
	}, 2*time.Second, 5*time.Millisecond, "batch delivered")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, received, 1, "storm delivered as ONE coalesced batch, not flooded")
	require.Len(t, received[0].Matches, storm, "no error dropped and none split out")
	for i := 0; i < storm; i++ {
		assert.Equal(t, fmt.Sprintf("error number %d failed", i), received[0].Matches[i].Line,
			"arrival order preserved through coalescing")
	}
}

// TestAlertScanner_ErrorPullsInWarningWindow proves the highest-severity signal
// pulls the flush deadline IN even when a low-severity alert started the batch
// on the long window. Without this, a warning arriving first would make a
// following fatal wait the full 3s — the exact inversion this task fixes.
func TestAlertScanner_ErrorPullsInWarningWindow(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	fts := &fakeTimerSet{}
	defer fts.drainTimers()

	var mu sync.Mutex
	var received []*AlertBatch

	scanner := NewAlertScanner(AlertScannerConfig{
		DedupeWindow: 10 * time.Minute,
		OnAlert: func(b *AlertBatch) {
			mu.Lock()
			received = append(received, b)
			mu.Unlock()
		},
	})
	scanner.afterFunc = fts.afterFunc
	defer scanner.Stop()

	scanner.Inject(warnMatch(0)) // starts the batch on the full 3s window
	scanner.Inject(errMatch(0))  // must pull the deadline in to the error window

	// A timer scheduled at the short error window now exists.
	var sawErrorWindow bool
	for _, ft := range fts.timers {
		if ft.deadline == errorBatchWindow {
			sawErrorWindow = true
		}
	}
	assert.True(t, sawErrorWindow, "error alert pulled the flush in to the short window")

	fts.FireAll()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) > 0
	}, 2*time.Second, 5*time.Millisecond, "batch delivered")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, received, 1, "warning + error coalesced into one batch")
	assert.Len(t, received[0].Matches, 2, "both the warning and the error delivered together")
}

// TestAlertScanner_ErrorStormDoesNotPushOutFlush is the storm-latency
// guarantee: successive errors NEVER defer their own flush. The window is
// measured from the FIRST error; later errors join the batch without moving the
// deadline. Asserted by injected-clock reschedule accounting, not wall time.
func TestAlertScanner_ErrorStormDoesNotPushOutFlush(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	clk := newStubClock(time.Now())
	fts := &fakeTimerSet{}
	defer fts.drainTimers()

	scanner := NewAlertScanner(AlertScannerConfig{
		DedupeWindow: 10 * time.Minute,
		OnAlert:      func(*AlertBatch) {},
	})
	scanner.clockNow = clk.Now
	scanner.afterFunc = fts.afterFunc
	defer scanner.Stop()

	scanner.Inject(errMatch(0)) // schedules flush at T + errorBatchWindow
	require.Equal(t, 1, fts.Count(), "first error schedules exactly one flush")

	// A later error, still inside the window, must NOT reschedule (its would-be
	// deadline is later than the pending one) — otherwise a storm would keep
	// deferring itself, defeating the whole point.
	clk.Advance(errorBatchWindow / 2)
	scanner.Inject(errMatch(1))
	assert.Equal(t, 1, fts.Count(),
		"later error joins the batch without pushing the flush out")
}
