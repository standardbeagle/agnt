package incident

import (
	"sync"
	"time"
)

// PingDelays configures the exponential backoff for coalesced ping emission.
// Initial is the first-occurrence delay; each repeat doubles it up to Max.
// After ResetAfter of no activity on a fingerprint the counter resets to Initial.
type PingDelays struct {
	Initial    time.Duration // first ping delay (default 500ms)
	Max        time.Duration // backoff ceiling (default 10s)
	ResetAfter time.Duration // inactivity window before counter resets (default 30s)
	// MaxWait bounds how long a pending ping can be pushed back by further
	// events. Every new event on a pending fingerprint re-arms the timer, so
	// without a ceiling a stream whose gaps are shorter than the current delay
	// never emits at all — the agent hears nothing for exactly as long as the
	// flood lasts. Zero means "use Max".
	MaxWait time.Duration
}

// DefaultPingDelays are the production backoff parameters.
var DefaultPingDelays = PingDelays{
	Initial:    500 * time.Millisecond,
	Max:        10 * time.Second,
	ResetAfter: 30 * time.Second,
	MaxWait:    10 * time.Second,
}

// NextPingDelay computes the ping delay for the nth occurrence of a fingerprint.
// occurrence is 1-based. The sequence doubles from Initial each occurrence until Max.
func NextPingDelay(occurrence int, delays PingDelays) time.Duration {
	if occurrence <= 1 {
		return delays.Initial
	}
	d := delays.Initial
	for i := 1; i < occurrence; i++ {
		d *= 2
		if d >= delays.Max {
			return delays.Max
		}
	}
	return d
}

// coalesceTimer is the cancellable handle a scheduled fire returns.
// *time.Timer satisfies it.
type coalesceTimer interface {
	Stop() bool
}

// coalesceSlot is one fingerprint's backoff state. It outlives the ping it
// schedules: occurrence and lastSeen are the backoff memory, and discarding
// them on every fire (the previous behaviour) made each recurrence a "first
// occurrence" again, so a steadily recurring event pinged at Initial forever
// and the ResetAfter branch could never be reached.
type coalesceSlot struct {
	event      IncidentEvent
	occurrence int
	lastSeen   time.Time

	// pending is true while a ping is scheduled and not yet emitted.
	pending bool
	// pendingSince is when the currently pending ping was first scheduled; the
	// MaxWait ceiling is measured from it.
	pendingSince time.Time
	timer        coalesceTimer
	// firedRank is the severity rank of the last ping emitted inside the
	// current backoff window (-1 = none). A later event that outranks it is not
	// "more of the same" and must not inherit the backed-off delay.
	firedRank int
	// gen invalidates a timer callback that lost a race with a reschedule or
	// flush: Stop cannot recall a callback already waiting on the lock.
	gen uint64
}

// coalesceSlotPruneAt is the slot count above which Schedule sweeps idle
// slots. Slots persist across fires, so without a sweep a caller that keys by
// real fingerprints would grow the map for the life of the session.
const coalesceSlotPruneAt = 64

// Coalescer schedules ping emissions with exponential backoff per fingerprint.
// The first occurrence fires after Initial; repeats — whether they arrive while
// a ping is pending or after it fired — are delayed more, up to Max, until the
// fingerprint has been idle for ResetAfter. A pending ping is never pushed back
// past MaxWait. Severity escalation (e.g. warning → error on the same
// fingerprint) immediately cancels the pending timer and fires the ping.
type Coalescer struct {
	delays PingDelays
	pingFn func(IncidentEvent)

	// now and afterFunc are the clock seams; tests replace them to assert the
	// schedule's shape without sleeping it out.
	now       func() time.Time
	afterFunc func(time.Duration, func()) coalesceTimer

	mu    sync.Mutex
	slots map[string]*coalesceSlot
}

// NewCoalescer creates a Coalescer that calls pingFn when a ping should emit.
// pingFn is invoked synchronously after the coalescer lock is released — on
// the caller's goroutine for Schedule/ForceFlush and on the timer goroutine
// for fire — so it must not block and must never call back into the
// Coalescer.
func NewCoalescer(delays PingDelays, pingFn func(IncidentEvent)) *Coalescer {
	if delays.MaxWait <= 0 {
		delays.MaxWait = delays.Max
	}
	return &Coalescer{
		delays: delays,
		pingFn: pingFn,
		now:    time.Now,
		afterFunc: func(d time.Duration, f func()) coalesceTimer {
			return time.AfterFunc(d, f)
		},
		slots: make(map[string]*coalesceSlot),
	}
}

