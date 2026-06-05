package daemon

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAutostartManager_GetOrCreate_ExactlyOnce verifies that concurrent calls
// to GetOrCreate for the same project path result in startFn being called
// exactly once and all callers receiving the same handle.
func TestAutostartManager_GetOrCreate_ExactlyOnce(t *testing.T) {
	t.Parallel()
	mgr := NewAutostartManager()
	tmpDir := t.TempDir()

	var startCount atomic.Int32
	startFn := func(ctx context.Context, progress chan<- AutostartProgress) *AutostartResult {
		startCount.Add(1)
		// Block briefly so concurrent callers can race.
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
		return &AutostartResult{Scripts: []string{"a"}}
	}

	const N = 20
	var wg sync.WaitGroup
	handles := make([]*AutostartHandle, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			handles[idx] = mgr.GetOrCreate(tmpDir, startFn)
		}(i)
	}
	wg.Wait()

	// Wait for completion of the single start.
	<-handles[0].Done()

	assert.Equal(t, int32(1), startCount.Load(), "startFn should be called exactly once")

	// All handles should point to the same underlying *AutostartHandle.
	for i := 1; i < N; i++ {
		assert.Same(t, handles[0], handles[i], "all callers should receive the same handle (idx=%d)", i)
	}

	// Result should be populated.
	require.NotNil(t, handles[0].Result())
	assert.Equal(t, []string{"a"}, handles[0].Result().Scripts)
}

// TestAutostartManager_Cancel_RunningHandle verifies that cancelling a running
// handle terminates the worker, closes the done channel, and the context
// passed to startFn is cancelled.
func TestAutostartManager_Cancel_RunningHandle(t *testing.T) {
	t.Parallel()
	mgr := NewAutostartManager()
	tmpDir := t.TempDir()

	started := make(chan struct{})
	var ctxErr atomic.Value
	startFn := func(ctx context.Context, progress chan<- AutostartProgress) *AutostartResult {
		close(started)
		<-ctx.Done()
		ctxErr.Store(ctx.Err())
		return &AutostartResult{}
	}

	handle := mgr.GetOrCreate(tmpDir, startFn)

	// Wait until startFn is actually running.
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("startFn never started")
	}

	mgr.Cancel(tmpDir)

	// Done should close shortly after.
	select {
	case <-handle.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("handle did not close after Cancel")
	}

	err, ok := ctxErr.Load().(error)
	require.True(t, ok, "expected ctx.Err() to be recorded")
	assert.ErrorIs(t, err, context.Canceled)
}

// TestAutostartManager_Cancel_CompletedHandle verifies that cancelling an
// already-completed handle is a safe no-op.
func TestAutostartManager_Cancel_CompletedHandle(t *testing.T) {
	t.Parallel()
	mgr := NewAutostartManager()
	tmpDir := t.TempDir()

	startFn := func(ctx context.Context, progress chan<- AutostartProgress) *AutostartResult {
		return &AutostartResult{}
	}

	handle := mgr.GetOrCreate(tmpDir, startFn)
	<-handle.Done()

	// Cancel after completion must not panic.
	assert.NotPanics(t, func() {
		mgr.Cancel(tmpDir)
	})

	// Done channel must remain closed (reading from it should not block).
	select {
	case <-handle.Done():
	default:
		t.Fatal("done channel should be closed")
	}
}

