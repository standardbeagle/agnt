package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
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
