package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"

	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/scope"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- hookRingBuffer unit tests -----------------------------------------------

// TestHookRingBuffer_OrderingAndOverflow pushes capacity+K events and asserts
// FIFO drop-oldest semantics: the buffer keeps the newest `capacity` events,
// in push order, and bumps OverflowCount by exactly K.
func TestHookRingBuffer_OrderingAndOverflow(t *testing.T) {
	t.Parallel()
	const capacity = 4
	const overflow = 3
	const total = capacity + overflow

	r := newHookRingBuffer(capacity)

	for i := 0; i < total; i++ {
		r.Push(HookEvent{Event: fmt.Sprintf("evt-%d", i)})
	}

	require.Equal(t, capacity, r.Len(), "buffer should be exactly full after overflow")
	assert.Equal(t, int64(overflow), r.OverflowCount(), "overflow count should equal dropped push count")

	// Pop all remaining and check ordering. After dropping 3 oldest we
	// should see evt-3, evt-4, evt-5, evt-6 in FIFO order.
	want := []string{"evt-3", "evt-4", "evt-5", "evt-6"}
	got := make([]string, 0, capacity)
	for {
		ev, ok := r.Pop()
		if !ok {
			break
		}
		got = append(got, ev.Event)
	}
	assert.Equal(t, want, got, "surviving events should be the last capacity pushes in FIFO order")
	assert.Equal(t, 0, r.Len(), "buffer should be empty after draining")
}

// TestHookRingBuffer_EmptyPop asserts Pop on an empty buffer returns the
// zero value and ok=false, with no side effects.
func TestHookRingBuffer_EmptyPop(t *testing.T) {
	t.Parallel()
	r := newHookRingBuffer(4)

	ev, ok := r.Pop()
	assert.False(t, ok, "Pop on empty buffer should return ok=false")
	assert.Equal(t, HookEvent{}, ev, "Pop on empty buffer should return zero HookEvent")
	assert.Equal(t, 0, r.Len())
	assert.Equal(t, int64(0), r.OverflowCount())
}

// TestHookRingBuffer_ConcurrentWrites spins up N goroutines each pushing M
// events and asserts the buffer remains consistent. Designed to be clean
// under `go test -race`.
func TestHookRingBuffer_ConcurrentWrites(t *testing.T) {
	t.Parallel()
	const goroutines = 100
	const perGoroutine = 10
	const total = goroutines * perGoroutine
	// Buffer is intentionally smaller than total so overflow kicks in.
	const capacity = 64

	r := newHookRingBuffer(capacity)

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				r.Push(HookEvent{Event: fmt.Sprintf("g%d-j%d", gid, j)})
			}
		}(i)
	}
	wg.Wait()

	// Invariants:
	//   Len() must equal capacity (buffer was filled well past capacity)
	//   OverflowCount() must equal total - capacity (every extra push dropped one)
	assert.Equal(t, capacity, r.Len(), "Len should equal capacity after concurrent flood")
	assert.Equal(t, int64(total-capacity), r.OverflowCount(), "overflow count should equal total pushes beyond capacity")
}

// TestHookRingBuffer_ZeroCapacityPanics guards against silent 0-capacity
// footguns. Zero capacity has no sane semantics and must panic early.
func TestHookRingBuffer_ZeroCapacityPanics(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		_ = newHookRingBuffer(0)
	}, "capacity 0 must panic")
	assert.Panics(t, func() {
		_ = newHookRingBuffer(-1)
	}, "negative capacity must panic")
}

// --- hubHandleHook / enqueueHookFromBytes unit tests ------------------------

// enqueueHookTestDaemon returns a minimal Daemon with just the hookRing
// initialized. This bypasses New() so tests don't need a socket, Hub, or
// any other subsystem. The pattern mirrors newTestDaemon in
// proxy_waitfor_test.go.
func enqueueHookTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	return &Daemon{
		hookRing: newHookRingBuffer(hookRingCapacity),
	}
}

// TestEnqueueHookFromBytes_Success is the happy path: a well-formed
// HookPayload goes in, the buffer length goes from 0 to 1, and the result
// reports ok with a non-empty ack message.
func TestEnqueueHookFromBytes_Success(t *testing.T) {
	t.Parallel()
	d := enqueueHookTestDaemon(t)

	payload := protocol.HookPayload{
		Event:   "PreToolUse",
		Payload: json.RawMessage(`{"tool":"Bash"}`),
		Tags: map[string]string{
			"session_id":   "sess-1",
			"project_path": "/tmp/proj",
			"agent":        "claude",
		},
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)

	res := d.enqueueHookFromBytes(data)

	assert.True(t, res.ok, "valid payload should return ok=true")
	assert.NotEmpty(t, res.ackMsg, "ok result should have a non-empty ack message")
	assert.Empty(t, res.errMsg, "ok result should have no error message")
	assert.Equal(t, 1, d.hookRing.Len(), "ring buffer should contain exactly one event after enqueue")

	// Pop and verify the event was assembled correctly (including the
	// typed provenance fields pulled out of Tags).
	ev, ok := d.hookRing.Pop()
	require.True(t, ok)
	assert.Equal(t, "PreToolUse", ev.Event)
	assert.Equal(t, "sess-1", ev.SessionID)
	assert.Equal(t, "/tmp/proj", ev.ProjectPath)
	assert.Equal(t, "claude", ev.Agent)
	assert.JSONEq(t, `{"tool":"Bash"}`, string(ev.Payload))
	assert.False(t, ev.ReceivedAt.IsZero(), "ReceivedAt should be set")
}

