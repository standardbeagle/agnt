// Package daemon — hold_buffer.go
//
// HoldBuffer is the per-proxy hold-and-emit gate that backs the transport
// outage suppression logic. When the broadcast gate decides a proxy is in
// outage and an entry should be suppressed, the entry is pushed into the
// hold buffer rather than dropped. The buffer takes one of three actions
// per held entry:
//
//   1. Recovery before window expires AND entry matches a transport /
//      JS-cascade pattern → drop (was rebuild noise).
//   2. Recovery before window expires AND entry is a non-cascade error →
//      emit immediately (genuine error that occurred during the outage).
//   3. Window expires with no recovery → emit (real outage; user needs
//      to see it).
//
// Held entries with the same (proxyID, fingerprint) coalesce: the second
// occurrence increments a count rather than enqueueing a new entry. The
// emitted entry carries the most recent payload but the merge count is
// surfaced via a synthesised diagnostic prefix on the summary so the AI
// agent sees the burst was N events, not 1.
//
// The buffer is driven by a single goroutine per buffer instance. All
// public methods are non-blocking — they push messages onto the loop's
// channel and return immediately. The loop owns the entries map and the
// timer. This avoids the "deadline-driven heap with concurrent eviction"
// dance that would otherwise be required.

package daemon

import (
	"container/heap"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/proxy"
)

// HoldEmitFn is the callback invoked when the buffer decides to release
// a held entry. The implementation is responsible for fanning the entry
// to all consumers (AlertHub stream sinks, incident bus adapters, etc).
type HoldEmitFn func(entry proxy.LogEntry, proxyID string, mergedCount int)

// holdEntry is the internal record kept per (proxyID, fingerprint).
type holdEntry struct {
	proxyID     string
	fingerprint string
	entry       proxy.LogEntry
	enqueuedAt  time.Time
	emitAt      time.Time
	count       int
	cascade     bool

	// heapIndex is maintained by the heap.Interface methods; -1 when not
	// in the heap (entry was emitted or dropped).
	heapIndex int
}

// holdHeap is a min-heap on emitAt. Owned by the buffer's loop goroutine;
// no locking on this side.
type holdHeap []*holdEntry

func (h holdHeap) Len() int           { return len(h) }
func (h holdHeap) Less(i, j int) bool { return h[i].emitAt.Before(h[j].emitAt) }
func (h holdHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].heapIndex = i
	h[j].heapIndex = j
}
func (h *holdHeap) Push(x any) {
	e := x.(*holdEntry)
	e.heapIndex = len(*h)
	*h = append(*h, e)
}
func (h *holdHeap) Pop() any {
	old := *h
	n := len(old)
	e := old[n-1]
	old[n-1] = nil
	e.heapIndex = -1
	*h = old[:n-1]
	return e
}

// holdMessage is the channel envelope for the loop goroutine.
type holdMessage struct {
	kind     holdMsgKind
	entry    proxy.LogEntry
	proxyID  string
	fp       string
	cascade  bool
	now      time.Time
	resultCh chan int // for synchronous size-query in tests; nil otherwise
}

type holdMsgKind int

const (
	holdMsgPush holdMsgKind = iota
	holdMsgRecover
	holdMsgForget
	holdMsgSize
	holdMsgStop
)

// HoldBuffer suppresses transport-cascade noise during proxy outages.
// One instance is shared across all proxies in the daemon; per-proxy
// state lives in the entries map keyed by (proxyID|fingerprint).
type HoldBuffer struct {
	cfg      *config.OutageHoldConfig
	emit     HoldEmitFn
	nowFn    func() time.Time
	patterns []string // lowercased cache of cfg.JSCascadePatterns

	ch     chan holdMessage
	stopCh chan struct{}
	doneCh chan struct{}

	// closed gates re-entry into the channel after Stop. Reads/writes are
	// atomic; the loop goroutine is the only writer.
	closed atomic.Bool
}

