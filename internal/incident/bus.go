package incident

import (
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
)

// Bus is the central sink for all incident events.
// MPSCBus is the production implementation; NopBus discards events.
type Bus interface {
	Publish(IncidentEvent)
}

// NopBus discards every event published to it.
type NopBus struct{}

func (NopBus) Publish(IncidentEvent) {}

// ── constants ─────────────────────────────────────────────────────────────────

const (
	busInboundCap     = 4096
	metaOverflowEvery = 100
	metaBusOverflowFP = "meta:bus_overflow"
	// dedupTrimEvery bounds Deduplicator growth: every Nth ingest into a session
	// prunes that session's expired fingerprints. Trim has no other production
	// caller, so without this a long-lived session accumulates one DedupEntry per
	// distinct fingerprint (each holding two IncidentEvent copies) for the life of
	// the session.
	dedupTrimEvery = 256
)

// ── sessionPipeline ───────────────────────────────────────────────────────────

// sessionPipeline holds per-session processing components.
type sessionPipeline struct {
	inbox  *Inbox
	dedup  *Deduplicator
	flow   *FlowController
	pinger *PingEmitter
	blobs  *BlobStore

	stopCh      chan struct{}
	ingestCount atomic.Int64 // drives opportunistic dedup Trim
}

// ── MPSCBus ───────────────────────────────────────────────────────────────────

// MPSCBus is a backpressure-safe, non-blocking event bus. Multiple producers
// call Fire or Publish; a single dispatch goroutine fans events to all active
// session pipelines. If the inbound channel is full, the newest event is
// dropped and the drop counter incremented. Every 100th drop synthesises one
// meta:bus_overflow warning incident so the AI agent sees bus pressure.
//
// Session lifecycles:
//   - AddSession: creates the sessionPipeline.
//   - RemoveSession: removes the pipeline; delivery is gated on stopCh under the
//     bus lock, so no drain wait is needed.
//   - Close: removes all sessions and stops the bus.
type MPSCBus struct {
	inbound chan *IncidentEvent
	meta    chan *IncidentEvent // small queue for meta:bus_overflow synthetics
	closed  chan struct{}
	once    sync.Once

	dropped atomic.Int64

	mu         sync.RWMutex
	perSession map[string]*sessionPipeline
	dispatchWg sync.WaitGroup

	// acks lets a caller await a control op it enqueued on the FIFO inbound
	// channel. Keyed by the control event's synthetic id (carried in its
	// Fingerprint, which a control event never uses for dedup because deliver
	// intercepts it before any inbox ingest).
	ackMu  sync.Mutex
	acks   map[string]chan int
	ackSeq atomic.Uint64

	// dispatchGate: when non-nil the dispatch goroutine waits on it before
	// processing each event. Closed by resumeDispatch (test-only).
	dispatchGate atomic.Pointer[chan struct{}]
}

// NewMPSCBus creates a ready-to-use MPSCBus. pingConfig may be nil for
// a headless bus (no pinger). The bus starts the central dispatch goroutine
// immediately.
func NewMPSCBus(pingConfig *PingConfig) *MPSCBus {
	b := &MPSCBus{
		inbound:    make(chan *IncidentEvent, busInboundCap),
		meta:       make(chan *IncidentEvent, 32),
		closed:     make(chan struct{}),
		perSession: make(map[string]*sessionPipeline),
		acks:       make(map[string]chan int),
	}
	b.dispatchWg.Add(1)
	go b.dispatchLoop()
	return b
}

// Fire is the single non-blocking entry point for signal producers.
// It must complete in <1μs even when the bus is saturated.
func (b *MPSCBus) Fire(ev *IncidentEvent) {
	b.fire(ev)
}

// fire is Fire with an enqueued/not-enqueued answer, so a caller that waits for
// the event to be applied can fail loud instead of blocking on a drop.
func (b *MPSCBus) fire(ev *IncidentEvent) bool {
	// Check closed state atomically before attempting to send.
	select {
	case <-b.closed:
		return false
	default:
	}
	select {
	case b.inbound <- ev:
		return true
	case <-b.closed:
		// Bus shut down mid-flight.
		return false
	default:
		n := b.dropped.Add(1)
		if n%metaOverflowEvery == 0 {
			b.emitMetaOverflow(n)
		}
		return false
	}
}