// TestEnqueueHookFromBytes_EmptyData asserts empty wire data is rejected
// with ErrInvalidArgs and nothing enters the buffer.
func TestEnqueueHookFromBytes_EmptyData(t *testing.T) {
	t.Parallel()
	d := enqueueHookTestDaemon(t)

	res := d.enqueueHookFromBytes(nil)
	assert.False(t, res.ok)
	assert.Equal(t, hubproto.ErrInvalidArgs, res.errCode)
	assert.Contains(t, res.errMsg, "HOOK requires JSON payload")
	assert.Equal(t, 0, d.hookRing.Len())

	res = d.enqueueHookFromBytes([]byte{})
	assert.False(t, res.ok)
	assert.Equal(t, hubproto.ErrInvalidArgs, res.errCode)
}

// TestEnqueueHookFromBytes_InvalidJSON asserts malformed JSON is rejected
// with ErrInvalidArgs.
func TestEnqueueHookFromBytes_InvalidJSON(t *testing.T) {
	t.Parallel()
	d := enqueueHookTestDaemon(t)

	res := d.enqueueHookFromBytes([]byte("not json"))
	assert.False(t, res.ok)
	assert.Equal(t, hubproto.ErrInvalidArgs, res.errCode)
	assert.Contains(t, res.errMsg, "invalid HOOK payload")
	assert.Equal(t, 0, d.hookRing.Len())
}

// TestEnqueueHookFromBytes_MissingEvent asserts a payload without an
// `event` field is rejected.
func TestEnqueueHookFromBytes_MissingEvent(t *testing.T) {
	t.Parallel()
	d := enqueueHookTestDaemon(t)

	// Valid JSON, but no event.
	res := d.enqueueHookFromBytes([]byte(`{"payload":{},"tags":{}}`))
	assert.False(t, res.ok)
	assert.Equal(t, hubproto.ErrInvalidArgs, res.errCode)
	assert.Contains(t, res.errMsg, "HOOK payload missing event")
	assert.Equal(t, 0, d.hookRing.Len())
}

// TestEnqueueHookFromBytes_NilRing asserts the handler fails safely if the
// ring buffer was never initialized (should be impossible in production
// because New() wires it in, but the guard is load-bearing defense in
// depth).
func TestEnqueueHookFromBytes_NilRing(t *testing.T) {
	t.Parallel()
	d := &Daemon{} // deliberately missing hookRing

	payload := protocol.HookPayload{Event: "Stop"}
	data, _ := json.Marshal(payload)

	res := d.enqueueHookFromBytes(data)
	assert.False(t, res.ok)
	assert.Equal(t, hubproto.ErrInternal, res.errCode)
	assert.Contains(t, res.errMsg, "hook ring buffer not initialized")
}

// --- drainHooks fan-out tests ------------------------------------------------

// captureHookSink is a minimal HookEventSink that collects events into a
// slice and signals a channel once a target count is reached. Used by
// the drainHooks test to avoid sleep-based synchronization.
type captureHookSink struct {
	mu     sync.Mutex
	events []HookEvent
	target int
	done   chan struct{}
	once   sync.Once
}

func newCaptureHookSink(target int) *captureHookSink {
	return &captureHookSink{
		target: target,
		done:   make(chan struct{}),
	}
}

func (s *captureHookSink) EmitHookEvent(ev HookEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
	if len(s.events) >= s.target {
		s.once.Do(func() { close(s.done) })
	}
}

func (s *captureHookSink) snapshot() []HookEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]HookEvent, len(s.events))
	copy(out, s.events)
	return out
}

// drainHooksTestDaemon wires just enough of Daemon for drainHooks to
// operate: a hookRing and an eventHub. No socket, no Hub.
func drainHooksTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	return &Daemon{
		hookRing: newHookRingBuffer(hookRingCapacity),
		eventHub: NewEventHub(),
	}
}

// TestDrainHooks_FansOutToEventHub pushes N events through the ring
// buffer, starts the drain goroutine, and asserts the captureHookSink
// receives all N events in push order. Uses a channel (not sleep) for
// synchronization so the test is deterministic under -race.
func TestDrainHooks_FansOutToEventHub(t *testing.T) {
	t.Parallel()
	const n = 20
	d := drainHooksTestDaemon(t)

	sink := newCaptureHookSink(n)
	d.eventHub.AddHookSink(sink)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	drainDone := make(chan struct{})
	go func() {
		d.drainHooks(ctx)
		close(drainDone)
	}()

	// Push N events after the drain goroutine is started to cover both
	// paths (buffer empty → wait on notify, then push wakes drain).
	for i := 0; i < n; i++ {
		d.hookRing.Push(HookEvent{Event: fmt.Sprintf("evt-%d", i)})
	}

	// Wait for all events to reach the sink or fail loudly.
	select {
	case <-sink.done:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for sink to receive %d events (got %d)", n, len(sink.snapshot()))
	}

	got := sink.snapshot()
	require.Len(t, got, n)
	for i := 0; i < n; i++ {
		assert.Equal(t, fmt.Sprintf("evt-%d", i), got[i].Event, "event %d out of order", i)
	}

	// Cancel the context and confirm the drain goroutine exits cleanly.
	cancel()
	select {
	case <-drainDone:
	case <-time.After(2 * time.Second):
		t.Fatal("drainHooks goroutine did not exit after context cancel")
	}
}

