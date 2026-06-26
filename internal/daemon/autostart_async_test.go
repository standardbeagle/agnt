//go:build unix

package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clusterAConfig is one long-running script named "test"; shared by Cluster A subtests.
const clusterAConfig = `
scripts {
    test {
        run "sleep 60"
        autostart true
    }
}
`

// clusterBConfig builds the shared Cluster B config with a per-call ephemeral
// port for script "a". The port is intentionally one no process will bind to
// (the script just sleeps), so URL detection never fires and the dependency
// wait is driven by timeout/context cancellation paths.
func clusterBConfig(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf(`
scripts {
    a {
        run "sleep 60"
        autostart true
        ports %d
    }
    b {
        run "sleep 60"
        autostart true
        depends-on "a"
    }
}
`, ephemeralPort(t))
}

// newDaemon boots a daemon with a unique socket under dir and registers a
// t.Cleanup that stops it gracefully (2 s budget).
func newDaemon(t *testing.T, dir string) *Daemon {
	t.Helper()
	// Route through NewForTest, NOT New()+Start(): Start() runs the host-global
	// startup ops (cleanupOrphans, startupPortCleanup, startupOrphanPGIDScan,
	// restoreProxies) that walk /proc and issue kill(2) on scan-discovered PIDs.
	// Under parallel load those reap other daemons' `sleep 60` children, which
	// surfaced here as "process exited with code -1 during startup". This test
	// exercises autostart, not the startup ops, so it must skip them. NewForTest
	// registers its own t.Cleanup(Stop).
	return NewForTest(t, DaemonConfig{
		SocketPath:   filepath.Join(dir, "test.sock"),
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})
}

// writeConfig writes content to <dir>/.agnt.kdl.
func writeConfig(t *testing.T, dir, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".agnt.kdl"), []byte(content), 0o644))
}

// drainProgress drains a closed progress channel into a slice.
func drainProgress(ch chan AutostartProgress) []AutostartProgress {
	var out []AutostartProgress
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

// hasPhase returns true if any event in events has the given phase.
func hasPhase(events []AutostartProgress, phase AutostartPhase) bool {
	for _, ev := range events {
		if ev.Phase == phase {
			return true
		}
	}
	return false
}

// countPhase counts events with the given phase.
func countPhase(events []AutostartProgress, phase AutostartPhase) int {
	n := 0
	for _, ev := range events {
		if ev.Phase == phase {
			n++
		}
	}
	return n
}

// hasDependencyWaitEvent returns true if any event is a dep-wait for the given
// script+dependency pair.
func hasDependencyWaitEvent(events []AutostartProgress, script, dep string) bool {
	for _, ev := range events {
		if ev.Phase == PhaseDependencyWaitStart && ev.Script == script && ev.Dependency == dep {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Cluster A — "simple autostart works"
// One daemon boot, two subtests via t.Run.
// ---------------------------------------------------------------------------

func TestRunAutostartAsync_SimpleScripts(t *testing.T) {
	clusterDir := t.TempDir()
	d := newDaemon(t, clusterDir)

	t.Run("nil progress channel", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, clusterAConfig)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		result := d.RunAutostartAsync(ctx, dir, nil)

		require.NotNil(t, result, "result must not be nil")
		assert.Contains(t, result.Scripts, "test", "script 'test' should appear in result.Scripts")
		assert.Len(t, result.Scripts, 1, "exactly 1 script should start")
		assert.Empty(t, result.Errors, "no errors expected; got: %v", result.Errors)
	})

	t.Run("delegates to async (RunAutostart wraps)", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, clusterAConfig)

		result := d.RunAutostart(context.Background(), dir)

		require.NotNil(t, result, "result must not be nil")
		assert.Contains(t, result.Scripts, "test", "script 'test' should appear in result.Scripts")
		assert.Len(t, result.Scripts, 1, "exactly 1 script should start")
		assert.Empty(t, result.Errors, "RunAutostart should surface no errors for a healthy script")
	})

	t.Run("progress_events_in_order", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, `
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
`)
		progress := make(chan AutostartProgress, 200)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		result := d.RunAutostartAsync(ctx, dir, progress)
		close(progress)
		events := drainProgress(progress)

		require.NotNil(t, result, "result must not be nil")
		assert.Empty(t, result.Errors, "expected no errors, got: %v", result.Errors)
		assert.Len(t, result.Scripts, 3, "expected 3 scripts started")

		// All expected phase types must appear.
		assert.True(t, hasPhase(events, PhaseScriptStarting), "missing PhaseScriptStarting")
		assert.True(t, hasPhase(events, PhaseScriptStarted), "missing PhaseScriptStarted")
		assert.True(t, hasPhase(events, PhaseLayerComplete), "missing PhaseLayerComplete")
		assert.True(t, hasPhase(events, PhaseDependencyWaitStart), "missing PhaseDependencyWaitStart")
		assert.True(t, hasPhase(events, PhaseDependencyReady), "missing PhaseDependencyReady")

		// With a 3-script chain there are >=2 layer-complete events.
		assert.GreaterOrEqual(t, countPhase(events, PhaseLayerComplete), 2,
			"3-script chain should produce >=2 PhaseLayerComplete events")

		// Each of the 3 scripts must appear in PhaseScriptStarting.
		startingScripts := map[string]bool{}
		for _, ev := range events {
			if ev.Phase == PhaseScriptStarting {
				startingScripts[ev.Script] = true
			}
		}
		assert.True(t, startingScripts["a"], "script 'a' should have a PhaseScriptStarting event")
		assert.True(t, startingScripts["b"], "script 'b' should have a PhaseScriptStarting event")
		assert.True(t, startingScripts["c"], "script 'c' should have a PhaseScriptStarting event")

		// Ordering: layer 1 starts must follow layer 0 completion; layer 2 starts
		// must follow layer 1 completion.
		var lastLayer0Complete, lastLayer1Complete int
		var firstLayer1Start, firstLayer2Start = -1, -1
		for i, ev := range events {
			if ev.Phase == PhaseLayerComplete && ev.Layer == 0 {
				lastLayer0Complete = i
			}
			if ev.Phase == PhaseLayerComplete && ev.Layer == 1 {
				lastLayer1Complete = i
			}
			if ev.Phase == PhaseScriptStarting && ev.Layer == 1 && firstLayer1Start == -1 {
				firstLayer1Start = i
			}
			if ev.Phase == PhaseScriptStarting && ev.Layer == 2 && firstLayer2Start == -1 {
				firstLayer2Start = i
			}
		}
		if firstLayer1Start >= 0 {
			assert.Greater(t, firstLayer1Start, lastLayer0Complete,
				"layer 1 start must follow layer 0 completion")
		}
		if firstLayer2Start >= 0 {
			assert.Greater(t, firstLayer2Start, lastLayer1Complete,
				"layer 2 start must follow layer 1 completion")
		}
	})
}