// Publish implements the Bus interface. It wraps Fire for callers that own the
// event value (not a pointer).
func (b *MPSCBus) Publish(ev IncidentEvent) {
	b.Fire(&ev)
}

// Dropped returns the cumulative count of dropped events.
func (b *MPSCBus) Dropped() int64 {
	return b.dropped.Load()
}

// AddSession registers a new session pipeline. It is idempotent — calling
// again with the same sessionID replaces the old pipeline (the old one is torn
// down first).
//
// mcpNotify, channelNotify, and ptyInject may all be nil (pinger channels are
// skipped). pingConfig may be nil; DefaultPingConfig() is used in that case.
func (b *MPSCBus) AddSession(sessionID string, mcpNotify MCPNotifyFn, channelNotify ChannelNotifyFn, ptyInject PTYInjectFn) {
	inbox := NewInbox(sessionID)
	dedup := NewDeduplicator(30 * time.Second)
	flow := NewFlowController(DefaultBucketConfigs)

	cfg := PingConfig{
		MCPNotifications: mcpNotify != nil,
		ChannelEnabled:   channelNotify != nil,
		PTYInjection:     ptyInject != nil,
	}
	var pinger *PingEmitter
	if mcpNotify != nil || channelNotify != nil || ptyInject != nil {
		pinger = NewPingEmitter(inbox, cfg, flow, mcpNotify, channelNotify, ptyInject)
	}

	pl := &sessionPipeline{
		inbox:  inbox,
		dedup:  dedup,
		flow:   flow,
		pinger: pinger,
		blobs:  NewBlobStore(0),
		stopCh: make(chan struct{}),
	}

	// Remove+insert must be one atomic step: two concurrent AddSession calls
	// with the same ID would otherwise both build pipelines, and the loser of
	// the final insert would leak its pinger and blob-store goroutines with a
	// stopCh nothing ever closes.
	b.mu.Lock()
	old, replaced := b.perSession[sessionID]
	b.perSession[sessionID] = pl
	b.mu.Unlock()

	if replaced {
		b.teardownPipeline(old)
	}
}

// RemoveSession tears down the session pipeline for sessionID. Delivery runs
// synchronously on the central dispatch goroutine and consults stopCh under the
// same lock that removes the pipeline, so once RemoveSession returns no new
// event can be ingested into this pipeline — there is nothing to drain.
func (b *MPSCBus) RemoveSession(sessionID string) {
	b.mu.Lock()
	pl, ok := b.perSession[sessionID]
	if ok {
		delete(b.perSession, sessionID)
	}
	b.mu.Unlock()

	if !ok {
		return
	}

	b.teardownPipeline(pl)
}

// teardownPipeline stops a pipeline's goroutines and releases its resources.
// Called after the pipeline is unlinked from perSession under b.mu, so no new
// event can be ingested into it.
func (b *MPSCBus) teardownPipeline(pl *sessionPipeline) {
	close(pl.stopCh)

	if pl.pinger != nil {
		pl.pinger.Stop()
	}
	pl.blobs.Close()
}

// Close shuts down the bus and all session pipelines.
func (b *MPSCBus) Close() {
	b.once.Do(func() {
		// Remove all sessions (tears down their dispatch goroutines).
		b.mu.RLock()
		ids := make([]string, 0, len(b.perSession))
		for id := range b.perSession {
			ids = append(ids, id)
		}
		b.mu.RUnlock()
		for _, id := range ids {
			b.RemoveSession(id)
		}

		// Signal closed first so Fire stops sending.
		close(b.closed)
		b.dispatchWg.Wait()
	})
}

// HasSession reports whether a pipeline exists for the given session.
func (b *MPSCBus) HasSession(sessionID string) bool {
	return b.getSessionPipeline(sessionID) != nil
}

// QuerySession queries the incident inbox for the given session.
// Returns nil entries and empty stats if no pipeline exists for the session.
func (b *MPSCBus) QuerySession(sessionID string, filter QueryFilter) ([]InboxEntry, Stats) {
	pl := b.getSessionPipeline(sessionID)
	if pl == nil {
		return nil, Stats{}
	}
	return pl.inbox.Query(filter)
}

// sourceControlClear is the reserved Source for retention control ops. A
// control event rides the normal inbound channel so it is FIFO-ordered with
// the incident events published before it: a build-success clear enqueued
// after a batch of compile errors always executes after those errors landed
// in the inboxes, never racing ahead of them. Control events are intercepted
// by deliver() and never ingested into any inbox.
const sourceControlClear Source = "control_clear_process"