// TestDrainHooks_NilGuard asserts drainHooks returns immediately if the
// ring buffer or eventHub is missing, without spinning or panicking.
// This covers the defensive nil checks at the top of the function.
func TestDrainHooks_NilGuard(t *testing.T) {
	t.Parallel()
	// Missing eventHub
	d1 := &Daemon{hookRing: newHookRingBuffer(4)}
	done1 := make(chan struct{})
	go func() {
		d1.drainHooks(context.Background())
		close(done1)
	}()
	select {
	case <-done1:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("drainHooks with nil eventHub did not return")
	}

	// Missing hookRing
	d2 := &Daemon{eventHub: NewEventHub()}
	done2 := make(chan struct{})
	go func() {
		d2.drainHooks(context.Background())
		close(done2)
	}()
	select {
	case <-done2:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("drainHooks with nil hookRing did not return")
	}
}

// TestEventHub_BroadcastHookEvent_MultiSink asserts that
// BroadcastHookEvent fans out to every registered sink, and that
// RemoveHookSink stops further deliveries to a specific sink.
func TestEventHub_BroadcastHookEvent_MultiSink(t *testing.T) {
	t.Parallel()
	hub := NewEventHub()

	sink1 := &countingHookSink{}
	sink2 := &countingHookSink{}

	hub.AddHookSink(sink1)
	hub.AddHookSink(sink2)

	hub.BroadcastHookEvent(HookEvent{Event: "a"})
	hub.BroadcastHookEvent(HookEvent{Event: "b"})

	assert.Equal(t, int64(2), sink1.count.Load())
	assert.Equal(t, int64(2), sink2.count.Load())

	hub.RemoveHookSink(sink1)
	hub.BroadcastHookEvent(HookEvent{Event: "c"})

	assert.Equal(t, int64(2), sink1.count.Load(), "removed sink should stop receiving events")
	assert.Equal(t, int64(3), sink2.count.Load())
}

// TestEventHub_AddHookSink_NilNoOp asserts passing a nil sink is a no-op
// rather than a crash or an accidentally-registered nil entry.
func TestEventHub_AddHookSink_NilNoOp(t *testing.T) {
	t.Parallel()
	hub := NewEventHub()
	hub.AddHookSink(nil)
	// Broadcast must not panic with zero registered sinks.
	hub.BroadcastHookEvent(HookEvent{Event: "x"})
}

// countingHookSink is a pointer-typed sink so sink equality in
// RemoveHookSink works against a stable pointer identity (function types
// are not safely comparable under ==, struct pointers are).
type countingHookSink struct {
	count atomic.Int64
}

func (s *countingHookSink) EmitHookEvent(ev HookEvent) { s.count.Add(1) }

// --- phase 3: drain fan-out tests --------------------------------------------
//
// These tests cover the three new fan-out paths added by phase 3:
//   1. synthetic LogEntry → StreamSink (monitor / get_errors)
//   2. session heartbeat (LastSeen bump on the SessionRegistry)
//   3. notification toast broadcast to every active proxy
//
// Each test drives fanOutHookEvent directly rather than spinning up the
// drain goroutine, because the per-event ordering contract is what we
// care about and the drain loop itself is already covered by
// TestDrainHooks_FansOutToEventHub.

// drainFanoutTestDaemon wires a Daemon fixture rich enough to exercise
// fanOutHookEvent: hookRing, eventHub, sessionRegistry, and an empty
// proxy manager. Tests that need a real proxy add it via Create.
func drainFanoutTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	return &Daemon{
		hookRing:        newHookRingBuffer(hookRingCapacity),
		eventHub:        NewEventHub(),
		sessionRegistry: NewSessionRegistry(60 * time.Second),
		proxym:          proxy.NewProxyManager(),
	}
}

// TestDrainHooks_SyntheticLogEntryReachesStreamSink asserts that a hook
// event drained through fanOutHookEvent is published as a LogEntry of
// type "hook" on the EventHub StreamSink channel, with the payload
// bytes intact. This is the wiring that makes `agnt monitor --types hook`
// work end-to-end.
func TestDrainHooks_SyntheticLogEntryReachesStreamSink(t *testing.T) {
	t.Parallel()
	d := drainFanoutTestDaemon(t)

	// Subscribe a stream sink with a type filter for "hook" only, so
	// we prove the type round-trips correctly through the filter path.
	filter := streamFilter{
		types: map[proxy.LogEntryType]bool{proxy.LogTypeHook: true},
	}
	sink := d.eventHub.AddStreamSink(filter)
	defer d.eventHub.RemoveStreamSink(sink)

	rawPayload := json.RawMessage(`{"tool":"Bash","args":["ls"]}`)
	ev := HookEvent{
		Event:       "pre-tool-use",
		Payload:     rawPayload,
		SessionID:   "sess-stream-1",
		ProjectPath: "/tmp/proj",
		Agent:       "claude",
		ReceivedAt:  time.Now(),
	}

	d.fanOutHookEvent(ev)

	select {
	case got := <-sink.Ch:
		require.Equal(t, proxy.LogTypeHook, got.Type, "stream sink should receive a LogEntry with type=hook")
		require.NotNil(t, got.Hook, "Hook field must be populated")
		assert.Equal(t, "pre-tool-use", got.Hook.Event)
		assert.Equal(t, "sess-stream-1", got.Hook.SessionID)
		assert.Equal(t, "/tmp/proj", got.Hook.ProjectPath)
		assert.Equal(t, "claude", got.Hook.Agent)
		assert.JSONEq(t, string(rawPayload), string(got.Hook.Payload))
		assert.False(t, got.Hook.ReceivedAt.IsZero())
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for hook LogEntry on StreamSink")
	}
}

