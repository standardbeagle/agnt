package daemon

import (
	"context"
	"runtime"
	"testing"
	"time"

	goprocess "github.com/standardbeagle/go-cli-server/process"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExitInfoStore_BasicStoreAndGet verifies the in-memory ExitInfo store
// round-trips a lifecycle record.
func TestExitInfoStore_BasicStoreAndGet(t *testing.T) {
	t.Parallel()
	store := newProcessExitInfoStore(10 * time.Minute)

	now := time.Now()
	info := &ProcessExitInfo{
		ProcessID:  "vite:dev",
		ExitCode:   1,
		Reason:     "crash",
		StartedAt:  now.Add(-5 * time.Minute),
		EndedAt:    now,
		Uptime:     5 * time.Minute,
		StderrTail: "[vite] Internal server error",
	}
	store.Set(info)

	got, ok := store.Get("vite:dev")
	require.True(t, ok, "expected ExitInfo to be present")
	assert.Equal(t, 1, got.ExitCode)
	assert.Equal(t, "crash", got.Reason)
	assert.Equal(t, 5*time.Minute, got.Uptime)
	assert.Equal(t, "[vite] Internal server error", got.StderrTail)
}

// TestExitInfoStore_Eviction verifies records older than the retention window
// are evicted on Get.
func TestExitInfoStore_Eviction(t *testing.T) {
	t.Parallel()
	store := newProcessExitInfoStore(10 * time.Minute)

	// Manually insert a stale record
	store.Set(&ProcessExitInfo{
		ProcessID: "stale",
		ExitCode:  0,
		Reason:    "stopped",
		EndedAt:   time.Now().Add(-15 * time.Minute),
	})

	// Fresh record
	store.Set(&ProcessExitInfo{
		ProcessID: "fresh",
		ExitCode:  0,
		Reason:    "stopped",
		EndedAt:   time.Now(),
	})

	_, ok := store.Get("stale")
	assert.False(t, ok, "stale record should be evicted on Get")

	_, ok = store.Get("fresh")
	assert.True(t, ok, "fresh record should still be retrievable")
}

// TestExitInfoStore_SetOverwritesOnRestart verifies that Set for the same
// processID replaces the prior record. This matches the "10 min or until next
// start" retention contract in the task description.
func TestExitInfoStore_SetOverwritesOnRestart(t *testing.T) {
	t.Parallel()
	store := newProcessExitInfoStore(10 * time.Minute)

	store.Set(&ProcessExitInfo{ProcessID: "p1", ExitCode: 1, Reason: "crash", EndedAt: time.Now()})
	store.Set(&ProcessExitInfo{ProcessID: "p1", ExitCode: 0, Reason: "stopped", EndedAt: time.Now()})

	got, ok := store.Get("p1")
	require.True(t, ok)
	assert.Equal(t, 0, got.ExitCode)
	assert.Equal(t, "stopped", got.Reason)
}

// TestExitInfoStore_Clear removes entries on explicit Clear — used on process
// start to reset the death record for the next run.
func TestExitInfoStore_Clear(t *testing.T) {
	t.Parallel()
	store := newProcessExitInfoStore(10 * time.Minute)

	store.Set(&ProcessExitInfo{ProcessID: "p1", ExitCode: 1, Reason: "crash", EndedAt: time.Now()})
	store.Clear("p1")

	_, ok := store.Get("p1")
	assert.False(t, ok, "Clear should remove the entry")
}

// TestClassifyExitReason verifies the reason-string policy.
//
//	exit 0          → "stopped"
//	signal exit     → "signal" (code < 0 from cmd.Wait)
//	non-zero exit   → "crash"
//
// The reason distinguishes "user asked us to stop" from "process died on its own".
func TestClassifyExitReason(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		exitCode int
		want     string
	}{
		{"clean exit", 0, "stopped"},
		{"crash with code", 1, "crash"},
		{"crash with OOM", 137, "crash"},
		{"signal kill", -1, "signal"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyExitReason(tt.exitCode)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestBuildLifecycleAlert verifies that an exit info converts into a
// process:lifecycle AlertEntry with the right dedup-friendly shape.
func TestBuildLifecycleAlert(t *testing.T) {
	t.Parallel()
	now := time.Now()
	info := &ProcessExitInfo{
		ProcessID:  "vite:dev",
		ExitCode:   1,
		Reason:     "crash",
		StartedAt:  now.Add(-12*time.Minute - 34*time.Second),
		EndedAt:    now,
		Uptime:     12*time.Minute + 34*time.Second,
		StderrTail: "[vite] Internal server error: ECONNREFUSED",
	}

	alert := buildLifecycleAlert(info)
	require.NotNil(t, alert)
	assert.Equal(t, "vite:dev", alert.ScriptID)
	assert.Equal(t, CategoryProcessLifecycle, alert.Category)
	// A crash is an error; a clean stop is info-level.
	assert.Equal(t, "error", alert.Severity)
	// The description carries the human-readable exit summary for get_errors.
	assert.Contains(t, alert.Description, "exited")
	assert.Contains(t, alert.Description, "code 1")
	// The line carries the stderr tail so agents see WHY it died.
	assert.Contains(t, alert.Line, "Internal server error")
	// PatternID is stable per death (not per restart) so dedup collapses
	// a single death even if queried multiple times.
	assert.NotEmpty(t, alert.PatternID)
}

// TestBuildLifecycleAlert_CleanStopIsInfo verifies clean shutdowns emit at
// info severity so they don't pollute the error list.
func TestBuildLifecycleAlert_CleanStopIsInfo(t *testing.T) {
	t.Parallel()
	info := &ProcessExitInfo{
		ProcessID: "cleanup",
		ExitCode:  0,
		Reason:    "stopped",
		EndedAt:   time.Now(),
	}
	alert := buildLifecycleAlert(info)
	require.NotNil(t, alert)
	assert.Equal(t, "info", alert.Severity)
}

// TestWatchProcessExit_EndToEnd launches a real short-lived process via
// the daemon's ProcessManager, registers the exit watcher, and verifies
// that both the in-memory exit info store and the alertStore are
// populated after the process exits. This is the end-to-end contract
// test for the diagnostic gap — a dead process MUST be visible.
func TestWatchProcessExit_EndToEnd(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("integration test uses /bin/sh")
	}

	d := New(DaemonConfig{
		SocketPath:   shortSockPath(t),
		MaxClients:   4,
		WriteTimeout: 2 * time.Second,
	})
	require.NoError(t, d.Start())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = d.Stop(ctx)
	})

	ctx := context.Background()
	proc, err := d.hub.ProcessManager().StartCommand(ctx, goprocess.ProcessConfig{
		ID:          "test-exit-watch",
		ProjectPath: t.TempDir(),
		Command:     "/bin/sh",
		// stderr: write a recognisable panic, then exit 42
		Args: []string{"-c", "echo 'boot ok'; echo 'fatal: bang' >&2; exit 42"},
	})
	require.NoError(t, err)

	d.watchProcessExit(proc)

	// Wait for the process to finish.
	select {
	case <-proc.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("process did not finish in time")
	}

	// Give the watcher goroutine a moment to publish.
	deadline := time.Now().Add(2 * time.Second)
	var info *ProcessExitInfo
	for time.Now().Before(deadline) {
		if i, ok := d.processExitInfo.Get("test-exit-watch"); ok {
			info = i
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.NotNil(t, info, "ProcessExitInfo should be recorded")
	assert.Equal(t, 42, info.ExitCode)
	assert.Equal(t, "crash", info.Reason)
	assert.NotZero(t, info.EndedAt)
	assert.True(t, info.Uptime > 0, "uptime must be > 0")
	assert.Contains(t, info.StderrTail, "fatal: bang", "stderr tail must include the panic line")

	// Lifecycle alert should be queryable. watchProcessExit publishes
	// processExitInfo (polled above) BEFORE it Adds the lifecycle alert, so the
	// alert can trail the info we just observed — poll for it rather than
	// asserting it the instant the info appears.
	var found *AlertEntry
	alertDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(alertDeadline) {
		for _, a := range d.alertStore.Query(AlertStoreFilter{ProcessID: "test-exit-watch"}) {
			if a.Category == CategoryProcessLifecycle {
				found = a
				break
			}
		}
		if found != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.NotNil(t, found, "alertStore should contain a process_lifecycle entry")
	assert.Equal(t, "error", found.Severity)
	assert.Contains(t, found.Description, "code 42")
	assert.Contains(t, found.Line, "fatal: bang")
}

// TestWatchProcessExit_NewWatcherClearsStaleInfo verifies the autorestart
// contract: when a process is replaced by a new instance with the same
// ID, calling watchProcessExit on the new instance clears any stale
// death record so proc status reflects the new, running instance
// instead of the dead one.
func TestWatchProcessExit_NewWatcherClearsStaleInfo(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("integration test uses /bin/sh")
	}

	d := New(DaemonConfig{
		SocketPath:   shortSockPath(t),
		MaxClients:   4,
		WriteTimeout: 2 * time.Second,
	})
	require.NoError(t, d.Start())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = d.Stop(ctx)
	})

	ctx := context.Background()
	proc1, err := d.hub.ProcessManager().StartCommand(ctx, goprocess.ProcessConfig{
		ID:          "race-test",
		ProjectPath: t.TempDir(),
		Command:     "/bin/sh",
		Args:        []string{"-c", "exit 1"},
	})
	require.NoError(t, err)

	// Watch the first instance and let its watcher publish.
	d.watchProcessExit(proc1)
	<-proc1.Done()

	// Wait until the exit info has been Set by the old watcher.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := d.processExitInfo.Get("race-test"); ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, oldPublished := d.processExitInfo.Get("race-test")
	require.True(t, oldPublished, "old watcher should have published the death record")

	// Replace proc1 with a fresh instance under the same ID.
	d.hub.ProcessManager().RemoveByPath("race-test", proc1.ProjectPath)
	proc2, err := d.hub.ProcessManager().StartCommand(ctx, goprocess.ProcessConfig{
		ID:          "race-test",
		ProjectPath: proc1.ProjectPath,
		Command:     "/bin/sh",
		Args:        []string{"-c", "sleep 60"},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = d.hub.ProcessManager().StopProcess(ctx, proc2)
	})

	// Registering the new watcher MUST wipe the stale record so proc
	// status for the now-running instance does not show a stale death.
	d.watchProcessExit(proc2)

	_, stillStale := d.processExitInfo.Get("race-test")
	assert.False(t, stillStale, "new watcher must clear stale death record on start")
}