// Schedule registers an event for coalesced ping emission. If a pending ping
// exists for the same fingerprint, its timer is rescheduled (potentially with a
// longer delay, but never past MaxWait from when it first became pending)
// unless the new event's severity is higher — in that case the pending timer is
// cancelled and the ping fires immediately.
func (c *Coalescer) Schedule(ev IncidentEvent) {
	now := c.now()
	fp := ev.Fingerprint

	c.mu.Lock()

	s, exists := c.slots[fp]
	if !exists {
		if len(c.slots) >= coalesceSlotPruneAt {
			c.pruneIdleLocked(now)
		}
		s = &coalesceSlot{firedRank: -1}
		c.slots[fp] = s
	} else if now.Sub(s.lastSeen) > c.delays.ResetAfter {
		// Idle long enough: the next ping is prompt again.
		s.occurrence = 0
		s.firedRank = -1
	}
	s.occurrence++
	s.lastSeen = now

	if s.pending {
		oldSev := s.event.Severity
		// The pending ping carries the highest severity that joined it. A
		// lower-severity event must not overwrite it: the next event at the
		// original severity would then read as an escalation and fire at once,
		// and a fire would record the lower rank, restarting the backoff.
		if sevRank(ev.Severity) >= sevRank(oldSev) {
			s.event = ev
		}
		// Severity escalation: cancel pending timer and fire immediately.
		if sevRank(ev.Severity) > sevRank(oldSev) {
			c.clearPendingLocked(s)
			s.firedRank = sevRank(ev.Severity)
			c.mu.Unlock()
			// Same invocation contract as fire/ForceFlush: synchronous, after
			// unlocking — never a goroutine spawned while holding c.mu.
			c.pingFn(ev)
			return
		}
	} else {
		if sevRank(ev.Severity) > s.firedRank {
			// First event at a severity this window has not pinged about yet:
			// prompt, not delayed behind the lower-severity backoff.
			s.occurrence = 1
		}
		s.event = ev
		s.pending = true
		s.pendingSince = now
	}

	// (Re)arm with the occurrence's delay, capped so the ping still emits
	// within MaxWait of first becoming pending however fast events arrive.
	delay := NextPingDelay(s.occurrence, c.delays)
	if remaining := c.delays.MaxWait - now.Sub(s.pendingSince); delay > remaining {
		delay = remaining
	}
	if delay < 0 {
		delay = 0
	}
	if s.timer != nil {
		s.timer.Stop()
	}
	s.gen++
	gen := s.gen
	s.timer = c.afterFunc(delay, func() { c.fire(fp, gen) })
	c.mu.Unlock()
}

// clearPendingLocked marks the slot's ping as no longer pending while keeping
// its backoff memory. Caller holds c.mu.
func (c *Coalescer) clearPendingLocked(s *coalesceSlot) {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.gen++
	s.pending = false
}

// pruneIdleLocked drops slots with no pending ping whose backoff memory has
// already expired (idle past ResetAfter) — forgetting them changes nothing.
// Caller holds c.mu.
func (c *Coalescer) pruneIdleLocked(now time.Time) {
	for fp, s := range c.slots {
		if !s.pending && now.Sub(s.lastSeen) > c.delays.ResetAfter {
			delete(c.slots, fp)
		}
	}
}

// ForceFlush immediately emits a pending ping for fp, if any. The slot's
// backoff memory is kept.
func (c *Coalescer) ForceFlush(fp string) {
	c.mu.Lock()
	s, ok := c.slots[fp]
	if !ok || !s.pending {
		c.mu.Unlock()
		return
	}
	ev := s.event
	c.clearPendingLocked(s)
	s.firedRank = sevRank(ev.Severity)
	c.mu.Unlock()
	c.pingFn(ev)
}

// Stop cancels all pending timers without firing them and forgets all backoff
// state. Call on shutdown. The Coalescer stays usable afterwards.
func (c *Coalescer) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.slots {
		c.clearPendingLocked(s)
	}
	c.slots = make(map[string]*coalesceSlot)
}

func (c *Coalescer) fire(fp string, gen uint64) {
	c.mu.Lock()
	s, ok := c.slots[fp]
	if !ok || !s.pending || s.gen != gen {
		// Superseded by a reschedule, a flush, or Stop.
		c.mu.Unlock()
		return
	}
	ev := s.event
	s.timer = nil
	s.pending = false
	s.firedRank = sevRank(ev.Severity)
	c.mu.Unlock()
	c.pingFn(ev)
}

// sevRank maps Severity to an integer for escalation comparison.
func sevRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 3
	case SeverityError:
		return 2
	case SeverityWarning:
		return 1
	default:
		return 0
	}
}