// TestAutostartManager_ProgressAccumulation verifies that progress events
// emitted through the channel are accumulated and retrievable from the handle.
func TestAutostartManager_ProgressAccumulation(t *testing.T) {
	t.Parallel()
	mgr := NewAutostartManager()
	tmpDir := t.TempDir()

	startFn := func(ctx context.Context, progress chan<- AutostartProgress) *AutostartResult {
		progress <- AutostartProgress{Phase: PhaseScriptStarting, Script: "a", Layer: 0}
		progress <- AutostartProgress{Phase: PhaseScriptStarted, Script: "a", Layer: 0}
		progress <- AutostartProgress{Phase: PhaseLayerComplete, Layer: 0}
		return &AutostartResult{Scripts: []string{"a"}}
	}

	handle := mgr.GetOrCreate(tmpDir, startFn)
	<-handle.Done()

	events := handle.Progress()
	// Three worker-emitted events plus the synthetic PhaseDone appended by
	// the manager when the worker returns.
	require.Len(t, events, 4)
	assert.Equal(t, PhaseScriptStarting, events[0].Phase)
	assert.Equal(t, "a", events[0].Script)
	assert.Equal(t, PhaseScriptStarted, events[1].Phase)
	assert.Equal(t, PhaseLayerComplete, events[2].Phase)
	assert.Equal(t, PhaseDone, events[3].Phase)

	// ProjectPath should be stamped on every event.
	expectedPath := normalizePath(tmpDir)
	for i, ev := range events {
		assert.Equal(t, expectedPath, ev.ProjectPath, "event %d missing ProjectPath", i)
	}
}

// TestAutostartManager_DifferentPaths verifies that different project paths
// get independent handles and startFn runs once per path.
func TestAutostartManager_DifferentPaths(t *testing.T) {
	t.Parallel()
	mgr := NewAutostartManager()
	path1 := t.TempDir()
	path2 := t.TempDir()

	var starts atomic.Int32
	startFn := func(ctx context.Context, progress chan<- AutostartProgress) *AutostartResult {
		starts.Add(1)
		return &AutostartResult{}
	}

	h1 := mgr.GetOrCreate(path1, startFn)
	h2 := mgr.GetOrCreate(path2, startFn)

	<-h1.Done()
	<-h2.Done()

	assert.NotSame(t, h1, h2, "different paths must get distinct handles")
	assert.Equal(t, int32(2), starts.Load(), "startFn should run once per path")
}

// TestAutostartManager_LateJoinerSnapshot verifies that calling Progress()
// after the handle is done returns the complete history.
func TestAutostartManager_LateJoinerSnapshot(t *testing.T) {
	t.Parallel()
	mgr := NewAutostartManager()
	tmpDir := t.TempDir()

	startFn := func(ctx context.Context, progress chan<- AutostartProgress) *AutostartResult {
		for i := 0; i < 5; i++ {
			progress <- AutostartProgress{
				Phase:  PhaseScriptStarting,
				Script: "s",
				Layer:  i,
			}
		}
		return &AutostartResult{}
	}

	handle := mgr.GetOrCreate(tmpDir, startFn)
	<-handle.Done()

	// Late joiner after completion sees full history: 5 worker events plus
	// the synthetic PhaseDone from the manager.
	events := handle.Progress()
	require.Len(t, events, 6)
	for i := 0; i < 5; i++ {
		assert.Equal(t, i, events[i].Layer)
		assert.Equal(t, PhaseScriptStarting, events[i].Phase)
	}
	assert.Equal(t, PhaseDone, events[5].Phase)

	// Multiple calls should return independent snapshots (no shared mutation).
	events2 := handle.Progress()
	assert.Equal(t, events, events2)
	events2[0].Layer = 999
	events3 := handle.Progress()
	assert.Equal(t, 0, events3[0].Layer, "Progress() must return a copy, not shared slice")
}

// TestAutostartManager_PathNormalization verifies that different path
// representations pointing to the same directory share a single handle.
func TestAutostartManager_PathNormalization(t *testing.T) {
	t.Parallel()
	mgr := NewAutostartManager()
	tmpDir := t.TempDir()

	var starts atomic.Int32
	started := make(chan struct{})
	startFn := func(ctx context.Context, progress chan<- AutostartProgress) *AutostartResult {
		if starts.Add(1) == 1 {
			close(started)
		}
		<-ctx.Done()
		return &AutostartResult{}
	}

	h1 := mgr.GetOrCreate(tmpDir, startFn)
	// Wait until the first worker has actually entered startFn so the
	// counter is observable before we make the second call.
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("startFn never entered")
	}

	// Same path with trailing separator and an extra component+..
	alt := filepath.Join(tmpDir, "sub", "..") + string(filepath.Separator)
	h2 := mgr.GetOrCreate(alt, startFn)

	assert.Same(t, h1, h2, "equivalent paths must resolve to the same handle")
	assert.Equal(t, int32(1), starts.Load())

	mgr.Cancel(tmpDir)
	<-h1.Done()
}

