package daemon

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/protocol"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

// hookRingCapacity is the fixed ring buffer size for Claude Code hook events.
// 1024 is deliberately generous: a busy coding session might fire a couple of
// hooks per second, so the buffer only fills if the drain goroutine is
// wedged. On overflow we drop the oldest event and bump an atomic counter —
// hooks are fire-and-forget, and dropping is strictly better than applying
// backpressure on the RPC hot path and stretching Claude's tool call latency.
const hookRingCapacity = 1024

// HookEvent is the in-memory representation of a single Claude Code hook
// invocation after it has been pushed into the daemon ring buffer. The raw
// wire form is protocol.HookPayload; this struct wraps it with daemon-side
// provenance fields so downstream consumers (phase 3 fan-out to the overlay
// panel, StreamSink, etc) can filter and attribute events.
//
// Payload stays as json.RawMessage on purpose: the daemon has no schema for
// the opaque Claude Code hook payload and re-marshalling would just burn
// CPU on the drain path. Consumers that care about specific fields unmarshal
// into their own local struct.
type HookEvent struct {
	Event       string            `json:"event"`
	Payload     json.RawMessage   `json:"payload,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
	ReceivedAt  time.Time         `json:"received_at"`
	SessionID   string            `json:"session_id,omitempty"`
	ProjectPath string            `json:"project_path,omitempty"`
	Agent       string            `json:"agent,omitempty"`
}

// hookRingBuffer is a bounded FIFO queue of HookEvent values with
// drop-oldest overflow semantics. The push path is the hot path; it takes a
// single short-duration mutex and does no allocation beyond the entry
// itself. The drain path pops one event at a time so that a slow consumer
// only blocks the drain goroutine, never a pusher.
//
// A mutex over a fixed-size slice is deliberately chosen over a lock-free
// SPSC ring here: the hot path is a single push per RPC (not a high-rate
// burst), and the mutex code is ~20 lines while an atomic ring is ~100
// and needs careful memory-ordering proof. Per the agnt project rules
// (CLAUDE.md: "prefer lock-free systems … but over-engineering is expressly
// forbidden"), the mutex is the right fit until we have evidence that push
// contention is the bottleneck.
type hookRingBuffer struct {
	mu       sync.Mutex
	entries  []HookEvent
	head     int // index of oldest entry
	size     int // number of valid entries
	capacity int
	overflow atomic.Int64

	// notify is signaled (non-blocking) after each push so the drain
	// goroutine can wake without polling. The drain path uses a
	// buffered-length-1 channel so two rapid pushes still leave exactly
	// one pending wakeup.
	notify chan struct{}
}

// newHookRingBuffer constructs a ring buffer with the given capacity.
// Capacity must be >0; zero or negative values panic because there is no
// sane default for a "zero capacity ring buffer" and silently substituting
// hookRingCapacity would hide programming errors.
func newHookRingBuffer(capacity int) *hookRingBuffer {
	if capacity <= 0 {
		panic("hookRingBuffer: capacity must be >0")
	}
	return &hookRingBuffer{
		entries:  make([]HookEvent, capacity),
		capacity: capacity,
		notify:   make(chan struct{}, 1),
	}
}

// Push inserts an event at the tail. If the buffer is full the oldest entry
// is dropped (FIFO drop-oldest), the overflow counter is bumped, and a
// single debug log line is emitted per drop so we can correlate bursts with
// wedged drains without spamming the log file under sustained overflow.
func (r *hookRingBuffer) Push(ev HookEvent) {
	r.mu.Lock()
	tail := (r.head + r.size) % r.capacity
	if r.size == r.capacity {
		// Buffer full: overwrite the oldest slot (which lives at head),
		// advance head forward so the old entry is forgotten, and leave
		// size unchanged. The new event logically becomes the newest,
		// and the write target is the current head position — that
		// slot is about to stop being the head.
		r.entries[r.head] = ev
		r.head = (r.head + 1) % r.capacity
		r.overflow.Add(1)
		debug.Log("hook-hub", "ring buffer full, dropped oldest event (overflow=%d)", r.overflow.Load())
	} else {
		r.entries[tail] = ev
		r.size++
	}
	r.mu.Unlock()

	// Non-blocking wakeup. If a wake is already pending the drain
	// goroutine will see this push on its next pop, so we don't need a
	// second signal.
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

// Pop removes and returns the oldest event. ok is false if the buffer is
// empty. Pop is the only consumer-facing method; the drain goroutine calls
// it in a loop after each notify signal.
func (r *hookRingBuffer) Pop() (HookEvent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size == 0 {
		return HookEvent{}, false
	}
	ev := r.entries[r.head]
	// Zero the slot so any referenced payload bytes can be GC'd.
	r.entries[r.head] = HookEvent{}
	r.head = (r.head + 1) % r.capacity
	r.size--
	return ev, true
}

// Len returns the current number of buffered events. Primarily useful for
// tests and diagnostics; the drain goroutine uses Pop in a loop instead.
func (r *hookRingBuffer) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.size
}

// OverflowCount returns the total number of drop-oldest events since the
// ring buffer was created. Exposed so diagnostic code (doctor, get_errors)
// can surface buffer pressure to the AI agent.
func (r *hookRingBuffer) OverflowCount() int64 {
	return r.overflow.Load()
}

// enqueueHookResult is the decoupled return value of enqueueHookFromBytes.
// It contains either a success ack message or an error (code + human
// text) that the Connection-facing wrapper turns into a WriteOK /
// WriteErr. Splitting this out lets unit tests exercise every branch
// without constructing a real hubpkg.Connection.
type enqueueHookResult struct {
	ok      bool
	ackMsg  string
	errCode hubproto.ErrorCode
	errMsg  string
}

// enqueueHookFromBytes decodes a HookPayload wire blob, assembles a
// HookEvent, and pushes it into the ring buffer. It is the pure-Go
// business logic half of hubHandleHook with zero Connection awareness,
// so it can be unit-tested directly against a Daemon fixture that only
// carries a hookRing.
func (d *Daemon) enqueueHookFromBytes(data []byte) enqueueHookResult {
	if d.hookRing == nil {
		return enqueueHookResult{errCode: hubproto.ErrInternal, errMsg: "hook ring buffer not initialized"}
	}
	if len(data) == 0 {
		return enqueueHookResult{errCode: hubproto.ErrInvalidArgs, errMsg: "HOOK requires JSON payload"}
	}

	var payload protocol.HookPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return enqueueHookResult{errCode: hubproto.ErrInvalidArgs, errMsg: "invalid HOOK payload: " + err.Error()}
	}
	if payload.Event == "" {
		return enqueueHookResult{errCode: hubproto.ErrInvalidArgs, errMsg: "HOOK payload missing event"}
	}

	ev := HookEvent{
		Event:      payload.Event,
		Payload:    payload.Payload,
		Tags:       payload.Tags,
		ReceivedAt: time.Now(),
	}
	if payload.Tags != nil {
		// Copy well-known tags into typed fields for easy filtering.
		// Unknown tags stay on the map.
		ev.SessionID = payload.Tags["session_id"]
		ev.ProjectPath = payload.Tags["project_path"]
		ev.Agent = payload.Tags["agent"]
	}

	d.hookRing.Push(ev)
	return enqueueHookResult{ok: true, ackMsg: "hook enqueued"}
}

// hubHandleHook is the HOOK verb handler. It lives on the socket accept
// path and is bound by the hot-path latency budget described in the parent
// task: push to ring buffer and ack OK. All heavy work happens on the drain
// goroutine. This function is the thinnest possible Connection adapter
// around enqueueHookFromBytes.
func (d *Daemon) hubHandleHook(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	res := d.enqueueHookFromBytes(cmd.Data)
	if res.ok {
		return conn.WriteOK(res.ackMsg)
	}
	return conn.WriteErr(res.errCode, res.errMsg)
}

// drainHooks is the single drain goroutine launched from Daemon.Start. It
// pops events from the ring buffer and fans them out to the AlertHub. The
// loop exits on ctx cancel; any events remaining in the buffer at shutdown
// are discarded, which is safe because hook events are fire-and-forget.
func (d *Daemon) drainHooks(ctx context.Context) {
	if d.hookRing == nil || d.alertHub == nil {
		return
	}

	for {
		// Drain everything currently buffered before blocking on the
		// notify channel. This is important under burst load: N pushes
		// may produce only one notify signal, so after a single wake
		// we need to keep popping until empty.
		for {
			ev, ok := d.hookRing.Pop()
			if !ok {
				break
			}
			d.alertHub.BroadcastHookEvent(ev)
		}

		select {
		case <-ctx.Done():
			return
		case <-d.hookRing.notify:
			// wake and drain again
		}
	}
}
