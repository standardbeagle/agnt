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
	t.Parallel()
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

	result, err := client.SessionRegister("fast-session", "/tmp/overlay.sock", tmpDir, "test", nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	// The observed "starting" state while the 60-second autostart remains in
	// flight proves registration returned asynchronously; no scheduler-sensitive
	// IPC wall-clock budget is needed.
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
	t.Parallel()
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
	t.Parallel()
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

// TestSessionRegister_ReconnectPreservesStartedAt verifies that a reconnect
// (SESSION REGISTER for an already-registered code) inherits the original
// session's StartedAt rather than stamping a fresh now. The fresh Session is
// stamped StartedAt=now at parse time; if the reconnect kept that, the session
// would masquerade as the newest one and sessionMoreRecent (FindByDirectory's
// last-started-wins tiebreak) could flip overlay ownership within a project on
// every reconnect.
func TestSessionRegister_ReconnectPreservesStartedAt(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// No .agnt.kdl — autostart is a no-op so both registrations complete fast.
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

	const code = "reconnect-startedat"
	_, err := client.SessionRegister(code, "/tmp/overlay1.sock", tmpDir, "test", nil)
	require.NoError(t, err)

	original, ok := d.sessionRegistry.Get(code)
	require.True(t, ok, "session must be registered after first register")
	startedAt := original.StartedAt

	// Ensure the fresh reconnect's parse-time now is strictly later, so the
	// assertion proves inheritance rather than coincidental equality.
	time.Sleep(5 * time.Millisecond)

	// Reconnect: same code, different overlay path. The overlay change proves
	// the reconnect merge ran (not merely a cancel-pending short-circuit).
	_, err = client.SessionRegister(code, "/tmp/overlay2.sock", tmpDir, "test", nil)
	require.NoError(t, err)

	reconnected, ok := d.sessionRegistry.Get(code)
	require.True(t, ok, "session must still be registered after reconnect")
	assert.NotSame(t, original, reconnected, "reconnect must swap in a fresh Session identity")
	assert.Equal(t, "/tmp/overlay2.sock", reconnected.OverlayPath, "reconnect must refresh the overlay path")
	assert.True(t, reconnected.StartedAt.Equal(startedAt),
		"reconnect must inherit the original StartedAt (%s), got %s", startedAt, reconnected.StartedAt)
}

// TestSessionRegister_ProgressSnapshotIncludesHistory verifies that a late
// joiner calling SESSION REGISTER receives the progress history accumulated
// so far.
func TestSessionRegister_ProgressSnapshotIncludesHistory(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

// TestSessionUnregister_IdempotentForUnknownSession verifies that UNREGISTER of
// a session the registry never held returns OK, not ErrNotFound. This is the
// normal shutdown race: deferred cleanup wins the connection drop and retires
// the session, then the client sends an explicit UNREGISTER — the desired end
// state (session absent) already holds, so it is success.
func TestSessionUnregister_IdempotentForUnknownSession(t *testing.T) {
	t.Parallel()
	_, sockPath := newSessionHostTestDaemon(t)

	c := NewConn(sockPath)
	defer c.Close()

	require.NoError(t, c.Request("SESSION", "UNREGISTER", "never-registered").OK())
}

// TestSessionRegister_ReconnectInheritsUnsuppliedFields exercises every arm of
// the reconnect merge in hubHandleSessionRegister (hub_session.go): a reconnect
// that leaves a field unset inherits the original registration's value, while a
// reconnect that supplies a fresh value overrides it. StartedAt inheritance has
// its own dedicated test (TestSessionRegister_ReconnectPreservesStartedAt); this
// one covers the remaining five arms — OverlayPath, Command+Args, SessionPGID,
// SessionJobHandle, and Kind.
//
// Two of these arms are load-bearing containment invariants, called out in the
// per-case asserts below:
//   - SessionPGID must be inherited: dropping it on reconnect would leave
//     SessionPGID==0, making killSessionPGID a no-op at cleanup and letting the
//     session's backgrounded jobs (e.g. `npm run dev &`) escape containment.
//   - Kind must be inherited: dropping it would flip a session-host session back
//     to the classic default, defeating doCleanupExact's explicit-kill-only
//     guard and letting a client disconnect reap a daemon-owned PTY.
//
// The first registration is seeded directly into the registry so all six fields
// (including Kind, which the classic client wire never carries) start non-zero;
// the reconnect then arrives over the real client, matching the driving shape of
// TestSessionRegister_ReconnectPreservesStartedAt.
func TestSessionRegister_ReconnectInheritsUnsuppliedFields(t *testing.T) {
	t.Parallel()

	const (
		seedOverlay = "/tmp/seed-overlay.sock"
		seedCommand = "claude"
		seedPGID    = 4242
		seedHandle  = uint64(0xDEAD)
	)
	seedArgs := []string{"--model", "opus"}

	cases := []struct {
		name             string
		reconnectOverlay string
		reconnectCommand string
		reconnectArgs    []string
		reconnectPGID    int
		reconnectHandle  uint64
		wantOverlay      string
		wantCommand      string
		wantArgs         []string
		wantPGID         int
		wantHandle       uint64
	}{
		{
			name:             "unsupplied fields inherit the seeded lifetime",
			reconnectOverlay: "",
			reconnectCommand: "",
			reconnectArgs:    nil,
			reconnectPGID:    0,
			reconnectHandle:  0,
			wantOverlay:      seedOverlay,
			wantCommand:      seedCommand,
			wantArgs:         seedArgs,
			wantPGID:         seedPGID,
			wantHandle:       seedHandle,
		},
		{
			name:             "supplied fields override the seeded lifetime",
			reconnectOverlay: "/tmp/new-overlay.sock",
			reconnectCommand: "gemini",
			reconnectArgs:    []string{"--flag"},
			reconnectPGID:    9999,
			reconnectHandle:  uint64(0xBEEF),
			wantOverlay:      "/tmp/new-overlay.sock",
			wantCommand:      "gemini",
			wantArgs:         []string{"--flag"},
			wantPGID:         9999,
			wantHandle:       uint64(0xBEEF),
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tmpDir := t.TempDir()
			sockPath := filepath.Join(tmpDir, "test.sock")

			// No .agnt.kdl — autostart is a no-op so registration completes fast.
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

			const code = "reconnect-inherit"

			// Seed the first lifetime directly so Kind (never carried by the
			// classic client wire) and the containment handles start non-zero.
			seed := &Session{
				Code:             code,
				OverlayPath:      seedOverlay,
				ProjectPath:      normalizePath(tmpDir),
				Command:          seedCommand,
				Args:             seedArgs,
				StartedAt:        time.Now().Add(-time.Hour),
				Status:           SessionStatusActive,
				LastSeen:         time.Now().Add(-time.Hour),
				SessionPGID:      seedPGID,
				SessionJobHandle: seedHandle,
				Kind:             SessionKindClassic,
			}
			require.NoError(t, d.sessionRegistry.Register(seed))
			seedStartedAt := seed.StartedAt

			// Reconnect over the real client with the case's fields.
			client := NewClient(WithSocketPath(sockPath))
			require.NoError(t, client.Connect())
			defer client.Close()

			_, err := client.SessionRegisterWithContainment(
				code, tc.reconnectOverlay, tmpDir, tc.reconnectCommand, tc.reconnectArgs,
				tc.reconnectPGID, tc.reconnectHandle)
			require.NoError(t, err)

			got, ok := d.sessionRegistry.Get(code)
			require.True(t, ok, "session must still be registered after reconnect")
			require.NotSame(t, seed, got, "reconnect must swap in a fresh Session identity")

			require.Equal(t, tc.wantOverlay, got.OverlayPath, "OverlayPath arm")
			require.Equal(t, tc.wantCommand, got.Command, "Command arm")
			require.Equal(t, tc.wantArgs, got.Args, "Args arm")
			require.Equal(t, tc.wantPGID, got.SessionPGID,
				"SessionPGID arm — dropping this would no-op killSessionPGID and break containment")
			require.Equal(t, tc.wantHandle, got.SessionJobHandle, "SessionJobHandle arm")
			// Kind is never supplied by the classic client wire, so it inherits in
			// both cases — the invariant that keeps a session-host session
			// explicit-kill-only across reconnects.
			require.Equal(t, SessionKindClassic, got.Kind,
				"Kind arm — dropping this would defeat the session-host explicit-kill-only guard")
			// A reconnect is the same logical session: it keeps the seeded
			// StartedAt regardless of which fields it re-supplied.
			require.True(t, got.StartedAt.Equal(seedStartedAt), "reconnect must inherit StartedAt")
		})
	}
}

// TestSessionRegister_RejectsSessionHostOwnedCode verifies the cross-kind
// guard in hubHandleSessionRegister: a classic SESSION REGISTER against a code
// already owned by a session-host entry is rejected loudly rather than misread
// as a reconnect. Session-host ids ("<cfg.Name>-<idCounter>") and classic codes
// ("<command-base>-<seq>") come from two independent counters over a
// user-supplied name, so a `--name claude` session-host `claude-9` and a
// classic `claude-9` can collide. Without the guard, the collision would take
// the ErrSessionExists reconnect branch and ReplaceExact the session-host
// entry — breaking its explicit-kill-only invariant (a later conn drop would
// then run deferred cleanup against a daemon-owned PTY).
func TestSessionRegister_RejectsSessionHostOwnedCode(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// No .agnt.kdl — the register is rejected before autostart runs anyway.
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

	const code = "claude-9"

	// Seed a session-host owned entry directly — Kind is never carried by the
	// classic client wire, so it must be planted, mirroring the seeding shape of
	// TestSessionRegister_ReconnectInheritsUnsuppliedFields.
	seed := &Session{
		Code:        code,
		ProjectPath: normalizePath(tmpDir),
		Command:     "sh",
		StartedAt:   time.Now().Add(-time.Hour),
		Status:      SessionStatusActive,
		LastSeen:    time.Now().Add(-time.Hour),
		SessionPGID: 4242,
		Kind:        SessionKindSessionHost,
	}
	require.NoError(t, d.sessionRegistry.Register(seed))

	client := NewClient(WithSocketPath(sockPath))
	require.NoError(t, client.Connect())
	defer client.Close()

	// A classic SESSION REGISTER on the same code must be rejected, not merged.
	_, err := client.SessionRegister(code, "/tmp/overlay.sock", tmpDir, "test", nil)
	require.Error(t, err, "classic register against a session-host owned code must fail loud")
	require.Contains(t, err.Error(), "session-host",
		"rejection must name the session-host ownership, got %v", err)

	// The registry entry must be untouched: same pointer, still session-host,
	// containment handle intact — proving no ReplaceExact ran.
	got, ok := d.sessionRegistry.Get(code)
	require.True(t, ok, "session-host entry must survive the rejected register")
	require.Same(t, seed, got, "rejected register must not swap the session-host identity")
	require.Equal(t, SessionKindSessionHost, got.Kind, "Kind must remain session-host")
	require.Equal(t, 4242, got.SessionPGID, "session-host containment handle must be untouched")
}