// TestAutostartManager_BroadcastReceivesAllPhases verifies that the
// broadcast callback fires for every worker-emitted event AND for the
// synthetic PhaseDone, in order, with stamped ProjectPath and non-zero
// Timestamp.
func TestAutostartManager_BroadcastReceivesAllPhases(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	expectedPath := normalizePath(tmpDir)

	var (
		mu        sync.Mutex
		broadcast []AutostartProgress
	)
	cb := func(projectPath string, ev AutostartProgress) {
		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, expectedPath, projectPath, "callback projectPath must be normalized")
		broadcast = append(broadcast, ev)
	}

	mgr := NewAutostartManagerWithBroadcast(cb)

	startFn := func(ctx context.Context, progress chan<- AutostartProgress) *AutostartResult {
		progress <- AutostartProgress{Phase: PhaseScriptStarting, Script: "a", Layer: 0}
		progress <- AutostartProgress{Phase: PhaseScriptStarted, Script: "a", Layer: 0}
		progress <- AutostartProgress{Phase: PhaseLayerComplete, Layer: 0}
		return &AutostartResult{Scripts: []string{"a"}}
	}

	handle := mgr.GetOrCreate(tmpDir, startFn)
	<-handle.Done()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, broadcast, 4, "broadcast should receive 3 worker events + PhaseDone")

	assert.Equal(t, PhaseScriptStarting, broadcast[0].Phase)
	assert.Equal(t, "a", broadcast[0].Script)
	assert.Equal(t, PhaseScriptStarted, broadcast[1].Phase)
	assert.Equal(t, PhaseLayerComplete, broadcast[2].Phase)
	assert.Equal(t, PhaseDone, broadcast[3].Phase)

	for i, ev := range broadcast {
		assert.Equal(t, expectedPath, ev.ProjectPath, "event %d ProjectPath must be stamped", i)
		assert.False(t, ev.Timestamp.IsZero(), "event %d Timestamp must be stamped", i)
	}
}

// TestAutostartManager_BroadcastFailureEvent verifies that PhaseScriptFailed
// progress events propagate through the broadcast callback with the original
// error attached, so subscribers can route them as diagnostic errors.
func TestAutostartManager_BroadcastFailureEvent(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	var (
		mu       sync.Mutex
		failures []AutostartProgress
	)
	cb := func(_ string, ev AutostartProgress) {
		if ev.Phase != PhaseScriptFailed {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		failures = append(failures, ev)
	}

	mgr := NewAutostartManagerWithBroadcast(cb)

	bootErr := errBoot
	startFn := func(ctx context.Context, progress chan<- AutostartProgress) *AutostartResult {
		progress <- AutostartProgress{Phase: PhaseScriptStarting, Script: "broken", Layer: 0}
		progress <- AutostartProgress{Phase: PhaseScriptFailed, Script: "broken", Layer: 0, Err: bootErr}
		return &AutostartResult{Errors: []string{bootErr.Error()}}
	}

	handle := mgr.GetOrCreate(tmpDir, startFn)
	<-handle.Done()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, failures, 1)
	assert.Equal(t, "broken", failures[0].Script)
	require.NotNil(t, failures[0].Err)
	assert.ErrorIs(t, failures[0].Err, bootErr)
}