// ClearProcessBefore retires processID's incidents (LastSeenAt <= before)
// from every session inbox. Ordered FIFO with previously published events;
// non-blocking like Fire — under a saturated bus the clear is dropped, which
// only means stale entries linger until the next trigger.
func (b *MPSCBus) ClearProcessBefore(processID string, before time.Time) {
	if processID == "" {
		return
	}
	ev := &IncidentEvent{
		Source:     sourceControlClear,
		ReceivedAt: before,
		Ctx:        Context{ProcessID: processID},
	}
	b.Fire(ev)
}

// ClearSessionBefore retires every incident (LastSeenAt <= before) from one
// session's inbox — the agent's explicit project-wide clear. Same FIFO
// control-op path as ClearProcessBefore.
func (b *MPSCBus) ClearSessionBefore(sessionID string, before time.Time) {
	if sessionID == "" {
		return
	}
	ev := &IncidentEvent{
		Source:     sourceControlClear,
		ReceivedAt: before,
		Ctx:        Context{SessionID: sessionID},
	}
	b.Fire(ev)
}

// controlAckTimeout bounds the wait for a control op to be applied. The
// dispatch goroutine only ever blocks on the test-only gate, so this is a
// liveness ceiling, not an expected latency — hitting it is reported to the
// caller rather than silently treated as success.
const controlAckTimeout = 5 * time.Second

// ClearSessionBeforeSync is ClearSessionBefore that waits for the dispatch
// goroutine to apply the clear, returning how many entries were retired.
//
// It deliberately reuses the SAME FIFO control-clear path as the daemon's
// internal retention triggers rather than reaching into the inbox directly:
// events already queued on the inbound channel were published BEFORE the
// caller asked to clear, so a direct clear would let them land afterwards and
// survive a boundary they are older than. One clear mechanism, one ordering
// rule; the only thing added here is an acknowledgement.
//
// ok=false means the bus was saturated or shutting down and the clear did NOT
// run — the caller must surface that, never report a clear that did not happen.
func (b *MPSCBus) ClearSessionBeforeSync(sessionID string, before time.Time) (int, bool) {
	if sessionID == "" {
		return 0, false
	}
	id := "control:" + strconv.FormatUint(b.ackSeq.Add(1), 10)
	ack := make(chan int, 1)

	b.ackMu.Lock()
	b.acks[id] = ack
	b.ackMu.Unlock()

	ev := &IncidentEvent{
		Source:      sourceControlClear,
		Fingerprint: id,
		ReceivedAt:  before,
		Ctx:         Context{SessionID: sessionID},
	}
	if !b.fire(ev) {
		b.releaseAck(id)
		return 0, false
	}

	timer := time.NewTimer(controlAckTimeout)
	defer timer.Stop()
	select {
	case n := <-ack:
		return n, true
	case <-b.closed:
		b.releaseAck(id)
		return 0, false
	case <-timer.C:
		b.releaseAck(id)
		return 0, false
	}
}

// completeControl delivers the applied count to a waiting caller, if any.
// Non-blocking (the ack channel is buffered), so the dispatch goroutine never
// stalls on a caller that already gave up.
func (b *MPSCBus) completeControl(id string, applied int) {
	if id == "" {
		return
	}
	b.ackMu.Lock()
	ack, ok := b.acks[id]
	delete(b.acks, id)
	b.ackMu.Unlock()
	if ok {
		select {
		case ack <- applied:
		default:
		}
	}
}

func (b *MPSCBus) releaseAck(id string) {
	b.ackMu.Lock()
	delete(b.acks, id)
	b.ackMu.Unlock()
}

// PinSession pins a fingerprint in one session's inbox so it survives band
// eviction and every retention clear. Errors (ErrPinTargetNotFound,
// ErrPinLimitReached) are returned to the caller, never swallowed. Scoped to
// the named session: there is no cross-session pin path (numbered contract 1).
func (b *MPSCBus) PinSession(sessionID, fingerprint, tag string) (*InboxEntry, error) {
	pl := b.getSessionPipeline(sessionID)
	if pl == nil {
		return nil, ErrPinTargetNotFound
	}
	return pl.inbox.Pin(fingerprint, tag)
}

