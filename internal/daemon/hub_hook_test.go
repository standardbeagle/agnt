package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- hookRingBuffer unit tests -----------------------------------------------

// TestHookRingBuffer_OrderingAndOverflow pushes capacity+K events and asserts
// FIFO drop-oldest semantics: the buffer keeps the newest `capacity` events,
// in push order, and bumps OverflowCount by exactly K.
func TestHookRingBuffer_OrderingAndOverflow(t *testing.T) {
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
// operate: a hookRing and an alertHub. No socket, no Hub.
func drainHooksTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	return &Daemon{
		hookRing: newHookRingBuffer(hookRingCapacity),
		alertHub: NewAlertHub(),
	}
}

// TestDrainHooks_FansOutToAlertHub pushes N events through the ring
// buffer, starts the drain goroutine, and asserts the captureHookSink
// receives all N events in push order. Uses a channel (not sleep) for
// synchronization so the test is deterministic under -race.
func TestDrainHooks_FansOutToAlertHub(t *testing.T) {
	const n = 20
	d := drainHooksTestDaemon(t)

	sink := newCaptureHookSink(n)
	d.alertHub.AddHookSink(sink)

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
// ring buffer or alertHub is missing, without spinning or panicking.
// This covers the defensive nil checks at the top of the function.
func TestDrainHooks_NilGuard(t *testing.T) {
	// Missing alertHub
	d1 := &Daemon{hookRing: newHookRingBuffer(4)}
	done1 := make(chan struct{})
	go func() {
		d1.drainHooks(context.Background())
		close(done1)
	}()
	select {
	case <-done1:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("drainHooks with nil alertHub did not return")
	}

	// Missing hookRing
	d2 := &Daemon{alertHub: NewAlertHub()}
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

// TestAlertHub_BroadcastHookEvent_MultiSink asserts that
// BroadcastHookEvent fans out to every registered sink, and that
// RemoveHookSink stops further deliveries to a specific sink.
func TestAlertHub_BroadcastHookEvent_MultiSink(t *testing.T) {
	hub := NewAlertHub()

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

// TestAlertHub_AddHookSink_NilNoOp asserts passing a nil sink is a no-op
// rather than a crash or an accidentally-registered nil entry.
func TestAlertHub_AddHookSink_NilNoOp(t *testing.T) {
	hub := NewAlertHub()
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
