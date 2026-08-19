package overlay

// Alert delivery machinery: the single delivery goroutine that serializes
// onAlert calls (PTY injection), the flush/drain paths that feed it, and
// the Stop drain contract. Scanner core lives in alerts.go, batch
// formatting in alert_batch.go.

import (
	"sort"
	"time"
)

// ensureDeliveryLoop starts the single delivery goroutine on demand, exactly
// once, and never after Stop. Returns whether the loop is running so callers
// know a send to deliverCh will be consumed.
func (s *AlertScanner) ensureDeliveryLoop() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deliveryStarted {
		return true
	}
	if s.stopped.Load() {
		return false
	}
	s.deliveryStarted = true
	go s.deliveryLoop()
	return true
}

// deliveryLoop is the single consumer of deliverCh. It calls onAlert one batch
// at a time so PTY injection is never concurrent. On Stop it drains any queued
// batches and exits, signaling deliverDone.
func (s *AlertScanner) deliveryLoop() {
	deliver := func(b *AlertBatch) {
		if s.onAlert != nil {
			s.onAlert(b)
		}
	}
	for {
		select {
		case b := <-s.deliverCh:
			deliver(b)
		case <-s.stopCh:
			for {
				select {
				case b := <-s.deliverCh:
					deliver(b)
				default:
					close(s.deliverDone)
					return
				}
			}
		}
	}
}

// enqueue hands a batch to the delivery goroutine, starting it on first use.
// It blocks (providing backpressure) if the buffer is full, but never after Stop.
func (s *AlertScanner) enqueue(b *AlertBatch) {
	if !s.ensureDeliveryLoop() {
		return // stopped before any delivery; nothing will consume the batch
	}
	select {
	case s.deliverCh <- b:
	case <-s.stopCh:
	}
}

// flush delivers the current batch of alerts.
func (s *AlertScanner) flush() {
	if s.stopped.Load() {
		return
	}

	s.mu.Lock()

	// If AI is active, defer the flush (up to maxRetries)
	if s.actState != nil && s.actState() == ActivityActive && s.flushRetries < s.maxRetries {
		s.flushRetries++
		s.batchTimer = s.afterFunc(s.retryInterval, func() {
			s.flush()
		})
		s.mu.Unlock()
		return
	}

	if len(s.pending) == 0 {
		s.batchTimer = nil
		s.batchDeadline = time.Time{}
		s.flushRetries = 0
		s.mu.Unlock()
		return
	}

	s.batchTimer = nil
	s.batchDeadline = time.Time{}
	s.flushRetries = 0
	byScript, suppressed := s.drainPendingLocked()
	s.mu.Unlock()

	// Deliver batches via the single delivery goroutine so PTY injection is
	// serialized even if this flush overlaps another. Only the post-drain
	// deliverPending path may call onAlert directly.
	if s.onAlert != nil {
		deliverByScript(byScript, suppressed, s.enqueue)
	}

	// Prune old dedup entries periodically
	s.pruneDedup()
}

// drainPendingLocked takes the pending batch and the suppressed count,
// resetting both, and returns the matches grouped by script for delivery.
// Caller must hold s.mu.
func (s *AlertScanner) drainPendingLocked() (map[string][]*AlertMatch, int) {
	byScript := map[string][]*AlertMatch{}
	for _, m := range s.pending {
		byScript[m.ScriptID] = append(byScript[m.ScriptID], m)
	}
	s.pending = nil
	suppressed := s.suppressed
	s.suppressed = 0
	return byScript, suppressed
}

// deliverByScript dispatches grouped matches in deterministic script order,
// attaching the suppressed count to the first batch so the overload-throttle
// summary is delivered exactly once.
func deliverByScript(byScript map[string][]*AlertMatch, suppressed int, onAlert func(*AlertBatch)) {
	scriptIDs := make([]string, 0, len(byScript))
	for id := range byScript {
		scriptIDs = append(scriptIDs, id)
	}
	sort.Strings(scriptIDs)

	for i, sid := range scriptIDs {
		batch := &AlertBatch{Matches: byScript[sid], ScriptID: sid}
		if i == 0 {
			batch.Suppressed = suppressed
		}
		onAlert(batch)
	}
}

// Stop stops the scanner and flushes any pending alerts.
func (s *AlertScanner) Stop() {
	if !s.stopped.CompareAndSwap(false, true) {
		return
	}
	close(s.stopCh)

	s.mu.Lock()
	if s.batchTimer != nil {
		s.batchTimer.Stop()
		s.batchTimer = nil
	}
	started := s.deliveryStarted
	s.mu.Unlock()

	// Wait for the delivery goroutine to drain queued batches and exit, so the
	// final flush below cannot call onAlert concurrently with it. Skip the wait
	// when the loop was never started (no alert ever enqueued) — deliverDone
	// would never be closed.
	//
	// The wait is bounded: onAlert injects into the PTY and can block for
	// several seconds per batch (it waits on child activity that never comes
	// when the child has already exited). Blocking teardown on that would stall
	// session/daemon shutdown. If the goroutine doesn't drain within the grace
	// window, return without the final flush — skipping deliverPending() is
	// also required for correctness, since the goroutine is still live and a
	// concurrent deliverPending() would invoke onAlert twice over.
	if started {
		select {
		case <-s.deliverDone:
			// Drained and exited — safe to flush synchronously.
			s.deliverPending()
		case <-time.After(alertStopGrace):
			// A batch is stuck in a blocking onAlert; don't stall teardown.
		}
		return
	}

	// Never started: no delivery goroutine, so the flush is race-free.
	s.deliverPending()
}

// alertStopGrace bounds how long Stop() waits for the delivery goroutine to
// drain before abandoning the final flush and returning, so a blocked PTY
// injection cannot stall session/daemon teardown.
const alertStopGrace = 2 * time.Second

// deliverPending delivers any remaining pending alerts without deferral.
func (s *AlertScanner) deliverPending() {
	s.mu.Lock()
	if len(s.pending) == 0 {
		s.mu.Unlock()
		return
	}

	byScript, suppressed := s.drainPendingLocked()
	s.mu.Unlock()

	if s.onAlert != nil {
		deliverByScript(byScript, suppressed, s.onAlert)
	}
}