// UnpinSession releases a pin in one session's inbox, reporting whether a pin
// was actually removed.
func (b *MPSCBus) UnpinSession(sessionID, fingerprint string) bool {
	pl := b.getSessionPipeline(sessionID)
	if pl == nil {
		return false
	}
	return pl.inbox.Unpin(fingerprint)
}

// PinnedCountSession reports how many pins one session currently holds.
func (b *MPSCBus) PinnedCountSession(sessionID string) int {
	pl := b.getSessionPipeline(sessionID)
	if pl == nil {
		return 0
	}
	return pl.inbox.PinnedCount()
}

// QueryAndMarkSession atomically queries a session inbox and marks the exact
// returned selection chosen from that snapshot before duplicate ingestion can
// mutate it.
func (b *MPSCBus) QueryAndMarkSession(sessionID string, filter QueryFilter, selectFingerprints func([]InboxEntry) []string, advanceCursor bool) ([]InboxEntry, Stats) {
	pl := b.getSessionPipeline(sessionID)
	if pl == nil {
		return nil, Stats{}
	}
	return pl.inbox.QueryAndMark(filter, selectFingerprints, advanceCursor)
}

// ReadSessionBlob hydrates a payload only from the caller session's bounded
// store. Hashes are content-addressed but stores are tenancy boundaries: never
// search another session when a hash is absent locally.
func (b *MPSCBus) ReadSessionBlob(sessionID, hash string) ([]byte, string, error) {
	pl := b.getSessionPipeline(sessionID)
	if pl == nil || pl.blobs == nil {
		return nil, "", ErrBlobEvicted
	}
	return pl.blobs.Read(hash)
}

// MarkReadSession marks entries as read in the given session's inbox.
// No-op if the session has no pipeline.
func (b *MPSCBus) MarkReadSession(sessionID string, fingerprints []string, advanceCursor bool) {
	pl := b.getSessionPipeline(sessionID)
	if pl == nil {
		return
	}
	pl.inbox.MarkRead(fingerprints, advanceCursor)
}

// FindFingerprintSession returns a copy of the inbox entry with the given
// fingerprint in sessionID's inbox, or nil when absent (or no pipeline).
func (b *MPSCBus) FindFingerprintSession(sessionID, fingerprint string) *InboxEntry {
	pl := b.getSessionPipeline(sessionID)
	if pl == nil {
		return nil
	}
	return pl.inbox.FindByFingerprint(fingerprint)
}

// getSessionPipeline returns the pipeline for sessionID, or nil.
// Used by tests and for diagnostic inspection.
func (b *MPSCBus) getSessionPipeline(sessionID string) *sessionPipeline {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.perSession[sessionID]
}

// drainMetaQueue returns all pending meta-incident events. Used by tests.
func (b *MPSCBus) drainMetaQueue() []*IncidentEvent {
	var out []*IncidentEvent
	for {
		select {
		case ev := <-b.meta:
			out = append(out, ev)
		default:
			return out
		}
	}
}

// ── internal ──────────────────────────────────────────────────────────────────

// pauseDispatch blocks the dispatch goroutine from consuming events.
// Test helper only. Must be paired with resumeDispatch.
func (b *MPSCBus) pauseDispatch() {
	ch := make(chan struct{})
	b.dispatchGate.Store(&ch)
}

// resumeDispatch unblocks the dispatch goroutine. Test helper only.
func (b *MPSCBus) resumeDispatch() {
	ptr := b.dispatchGate.Swap(nil)
	if ptr != nil {
		close(*ptr)
	}
}

// dispatchLoop is the central fan-out goroutine. It reads from the inbound
// channel and the meta channel, then delivers to all active sessions.
func (b *MPSCBus) dispatchLoop() {
	defer b.dispatchWg.Done()
	for {
		// Honor the test-only dispatch gate.
		if ptr := b.dispatchGate.Load(); ptr != nil {
			select {
			case <-*ptr:
			case <-b.closed:
				return
			}
		}

		select {
		case ev := <-b.inbound:
			if ev == nil {
				continue
			}
			b.deliver(ev)
		case ev := <-b.meta:
			b.deliver(ev)
		case <-b.closed:
			// Drain remaining inbound.
			for {
				select {
				case ev := <-b.inbound:
					if ev != nil {
						b.deliver(ev)
					}
				case ev := <-b.meta:
					b.deliver(ev)
				default:
					return
				}
			}
		}
	}
}