// ---------------------------------------------------------------------------
// Standalone: failure emits progress
// ---------------------------------------------------------------------------

func TestRunAutostartAsync_ScriptFailureEmitsProgress(t *testing.T) {
	tmpDir := t.TempDir()

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
	writeConfig(t, tmpDir, configContent)

	d := newDaemon(t, tmpDir)

	progress := make(chan AutostartProgress, 200)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result := d.RunAutostartAsync(ctx, tmpDir, progress)
	close(progress)
	events := drainProgress(progress)

	require.NotNil(t, result, "result must not be nil")

	// "good" must succeed.
	assert.Contains(t, result.Scripts, "good", "good script should have started")
	assert.NotContains(t, result.Scripts, "bad", "bad script must not appear in result.Scripts")

	// result.Errors must be non-empty (bad failed).
	assert.NotEmpty(t, result.Errors, "result.Errors must be non-empty when a script fails")

	// Exactly one PhaseScriptFailed event for "bad", with a non-nil Err.
	failedCount := 0
	for _, ev := range events {
		if ev.Phase == PhaseScriptFailed && ev.Script == "bad" {
			failedCount++
			assert.Error(t, ev.Err, "PhaseScriptFailed event must carry a non-nil error")
		}
	}
	assert.Equal(t, 1, failedCount, "expected exactly 1 PhaseScriptFailed event for 'bad'")
}

// ---------------------------------------------------------------------------
// Cluster B — dependency-wait scenarios
// Three subtests on ONE daemon boot. Each uses its own t.TempDir so
// script namespaces are isolated (project paths differ).
// ---------------------------------------------------------------------------

