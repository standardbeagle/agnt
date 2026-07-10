package incident

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testDelays are fast enough for test use: Initial=20ms, Max=160ms, ResetAfter=200ms.
var testDelays = PingDelays{
	Initial:    20 * time.Millisecond,
	Max:        160 * time.Millisecond,
	ResetAfter: 200 * time.Millisecond,
}

// awaitCount blocks until the counter reaches want, or fails. The deadline is a
// deadlock guard: a fixed sleep sized to the coalescer's delay is racing the
// very AfterFunc it waits for, and loses whenever the machine is loaded.
func awaitCount(t *testing.T, count *atomic.Int32, want int32, msg string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for count.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("%s: got %d, want %d", msg, count.Load(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestNextPingDelay_Sequence(t *testing.T) {
	t.Parallel()
	delays := PingDelays{Initial: 500 * time.Millisecond, Max: 10 * time.Second}
	// Production sequence: 500ms, 1s, 2s, 4s, 8s, 10s (capped), 10s, …
	want := []time.Duration{
		500 * time.Millisecond,
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		10 * time.Second,
		10 * time.Second,
		10 * time.Second,
		10 * time.Second,
		10 * time.Second,
	}
	for i, w := range want {
		got := NextPingDelay(i+1, delays)
		if got != w {
			t.Errorf("occurrence %d: got %v, want %v", i+1, got, w)
		}
	}
}

func TestCoalesce_FirstOccurrencePings(t *testing.T) {
	t.Parallel()
	var count atomic.Int32
	c := NewCoalescer(testDelays, func(ev IncidentEvent) { count.Add(1) })
	defer c.Stop()

	c.Schedule(makeEv("fp1"))
	awaitCount(t, &count, 1, "first occurrence should ping")

	// And exactly one: nothing else is scheduled, so give a spurious second ping
	// a chance to appear before declaring success.
	time.Sleep(3 * testDelays.Initial)
	if got := count.Load(); got != 1 {
		t.Errorf("ping count: got %d, want exactly 1", got)
	}
}

func TestCoalesce_BurstCoalesces(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var pings []time.Time
	c := NewCoalescer(testDelays, func(ev IncidentEvent) {
		mu.Lock()
		pings = append(pings, time.Now())
		mu.Unlock()
	})
	defer c.Stop()

	start := time.Now()
	// Fire 5 rapid events — each cancels the previous timer.
	for i := 0; i < 5; i++ {
		c.Schedule(makeEv("fp-burst"))
		time.Sleep(2 * time.Millisecond)
	}
	// Wait past max possible delay.
	time.Sleep(testDelays.Max + 60*time.Millisecond)

	mu.Lock()
	n := len(pings)
	mu.Unlock()

	if n == 0 {
		t.Fatalf("no pings fired after %v", time.Since(start))
	}
	// Burst of 5 should produce at most 3 pings (usually 1 or 2).
	if n > 3 {
		t.Errorf("burst coalesce: got %d pings, want ≤3", n)
	}
}

func TestCoalesce_SeverityEscalation_ForceFlushes(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var pings []IncidentEvent
	// A dedicated, deliberately long Initial delay — not testDelays'
	// 20ms — so the warning's own timer cannot possibly fire on its own
	// during the short window this test needs to schedule the escalating
	// error. With testDelays.Initial=20ms, the "schedule the error 5ms
	// later" step below only had a 15ms margin before the warning fired on
	// its own regular schedule; under CPU saturation that 5ms sleep can
	// itself stretch past 20ms, so the warning already fired before the
	// error was even scheduled — a false "escalation didn't fire
	// immediately" failure with nothing wrong in the escalation path.
	// 2s leaves a three-orders-of-magnitude margin.
	delays := PingDelays{Initial: 2 * time.Second, Max: 2 * time.Second, ResetAfter: 2 * time.Second}
	c := NewCoalescer(delays, func(ev IncidentEvent) {
		mu.Lock()
		pings = append(pings, ev)
		mu.Unlock()
	})
	defer c.Stop()

	warn := makeEv("fp-esc")
	warn.Severity = SeverityWarning
	c.Schedule(warn)

	// Before the (2s) timer could possibly fire, schedule an error on the
	// same fingerprint.
	time.Sleep(5 * time.Millisecond)
	errEv := makeEv("fp-esc")
	errEv.Severity = SeverityError
	c.Schedule(errEv)

	// The error must force-flush synchronously rather than waiting for the
	// coalesce delay to elapse (see Coalescer.Schedule's escalation path).
	// A fixed sleep here raced that force-flush against the goroutine that
	// runs it — 15ms is razor-thin once the scheduler is loaded, which is
	// exactly the flake this task exists to remove. Poll instead, with a
	// deadline generous enough that only a real regression (the escalation
	// falling back to waiting out the 2s Initial delay above) would trip
	// it, not ordinary scheduler jitter. Kept well under that 2s so a
	// "the warning's own timer eventually fired" false pass can't sneak in.
	deadline := time.Now().Add(500 * time.Millisecond)
	var n int
	var firstSev Severity
	for {
		mu.Lock()
		n = len(pings)
		if n > 0 {
			firstSev = pings[0].Severity
		}
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}

	if n < 1 {
		t.Fatal("no ping fired after severity escalation")
	}
	if firstSev != SeverityError {
		t.Errorf("first ping severity: got %q, want error (escalation fires immediately)", firstSev)
	}
}

func TestCoalesce_ForceFlush_EmitsPending(t *testing.T) {
	t.Parallel()
	var count atomic.Int32
	c := NewCoalescer(PingDelays{
		Initial:    10 * time.Second, // long delay — timer won't fire in test
		Max:        10 * time.Second,
		ResetAfter: 30 * time.Second,
	}, func(IncidentEvent) { count.Add(1) })
	defer c.Stop()

	c.Schedule(makeEv("fp-flush"))
	c.ForceFlush("fp-flush")

	if count.Load() != 1 {
		t.Errorf("ForceFlush: count=%d, want 1", count.Load())
	}
}

func TestCoalesce_Stop_NoPanics(t *testing.T) {
	t.Parallel()
	c := NewCoalescer(testDelays, func(IncidentEvent) {})
	for i := 0; i < 10; i++ {
		c.Schedule(makeEv("fp-stop"))
	}
	c.Stop() // must not panic or deadlock
}

func TestCoalesce_ResetAfterIdle(t *testing.T) {
	t.Parallel()
	delays := PingDelays{
		Initial:    5 * time.Millisecond,
		Max:        40 * time.Millisecond,
		ResetAfter: 50 * time.Millisecond, // short reset window for test
	}
	var count atomic.Int32
	c := NewCoalescer(delays, func(IncidentEvent) { count.Add(1) })
	defer c.Stop()

	// Fire a few times to build up occurrence count, wait for ping to fire
	// and the reset window to expire. Polling (with a deadline an order of
	// magnitude past the 50ms ResetAfter) instead of a fixed 60ms sleep:
	// under CPU saturation the coalescer's own AfterFunc goroutines race
	// wall time exactly as much as this test's sleep does, so a tight fixed
	// wait flakes independent of any real defect.
	for i := 0; i < 3; i++ {
		c.Schedule(makeEv("fp-reset"))
	}
	awaitCount(t, &count, 1, "no pings before reset test")
	time.Sleep(delays.ResetAfter) // ensure the reset window has actually elapsed

	priorCount := count.Load()

	// After ResetAfter of idle, next occurrence should use Initial delay
	// again — i.e. ping again promptly rather than needing a full escalated
	// delay. Same reasoning: poll instead of a fixed sleep.
	c.Schedule(makeEv("fp-reset"))
	awaitCount(t, &count, priorCount+1, "ping not fired after occurrence reset — Initial delay not applied")
}
