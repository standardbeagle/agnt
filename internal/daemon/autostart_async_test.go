//go:build unix

package daemon

import (
	"context"
	"os"
	"path/filepath"
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
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})
	require.NoError(t, d.Start())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		d.Stop(ctx)
	}()

	progress := make(chan AutostartProgress, 100)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	start := time.Now()
	result := d.RunAutostartAsync(ctx, tmpDir, progress)
	elapsed := time.Since(start)
	close(progress)

	var events []AutostartProgress
	for ev := range progress {
		events = append(events, ev)
	}

	// Should have completed within the 8s timeout (not 120s default)
	assert.Less(t, elapsed.Seconds(), 12.0,
		"should complete near the 8s context deadline, not hang")

	// "a" should have started (layer 0)
	assert.Contains(t, result.Scripts, "a", "script 'a' should have started")

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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