// TestAutostartManager_BroadcastResultVisibleOnPhaseDone verifies the
// ordering contract: when a subscriber receives PhaseDone via broadcast,
// handle.Result() is already populated.
func TestAutostartManager_BroadcastResultVisibleOnPhaseDone(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	resultObserved := make(chan *AutostartResult, 1)
	var observedOnce sync.Once

	var mgrPtr atomic.Pointer[AutostartManager]
	cb := func(projectPath string, ev AutostartProgress) {
		if ev.Phase != PhaseDone {
			return
		}
		observedOnce.Do(func() {
			mgr := mgrPtr.Load()
			if mgr == nil {
				resultObserved <- nil
				return
			}
			h := mgr.Get(projectPath)
			if h == nil {
				resultObserved <- nil
				return
			}
			resultObserved <- h.Result()
		})
	}

	mgr := NewAutostartManagerWithBroadcast(cb)
	mgrPtr.Store(mgr)

	startFn := func(ctx context.Context, progress chan<- AutostartProgress) *AutostartResult {
		progress <- AutostartProgress{Phase: PhaseLayerComplete, Layer: 0}
		return &AutostartResult{Scripts: []string{"x", "y"}}
	}

	handle := mgr.GetOrCreate(tmpDir, startFn)
	<-handle.Done()

	select {
	case r := <-resultObserved:
		require.NotNil(t, r, "handle.Result() must be populated when PhaseDone fires")
		assert.Equal(t, []string{"x", "y"}, r.Scripts)
	case <-time.After(2 * time.Second):
		t.Fatal("PhaseDone broadcast never fired")
	}
}

// TestAutostartManager_NilBroadcastIsSafe verifies that the manager works
// without a broadcast callback (the legacy NewAutostartManager constructor).
func TestAutostartManager_NilBroadcastIsSafe(t *testing.T) {
	t.Parallel()
	mgr := NewAutostartManager()
	tmpDir := t.TempDir()

	startFn := func(ctx context.Context, progress chan<- AutostartProgress) *AutostartResult {
		progress <- AutostartProgress{Phase: PhaseScriptStarting, Script: "n"}
		return &AutostartResult{}
	}

	assert.NotPanics(t, func() {
		handle := mgr.GetOrCreate(tmpDir, startFn)
		<-handle.Done()
	})
}

// TestAutostartManager_BroadcastRecordingOrder verifies the
// record-then-broadcast ordering: at the moment broadcast fires for an
// event, that event is already visible in handle.Progress().
func TestAutostartManager_BroadcastRecordingOrder(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	var mgrPtr atomic.Pointer[AutostartManager]
	mismatch := make(chan string, 10)

	cb := func(projectPath string, ev AutostartProgress) {
		mgr := mgrPtr.Load()
		if mgr == nil {
			mismatch <- "manager not loaded"
			return
		}
		h := mgr.Get(projectPath)
		if h == nil {
			mismatch <- "handle not found"
			return
		}
		snap := h.Progress()
		// The broadcast event must already be the last entry recorded.
		if len(snap) == 0 {
			mismatch <- "snapshot empty when broadcast fired"
			return
		}
		last := snap[len(snap)-1]
		if last.Phase != ev.Phase || last.Script != ev.Script || last.Layer != ev.Layer {
			mismatch <- "snapshot last entry does not match broadcast event"
		}
	}

	mgr := NewAutostartManagerWithBroadcast(cb)
	mgrPtr.Store(mgr)

	startFn := func(ctx context.Context, progress chan<- AutostartProgress) *AutostartResult {
		for i := 0; i < 5; i++ {
			progress <- AutostartProgress{Phase: PhaseScriptStarted, Script: "s", Layer: i}
		}
		return &AutostartResult{}
	}

	handle := mgr.GetOrCreate(tmpDir, startFn)
	<-handle.Done()

	close(mismatch)
	for msg := range mismatch {
		t.Errorf("ordering violation: %s", msg)
	}
}

// errBoot is a sentinel error used by failure-broadcast tests so we can
// assert via errors.Is rather than string matching.
var errBoot = errBootSentinel("boot failure")

type errBootSentinel string

func (e errBootSentinel) Error() string { return string(e) }

