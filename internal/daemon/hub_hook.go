package daemon

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
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
// pops events from the ring buffer and fans them out to four consumers in
// strict cheapest-first order so a slow downstream cannot wedge the drain:
//
//  1. session heartbeat (in-memory atomic-ish bump on the SessionRegistry)
//  2. StreamSink fan-out via BroadcastLogEntry (channel send with default,
//     drops on backpressure — the same contract as proxy log fan-out)
//  3. notification toast fan-out (only when ev.Event == "notification";
//     calls into proxy WS broadcast which can be slower if a client is wedged)
//  4. typed HookEventSink fan-out via BroadcastHookEvent (existing path)
//
// The loop exits on ctx cancel; any events remaining in the buffer at
// shutdown are discarded, which is safe because hook events are
// fire-and-forget by contract (see hookRingCapacity comment).
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
			d.fanOutHookEvent(ev)
		}

		select {
		case <-ctx.Done():
			return
		case <-d.hookRing.notify:
			// wake and drain again
		}
	}
}

// fanOutHookEvent dispatches a single hook event to every downstream
// consumer. Split out from drainHooks so unit tests can drive the fan-out
// path directly without standing up a real drain goroutine, and so the
// per-event ordering contract (cheapest first) is documented in one place.
//
// Errors from individual sinks are intentionally swallowed to debug log:
// hook delivery is fire-and-forget, and one wedged consumer must not
// stop the next consumer from receiving the event.
func (d *Daemon) fanOutHookEvent(ev HookEvent) {
	// 1. Session heartbeat (cheap, in-memory). Best-effort: a hook event
	//    with no SessionID, or one whose session is not registered, is a
	//    silent no-op — not every hook ties back to an active agnt run
	//    session (e.g. the user might run `agnt hook ...` from a script
	//    outside any PTY wrapper).
	if ev.SessionID != "" && d.sessionRegistry != nil {
		if sess, ok := d.sessionRegistry.Get(ev.SessionID); ok {
			sess.UpdateLastSeen()
		}
	}

	// 2. StreamSink fan-out via the unified LogEntry channel. The empty
	//    proxy ID means "global event, not tied to any single proxy" —
	//    same convention BroadcastProcessOutput uses for process lines.
	//    BroadcastLogEntry does a channel-send-with-default per sink, so
	//    a wedged consumer cannot stall the drain goroutine.
	logEntry := proxy.LogEntry{
		Type: proxy.LogTypeHook,
		Hook: &proxy.HookLogEntry{
			Event:       ev.Event,
			Payload:     ev.Payload,
			SessionID:   ev.SessionID,
			ProjectPath: ev.ProjectPath,
			Agent:       ev.Agent,
			ReceivedAt:  ev.ReceivedAt,
		},
	}
	d.alertHub.BroadcastLogEntry(logEntry, "")

	// 3. Toast fan-out for notification, stop, and stop-failure events.
	//    - notification: user-facing notification from the agent (back-compat
	//      for `agnt notify` and the Notification hook)
	//    - stop: agent finished responding successfully
	//    - stop-failure: turn ended due to an API error (Claude Code's
	//      StopFailure event; name follows the lowercase-hyphenated
	//      convention shared with pre-tool-use, subagent-stop, etc.)
	//    All other events skip the toast path.
	switch ev.Event {
	case "notification":
		d.broadcastNotificationToast(ev)
	case "stop":
		d.broadcastStopToast(ev)
	case "stop-failure":
		d.broadcastStopFailureToast(ev)
	}

	// 4. Typed HookEventSink fan-out (existing phase 1 path, kept for
	//    tests and future typed subscribers like the overlay panel).
	d.alertHub.BroadcastHookEvent(ev)
}

