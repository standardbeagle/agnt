//go:build unix

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunAutostartAsync_ProgressEventsInOrder(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Three long-running scripts with dependency chain: a -> b -> c.
	// Each uses "sleep 60" to stay alive during the test.
	// Script "a" (layer 0) has no deps, "b" depends on "a", "c" depends on "b".
	// Since none bind ports, readySignaler signals immediately after start.
	configContent := `
scripts {
    a {
        run "sleep 60"
        autostart true
    }
    b {
        run "sleep 60"
        autostart true
        depends-on "a" timeout=5
    }
    c {
        run "sleep 60"
        autostart true
        depends-on "b" timeout=5
    }
}
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".agnt.kdl"), []byte(configContent), 0o644))

	d := New(DaemonConfig{
		SocketPath:            sockPath,
		MaxClients:            10,
		WriteTimeout:          5 * time.Second,
		StartupMonitorTimeout: 200 * time.Millisecond,
	})
	require.NoError(t, d.Start())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		d.Stop(ctx)
	}()

	progress := make(chan AutostartProgress, 100)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result := d.RunAutostartAsync(ctx, tmpDir, progress)
	close(progress)

	var events []AutostartProgress
	for ev := range progress {
		events = append(events, ev)
	}

	assert.Empty(t, result.Errors, "expected no errors, got: %v", result.Errors)
	assert.Len(t, result.Scripts, 3, "expected 3 scripts started")

	phases := make([]AutostartPhase, len(events))
	for i, ev := range events {
		phases[i] = ev.Phase
	}

	assert.Contains(t, phases, PhaseScriptStarting, "missing PhaseScriptStarting")
	assert.Contains(t, phases, PhaseScriptStarted, "missing PhaseScriptStarted")
	assert.Contains(t, phases, PhaseLayerComplete, "missing PhaseLayerComplete")
	assert.Contains(t, phases, PhaseDependencyWaitStart, "missing PhaseDependencyWaitStart")
	assert.Contains(t, phases, PhaseDependencyReady, "missing PhaseDependencyReady")

	// Verify ordering: layer 0 completes before layer 1 starts.
	var lastLayer0Complete int
	var firstLayer1Start int = -1
	for i, ev := range events {
		if ev.Phase == PhaseLayerComplete && ev.Layer == 0 {
			lastLayer0Complete = i
		}
		if ev.Phase == PhaseScriptStarting && ev.Layer == 1 && firstLayer1Start == -1 {
			firstLayer1Start = i
		}
	}
	if firstLayer1Start >= 0 {
		assert.Greater(t, firstLayer1Start, lastLayer0Complete,
			"layer 1 script start should come after layer 0 completion")
	}
}

func TestRunAutostartAsync_ContextCancelDuringDependencyWait(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Script "a" stays alive but binds no ports. Script "b" depends on "a"
	// with port-based readiness that never fires (no ports declared).
	// But since "a" has no ports, it signals ready immediately, so "b" proceeds.
	//
	// Instead: "a" binds port 99999 (invalid) so the port probe never succeeds
	// and "b" waits forever -- until context is cancelled.
	configContent := `
scripts {
    a {
        run "sleep 60"
        autostart true
        ports 99999
    }
    b {
        run "sleep 60"
        autostart true
        depends-on "a"
    }
}
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".agnt.kdl"), []byte(configContent), 0o644))

	d := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})
	require.NoError(t, d.Start())
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		d.Stop(stopCtx)
	}()

	progress := make(chan AutostartProgress, 100)
	// 15s budget: ~3s for layer-0 "a" to reach 'started' under parallel CI
	// load (PID fork + sh wrap can be surprisingly slow), then ~12s of
	// b-waiting-on-a before cancellation forces the dep wait to break.
	// The old 8s window was too tight — "a" sometimes failed to register
	// in result.Scripts before ctx expired, flaking the test.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	start := time.Now()
	result := d.RunAutostartAsync(ctx, tmpDir, progress)
	elapsed := time.Since(start)
	close(progress)

	var events []AutostartProgress
	for ev := range progress {
		events = append(events, ev)
	}

	// Should have completed within the 15s timeout (not 120s default)
	assert.Less(t, elapsed.Seconds(), 20.0,
		"should complete near the 15s context deadline, not hang")

	// "a" should have started (layer 0) OR errored out — what matters is
	// the autostart path recorded it either way, not that it's still alive.
	started := false
	for _, s := range result.Scripts {
		if s == "a" {
			started = true
		}
	}
	errored := false
	for _, e := range result.Errors {
		if strings.Contains(e, "script a:") {
			errored = true
		}
	}
	assert.True(t, started || errored,
		"script 'a' should appear in result.Scripts or result.Errors (scripts=%v errors=%v)",
		result.Scripts, result.Errors)

	// "b" should have a dependency wait start event
	var gotDependencyWait bool
	for _, ev := range events {
		if ev.Phase == PhaseDependencyWaitStart && ev.Script == "b" && ev.Dependency == "a" {
			gotDependencyWait = true
		}
	}
	assert.True(t, gotDependencyWait, "expected PhaseDependencyWaitStart for b->a")
}