// NewHoldBuffer constructs a buffer with the given config and emit
// callback. The buffer goroutine starts immediately. cfg may be nil; the
// buffer behaves as enabled with default values when so.
func NewHoldBuffer(cfg *config.OutageHoldConfig, emit HoldEmitFn) *HoldBuffer {
	patterns := cfg.GetJSCascadePatterns()
	lowered := make([]string, len(patterns))
	for i, p := range patterns {
		lowered[i] = strings.ToLower(p)
	}
	b := &HoldBuffer{
		cfg:      cfg,
		emit:     emit,
		nowFn:    time.Now,
		patterns: lowered,
		ch:       make(chan holdMessage, 256),
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
	go b.loop()
	return b
}

// Hold pushes entry into the buffer for proxyID. classifyCascade is the
// caller's classification of whether the entry is a transport / JS
// cascade message (drop on recovery) versus a genuine error (emit on
// recovery). For transport diagnostics, callers always pass true. For
// browser-JS errors, callers pass the result of MatchesJSCascade. For
// HTTP entries, callers pass true (5xx during an outage is upstream
// flapping; the gate only forwards 5xx when not in outage).
//
// Non-blocking: queues onto the loop channel with a default branch so
// pathological backpressure drops the held entry rather than stalling
// the gate.
func (b *HoldBuffer) Hold(entry proxy.LogEntry, proxyID, fingerprint string, classifyCascade bool) {
	if b == nil || b.closed.Load() {
		return
	}
	msg := holdMessage{
		kind:    holdMsgPush,
		entry:   entry,
		proxyID: proxyID,
		fp:      fingerprint,
		cascade: classifyCascade,
		now:     b.nowFn(),
	}
	select {
	case b.ch <- msg:
	default:
		// Buffer overrun: emit immediately rather than lose the signal.
		// Worst case the agent sees a single noise entry; preferable to
		// a silent drop.
		if b.emit != nil {
			b.emit(entry, proxyID, 1)
		}
	}
}

// OnRecovery signals that proxyID has exited transport outage. Cascade
// entries are dropped; non-cascade entries are emitted immediately.
func (b *HoldBuffer) OnRecovery(proxyID string) {
	if b == nil || b.closed.Load() || proxyID == "" {
		return
	}
	select {
	case b.ch <- holdMessage{kind: holdMsgRecover, proxyID: proxyID, now: b.nowFn()}:
	default:
		// Recovery messages should never be dropped; if they are, held
		// entries for this proxy will still emit on window expiry, just
		// without the cascade-drop optimisation. Acceptable degradation.
	}
}

// Forget drops all held state for proxyID without emission. Called when
// a proxy is fully cleaned up.
func (b *HoldBuffer) Forget(proxyID string) {
	if b == nil || b.closed.Load() {
		return
	}
	select {
	case b.ch <- holdMessage{kind: holdMsgForget, proxyID: proxyID}:
	default:
	}
}

// Stop terminates the buffer loop. Pending entries are dropped without
// emission — callers should drain via Forget or wait for window expiry
// before stopping in production.
func (b *HoldBuffer) Stop() {
	if b == nil {
		return
	}
	if !b.closed.CompareAndSwap(false, true) {
		return
	}
	close(b.stopCh)
	<-b.doneCh
}

// MatchesJSCascade reports whether msg matches any configured cascade
// pattern. Case-insensitive substring match. Exposed for callers that
// classify entries before calling Hold.
func (b *HoldBuffer) MatchesJSCascade(msg string) bool {
	if b == nil {
		return false
	}
	lower := strings.ToLower(msg)
	for _, p := range b.patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// pendingCount returns the number of entries currently held. Test helper.
func (b *HoldBuffer) pendingCount() int {
	if b == nil || b.closed.Load() {
		return 0
	}
	resultCh := make(chan int, 1)
	select {
	case b.ch <- holdMessage{kind: holdMsgSize, resultCh: resultCh}:
	case <-time.After(time.Second):
		return -1
	}
	select {
	case n := <-resultCh:
		return n
	case <-time.After(time.Second):
		return -1
	}
}

// loop is the single owner goroutine for the entries map and timer.
func (b *HoldBuffer) loop() {
	defer close(b.doneCh)

	entries := make(map[string]*holdEntry)
	hh := &holdHeap{}
	heap.Init(hh)

	var timer *time.Timer
	var timerC <-chan time.Time

	resetTimer := func() {
		if hh.Len() == 0 {
			if timer != nil {
				timer.Stop()
			}
			timerC = nil
			return
		}
		next := (*hh)[0].emitAt
		d := next.Sub(b.nowFn())
		if d < 0 {
			d = 0
		}
		if timer == nil {
			timer = time.NewTimer(d)
		} else {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(d)
		}
		timerC = timer.C
	}

	emit := func(e *holdEntry) {
		if b.emit != nil {
			func() {
				defer func() { _ = recover() }()
				b.emit(e.entry, e.proxyID, e.count)
			}()
		}
	}

	for {
		select {
		case <-b.stopCh:
			if timer != nil {
				timer.Stop()
			}
			return
		case msg := <-b.ch:
			b.handleMessage(&msg, entries, hh, emit)
			resetTimer()
		case <-timerC:
			now := b.nowFn()
			for hh.Len() > 0 && !(*hh)[0].emitAt.After(now) {
				e := heap.Pop(hh).(*holdEntry)
				delete(entries, holdKey(e.proxyID, e.fingerprint))
				emit(e)
			}
			resetTimer()
		}
	}
}

func (b *HoldBuffer) handleMessage(msg *holdMessage, entries map[string]*holdEntry, hh *holdHeap, emit func(*holdEntry)) {
	switch msg.kind {
	case holdMsgPush:
		key := holdKey(msg.proxyID, msg.fp)
		if existing, ok := entries[key]; ok {
			existing.count++
			existing.entry = msg.entry
			// Cascade flag stays sticky once true (any cascade match in
			// the burst classifies the merged event as cascade).
			if msg.cascade {
				existing.cascade = true
			}
			return
		}
		emitAt := msg.now.Add(b.cfg.GetWindow())
		e := &holdEntry{
			proxyID:     msg.proxyID,
			fingerprint: msg.fp,
			entry:       msg.entry,
			enqueuedAt:  msg.now,
			emitAt:      emitAt,
			count:       1,
			cascade:     msg.cascade,
			heapIndex:   -1,
		}
		entries[key] = e
		heap.Push(hh, e)

	case holdMsgRecover:
		// Walk all entries for this proxy; emit non-cascade, drop cascade.
		for key, e := range entries {
			if e.proxyID != msg.proxyID {
				continue
			}
			delete(entries, key)
			if e.heapIndex >= 0 && e.heapIndex < hh.Len() {
				heap.Remove(hh, e.heapIndex)
			}
			if !e.cascade {
				emit(e)
			}
		}

	case holdMsgForget:
		for key, e := range entries {
			if e.proxyID != msg.proxyID {
				continue
			}
			delete(entries, key)
			if e.heapIndex >= 0 && e.heapIndex < hh.Len() {
				heap.Remove(hh, e.heapIndex)
			}
		}

	case holdMsgSize:
		if msg.resultCh != nil {
			msg.resultCh <- len(entries)
		}
	}
}

func holdKey(proxyID, fingerprint string) string {
	return proxyID + "|" + fingerprint
}

// FingerprintForEntry computes a stable fingerprint for a proxy log
// entry within the hold-buffer scope. Same shape entries collapse onto
// one held record.
func FingerprintForEntry(entry proxy.LogEntry) string {
	switch entry.Type {
	case proxy.LogTypeDiagnostic:
		if entry.Diagnostic == nil {
			return "diag:"
		}
		return fmt.Sprintf("diag:%s:%s", entry.Diagnostic.Category, entry.Diagnostic.Event)
	case proxy.LogTypeError:
		if entry.Error == nil {
			return "err:"
		}
		return "err:" + canonicalize(entry.Error.Message)
	case proxy.LogTypeHTTP:
		if entry.HTTP == nil {
			return "http:"
		}
		return fmt.Sprintf("http:%d:%s", entry.HTTP.StatusCode, entry.HTTP.URL)
	default:
		return "other:" + string(entry.Type)
	}
}

// canonicalize collapses whitespace and trims a message for fingerprint
// stability.
func canonicalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) > 256 {
		s = s[:256]
	}
	return s
}

// guard against unused-import warnings when fields are added later.
var _ sync.Mutex
