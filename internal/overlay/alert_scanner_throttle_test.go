package overlay

// Tests for the overload throttle (MaxPending cap + suppressed accounting) and
// the DepthSnapshot accessor added to AlertScanner. The throttle bounds how
// much a deferred burst can grow while the agent is busy, so the agent is not
// flooded the moment it goes idle.
//
// Reuses the stub clock + fake timer harness from alert_scanner_stress_test.go.

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// errMatch builds a pre-classified error-severity match with a unique line so
// each gets a distinct dedup fingerprint.
func errMatch(i int) *AlertMatch {
	return &AlertMatch{
		Pattern:  &AlertPattern{ID: "throttle-test", Severity: AlertSeverityError},
		Line:     fmt.Sprintf("error number %d failed", i),
		ScriptID: "svc",
	}
}

// TestAlertScanner_OverloadCapBoundsDelivery pushes far more distinct alerts
// than MaxPending while the agent is active. After going idle, exactly
// MaxPending matches are delivered and the remainder are reported as suppressed
// (not silently lost), with a one-line summary in the formatted batch.
func TestAlertScanner_OverloadCapBoundsDelivery(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	clk := newStubClock(time.Now())
	fts := &fakeTimerSet{}
	defer fts.drainTimers()

	var active atomic.Bool
	active.Store(true)

	var mu sync.Mutex
	var received []*AlertBatch

	const cap = 5
	const total = 20

	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:   1 * time.Millisecond,
		DedupeWindow:  10 * time.Minute,
		RetryInterval: 1 * time.Millisecond,
		MaxPending:    cap,
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

	for i := 0; i < total; i++ {
		scanner.Inject(errMatch(i))
	}

	// Throttle holds pending at the cap; the rest are counted as suppressed.
	snap := scanner.DepthSnapshot()
	assert.Equal(t, cap, snap.Pending, "pending bounded by MaxPending")
	assert.Equal(t, total-cap, snap.Suppressed, "overflow counted, not lost")

	// Go idle and deliver.
	active.Store(false)
	fts.FireAll()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) > 0
	}, 2*time.Second, 5*time.Millisecond, "batch delivered after idle")

	mu.Lock()
	defer mu.Unlock()

	var delivered, suppressed int
	var formatted string
	for _, b := range received {
		delivered += len(b.Matches)
		suppressed += b.Suppressed
		formatted += b.Format()
	}
	assert.Equal(t, cap, delivered, "delivered matches bounded by cap")
	assert.Equal(t, total-cap, suppressed, "suppressed surfaced exactly once")
	assert.Contains(t, formatted, fmt.Sprintf("%d more alert(s) suppressed", total-cap),
		"formatted batch warns the agent the stream was throttled")

	// After flush, overflow accounting resets.
	assert.Equal(t, 0, scanner.DepthSnapshot().Suppressed, "overflow reset after flush")
}

// TestAlertScanner_ThrottleEvictsLowSeverityFirst verifies that under sustained
// pressure error-level alerts survive while info-level ones are evicted.
func TestAlertScanner_ThrottleEvictsLowSeverityFirst(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	clk := newStubClock(time.Now())
	fts := &fakeTimerSet{}
	defer fts.drainTimers()

	var mu sync.Mutex
	var received []*AlertBatch

	const cap = 3

	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:  1 * time.Millisecond,
		DedupeWindow: 10 * time.Minute,
		MaxPending:   cap,
		OnAlert: func(b *AlertBatch) {
			mu.Lock()
			received = append(received, b)
			mu.Unlock()
		},
	})
	scanner.clockNow = clk.Now
	scanner.afterFunc = fts.afterFunc
	defer scanner.Stop()

	// Fill with info, then push errors that should displace the info entries.
	for i := 0; i < cap; i++ {
		scanner.Inject(&AlertMatch{
			Pattern:  &AlertPattern{ID: "info-test", Severity: AlertSeverityInfo},
			Line:     fmt.Sprintf("info note %d", i),
			ScriptID: "svc",
		})
	}
	for i := 0; i < cap; i++ {
		scanner.Inject(errMatch(i))
	}

	fts.FireAll()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) > 0
	}, 2*time.Second, 5*time.Millisecond, "batch delivered")

	mu.Lock()
	defer mu.Unlock()
	var errs int
	for _, b := range received {
		for _, m := range b.Matches {
			assert.Equal(t, AlertSeverityError, m.Pattern.Severity, "only errors survive eviction")
			errs++
		}
	}
	assert.Equal(t, cap, errs, "all error-level alerts retained")
}

// TestAlertScanner_DepthSnapshotDeferred verifies Deferred flips true while a
// flush is held off because the agent is active.
func TestAlertScanner_DepthSnapshotDeferred(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	fts := &fakeTimerSet{}
	defer fts.drainTimers()

	var active atomic.Bool
	active.Store(true)

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
		OnAlert: func(b *AlertBatch) {},
	})
	scanner.afterFunc = fts.afterFunc
	defer scanner.Stop()

	scanner.Inject(errMatch(1))
	assert.False(t, scanner.DepthSnapshot().Deferred, "not deferred before first flush attempt")

	// Fire the batch timer: flush sees Active and defers, scheduling a retry.
	fts.FireAll()
	assert.True(t, scanner.DepthSnapshot().Deferred, "deferred while agent active")

	// Going idle and firing the retry clears the deferred state via delivery.
	active.Store(false)
	fts.FireAll()
	require.Eventually(t, func() bool {
		s := scanner.DepthSnapshot()
		return !s.Deferred && s.Pending == 0
	}, 2*time.Second, 5*time.Millisecond, "deferred clears after idle delivery")
}

