package incident

import (
	"testing"
	"time"
)

// fakeClock drives a Coalescer without sleeping: Schedule records the delay it
// armed, and the test fires the armed timer by hand.
type fakeClock struct {
	t      time.Time
	delays []time.Duration
	fire   func()
}

type fakeTimer struct{}

func (fakeTimer) Stop() bool { return true }

func newFakeCoalescer(delays PingDelays, pings *int) (*Coalescer, *fakeClock) {
	fc := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	c := NewCoalescer(delays, func(IncidentEvent) { *pings++ })
	c.now = func() time.Time { return fc.t }
	c.afterFunc = func(d time.Duration, f func()) coalesceTimer {
		fc.delays = append(fc.delays, d)
		fc.fire = f
		return fakeTimer{}
	}
	return c, fc
}

func (fc *fakeClock) advance(d time.Duration) { fc.t = fc.t.Add(d) }
func (fc *fakeClock) last() time.Duration     { return fc.delays[len(fc.delays)-1] }

var scheduleDelays = PingDelays{
	Initial:    500 * time.Millisecond,
	Max:        10 * time.Second,
	ResetAfter: 30 * time.Second,
	MaxWait:    10 * time.Second,
}

func sevEv(fp string, sev Severity) IncidentEvent {
	return IncidentEvent{Fingerprint: fp, Severity: sev}
}

// Backoff memory must survive a fire. It used to be deleted with the slot, so
// an event recurring every few seconds was a "first occurrence" every time and
// pinged at Initial forever — and the ResetAfter branch was unreachable.
func TestCoalesce_BackoffSurvivesFire_AndResetsAfterIdle(t *testing.T) {
	t.Parallel()
	pings := 0
	c, fc := newFakeCoalescer(scheduleDelays, &pings)

	want := []time.Duration{
		500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second, 10 * time.Second,
	}
	for i, w := range want {
		c.Schedule(sevEv("fp", SeverityError))
		if got := fc.last(); got != w {
			t.Fatalf("occurrence %d: armed %v, want %v (backoff must persist across fires)", i+1, got, w)
		}
		fc.advance(fc.last())
		fc.fire() // the ping emits; the slot's memory stays
		fc.advance(5 * time.Second)
	}
	if pings != len(want) {
		t.Fatalf("pings = %d, want %d (every armed ping must emit)", pings, len(want))
	}

	// Idle past ResetAfter: prompt again.
	fc.advance(scheduleDelays.ResetAfter + time.Second)
	c.Schedule(sevEv("fp", SeverityError))
	if got := fc.last(); got != scheduleDelays.Initial {
		t.Fatalf("after idle reset armed %v, want Initial %v", got, scheduleDelays.Initial)
	}
}

// A pending ping must emit within MaxWait however fast events keep arriving.
// Each event re-arms the timer; uncapped, a stream with gaps shorter than the
// delay never pinged until it went quiet.
func TestCoalesce_MaxWaitBoundsADenseStream(t *testing.T) {
	t.Parallel()
	pings := 0
	c, fc := newFakeCoalescer(scheduleDelays, &pings)

	start := fc.t
	const gap = 300 * time.Millisecond
	for i := 0; i < 200; i++ { // 60s of events, never a quiet gap
		c.Schedule(sevEv("fp", SeverityError))
		deadline := fc.t.Add(fc.last())
		if over := deadline.Sub(start) - scheduleDelays.MaxWait; over > 0 && pings == 0 {
			t.Fatalf("event %d pushed the first ping %v past MaxWait", i, over)
		}
		if fc.last() <= gap { // the armed timer is due before the next event
			fc.advance(fc.last())
			fc.fire()
			fc.advance(gap - fc.last())
			continue
		}
		fc.advance(gap)
	}
	// 60s of stream with a 10s ceiling: at least five pings, not zero.
	if pings < 5 {
		t.Fatalf("dense stream emitted %d pings in 60s, want >= 5 (MaxWait ceiling)", pings)
	}
}

// A stale timer callback (one that lost the race with a reschedule) must not
// emit the ping early.
func TestCoalesce_SupersededTimerDoesNotFire(t *testing.T) {
	t.Parallel()
	pings := 0
	c, fc := newFakeCoalescer(scheduleDelays, &pings)

	c.Schedule(sevEv("fp", SeverityError))
	stale := fc.fire
	c.Schedule(sevEv("fp", SeverityError)) // re-arms; the first callback is now stale
	stale()
	if pings != 0 {
		t.Fatalf("superseded timer emitted %d ping(s)", pings)
	}
	fc.fire()
	if pings != 1 {
		t.Fatalf("current timer emitted %d ping(s), want 1", pings)
	}
}

// A higher severity than anything this backoff window has pinged about is not
// "more of the same": it must not wait behind the backed-off delay.
func TestCoalesce_HigherSeverityAfterFireIsPrompt(t *testing.T) {
	t.Parallel()
	pings := 0
	c, fc := newFakeCoalescer(scheduleDelays, &pings)

	for i := 0; i < 6; i++ { // back the warning stream off to Max
		c.Schedule(sevEv("fp", SeverityWarning))
		fc.advance(fc.last())
		fc.fire()
	}
	c.Schedule(sevEv("fp", SeverityWarning))
	if fc.last() != scheduleDelays.Max {
		t.Fatalf("setup: warning stream armed %v, want Max", fc.last())
	}
	fc.advance(fc.last())
	fc.fire()

	c.Schedule(sevEv("fp", SeverityError))
	if got := fc.last(); got != scheduleDelays.Initial {
		t.Fatalf("first error after a backed-off warning stream armed %v, want Initial", got)
	}
}
