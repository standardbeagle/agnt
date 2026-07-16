package incident

import (
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
	}
	b.dispatchWg.Add(1)
	go b.dispatchLoop()
	return b
}

// Fire is the single non-blocking entry point for signal producers.
// It must complete in <1μs even when the bus is saturated.
func (b *MPSCBus) Fire(ev *IncidentEvent) {
	// Check closed state atomically before attempting to send.
	select {
	case <-b.closed:
		return
	default:
	}
	select {
	case b.inbound <- ev:
	case <-b.closed:
		// Bus shut down mid-flight.
	default:
		n := b.dropped.Add(1)
		if n%metaOverflowEvery == 0 {
			b.emitMetaOverflow(n)
		}
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
	b.RemoveSession(sessionID)

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
		stopCh: make(chan struct{}),
	}

	b.mu.Lock()
	b.perSession[sessionID] = pl
	b.mu.Unlock()
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

	close(pl.stopCh)

	if pl.pinger != nil {
		pl.pinger.Stop()
	}
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

// MarkReadSession marks entries as read in the given session's inbox.
// No-op if the session has no pipeline.
func (b *MPSCBus) MarkReadSession(sessionID string, fingerprints []string, advanceCursor bool) {
	pl := b.getSessionPipeline(sessionID)
	if pl == nil {
		return
	}
	pl.inbox.MarkRead(fingerprints, advanceCursor)
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
// Events without a session ID are bus-wide (for example, bus overflow meta
// incidents) and retain the legacy fan-out behavior.
func (b *MPSCBus) deliver(ev *IncidentEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()
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

// ingestToSession runs dedup → inbox for one session. Called on the dispatch
// goroutine; must not block.
func (b *MPSCBus) ingestToSession(pl *sessionPipeline, ev *IncidentEvent) {
	merged, de := pl.dedup.Ingest(pl.inbox.SessionID, *ev)
	entry := &InboxEntry{
		Fingerprint: ev.Fingerprint,
		FirstSeenAt: de.First.ReceivedAt,
		LastSeenAt:  de.Last.ReceivedAt,
		// One occurrence per delivered event. Inbox.Ingest merges additively
		// (existing.Count += entry.Count), so passing the dedup's cumulative
		// de.Count here would compound into a triangular sum.
		Count:    1,
		Sample:   &de.Last,
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
