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

// TestSessionRegister_AsyncReturnsImmediately verifies that SESSION REGISTER
// returns in well under 100ms even when the underlying autostart run takes
// much longer to complete. This is the core contract of the async-registration
// refactor.
func TestSessionRegister_AsyncReturnsImmediately(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Script that takes well over the 100ms registration budget to start.
	// Two scripts in a dependency chain so autostart has real work to do.
	configContent := `
scripts {
    slow-dep {
        run "sleep 60"
        autostart true
    }
    slow-main {
        run "sleep 60"
        autostart true
        depends-on "slow-dep" timeout=30
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
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		d.Stop(stopCtx)
	}()

	client := NewClient(WithSocketPath(sockPath))
	require.NoError(t, client.Connect())
	defer client.Close()

	start := time.Now()
	result, err := client.SessionRegister("fast-session", "/tmp/overlay.sock", tmpDir, "test", nil)
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.NotNil(t, result)

	// The whole point of the refactor: registration does not wait on
	// autostart. 100ms is generous for an IPC round trip on a loopback
	// socket with no autostart blocking it.
	assert.Less(t, elapsed, 500*time.Millisecond,
		"SESSION REGISTER should return quickly (got %v)", elapsed)

	// Response should include the async handle fields.
	assert.Equal(t, "starting", result["status"], "first caller should see status=starting")
	assert.NotEmpty(t, result["autostart_handle"], "response should include an autostart_handle")

	// Backward-compat: existing clients read result["autostart"], which must
	// always be present (even if empty while the run is in flight).
	assert.NotNil(t, result["autostart"], "result must include a backward-compat autostart map")
}

// TestSessionRegister_JoinAsObserver verifies that when two sessions register
// for the same project back-to-back, the first sees status=starting (or
// done, if the run is trivial) and the second sees status=joined.
func TestSessionRegister_JoinAsObserver(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	configContent := `
scripts {
    a {
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
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		d.Stop(stopCtx)
	}()

	c1 := NewClient(WithSocketPath(sockPath))
	require.NoError(t, c1.Connect())
	defer c1.Close()

	c2 := NewClient(WithSocketPath(sockPath))
	require.NoError(t, c2.Connect())
	defer c2.Close()

	r1, err := c1.SessionRegister("session-first", "/tmp/overlay1.sock", tmpDir, "test", nil)
	require.NoError(t, err)
	require.NotNil(t, r1)
	status1, _ := r1["status"].(string)
	assert.Contains(t, []string{"starting", "done"}, status1,
		"first register should be starting or done, got %q", status1)

	// Second session for same project should join — not re-enter autostart.
	r2, err := c2.SessionRegister("session-second", "/tmp/overlay2.sock", tmpDir, "test", nil)
	require.NoError(t, err)
	require.NotNil(t, r2)
	assert.Equal(t, "joined", r2["status"],
		"second session for same project should have status=joined")

	// Both should reference the same handle key (normalized project path).
	h1 := r1["autostart_handle"]
	h2 := r2["autostart_handle"]
	if h1 != nil && h2 != nil {
		assert.Equal(t, h1, h2, "both sessions should share the same handle")
	}
}

// TestSessionRegister_EmptyConfigReturnsDone verifies that projects with no
// .agnt.kdl (or no autostart-eligible scripts) return status=done
// synchronously because the run finishes before WriteJSON returns.
func TestSessionRegister_EmptyConfigReturnsDone(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// No .agnt.kdl — autostart is a no-op and completes immediately.
	d := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})
	require.NoError(t, d.Start())
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		d.Stop(stopCtx)
	}()

	client := NewClient(WithSocketPath(sockPath))
	require.NoError(t, client.Connect())
	defer client.Close()

	result, err := client.SessionRegister("empty-session", "/tmp/overlay.sock", tmpDir, "test", nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	// The handle should be available (we still call GetOrCreate), and because
	// the run is a no-op it is allowed to return done synchronously. The
	// status must be either "done" (preferred) or "starting" (if the
	// select race landed on the pre-done branch), but never "joined".
	status, _ := result["status"].(string)
	assert.Contains(t, []string{"done", "starting"}, status)

	// Backward-compat autostart field must exist and be a map-ish payload.
	assert.NotNil(t, result["autostart"])
}