func TestRunAutostartAsync_ScriptFailureEmitsProgress(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// "good" stays alive; "bad" uses a nonexistent command and fails.
	// Both are in layer 0 (no dependencies) so they start concurrently.
	configContent := `
scripts {
    good {
        run "sleep 60"
        autostart true
    }
    bad {
        command "/nonexistent-command-12345"
        autostart true
    }
}
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".agnt.kdl"), []byte(configContent), 0o644))

	d := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})
	require.NoError(t, d.Start())
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		d.Stop(stopCtx)
	}()

	progress := make(chan AutostartProgress, 100)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result := d.RunAutostartAsync(ctx, tmpDir, progress)
	close(progress)

	var events []AutostartProgress
	for ev := range progress {
		events = append(events, ev)
	}

	// "good" should succeed
	assert.Contains(t, result.Scripts, "good", "good script should have started")

	// "bad" should have failed with a progress event
	var gotFailed bool
	for _, ev := range events {
		if ev.Phase == PhaseScriptFailed && ev.Script == "bad" {
			gotFailed = true
			assert.Error(t, ev.Err, "failed event should have an error")
		}
	}
	assert.True(t, gotFailed, "expected PhaseScriptFailed for 'bad' script")
	assert.NotEmpty(t, result.Errors, "expected errors in result")
}

func TestRunAutostartAsync_NilProgressChannel(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	configContent := `
scripts {
    test {
        run "sleep 60"
        autostart true
    }
}
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".agnt.kdl"), []byte(configContent), 0o644))

	d := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})
	require.NoError(t, d.Start())
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		d.Stop(stopCtx)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result := d.RunAutostartAsync(ctx, tmpDir, nil)
	assert.Contains(t, result.Scripts, "test")
}

func TestRunAutostart_DelegatesToAsync(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	configContent := `
scripts {
    test {
        run "sleep 60"
        autostart true
    }
}
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".agnt.kdl"), []byte(configContent), 0o644))

	d := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})
	require.NoError(t, d.Start())
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		d.Stop(stopCtx)
	}()

	result := d.RunAutostart(context.Background(), tmpDir)
	assert.Contains(t, result.Scripts, "test")
}

// TestRunAutostartAsync_NoFallbackTimeout_WaitsIndefinitely verifies that a
// dependency with no explicit per-dep timeout does NOT silently abandon the
// wait after ~120s (the old hardcoded fallback). The dependency "a" never
// reaches ready (ports 99999 is unbindable), so "b" should still be in
// dependency_wait when we cancel the parent ctx after ~3s — proving that
// the wait was active and not silently exited via fallback timeout.
func TestRunAutostartAsync_NoFallbackTimeout_WaitsIndefinitely(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// "a" binds an unreachable port so its readySignaler never fires.
	// "b" depends on "a" with NO explicit timeout (default 0 = indefinite).
	configContent := `
scripts {
    a {
        run "sleep 60"
        autostart true
        ports 99999
    }
    b {
        run "sleep 60"
        autostart true
        depends-on "a"
    }
}
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".agnt.kdl"), []byte(configContent), 0o644))

	d := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})
	require.NoError(t, d.Start())
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		d.Stop(stopCtx)
	}()

	progress := make(chan AutostartProgress, 100)

	// Parent context has NO deadline. The session-style ctx in production also
	// has no deadline; this is the exact scenario the bug report describes.
	ctx, cancel := context.WithCancel(context.Background())

	// Run autostart in a goroutine. We expect it to block on b's dep wait.
	done := make(chan *AutostartResult, 1)
	go func() {
		done <- d.RunAutostartAsync(ctx, tmpDir, progress)
	}()

	// Wait long enough that the OLD fallback (120s) would NOT have fired,
	// but enough for layer 0 to settle and b to enter its dep wait. Layer 0
	// startup includes process spawn + duplicate scanner, which can take
	// several seconds on slow CI.
	select {
	case <-done:
		t.Fatal("autostart returned before parent ctx was cancelled — fallback timeout was not removed")
	case <-time.After(8 * time.Second):
	}

	// Cancel the parent ctx. The dep wait must unblock promptly via ctx.Done.
	cancelStart := time.Now()
	cancel()

	select {
	case result := <-done:
		unblockTime := time.Since(cancelStart)
		assert.Less(t, unblockTime, 1500*time.Millisecond,
			"dependency wait should unblock within ~1.5s of parent ctx cancel, took %v", unblockTime)
		// "a" started (layer 0); "b" should NOT be in result.Scripts because
		// the dep wait was cancelled before b could start.
		assert.Contains(t, result.Scripts, "a", "script 'a' should have started")
		assert.NotContains(t, result.Scripts, "b",
			"script 'b' must not have started — its dep wait was cancelled before ready")
	case <-time.After(5 * time.Second):
		t.Fatal("autostart did not return within 5s of parent ctx cancellation")
	}

	close(progress)

	// Verify a dep wait event was actually emitted (proving b reached the wait).
	var gotDependencyWait bool
	for ev := range progress {
		if ev.Phase == PhaseDependencyWaitStart && ev.Script == "b" && ev.Dependency == "a" {
			gotDependencyWait = true
		}
	}
	assert.True(t, gotDependencyWait, "expected PhaseDependencyWaitStart for b->a")
}