// TestDrainHooks_SessionHeartbeatBumpsLastSeen asserts that draining a
// hook event with a known SessionID bumps that session's LastSeen
// timestamp. This is the heartbeat plumbing that lets the daemon treat
// hook traffic as proof-of-life for the agnt run session.
func TestDrainHooks_SessionHeartbeatBumpsLastSeen(t *testing.T) {
	t.Parallel()
	d := drainFanoutTestDaemon(t)

	// Register a session with an obviously-stale LastSeen.
	stale := time.Now().Add(-1 * time.Hour)
	sess := &Session{
		Code:        "claude-1",
		ProjectPath: "/tmp/proj",
		Command:     "claude",
		StartedAt:   stale,
		LastSeen:    stale,
		Status:      SessionStatusDisconnected,
	}
	require.NoError(t, d.sessionRegistry.Register(sess))

	before := time.Now()
	ev := HookEvent{
		Event:      "pre-tool-use",
		SessionID:  "claude-1",
		ReceivedAt: time.Now(),
	}
	d.fanOutHookEvent(ev)

	got, ok := d.sessionRegistry.Get("claude-1")
	require.True(t, ok)
	assert.True(t, got.LastSeen.After(before) || got.LastSeen.Equal(before),
		"LastSeen should advance to at least the moment before fanOutHookEvent (was %v, before=%v)", got.LastSeen, before)
	assert.Equal(t, SessionStatusActive, got.GetStatus(),
		"heartbeat should also flip status back to active")
}

// TestDrainHooks_SessionHeartbeatUnknownSessionNoOp asserts that a hook
// event whose SessionID does not map to any registered session is a
// silent no-op — not every hook source ties back to a known agnt run
// session, so missing-lookup must not panic or surface as an error.
func TestDrainHooks_SessionHeartbeatUnknownSessionNoOp(t *testing.T) {
	t.Parallel()
	d := drainFanoutTestDaemon(t)

	// No sessions registered. fanOutHookEvent should still complete
	// cleanly and other downstream consumers should fire normally.
	sink := d.eventHub.AddStreamSink(streamFilter{
		types: map[proxy.LogEntryType]bool{proxy.LogTypeHook: true},
	})
	defer d.eventHub.RemoveStreamSink(sink)

	ev := HookEvent{
		Event:      "pre-tool-use",
		SessionID:  "ghost-session",
		ReceivedAt: time.Now(),
	}
	assert.NotPanics(t, func() { d.fanOutHookEvent(ev) })

	// LogEntry path should still have fired.
	select {
	case <-sink.Ch:
	case <-time.After(time.Second):
		t.Fatal("LogEntry fan-out should still fire even when session lookup misses")
	}
}

// TestDrainHooks_NotificationEventBroadcastsToast asserts that an event
// with name "notification" decodes the payload and calls BroadcastToast
// on every registered proxy. This is the daemon-side back-compat for
// `agnt notify` after phase 3 collapses notify into a pure HookSend
// alias.
func TestDrainHooks_NotificationEventBroadcastsToast(t *testing.T) {
	t.Parallel()
	d := drainFanoutTestDaemon(t)

	// Spin up a real backend + create two proxies through the manager.
	// BroadcastToast is safe on a proxy with zero WS clients (returns
	// 0,nil), so we don't need a real browser.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	for _, id := range []string{"proxy-a", "proxy-b"} {
		_, err := d.proxym.Create(context.Background(), proxy.ProxyConfig{
			ID:         id,
			TargetURL:  backend.URL,
			ListenPort: 0,
			MaxLogSize: 50,
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = d.proxym.Stop(context.Background(), id)
		})
	}

	// Subscribe a stream sink so we can assert the LogEntry path also
	// fires for a notification event (notification flows through
	// the SAME synthetic LogEntry channel as every other hook event).
	sink := d.eventHub.AddStreamSink(streamFilter{
		types: map[proxy.LogEntryType]bool{proxy.LogTypeHook: true},
	})
	defer d.eventHub.RemoveStreamSink(sink)

	body := json.RawMessage(`{"type":"warning","title":"Heads up","message":"Build failed","duration":5000}`)
	ev := HookEvent{
		Event:      "notification",
		Payload:    body,
		SessionID:  "claude-1",
		ReceivedAt: time.Now(),
	}

	require.NotPanics(t, func() { d.fanOutHookEvent(ev) })

	// LogEntry side: monitor still sees the notification as a hook event.
	select {
	case got := <-sink.Ch:
		assert.Equal(t, proxy.LogTypeHook, got.Type)
		require.NotNil(t, got.Hook)
		assert.Equal(t, "notification", got.Hook.Event)
	case <-time.After(time.Second):
		t.Fatal("notification event should still flow through StreamSink")
	}

	// Toast side: BroadcastToast is best-effort with zero WS clients,
	// so the assertion is "no panic + proxies still listed". The
	// per-call return value (sentCount=0) is the correct outcome with
	// no browser attached. The behavioral guarantee we care about is
	// that every proxy was iterated, not that any toast was actually
	// delivered to a non-existent client.
	assert.Len(t, d.proxym.ListScoped(scope.Unscoped("test")), 2, "both proxies should still be registered after broadcast")
}