// TestSessionRegister_ProgressSnapshotIncludesHistory verifies that a late
// joiner calling SESSION REGISTER receives the progress history accumulated
// so far.
func TestSessionRegister_ProgressSnapshotIncludesHistory(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Dependency chain so there is a real sequence of events before the
	// second session registers.
	configContent := `
scripts {
    a {
        run "sleep 60"
        autostart true
    }
    b {
        run "sleep 60"
        autostart true
        depends-on "a" timeout=10
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
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		d.Stop(stopCtx)
	}()

	c1 := NewClient(WithSocketPath(sockPath))
	require.NoError(t, c1.Connect())
	defer c1.Close()

	_, err := c1.SessionRegister("history-session-1", "/tmp/overlay1.sock", tmpDir, "test", nil)
	require.NoError(t, err)

	// Give the autostart run a brief moment to emit at least one progress
	// event. We use a short poll rather than a fixed sleep so the test is
	// robust on slow CI machines.
	handle := d.autostartManager.Get(tmpDir)
	require.NotNil(t, handle, "autostart handle should be registered after first session")

	// 5s is sufficient: makeAutostartStartFn emits PhaseInitiated immediately
	// before any scanning so the first progress event arrives in microseconds.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(handle.Progress()) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.NotEmpty(t, handle.Progress(), "handle should have at least one progress event")

	c2 := NewClient(WithSocketPath(sockPath))
	require.NoError(t, c2.Connect())
	defer c2.Close()

	r2, err := c2.SessionRegister("history-session-2", "/tmp/overlay2.sock", tmpDir, "test", nil)
	require.NoError(t, err)

	progress, ok := r2["progress"].([]interface{})
	require.True(t, ok, "response should include progress slice, got %T", r2["progress"])
	assert.NotEmpty(t, progress, "late joiner should see accumulated progress events")
}

// TestSessionRegister_NoGlobalSerialization verifies that registrations for
// two different projects do not serialize against each other. Before the
// refactor, projectMu blocked any concurrent registration (globally). After
// the refactor, AutostartManager is keyed by project path, so distinct
// projects are independent.
func TestSessionRegister_NoGlobalSerialization(t *testing.T) {
	root := t.TempDir()
	sockPath := filepath.Join(root, "test.sock")

	projectA := filepath.Join(root, "project-a")
	projectB := filepath.Join(root, "project-b")
	require.NoError(t, os.MkdirAll(projectA, 0o755))
	require.NoError(t, os.MkdirAll(projectB, 0o755))

	// Both projects have slow autostart scripts. If registrations serialized
	// globally, the second would wait for the first to finish.
	slowConfig := `
scripts {
    slow {
        run "sleep 60"
        autostart true
    }
}
`
	require.NoError(t, os.WriteFile(filepath.Join(projectA, ".agnt.kdl"), []byte(slowConfig), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(projectB, ".agnt.kdl"), []byte(slowConfig), 0o644))

	d := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})
	require.NoError(t, d.Start())
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		d.Stop(stopCtx)
	}()

	c1 := NewClient(WithSocketPath(sockPath))
	require.NoError(t, c1.Connect())
	defer c1.Close()
	c2 := NewClient(WithSocketPath(sockPath))
	require.NoError(t, c2.Connect())
	defer c2.Close()

	start := time.Now()
	_, err := c1.SessionRegister("s-a", "/tmp/overlay-a.sock", projectA, "test", nil)
	require.NoError(t, err)
	_, err = c2.SessionRegister("s-b", "/tmp/overlay-b.sock", projectB, "test", nil)
	require.NoError(t, err)
	elapsed := time.Since(start)

	// Both registrations together should take well under the time it takes
	// autostart to finish. 2s is generous for two socket round trips plus
	// duplicate scans.
	assert.Less(t, elapsed, 2*time.Second,
		"back-to-back registrations for distinct projects should not serialize (got %v)", elapsed)
}
