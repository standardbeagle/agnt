package daemon

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// A manual proxy that cannot be restored at startup is dropped from state; the
// drop must reach the project's startup log, not only the debug file.
func TestRestoreProxies_FailedManualRestoreIsInStartupLog(t *testing.T) {
	d := NewForTest(t, DaemonConfig{
		EnableStatePersistence: true,
		StatePath:              filepath.Join(t.TempDir(), "state.json"),
	})
	require.NotNil(t, d.stateMgr)

	project := t.TempDir()
	id := makeProcessID(project, "manual")
	d.stateMgr.AddProxy(PersistentProxyConfig{
		ID:        id,
		TargetURL: "http://[::1", // unparseable: Create fails
		Path:      project,
	})

	d.restoreProxies()

	entries := d.startupErrorStore.Query(StartupLogFilter{ProjectPath: project})
	var found bool
	for _, e := range entries {
		if e.EventType == "proxy_restore_failed" && e.Level == "error" && e.ProcessID == id {
			found = true
		}
	}
	require.True(t, found, "failed proxy restore not in the project's startup log: %+v", entries)
}