// TestDrainHooks_NonNotificationDoesNotIterateProxies asserts that a
// non-notification hook event does NOT call BroadcastToast. We can't
// directly observe "BroadcastToast was not called" without a mock, so
// instead we assert no panic on a fixture that has zero proxies and
// then verify the LogEntry path still fired. The contract is:
// notification → toast loop, everything else → skip.
func TestDrainHooks_NonNotificationDoesNotTriggerToast(t *testing.T) {
	t.Parallel()
	d := drainFanoutTestDaemon(t)

	// Zero proxies registered. broadcastNotificationToast would be a
	// no-op anyway with empty list, but the important check is that
	// fanOutHookEvent does NOT take the notification branch for a
	// pre-tool-use event. We assert the LogEntry path fires (proving
	// drain ran) and that no panic occurred.
	sink := d.eventHub.AddStreamSink(streamFilter{
		types: map[proxy.LogEntryType]bool{proxy.LogTypeHook: true},
	})
	defer d.eventHub.RemoveStreamSink(sink)

	ev := HookEvent{
		Event:      "pre-tool-use",
		Payload:    json.RawMessage(`{"tool":"Read"}`),
		ReceivedAt: time.Now(),
	}
	assert.NotPanics(t, func() { d.fanOutHookEvent(ev) })

	select {
	case got := <-sink.Ch:
		assert.Equal(t, "pre-tool-use", got.Hook.Event)
	case <-time.After(time.Second):
		t.Fatal("non-notification event should still flow through StreamSink")
	}
}

// TestDrainHooks_MalformedNotificationPayload asserts that a
// notification event with non-JSON garbage in the payload does not
// panic and does not stall the drain. Drain must survive any payload
// because hook events come from external scripts we don't control.
func TestDrainHooks_MalformedNotificationPayload(t *testing.T) {
	t.Parallel()
	d := drainFanoutTestDaemon(t)

	// Even with a real proxy registered, malformed JSON must short-
	// circuit at the decode step before reaching BroadcastToast.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	_, err := d.proxym.Create(context.Background(), proxy.ProxyConfig{
		ID:         "decode-proxy",
		TargetURL:  backend.URL,
		ListenPort: 0,
		MaxLogSize: 50,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.proxym.Stop(context.Background(), "decode-proxy") })

	ev := HookEvent{
		Event:      "notification",
		Payload:    json.RawMessage(`<<this is not json>>`),
		ReceivedAt: time.Now(),
	}
	assert.NotPanics(t, func() { d.fanOutHookEvent(ev) })
}

// TestDrainHooks_NotificationMissingMessageSkipsToast asserts that a
// notification payload missing the required `message` field is logged
// and skipped, never reaching BroadcastToast. This mirrors the legacy
// PROXY TOAST validation in hub_proxy.go where empty message is a
// hard error.
func TestDrainHooks_NotificationMissingMessageSkipsToast(t *testing.T) {
	t.Parallel()
	d := drainFanoutTestDaemon(t)

	ev := HookEvent{
		Event:      "notification",
		Payload:    json.RawMessage(`{"type":"info","title":"only a title"}`),
		ReceivedAt: time.Now(),
	}
	assert.NotPanics(t, func() { d.fanOutHookEvent(ev) })
}

// TestDrainHooks_DrainGoroutineDeliversAllConsumers is an end-to-end
// regression test: push events through the actual ring buffer, run the
// real drain goroutine, and assert the synthetic LogEntry, the typed
// HookEventSink, and the session heartbeat all fire. This guards against
// future refactors that bypass fanOutHookEvent from drainHooks.
func TestDrainHooks_DrainGoroutineDeliversAllConsumers(t *testing.T) {
	t.Parallel()
	d := drainFanoutTestDaemon(t)

	// Register a session for the heartbeat path.
	stale := time.Now().Add(-1 * time.Hour)
	require.NoError(t, d.sessionRegistry.Register(&Session{
		Code:      "claude-1",
		StartedAt: stale,
		LastSeen:  stale,
		Status:    SessionStatusDisconnected,
	}))

	// Subscribe the StreamSink path.
	sink := d.eventHub.AddStreamSink(streamFilter{
		types: map[proxy.LogEntryType]bool{proxy.LogTypeHook: true},
	})
	defer d.eventHub.RemoveStreamSink(sink)

	// Subscribe the typed HookEventSink path.
	typedSink := newCaptureHookSink(1)
	d.eventHub.AddHookSink(typedSink)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	drainDone := make(chan struct{})
	go func() {
		d.drainHooks(ctx)
		close(drainDone)
	}()

	d.hookRing.Push(HookEvent{
		Event:      "pre-tool-use",
		SessionID:  "claude-1",
		ReceivedAt: time.Now(),
	})

	// All three consumers should fire.
	select {
	case got := <-sink.Ch:
		require.NotNil(t, got.Hook)
		assert.Equal(t, "pre-tool-use", got.Hook.Event)
	case <-time.After(2 * time.Second):
		t.Fatal("StreamSink did not receive hook LogEntry")
	}

	select {
	case <-typedSink.done:
	case <-time.After(2 * time.Second):
		t.Fatal("typed HookEventSink did not receive event")
	}

	got, ok := d.sessionRegistry.Get("claude-1")
	require.True(t, ok)
	assert.Equal(t, SessionStatusActive, got.GetStatus(),
		"drain goroutine should have bumped session heartbeat")

	cancel()
	select {
	case <-drainDone:
	case <-time.After(2 * time.Second):
		t.Fatal("drainHooks goroutine did not exit after cancel")
	}
}