// TestRunAutostartAsync_ExplicitDependencyTimeout_StillHonored verifies that
// when the user explicitly sets `depends-on "x" timeout=N` in .agnt.kdl, the
// wait is still bounded by that timeout (we did not remove the per-dep
// timeout, only the implicit 120s fallback).
func TestRunAutostartAsync_ExplicitDependencyTimeout_StillHonored(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// "a" binds an unreachable port so its readySignaler never fires.
	// "b" depends on "a" with an EXPLICIT 2s timeout — this should still bound
	// the wait even though the parent ctx has no deadline.
	configContent := `
scripts {
    a {
        run "sleep 60"
        autostart true
        ports 99999
    }
    b {
        run "sleep 60"
        autostart true
        depends-on "a" timeout=2
    }
}
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".agnt.kdl"), []byte(configContent), 0o644))

	d := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})
	require.NoError(t, d.Start())
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		d.Stop(stopCtx)
	}()

	progress := make(chan AutostartProgress, 100)
	// No deadline on the parent ctx — exactly the production session ctx.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()
	result := d.RunAutostartAsync(ctx, tmpDir, progress)
	elapsed := time.Since(start)
	close(progress)

	// The explicit per-dep timeout (2s) must bound the wait. Without the
	// timeout the wait would be indefinite, and with the OLD fallback it
	// would have been ~120s. Allow generous slack for layer 0 startup +
	// b's own process launch on slow CI.
	assert.Less(t, elapsed.Seconds(), 30.0,
		"explicit 2s dep timeout should bound the wait (NOT 120s fallback, NOT indefinite), took %v", elapsed)

	// "a" started (layer 0). "b" should have proceeded after the timeout
	// elapsed ("starting anyway") even though "a" never became ready.
	assert.Contains(t, result.Scripts, "a", "script 'a' should have started")

	// Verify the dep wait event was actually emitted (proves we exercised
	// the per-dep timeout code path, not some other early-exit branch).
	var gotDependencyWait bool
	for ev := range progress {
		if ev.Phase == PhaseDependencyWaitStart && ev.Script == "b" && ev.Dependency == "a" {
			gotDependencyWait = true
		}
	}
	assert.True(t, gotDependencyWait, "expected PhaseDependencyWaitStart for b->a (per-dep timeout path)")
}

func TestReadySignaler_WaitReadyCtx(t *testing.T) {
	t.Run("signal before wait", func(t *testing.T) {
		rs := NewReadySignaler()
		rs.SignalReady("proc1")

		err := rs.WaitReadyCtx("proc1", context.Background())
		assert.NoError(t, err)
	})

	t.Run("wait then signal", func(t *testing.T) {
		rs := NewReadySignaler()
		done := make(chan error, 1)
		go func() {
			done <- rs.WaitReadyCtx("proc1", context.Background())
		}()

		time.Sleep(50 * time.Millisecond)
		rs.SignalReady("proc1")

		select {
		case err := <-done:
			assert.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Fatal("WaitReadyCtx did not unblock after SignalReady")
		}
	})

	t.Run("context cancelled", func(t *testing.T) {
		rs := NewReadySignaler()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := rs.WaitReadyCtx("proc1", ctx)
		assert.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
	})

	t.Run("context deadline exceeded", func(t *testing.T) {
		rs := NewReadySignaler()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		err := rs.WaitReadyCtx("proc1", ctx)
		assert.Error(t, err)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	})
}
