package daemon

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAutostartManager_GetOrCreate_ExactlyOnce verifies that concurrent calls
// to GetOrCreate for the same project path result in startFn being called
// exactly once and all callers receiving the same handle.
func TestAutostartManager_GetOrCreate_ExactlyOnce(t *testing.T) {
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

// TestAutostartManager_ProgressQuery verifies that AutostartManager.Progress
// returns the history for a known path and nil for unknown paths.
func TestAutostartManager_ProgressQuery(t *testing.T) {
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
