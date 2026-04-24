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
	time.Sleep(50 * time.Millisecond) // 2.5× Initial (20ms)
	if count.Load() != 1 {
		t.Errorf("ping count: got %d, want 1", count.Load())
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
	c := NewCoalescer(testDelays, func(ev IncidentEvent) {
		mu.Lock()
		pings = append(pings, ev)
		mu.Unlock()
	})
	defer c.Stop()

	warn := makeEv("fp-esc")
	warn.Severity = SeverityWarning
	c.Schedule(warn)

	// Before the 20ms timer fires, schedule an error on the same fingerprint.
	time.Sleep(5 * time.Millisecond)
	errEv := makeEv("fp-esc")
	errEv.Severity = SeverityError
	c.Schedule(errEv)

	// The error must fire immediately (within a few ms via goroutine), not wait for delay.
	time.Sleep(15 * time.Millisecond)

	mu.Lock()
	n := len(pings)
	var firstSev Severity
	if n > 0 {
		firstSev = pings[0].Severity
	}
	mu.Unlock()

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

	// Fire a few times to build up occurrence count, wait for ping to fire.
	for i := 0; i < 3; i++ {
		c.Schedule(makeEv("fp-reset"))
	}
	time.Sleep(60 * time.Millisecond) // let the pending ping fire + reset window expire

	priorCount := count.Load()
	if priorCount == 0 {
		t.Fatal("no pings before reset test")
	}

	// After ResetAfter of idle, next occurrence should use Initial delay again.
	c.Schedule(makeEv("fp-reset"))
	time.Sleep(15 * time.Millisecond) // 3× Initial (5ms)
	if count.Load() <= priorCount {
		t.Error("ping not fired after occurrence reset — Initial delay not applied")
	}
}