// userMatch builds a protected (explicit user action) match.
func userMatch(i int) *AlertMatch {
	line := fmt.Sprintf("panel message %d", i)
	return &AlertMatch{
		Pattern:      &AlertPattern{ID: "user:panel_message", Severity: AlertSeverityInfo, Category: "user"},
		Line:         line,
		ScriptID:     "proxy:p1",
		Source:       AlertSourceUser,
		RenderedText: line,
		Protected:    true,
	}
}

// TestAlertScanner_ProtectedNeverDropped pushes far more protected (user) items
// than MaxPending. None may be evicted: the queue grows past the cap rather
// than sacrifice user content, and every item is delivered with zero
// suppressed.
func TestAlertScanner_ProtectedNeverDropped(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	clk := newStubClock(time.Now())
	fts := &fakeTimerSet{}
	defer fts.drainTimers()

	var mu sync.Mutex
	var received []*AlertBatch

	const cap = 3
	const total = 15

	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:  1 * time.Millisecond,
		DedupeWindow: 10 * time.Minute,
		MaxPending:   cap,
		OnAlert: func(b *AlertBatch) {
			mu.Lock()
			received = append(received, b)
			mu.Unlock()
		},
	})
	scanner.clockNow = clk.Now
	scanner.afterFunc = fts.afterFunc
	defer scanner.Stop()

	for i := 0; i < total; i++ {
		scanner.Inject(userMatch(i))
	}

	snap := scanner.DepthSnapshot()
	assert.Equal(t, total, snap.Pending, "protected entries are not capped")
	assert.Equal(t, 0, snap.Suppressed, "protected entries are never suppressed")

	fts.FireAll()
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) > 0
	}, 2*time.Second, 5*time.Millisecond, "delivered")

	mu.Lock()
	defer mu.Unlock()
	var delivered, suppressed int
	for _, b := range received {
		delivered += len(b.Matches)
		suppressed += b.Suppressed
	}
	assert.Equal(t, total, delivered, "every protected (user) item delivered")
	assert.Equal(t, 0, suppressed, "nothing suppressed")
}

// TestAlertScanner_ProtectedBypassesDedup verifies a repeated user action is
// delivered every time (deliberate repeats are not collapsed).
func TestAlertScanner_ProtectedBypassesDedup(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	clk := newStubClock(time.Now())
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
	scanner.clockNow = clk.Now
	scanner.afterFunc = fts.afterFunc
	defer scanner.Stop()

	// Same protected line three times within the dedup window.
	for i := 0; i < 3; i++ {
		scanner.Inject(userMatch(0))
	}
	fts.FireAll()
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) > 0
	}, 2*time.Second, 5*time.Millisecond, "delivered")

	mu.Lock()
	defer mu.Unlock()
	var delivered int
	for _, b := range received {
		delivered += len(b.Matches)
	}
	assert.Equal(t, 3, delivered, "repeated user action delivered each time, not deduped")
}

// TestAlertScanner_ProtectedSurvivesErrorFlood mixes protected user items with
// an error flood over the cap. Errors are evicted; every protected item
// survives.
func TestAlertScanner_ProtectedSurvivesErrorFlood(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	clk := newStubClock(time.Now())
	fts := &fakeTimerSet{}
	defer fts.drainTimers()

	var mu sync.Mutex
	var received []*AlertBatch

	const cap = 5
	const protectedN = 3
	const errorsN = 20

	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:  1 * time.Millisecond,
		DedupeWindow: 10 * time.Minute,
		MaxPending:   cap,
		OnAlert: func(b *AlertBatch) {
			mu.Lock()
			received = append(received, b)
			mu.Unlock()
		},
	})
	scanner.clockNow = clk.Now
	scanner.afterFunc = fts.afterFunc
	defer scanner.Stop()

	for i := 0; i < protectedN; i++ {
		scanner.Inject(userMatch(i))
	}
	for i := 0; i < errorsN; i++ {
		scanner.Inject(errMatch(i))
	}

	fts.FireAll()
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) > 0
	}, 2*time.Second, 5*time.Millisecond, "delivered")

	mu.Lock()
	defer mu.Unlock()
	var protectedDelivered int
	for _, b := range received {
		for _, m := range b.Matches {
			if m.Protected {
				protectedDelivered++
			}
		}
	}
	assert.Equal(t, protectedN, protectedDelivered, "all protected user items survive the error flood")
}

// TestAlertScanner_SuppressedNoteOnlyWhenThrottled guards against a spurious
// suppressed line on normal (under-cap) batches.
func TestAlertScanner_SuppressedNoteOnlyWhenThrottled(t *testing.T) {
	b := &AlertBatch{
		ScriptID: "svc",
		Matches: []*AlertMatch{
			{Pattern: &AlertPattern{ID: "x", Severity: AlertSeverityError}, Line: "boom"},
		},
	}
	assert.NotContains(t, b.Format(), "suppressed", "no throttle note when nothing dropped")
	b.Suppressed = 4
	assert.Contains(t, b.Format(), "4 more alert(s) suppressed", "throttle note when dropped")
}
