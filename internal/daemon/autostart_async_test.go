//go:build unix

package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clusterAConfig is one long-running script named "test"; shared by Cluster A subtests.
// Built from stayAliveCmd so the keep-alive duration is single-sourced and the
// worst-case orphan lifetime is bounded — see stayAliveSeconds.
func clusterAConfig() string {
	return `
scripts {
    test {
        run "` + stayAliveCmd() + `"
        autostart true
    }
}
`
}

// clusterBConfig builds the shared Cluster B config with a per-call ephemeral
// port for script "a". The port is intentionally one no process will bind to
// (the script just sleeps), so URL detection never fires and the dependency
// wait is driven by timeout/context cancellation paths.
func clusterBConfig(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf(`
scripts {
    a {
        run "%s"
        autostart true
        ports %d
    }
    b {
        run "%s"
        autostart true
        depends-on "a"
    }
}
`, stayAliveCmd(), ephemeralPort(t), stayAliveCmd())
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
		writeConfig(t, dir, clusterAConfig())

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
		writeConfig(t, dir, clusterAConfig())

		result := d.RunAutostart(context.Background(), dir)

		require.NotNil(t, result, "result must not be nil")
		assert.Contains(t, result.Scripts, "test", "script 'test' should appear in result.Scripts")
		assert.Len(t, result.Scripts, 1, "exactly 1 script should start")
		assert.Empty(t, result.Errors, "RunAutostart should surface no errors for a healthy script")
	})

	t.Run("progress_events_in_order", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, fmt.Sprintf(`
scripts {
    a {
        run "%[1]s"
        autostart true
    }
    b {
        run "%[1]s"
        autostart true
        depends-on "a" timeout=5
    }
    c {
        run "%[1]s"
        autostart true
        depends-on "b" timeout=5
    }
}
`, stayAliveCmd()))
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
        run "` + stayAliveCmd() + `"
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

	// A "context cancel during dependency wait" subtest used to sit here. It set
	// a 15s parent deadline and waited the whole thing out — 15s of gate time on
	// every run — then asserted only that it finished, that "a" started or
	// errored, and that "b" reached dep wait. It was deleted as dominated:
	//
	//   - Every observable it asserted is also asserted by "no fallback timeout
	//     waits indefinitely" below, which additionally pins that result.Scripts
	//     contains "a" and NOT "b", and proves the wait was genuinely ACTIVE
	//     (it waits for PhaseDependencyWaitStart before cancelling) rather than
	//     merely un-returned. It costs ~0.1s to prove strictly more.
	//   - Its one distinguishable production path — waitForSingleDependency's
	//     errors.Is(err, context.DeadlineExceeded) branch, which a parent
	//     deadline reaches but a parent cancel does not — is already exercised by
	//     "explicit dependency timeout still honored" below via a declared
	//     per-dep timeout. The only difference was the timeout value
	//     interpolated into a log line no test asserts on.
	//
	// Two subtests of one mechanism is double gate cost for single coverage.

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

	// explicitDepTimeout: per-dep timeout=1 bounds the wait even with no parent
	// deadline. 1s is the floor the config accepts: depends-on timeout=N is
	// parsed by config.toSeconds, which truncates to whole seconds
	// (timeout=0.5 becomes 0 == "wait indefinitely"), so sub-second is not
	// expressible here.
	t.Run("explicit dependency timeout still honored", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, fmt.Sprintf(`
scripts {
    a {
        run "%[1]s"
        autostart true
        ports %[2]d
    }
    b {
        run "%[1]s"
        autostart true
        depends-on "a" timeout=1
    }
}
`, stayAliveCmd(), ephemeralPort(t)))
		progress := make(chan AutostartProgress, 200)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		start := time.Now()
		result := d.RunAutostartAsync(ctx, dir, progress)
		elapsed := time.Since(start)
		close(progress)
		events := drainProgress(progress)

		require.NotNil(t, result, "result must not be nil")

		// "a" declares an ephemeral port nothing ever binds, so its port probe
		// never signals ready and the declared timeout is the only thing that can
		// end the wait. A lower bound is therefore a real, load-invariant
		// invariant (a timer cannot fire early): if the wait returns in well under
		// 1s, something other than the declared timeout released it.
		assert.GreaterOrEqual(t, elapsed.Seconds(), 0.9,
			"the declared timeout=1 must be what bounds the wait; returning early means "+
				"the dep was released by something else, took %v", elapsed)
		// Generous liveness ceiling, not a latency budget: its job is to catch a
		// regression that reinstates the old 120s implicit fallback (or drops the
		// bound entirely), which is 100x away from this value.
		assert.Less(t, elapsed.Seconds(), 10.0,
			"explicit 1s dep timeout should bound the wait, took %v", elapsed)

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