// broadcastNotificationToast decodes a notification hook event payload
// and fans the resulting ToastConfig out to every active proxy as a
// browser toast. This is the daemon-side replacement for the per-proxy
// ProxyToast loop that `agnt notify` used to do client-side: phase 3
// moves the iteration here so any HookSend("notification", ...) call
// from anywhere — `agnt notify`, a Claude Code hook script, a future
// MCP tool — produces the same browser surface.
//
// Failures are swallowed at debug level by design:
//   - malformed JSON payload: drain must not stall on garbage input
//   - per-proxy BroadcastToast errors: one wedged WS consumer must not
//     skip toasts on the other proxies
func (d *Daemon) broadcastNotificationToast(ev HookEvent) {
	if d.proxym == nil || len(ev.Payload) == 0 {
		return
	}

	// Decode into a small local struct that mirrors the wire shape of
	// `sendNotifyHook` in cmd/agnt/notify.go. We do not use
	// protocol.ToastConfig directly because notify uses lowercase JSON
	// field names ({type,title,message}) without the toast_ prefix that
	// the legacy PROXY TOAST verb expects — keeping the decode struct
	// local makes the contract explicit.
	var notif struct {
		Type     string `json:"type"`
		Title    string `json:"title"`
		Message  string `json:"message"`
		Duration int    `json:"duration"`
	}
	if err := json.Unmarshal(ev.Payload, &notif); err != nil {
		debug.Log("hook-hub", "notification payload decode failed (event=%s): %v", ev.Event, err)
		return
	}
	if notif.Message == "" {
		debug.Log("hook-hub", "notification event missing message field, skipping toast")
		return
	}
	if notif.Type == "" {
		notif.Type = "info"
	}

	d.toastAllProxies(notif.Type, notif.Title, notif.Message, notif.Duration)
}

// broadcastStopToast decodes a Stop hook event and sends a success toast
// to every active proxy. Claude Code's Stop payload contains
// {stop_hook_active, last_assistant_message, ...}. We skip the toast when
// stop_hook_active is true (Claude is re-activating because a previous
// stop hook told it to continue — the user doesn't need a repeated toast).
func (d *Daemon) broadcastStopToast(ev HookEvent) {
	if d.proxym == nil || len(ev.Payload) == 0 {
		return
	}

	var payload struct {
		StopHookActive   bool   `json:"stop_hook_active"`
		LastAssistantMsg string `json:"last_assistant_message"`
	}
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		debug.Log("hook-hub", "stop payload decode failed: %v", err)
		return
	}

	if payload.StopHookActive {
		return
	}

	message := payload.LastAssistantMsg
	if len(message) > 200 {
		message = message[:197] + "..."
	}
	if message == "" {
		message = "completed"
	}

	// Toast type must be one of success/error/warning/info — other values
	// fall through to the info color in the frontend palette (see
	// internal/proxy/scripts/toast.js). "success" is the right semantics
	// for a successful Stop.
	d.toastAllProxies("success", "Claude Finished", message, 0)
}

// broadcastStopFailureToast decodes a StopFailure hook event and sends an
// error toast to every active proxy. Claude Code's StopFailure payload
// contains {error, error_details, last_assistant_message, ...}.
func (d *Daemon) broadcastStopFailureToast(ev HookEvent) {
	if d.proxym == nil || len(ev.Payload) == 0 {
		return
	}

	var payload struct {
		Error       string `json:"error"`
		ErrorDetail string `json:"error_details"`
		LastMsg     string `json:"last_assistant_message"`
	}
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		debug.Log("hook-hub", "stop-failure payload decode failed: %v", err)
		return
	}

	var message string
	if payload.ErrorDetail != "" {
		message = payload.Error + ": " + payload.ErrorDetail
	} else if payload.LastMsg != "" {
		message = payload.LastMsg
	} else {
		message = "API error: " + payload.Error
	}
	if len(message) > 300 {
		message = message[:297] + "..."
	}

	d.toastAllProxies("error", "Claude Error", message, 0)
}

// toastAllProxies sends a BroadcastToast to every active proxy. Per-proxy
// errors are swallowed at debug level so one wedged WS client does not
// block the others.
func (d *Daemon) toastAllProxies(toastType, title, message string, duration int) {
	proxies := d.proxym.List()
	if len(proxies) == 0 {
		return
	}
	for _, p := range proxies {
		if p == nil {
			continue
		}
		if _, err := p.BroadcastToast(toastType, title, message, duration); err != nil {
			debug.Log("hook-hub", "BroadcastToast failed for proxy %s: %v", p.ID, err)
		}
	}
}
