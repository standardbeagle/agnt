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
}

// DefaultPingDelays are the production backoff parameters.
var DefaultPingDelays = PingDelays{
	Initial:    500 * time.Millisecond,
	Max:        10 * time.Second,
	ResetAfter: 30 * time.Second,
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

type coalesceSlot struct {
	pending    IncidentEvent
	occurrence int
	lastSeen   time.Time
	timer      *time.Timer
}

// Coalescer schedules ping emissions with exponential backoff per fingerprint.
// The first occurrence fires after Initial; repeats are delayed more. Severity
// escalation (e.g. warning → error on the same fingerprint) immediately cancels
// the pending timer and fires the ping.
type Coalescer struct {
	delays PingDelays
	pingFn func(IncidentEvent)

	mu    sync.Mutex
	slots map[string]*coalesceSlot
}

// NewCoalescer creates a Coalescer that calls pingFn when a ping should emit.
// pingFn must not block.
func NewCoalescer(delays PingDelays, pingFn func(IncidentEvent)) *Coalescer {
	return &Coalescer{
		delays: delays,
		pingFn: pingFn,
		slots:  make(map[string]*coalesceSlot),
	}
}

// Schedule registers an event for coalesced ping emission. If a pending ping
// exists for the same fingerprint, its timer is rescheduled (potentially with a
// longer delay) unless the new event's severity is higher — in that case the
// pending timer is cancelled and the ping fires immediately.
func (c *Coalescer) Schedule(ev IncidentEvent) {
	now := time.Now()
	fp := ev.Fingerprint

	c.mu.Lock()
	defer c.mu.Unlock()

	s, exists := c.slots[fp]
	if exists {
		// Reset occurrence counter if fingerprint was idle long enough.
		if now.Sub(s.lastSeen) > c.delays.ResetAfter {
			s.occurrence = 0
		}
		s.occurrence++
		s.lastSeen = now
		oldSev := s.pending.Severity
		s.pending = ev

		// Severity escalation: cancel pending timer and fire immediately.
		if sevRank(ev.Severity) > sevRank(oldSev) {
			s.timer.Stop()
			go c.pingFn(ev)
			delete(c.slots, fp)
			return
		}

		// Reschedule with new (possibly longer) delay.
		s.timer.Stop()
		delay := NextPingDelay(s.occurrence, c.delays)
		s.timer = time.AfterFunc(delay, func() { c.fire(fp) })
		return
	}

	// First occurrence.
	slot := &coalesceSlot{
		pending:    ev,
		occurrence: 1,
		lastSeen:   now,
	}
	delay := NextPingDelay(1, c.delays)
	slot.timer = time.AfterFunc(delay, func() { c.fire(fp) })
	c.slots[fp] = slot
}

// ForceFlush immediately emits a pending ping for fp, if any, and resets the slot.
func (c *Coalescer) ForceFlush(fp string) {
	c.mu.Lock()
	s, ok := c.slots[fp]
	if !ok {
		c.mu.Unlock()
		return
	}
	s.timer.Stop()
	ev := s.pending
	delete(c.slots, fp)
	c.mu.Unlock()
	c.pingFn(ev)
}

// Stop cancels all pending timers without firing them. Call on shutdown.
func (c *Coalescer) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.slots {
		s.timer.Stop()
	}
	c.slots = make(map[string]*coalesceSlot)
}

func (c *Coalescer) fire(fp string) {
	c.mu.Lock()
	s, ok := c.slots[fp]
	if !ok {
		c.mu.Unlock()
		return
	}
	ev := s.pending
	delete(c.slots, fp)
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