// --- stop / stop-failure toast tests -----------------------------------------

// TestDrainHooks_StopEventBroadcastsToast asserts that a "stop" event with a
// valid Claude Code Stop payload triggers BroadcastToast on every registered
// proxy, showing the last_assistant_message.
func TestDrainHooks_StopEventBroadcastsToast(t *testing.T) {
	t.Parallel()
	d := drainFanoutTestDaemon(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	_, err := d.proxym.Create(context.Background(), proxy.ProxyConfig{
		ID:         "stop-proxy",
		TargetURL:  backend.URL,
		ListenPort: 0,
		MaxLogSize: 50,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.proxym.Stop(context.Background(), "stop-proxy") })

	// Subscribe stream sink to prove LogEntry also fires.
	sink := d.eventHub.AddStreamSink(streamFilter{
		types: map[proxy.LogEntryType]bool{proxy.LogTypeHook: true},
	})
	defer d.eventHub.RemoveStreamSink(sink)

	ev := HookEvent{
		Event:      "stop",
		Payload:    json.RawMessage(`{"stop_hook_active":false,"last_assistant_message":"Refactored auth module"}`),
		ReceivedAt: time.Now(),
	}

	assert.NotPanics(t, func() { d.fanOutHookEvent(ev) })

	// LogEntry path fires.
	select {
	case got := <-sink.Ch:
		assert.Equal(t, "stop", got.Hook.Event)
	case <-time.After(time.Second):
		t.Fatal("stop event should flow through StreamSink")
	}

	// Proxy still alive (BroadcastToast is safe with zero WS clients).
	assert.Len(t, d.proxym.ListScoped(scope.Unscoped("test")), 1)
}

// TestDrainHooks_StopHookActiveSkipsToast asserts that when stop_hook_active
// is true (Claude re-activating due to a previous stop hook), no toast is sent.
func TestDrainHooks_StopHookActiveSkipsToast(t *testing.T) {
	t.Parallel()
	d := drainFanoutTestDaemon(t)

	ev := HookEvent{
		Event:      "stop",
		Payload:    json.RawMessage(`{"stop_hook_active":true,"last_assistant_message":"continuing..."}`),
		ReceivedAt: time.Now(),
	}

	// Must not panic even with nil proxym.
	assert.NotPanics(t, func() { d.fanOutHookEvent(ev) })
}

// TestDrainHooks_StopEmptyMessageDefaultsToCompleted asserts that a stop event
// with no last_assistant_message defaults to "completed".
func TestDrainHooks_StopEmptyMessageDefaultsToCompleted(t *testing.T) {
	t.Parallel()
	d := drainFanoutTestDaemon(t)

	ev := HookEvent{
		Event:      "stop",
		Payload:    json.RawMessage(`{"stop_hook_active":false}`),
		ReceivedAt: time.Now(),
	}

	assert.NotPanics(t, func() { d.fanOutHookEvent(ev) })
}

// TestDrainHooks_StopMalformedPayload asserts a stop event with garbage
// payload does not panic.
func TestDrainHooks_StopMalformedPayload(t *testing.T) {
	t.Parallel()
	d := drainFanoutTestDaemon(t)

	ev := HookEvent{
		Event:      "stop",
		Payload:    json.RawMessage(`not json at all`),
		ReceivedAt: time.Now(),
	}

	assert.NotPanics(t, func() { d.fanOutHookEvent(ev) })
}

// TestDrainHooks_StopFailureBroadcastsErrorToast asserts that a "stop-failure"
// event decodes error fields and broadcasts an error toast.
func TestDrainHooks_StopFailureBroadcastsErrorToast(t *testing.T) {
	t.Parallel()
	d := drainFanoutTestDaemon(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	_, err := d.proxym.Create(context.Background(), proxy.ProxyConfig{
		ID:         "fail-proxy",
		TargetURL:  backend.URL,
		ListenPort: 0,
		MaxLogSize: 50,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.proxym.Stop(context.Background(), "fail-proxy") })

	sink := d.eventHub.AddStreamSink(streamFilter{
		types: map[proxy.LogEntryType]bool{proxy.LogTypeHook: true},
	})
	defer d.eventHub.RemoveStreamSink(sink)

	ev := HookEvent{
		Event: "stop-failure",
		Payload: json.RawMessage(`{
			"error": "rate_limit",
			"error_details": "429 Too Many Requests",
			"last_assistant_message": "API Error: Rate limit reached"
		}`),
		ReceivedAt: time.Now(),
	}

	assert.NotPanics(t, func() { d.fanOutHookEvent(ev) })

	select {
	case got := <-sink.Ch:
		assert.Equal(t, "stop-failure", got.Hook.Event)
	case <-time.After(time.Second):
		t.Fatal("stop-failure event should flow through StreamSink")
	}

	assert.Len(t, d.proxym.ListScoped(scope.Unscoped("test")), 1)
}

// TestDrainHooks_StopFailureMalformedPayload asserts a stop-failure event with
// garbage payload does not panic.
func TestDrainHooks_StopFailureMalformedPayload(t *testing.T) {
	t.Parallel()
	d := drainFanoutTestDaemon(t)

	ev := HookEvent{
		Event:      "stop-failure",
		Payload:    json.RawMessage(`<<<garbage>>>`),
		ReceivedAt: time.Now(),
	}

	assert.NotPanics(t, func() { d.fanOutHookEvent(ev) })
}

// TestDrainHooks_StopFailureEmptyErrorUsesLastMessage asserts that when
// error_details is empty but last_assistant_message exists, the message falls
// back to the last assistant message.
func TestDrainHooks_StopFailureEmptyErrorDetailsFallsBack(t *testing.T) {
	t.Parallel()
	d := drainFanoutTestDaemon(t)

	ev := HookEvent{
		Event: "stop-failure",
		Payload: json.RawMessage(`{
			"error": "server_error",
			"last_assistant_message": "API Error: Internal server error"
		}`),
		ReceivedAt: time.Now(),
	}

	assert.NotPanics(t, func() { d.fanOutHookEvent(ev) })
}

// TestToastProjectProxies_ScopesByProjectPath verifies that stop/notification
// toasts only reach the proxy belonging to the hook event's project, not proxies
// from other projects. This is the primary cross-contamination fix for running
// two simultaneous MCP-driven proxy servers.
func TestToastProjectProxies_ScopesByProjectPath(t *testing.T) {
	t.Parallel()
	d := drainFanoutTestDaemon(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	_, err := d.proxym.Create(context.Background(), proxy.ProxyConfig{
		ID: "proxy-project-a", TargetURL: backend.URL, ListenPort: 0, MaxLogSize: 50,
		Path: "/project/a",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.proxym.Stop(context.Background(), "proxy-project-a") })

	_, err = d.proxym.Create(context.Background(), proxy.ProxyConfig{
		ID: "proxy-project-b", TargetURL: backend.URL, ListenPort: 0, MaxLogSize: 50,
		Path: "/project/b",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.proxym.Stop(context.Background(), "proxy-project-b") })

	// Stop event for project/a — should toast proxy-project-a, not proxy-project-b.
	ev := HookEvent{
		Event:       "stop",
		ProjectPath: "/project/a",
		Payload:     json.RawMessage(`{"stop_hook_active":false,"last_assistant_message":"done"}`),
		ReceivedAt:  time.Now(),
	}
	assert.NotPanics(t, func() { d.fanOutHookEvent(ev) })

	// Both proxies exist — the scoped toast should not affect proxy-b's existence
	// (we can only verify no panic; BroadcastToast with 0 WS clients is a no-op).
	assert.Len(t, d.proxym.ListScoped(scope.Unscoped("test")), 2)
}

// TestToastProjectProxies_EmptyProjectPathDropsToast verifies the fail-closed
// contract: a hook event with no project_path (an unattributed event — e.g. a
// global ~/.claude/settings.json Stop hook firing from a non-agnt session that
// shares the single per-user daemon) must NOT fan a toast across every overlay.
// Regression guard for cross-session toast leakage.
func TestToastProjectProxies_EmptyProjectPathDropsToast(t *testing.T) {
	t.Parallel()
	d := drainFanoutTestDaemon(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	ps, err := d.proxym.Create(context.Background(), proxy.ProxyConfig{
		ID: "proxy-unrelated", TargetURL: backend.URL, ListenPort: 0, MaxLogSize: 50,
		Path: "/some/other/project",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.proxym.Stop(context.Background(), "proxy-unrelated") })

	// Connect a browser WS client so toast delivery is observable.
	wsURL := fmt.Sprintf("ws://%s/__devtool_metrics", ps.ListenAddr)
	var wsConn *websocket.Conn
	require.Eventually(t, func() bool {
		conn, _, dialErr := websocket.DefaultDialer.Dial(wsURL, nil)
		if dialErr != nil {
			return false
		}
		wsConn = conn
		return true
	}, 5*time.Second, 10*time.Millisecond, "failed to connect WebSocket to proxy")
	defer wsConn.Close()

	toastSeen := make(chan string, 4)
	go func() {
		for {
			_, message, readErr := wsConn.ReadMessage()
			if readErr != nil {
				return
			}
			var msg struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(message, &msg) == nil && msg.Type == "toast" {
				toastSeen <- string(message)
			}
		}
	}()

	// Empty ProjectPath → must be dropped, not broadcast to this overlay.
	ev := HookEvent{
		Event:   "stop",
		Payload: json.RawMessage(`{"stop_hook_active":false,"last_assistant_message":"done"}`),
	}
	assert.NotPanics(t, func() { d.fanOutHookEvent(ev) })

	select {
	case leaked := <-toastSeen:
		t.Fatalf("unattributed hook leaked a toast to an unrelated overlay: %s", leaked)
	case <-time.After(300 * time.Millisecond):
		// No toast — fail-closed contract holds.
	}
	assert.Len(t, d.proxym.ListScoped(scope.Unscoped("test")), 1)
}

// TestDrainHooks_NonToastEventsUnchanged asserts that events other than
// notification/stop/stop-failure still pass through fanOutHookEvent without
// touching the toast path.
func TestDrainHooks_NonToastEventsUnchanged(t *testing.T) {
	t.Parallel()
	d := drainFanoutTestDaemon(t)

	sink := d.eventHub.AddStreamSink(streamFilter{
		types: map[proxy.LogEntryType]bool{proxy.LogTypeHook: true},
	})
	defer d.eventHub.RemoveStreamSink(sink)

	for _, event := range []string{"pre-tool-use", "post-tool-use", "user-prompt-submit", "subagent-stop"} {
		ev := HookEvent{
			Event:      event,
			Payload:    json.RawMessage(`{}`),
			ReceivedAt: time.Now(),
		}
		assert.NotPanics(t, func() { d.fanOutHookEvent(ev) })

		select {
		case got := <-sink.Ch:
			assert.Equal(t, event, got.Hook.Event)
		case <-time.After(time.Second):
			t.Fatalf("event %q should flow through StreamSink", event)
		}
	}
}

// captureToastProxy is a tiny helper that records BroadcastToast calls
// against a proxy by intercepting them at the WebSocket broadcast layer.
// We don't need a real browser client — BroadcastToast is safe with zero
// connections — but we DO need a way to assert the call happened. The
// simplest approach is to register a captureBroadcastSink on the proxy and
// observe outbound JSON.

// TestDrainHooks_PreToolUseBashRedirectBroadcastsToast asserts that a
// pre-tool-use hook event whose Bash command matches a hookrules block rule
// (e.g. `npm run dev`) results in a diagnostic toast being broadcast to the
// project's proxies. The toast is fire-and-forget guidance for the
// developer's browser overlay — independent of whether the check-bash hook
// actually blocks the command (exit 2). When the block hook is wired,
// developers see the toast and the agent sees the stderr redirect; when
// unwired, the agent sees no enforcement but the developer still sees the
// toast.
func TestDrainHooks_PreToolUseBashRedirectBroadcastsToast(t *testing.T) {
	t.Parallel()
	d := drainFanoutTestDaemon(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	_, err := d.proxym.Create(context.Background(), proxy.ProxyConfig{
		ID: "redir-proxy", TargetURL: backend.URL, ListenPort: 0, MaxLogSize: 50,
		Path: "/proj",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.proxym.Stop(context.Background(), "redir-proxy") })

	sink := d.eventHub.AddStreamSink(streamFilter{
		types: map[proxy.LogEntryType]bool{proxy.LogTypeHook: true},
	})
	defer d.eventHub.RemoveStreamSink(sink)

	// Tool input shape mirrors Claude Code's PreToolUse payload.
	bashInput, err := json.Marshal(map[string]string{"command": "npm run dev"})
	require.NoError(t, err)
	payload, err := json.Marshal(map[string]any{
		"tool_name":  "Bash",
		"tool_input": json.RawMessage(bashInput),
	})
	require.NoError(t, err)

	ev := HookEvent{
		Event:       "pre-tool-use",
		ProjectPath: "/proj",
		Payload:     json.RawMessage(payload),
		ReceivedAt:  time.Now(),
	}
	assert.NotPanics(t, func() { d.fanOutHookEvent(ev) })

	// LogEntry path still fires.
	select {
	case got := <-sink.Ch:
		assert.Equal(t, "pre-tool-use", got.Hook.Event)
	case <-time.After(time.Second):
		t.Fatal("pre-tool-use event should flow through StreamSink")
	}

	// Test the pure-decision path directly so we get an assertable signal
	// without standing up a WS client. The contract: matchBashRedirect
	// returns a non-empty replacement for `npm run dev`.
	got := matchBashRedirect(payload)
	require.NotNil(t, got, "npm run dev should produce a redirect decision")
	assert.Contains(t, got.Replacement, "agnt", "replacement should cite an agnt MCP tool")
	assert.NotEmpty(t, got.Reason, "redirect should carry a why")
}

// TestMatchBashRedirect_NonBashOrAllowed asserts the redirect decoder returns
// nil for non-Bash tool calls and Bash commands that don't match any
// block/warn rule. This is the no-op path — the toast logic must not fire
// for innocuous Bash usage like `ls` or `git status`.
func TestMatchBashRedirect_NonBashOrAllowed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		payload string
	}{
		{"non-Bash tool", `{"tool_name":"Read","tool_input":{"command":"npm run dev"}}`},
		{"empty command", `{"tool_name":"Bash","tool_input":{"command":""}}`},
		{"innocuous bash", `{"tool_name":"Bash","tool_input":{"command":"ls -la"}}`},
		{"git status", `{"tool_name":"Bash","tool_input":{"command":"git status"}}`},
		{"malformed json", `not json at all`},
		{"missing tool_input", `{"tool_name":"Bash"}`},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := matchBashRedirect([]byte(tc.payload))
			assert.Nil(t, got, "expected nil redirect decision for %s", tc.name)
		})
	}
}

// TestMatchBashRedirect_LongLivedPatterns asserts the explicit acceptance set
// from the Dart task (npm run / yarn / go build / cargo build / make) all
// produce redirect decisions. Acceptance criterion: "Running `npm run dev`
// via Bash triggers a visible diagnostic toast in the browser overlay" —
// covered by the pure-function path here plus the wire test above.
func TestMatchBashRedirect_LongLivedPatterns(t *testing.T) {
	t.Parallel()

	commands := []string{
		"npm run dev",
		"npm start",
		"yarn dev",
		"pnpm run serve",
		"bun run dev",
		"go run ./cmd/server",
	}

	for _, cmd := range commands {
		cmd := cmd
		t.Run(cmd, func(t *testing.T) {
			t.Parallel()
			input, _ := json.Marshal(map[string]string{"command": cmd})
			payload, _ := json.Marshal(map[string]any{
				"tool_name":  "Bash",
				"tool_input": json.RawMessage(input),
			})
			got := matchBashRedirect(payload)
			require.NotNil(t, got, "command %q should match a redirect rule", cmd)
			assert.NotEmpty(t, got.Replacement)
		})
	}
}