func TestRunAutostartAsync_DependencyWait(t *testing.T) {
	clusterDir := t.TempDir()
	d := newDaemon(t, clusterDir)

	// contextCancelDuringWait: "b" waits on "a" (ephemeral unreachable port);
	// 15 s context deadline forces cancellation.
	t.Run("context cancel during dependency wait", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, clusterBConfig(t))

		progress := make(chan AutostartProgress, 200)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		start := time.Now()
		result := d.RunAutostartAsync(ctx, dir, progress)
		elapsed := time.Since(start)
		close(progress)
		events := drainProgress(progress)

		require.NotNil(t, result, "result must not be nil")

		// Must complete at or near the 15 s deadline, not hang.
		assert.Less(t, elapsed.Seconds(), 20.0,
			"should complete near the 15s context deadline, not hang")

		// "a" should have started OR appeared in errors.
		aStarted := false
		for _, s := range result.Scripts {
			if s == "a" {
				aStarted = true
			}
		}
		aErrored := false
		for _, e := range result.Errors {
			if strings.Contains(e, "script a:") {
				aErrored = true
			}
		}
		assert.True(t, aStarted || aErrored,
			"script 'a' should appear in result.Scripts or result.Errors (scripts=%v errors=%v)",
			result.Scripts, result.Errors)

		// "b" must have entered dep wait.
		assert.True(t, hasDependencyWaitEvent(events, "b", "a"),
			"expected PhaseDependencyWaitStart for b->a")
	})

	// noFallbackTimeout: proves "b" stays in dep wait indefinitely (no silent
	// 120 s fallback). Uses event-driven signal instead of a blind sleep.
	t.Run("no fallback timeout waits indefinitely", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, clusterBConfig(t))

		progress := make(chan AutostartProgress, 200)
		ctx, cancel := context.WithCancel(context.Background())

		done := make(chan *AutostartResult, 1)
		go func() {
			done <- d.RunAutostartAsync(ctx, dir, progress)
		}()

		// Wait for "b" to enter dep wait — this is the event-driven proof that
		// the wait is actually active (not that it simply hasn't returned yet).
		gotDepWait := make(chan struct{})
		go func() {
			for ev := range progress {
				if ev.Phase == PhaseDependencyWaitStart && ev.Script == "b" && ev.Dependency == "a" {
					select {
					case gotDepWait <- struct{}{}:
					default:
					}
				}
			}
		}()

		select {
		case <-gotDepWait:
			// "b" confirmed in dep wait — autostart should still be running.
		case <-done:
			t.Fatal("autostart returned before 'b' entered dependency wait — fallback timeout may still exist")
		case <-time.After(15 * time.Second):
			t.Fatal("'b' never entered dep wait within 15s")
		}

		// Cancel the parent context; dep wait must unblock promptly.
		cancelStart := time.Now()
		cancel()

		select {
		case result := <-done:
			unblockTime := time.Since(cancelStart)
			require.NotNil(t, result, "result must not be nil after cancel")
			assert.Less(t, unblockTime, 1500*time.Millisecond,
				"dep wait should unblock within ~1.5s of ctx cancel, took %v", unblockTime)
			assert.Contains(t, result.Scripts, "a", "script 'a' should have started")
			assert.NotContains(t, result.Scripts, "b",
				"script 'b' must not have started — dep wait was cancelled")
		case <-time.After(5 * time.Second):
			t.Fatal("autostart did not return within 5s of parent ctx cancellation")
		}

		close(progress)
	})

	// explicitDepTimeout: per-dep timeout=2 bounds the wait even with no parent deadline.
	t.Run("explicit dependency timeout still honored", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, fmt.Sprintf(`
scripts {
    a {
        run "sleep 60"
        autostart true
        ports %d
    }
    b {
        run "sleep 60"
        autostart true
        depends-on "a" timeout=2
    }
}
`, ephemeralPort(t)))
		progress := make(chan AutostartProgress, 200)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		start := time.Now()
		result := d.RunAutostartAsync(ctx, dir, progress)
		elapsed := time.Since(start)
		close(progress)
		events := drainProgress(progress)

		require.NotNil(t, result, "result must not be nil")

		// Explicit 2 s dep timeout must bound the wait (not 120 s fallback, not infinite).
		assert.Less(t, elapsed.Seconds(), 30.0,
			"explicit 2s dep timeout should bound the wait, took %v", elapsed)

		assert.Contains(t, result.Scripts, "a", "script 'a' should have started")

		// Must have exercised the per-dep timeout code path.
		assert.True(t, hasDependencyWaitEvent(events, "b", "a"),
			"expected PhaseDependencyWaitStart for b->a (per-dep timeout path)")
	})
}

// ---------------------------------------------------------------------------
// TestReadySignaler_WaitReadyCtx — left as-is (already well-structured).
// ---------------------------------------------------------------------------

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