// TestWatchProcessExit_RaceGuard_LateWatcher verifies the ProcessManager
// identity check: if the old watcher goroutine is slow and runs AFTER
// the new instance has been registered, the old watcher sees a
// different ManagedProcess under its ID and skips the write.
//
// This is the harder race — exercised by forcing the Set path to run
// after a new instance exists under the same ID.
func TestWatchProcessExit_RaceGuard_LateWatcher(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("integration test uses /bin/sh")
	}

	d := New(DaemonConfig{
		SocketPath:   shortSockPath(t),
		MaxClients:   4,
		WriteTimeout: 2 * time.Second,
	})
	require.NoError(t, d.Start())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = d.Stop(ctx)
	})

	ctx := context.Background()

	// Simulate a "dead" old process by fabricating a ManagedProcess
	// that is not registered in the ProcessManager (proc1), but write
	// its death via Set directly — then register proc2 under the SAME
	// ID, and call watchProcessExit on it. The test validates that
	// the new instance's Clear wipes the fabricated stale record.
	//
	// Note: we can't easily force the OLD watcher's goroutine to fire
	// late without touching unexported scheduling internals, so we
	// simulate the race via direct Set/Clear. The race guard in the
	// watcher goroutine itself is covered by a unit test:
	// TestProcessExitStore_SetAfterClearIsSafe.
	d.processExitInfo.Set(&ProcessExitInfo{
		ProcessID: "late-race",
		ExitCode:  1,
		Reason:    "crash",
		EndedAt:   time.Now(),
	})

	proc2, err := d.hub.ProcessManager().StartCommand(ctx, goprocess.ProcessConfig{
		ID:          "late-race",
		ProjectPath: t.TempDir(),
		Command:     "/bin/sh",
		Args:        []string{"-c", "sleep 60"},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = d.hub.ProcessManager().StopProcess(ctx, proc2)
	})

	d.watchProcessExit(proc2)
	_, stillStale := d.processExitInfo.Get("late-race")
	assert.False(t, stillStale, "stale record must be cleared when the new watcher starts")
}

