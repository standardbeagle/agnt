package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAlertIngest_ManagedProcessStampsProjectPath proves the ingest-tagging
// gap at internal/daemon/daemon.go OnAlert: alerts produced by the daemon's
// own AlertScanner (the `proc run` managed-process path) are stored WITHOUT
// a ProjectPath, so even a correct session-scope filter on ALERTS QUERY
// cannot match them — the data is untagged at the source.
//
// RED until C2 stamps the owning project path onto scanner-ingested alerts
// (the ALERTS REPORT path already stamps from the payload; only this
// in-daemon path is missing it).
func TestAlertIngest_ManagedProcessStampsProjectPath(t *testing.T) {
	// No t.Parallel(): starts a real sleep process; PID-reuse kills it
	// under high concurrency.
	daemon, _, tmpDir := newHubProcTestDaemon(t)

	name := "ingest-dev"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, daemon.StartScriptExplicit(ctx, name, &config.ScriptConfig{Run: longRunningCmd()}, tmpDir, nil))

	processID := makeProcessID(tmpDir, name)
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = daemon.hub.ProcessManager().Stop(stopCtx, processID)
	})

	proc, err := daemon.hub.ProcessManager().Get(processID)
	require.NoError(t, err)
	wantProject := proc.ProjectPath
	require.NotEmpty(t, wantProject, "managed process must carry a project path to scope against")

	// Drive the production scanner the same way real process output does:
	// a matching error line for this managed process ID. OnAlert flushes
	// after the batch window and writes into d.alertStore.
	daemon.alertScanner.ProcessLine("panic: boom runtime error", processID)

	// The scanner's batch window (default 3s) gates delivery, so wait for
	// the entry to land before asserting its ProjectPath.
	require.Eventually(t, func() bool {
		return len(daemon.alertStore.Query(AlertStoreFilter{ProcessID: processID})) > 0
	}, 8*time.Second, 50*time.Millisecond, "scanner never wrote the alert into the store")

	entries := daemon.alertStore.Query(AlertStoreFilter{ProcessID: processID})
	require.NotEmpty(t, entries)
	got := entries[len(entries)-1]

	assert.Equal(t, processID, got.ScriptID, "alert must carry the producing process ID")
	assert.Equal(t, wantProject, got.ProjectPath,
		"managed-process alert must be stamped with its owning project path at ingest (gap at daemon.go OnAlert)")
}

// TestStartupLog_NoDefaultTimeWindow proves the second half of the
// "startup log always empty" fix: hubHandleStartupLog must not inject a
// 30-minute wall-clock Since when the caller supplies no `since`. Autostart is
// a one-shot at session start, so the old default left the log empty minutes
// later. The store is a capacity-bounded ring, so an unbounded-in-time query is
// safe; an aged entry must still come back.
func TestStartupLog_NoDefaultTimeWindow(t *testing.T) {
	// No t.Parallel(): shares the daemon startup log store.
	daemon, sockPath := newBootedDaemon(t)

	projectPath := normalizePath(t.TempDir())
	daemon.startupErrorStore.Add(&StartupLogEntry{
		ProcessID:  makeProcessID(projectPath, "dev"),
		ScriptName: "dev",
		Level:      "error",
		EventType:  "start_failed",
		Message:    "aged autostart failure",
		Timestamp:  time.Now().Add(-2 * time.Hour),
	})

	c := NewClient(WithSocketPath(sockPath))
	require.NoError(t, c.Connect())
	t.Cleanup(func() { _ = c.Close() })

	// Global query (no session needed), no `since`: the 2-hour-old entry must
	// still be returned. Under the old 30-minute default it would be dropped.
	raw, err := c.StartupLog(50, protocol.DirectoryFilter{Global: true})
	require.NoError(t, err)

	entries, ok := raw["entries"].([]interface{})
	require.True(t, ok, "response carries an entries array")

	var found bool
	for _, e := range entries {
		m, _ := e.(map[string]interface{})
		if m == nil {
			continue
		}
		if m["event_type"] == "start_failed" && m["message"] == "aged autostart failure" {
			found = true
			break
		}
	}
	assert.True(t, found, "startup_log with no `since` must return aged entries (no 30m wall-clock default)")
}