// TestAutostartManager_BroadcastIntegratesWithEventHub verifies the full
// chain: AutostartManager broadcast → daemon broadcastAutostartProgress →
// EventHub.BroadcastLogEntry → StreamSink. This is the contract that
// `agnt monitor --types diagnostic` and the MCP `watch` tool depend on.
func TestAutostartManager_BroadcastIntegratesWithEventHub(t *testing.T) {
	t.Parallel()
	hub := NewEventHub()
	d := &Daemon{eventHub: hub}

	// Subscribe to diagnostic events only — this mirrors what
	// `agnt monitor --types diagnostic` registers.
	sink := hub.AddStreamSink(streamFilter{
		types: map[proxy.LogEntryType]bool{proxy.LogTypeDiagnostic: true},
	})
	defer hub.RemoveStreamSink(sink)

	mgr := NewAutostartManagerWithBroadcast(func(projectPath string, ev AutostartProgress) {
		d.broadcastAutostartProgress(projectPath, ev)
	})

	tmpDir := t.TempDir()
	startFn := func(ctx context.Context, progress chan<- AutostartProgress) *AutostartResult {
		progress <- AutostartProgress{Phase: PhaseScriptStarting, Script: "web", Layer: 0}
		progress <- AutostartProgress{Phase: PhaseScriptFailed, Script: "web", Layer: 0, Err: errBoot}
		return &AutostartResult{Errors: []string{errBoot.Error()}}
	}

	handle := mgr.GetOrCreate(tmpDir, startFn)
	<-handle.Done()

	// We expect three diagnostic events: starting (info), failed (error),
	// and the synthetic done (info). Drain with a timeout in case any are
	// dropped on a full channel — none should be since the buffer is 64.
	received := make([]proxy.LogEntry, 0, 3)
	deadline := time.After(2 * time.Second)
loop:
	for len(received) < 3 {
		select {
		case ev := <-sink.Ch:
			received = append(received, ev)
		case <-deadline:
			break loop
		}
	}

	require.Len(t, received, 3, "expected 3 diagnostic events through hub, got %d", len(received))

	for i, ev := range received {
		require.Equal(t, proxy.LogTypeDiagnostic, ev.Type, "event %d type", i)
		require.NotNil(t, ev.Diagnostic, "event %d diagnostic", i)
		assert.Equal(t, "autostart", ev.Diagnostic.Category, "event %d category", i)
		assert.False(t, ev.Diagnostic.Timestamp.IsZero(), "event %d timestamp", i)
	}

	// Phase ordering: starting → failed → done
	assert.Equal(t, "starting", received[0].Diagnostic.Event)
	assert.Equal(t, proxy.DiagnosticInfo, received[0].Diagnostic.Level)

	assert.Equal(t, "failed", received[1].Diagnostic.Event)
	assert.Equal(t, proxy.DiagnosticError, received[1].Diagnostic.Level)
	assert.Contains(t, received[1].Diagnostic.Message, errBoot.Error(),
		"failure event should include the underlying error message")

	assert.Equal(t, "done", received[2].Diagnostic.Event)
	assert.Equal(t, proxy.DiagnosticInfo, received[2].Diagnostic.Level)
}

// TestAutostartManager_ProgressQuery verifies that AutostartManager.Progress
// returns the history for a known path and nil for unknown paths.
func TestAutostartManager_ProgressQuery(t *testing.T) {
	t.Parallel()
	mgr := NewAutostartManager()
	tmpDir := t.TempDir()

	startFn := func(ctx context.Context, progress chan<- AutostartProgress) *AutostartResult {
		progress <- AutostartProgress{Phase: PhaseScriptStarted, Script: "a"}
		return &AutostartResult{}
	}

	handle := mgr.GetOrCreate(tmpDir, startFn)
	<-handle.Done()

	events := mgr.Progress(tmpDir)
	// 1 worker event + PhaseDone appended by the manager.
	require.Len(t, events, 2)
	assert.Equal(t, "a", events[0].Script)
	assert.Equal(t, PhaseDone, events[1].Phase)

	assert.Nil(t, mgr.Progress(t.TempDir()), "unknown path returns nil")
}