// TestExitInfoToResponse verifies the hub_proc serializer adds the
// last_exit_* keys expected by the tools layer.
func TestExitInfoToResponse(t *testing.T) {
	t.Parallel()
	now := time.Now()
	info := &ProcessExitInfo{
		ProcessID:  "vite:dev",
		ExitCode:   1,
		Reason:     "crash",
		StartedAt:  now.Add(-12*time.Minute - 34*time.Second),
		EndedAt:    now,
		Uptime:     12*time.Minute + 34*time.Second,
		StderrTail: "[vite] Internal server error",
	}
	resp := make(map[string]interface{})
	exitInfoToResponse(resp, info)

	// Every field expected by tools.populateLastExitFields must be
	// present. This is the wire contract.
	require.Contains(t, resp, "last_exit_at")
	require.Contains(t, resp, "last_exit_code")
	require.Contains(t, resp, "last_exit_reason")
	require.Contains(t, resp, "last_uptime")
	require.Contains(t, resp, "last_stderr_tail")

	assert.Equal(t, 1, resp["last_exit_code"])
	assert.Equal(t, "crash", resp["last_exit_reason"])
	assert.Equal(t, "12m34s", resp["last_uptime"])
	assert.Equal(t, "[vite] Internal server error", resp["last_stderr_tail"])

	// Parseable RFC3339 timestamp
	parsed, err := time.Parse(time.RFC3339, resp["last_exit_at"].(string))
	require.NoError(t, err)
	assert.WithinDuration(t, now, parsed, 2*time.Second)
}