// deliver routes session-scoped events only to their originating pipeline.
// Only positively identified bus-overflow metadata is bus-wide; ambiguous
// sessionless events always fail closed.
func (b *MPSCBus) deliver(ev *IncidentEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if ev.Source == sourceControlClear {
		applied := 0
		for _, pl := range b.perSession {
			select {
			case <-pl.stopCh:
				continue
			default:
			}
			var n int
			switch {
			case ev.Ctx.ProcessID != "":
				n = pl.inbox.ClearProcessBefore(ev.Ctx.ProcessID, ev.ReceivedAt)
			case ev.Ctx.SessionID == pl.inbox.SessionID:
				n = pl.inbox.ClearAllBefore(ev.ReceivedAt)
			}
			applied += n
			if n > 0 {
				debug.Log("incident-retention", "cleared %d inbox entries (session %s)", n, pl.inbox.SessionID)
			}
		}
		b.completeControl(ev.Fingerprint, applied)
		return
	}
	if ev.Ctx.SessionID != "" {
		if pl := b.perSession[ev.Ctx.SessionID]; pl != nil {
			select {
			case <-pl.stopCh:
				return
			default:
				b.ingestToSession(pl, ev)
			}
		}
		return
	}
	if ev.Fingerprint != metaBusOverflowFP {
		// A sessionless production event has no trustworthy owner.
		return
	}
	for _, pl := range b.perSession {
		select {
		case <-pl.stopCh:
			// Pipeline is shutting down; skip delivery.
			continue
		default:
		}
		b.ingestToSession(pl, ev)
	}
}

// ingestToSession runs dedup → inbox for one session. Called on the dispatch// ingestToSession runs dedup → inbox for one session. Called on the dispatch
// goroutine; must not block.
func (b *MPSCBus) ingestToSession(pl *sessionPipeline, ev *IncidentEvent) {
	event := cloneIncidentEvent(*ev)
	if event.PayloadRef == nil && len(event.payload) > 0 {
		// Publish the reference only after bytes are committed. Fire remains
		// non-blocking (the dispatch goroutine owns this work), and query results
		// can never race an async spill that has not become readable yet.
		if ref, err := pl.blobs.Write(event.payload, "text/plain"); err == nil {
			event.PayloadRef = &ref
		}
		event.payload = nil
	}
	merged, de := pl.dedup.Ingest(pl.inbox.SessionID, event)
	// DedupEntry is a detached snapshot, but give Inbox explicit ownership of
	// its sample (including nested remediation maps/slices and PayloadRef).
	sample := cloneIncidentEvent(de.Last)
	entry := &InboxEntry{
		Fingerprint: ev.Fingerprint,
		FirstSeenAt: de.First.ReceivedAt,
		LastSeenAt:  de.Last.ReceivedAt,
		// One occurrence per delivered event. Inbox.Ingest merges additively
		// (existing.Count += entry.Count), so passing the dedup's cumulative
		// de.Count here would compound into a triangular sum.
		Count:    1,
		Sample:   &sample,
		Severity: ev.Severity,
	}
	if merged {
		entry.FirstSeenAt = de.First.ReceivedAt
	}
	pl.inbox.Ingest(entry)

	// Opportunistically reclaim expired dedup fingerprints. Runs on the single
	// dispatch goroutine, so no extra synchronization is needed here; Trim takes
	// the dedup's own lock.
	if pl.ingestCount.Add(1)%dedupTrimEvery == 0 {
		pl.dedup.Trim()
	}
}

// emitMetaOverflow synthesises a meta:bus_overflow warning incident and
// enqueues it on the meta channel. Non-blocking — if meta is full, the
// overflow incident itself is dropped (acceptable; agent will see the counter).
func (b *MPSCBus) emitMetaOverflow(dropCount int64) {
	ev := NewIncidentEvent(
		SourceProcessAlert,
		SeverityWarning,
		"bus_overflow",
		"incident bus overflow: events are being dropped",
		Context{},
		nil,
	)
	// Override fingerprint to a stable value for dedup.
	ev.Fingerprint = metaBusOverflowFP

	select {
	case b.meta <- &ev:
	default:
		// Contract #2 (drop-newest): the meta queue is also full, so even the
		// overflow notice is dropped. The cumulative counter (Dropped()) still
		// surfaces the pressure to the agent; log the dropped total for diagnosis.
		debug.Log("incident-bus", "meta overflow dropped; cumulative dropped=%d", dropCount)
	}
}