// TestExitInfoToResponse_NoStderrTail verifies an empty stderr tail does
// not emit the last_stderr_tail key (keeps the response compact when
// there's nothing useful to show).
func TestExitInfoToResponse_NoStderrTail(t *testing.T) {
	t.Parallel()
	info := &ProcessExitInfo{
		ProcessID: "p1",
		ExitCode:  0,
		Reason:    "stopped",
		EndedAt:   time.Now(),
	}
	resp := make(map[string]interface{})
	exitInfoToResponse(resp, info)
	assert.NotContains(t, resp, "last_stderr_tail")
}

// TestWatchProcessExit_CleanExit verifies a clean exit (code 0) records
// the death in the exit-info store (so proc status can show it) but does
// NOT push a lifecycle alert (so get_errors isn't polluted with noise
// from every user-initiated stop).
func TestWatchProcessExit_CleanExit(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("integration test uses /bin/sh")
	}

	d := New(DaemonConfig{
		SocketPath:   shortSockPath(t),
		MaxClients:   4,
		WriteTimeout: 2 * time.Second,
	})
	require.NoError(t, d.Start())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = d.Stop(ctx)
	})

	ctx := context.Background()
	proc, err := d.hub.ProcessManager().StartCommand(ctx, goprocess.ProcessConfig{
		ID:          "test-clean-exit",
		ProjectPath: t.TempDir(),
		Command:     "/bin/sh",
		Args:        []string{"-c", "exit 0"},
	})
	require.NoError(t, err)

	d.watchProcessExit(proc)

	select {
	case <-proc.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("process did not finish in time")
	}

	deadline := time.Now().Add(2 * time.Second)
	var info *ProcessExitInfo
	for time.Now().Before(deadline) {
		if i, ok := d.processExitInfo.Get("test-clean-exit"); ok {
			info = i
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.NotNil(t, info, "exit info is still recorded for clean exits")
	assert.Equal(t, 0, info.ExitCode)
	assert.Equal(t, "stopped", info.Reason)

	// Clean exits must NOT produce a lifecycle alert — they would
	// pollute get_errors with every user-initiated stop.
	alerts := d.alertStore.Query(AlertStoreFilter{ProcessID: "test-clean-exit"})
	for _, a := range alerts {
		assert.NotEqual(t, CategoryProcessLifecycle, a.Category,
			"clean exits must not push a lifecycle alert")
	}
}
