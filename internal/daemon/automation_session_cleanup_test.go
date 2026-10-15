package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/chromedp"
	"github.com/stretchr/testify/require"
)

// startFakeAutomation starts a chromedp automation session for projectPath
// without launching Chrome.
func startFakeAutomation(t *testing.T, d *Daemon, projectPath, name string) {
	t.Helper()
	id := makeProcessID(projectPath, name)
	_, err := d.sessionm.Start(context.Background(), id, chromedp.SessionConfig{ID: id, Headless: true, Path: projectPath})
	require.NoError(t, err)
}

// TestCleanupSessionResources_StopsAutomationSessions: when a project's last
// session leaves, its chromedp automation sessions stop and free their slots
// under the daemon-wide cap; another project's sessions keep running.
func TestCleanupSessionResources_StopsAutomationSessions(t *testing.T) {
	d := NewForTest(t, DaemonConfig{})
	chromedp.UseFakeLauncherForTest(t, d.sessionm)

	projA, projB := t.TempDir(), t.TempDir()
	registerSession(t, d, "session-A", projA)
	registerSession(t, d, "session-B", projB)
	startFakeAutomation(t, d, projA, "a1")
	startFakeAutomation(t, d, projA, "a2")
	startFakeAutomation(t, d, projB, "b1")
	require.Equal(t, 3, d.sessionm.ActiveCount())

	d.CleanupSessionResources("session-A")

	require.Eventually(t, func() bool { return d.sessionm.ActiveCount() == 1 },
		5*time.Second, 10*time.Millisecond,
		"project A's automation sessions must stop when its last session leaves")
	require.Empty(t, d.sessionm.ListByPath(projA))
	require.Len(t, d.sessionm.ListByPath(projB), 1, "project B's session must survive")
}

// TestStopResources_StopAutomationSessions: STOP-ALL (global and project
// scoped) stops automation sessions along with processes and proxies.
func TestStopResources_StopAutomationSessions(t *testing.T) {
	d := NewForTest(t, DaemonConfig{})
	chromedp.UseFakeLauncherForTest(t, d.sessionm)

	projA, projB := t.TempDir(), t.TempDir()
	startFakeAutomation(t, d, projA, "a1")
	startFakeAutomation(t, d, projB, "b1")

	d.StopProjectResources(context.Background(), projA)
	require.Eventually(t, func() bool { return len(d.sessionm.ListByPath(projA)) == 0 },
		5*time.Second, 10*time.Millisecond, "project-scoped stop must stop the project's automation sessions")
	require.Len(t, d.sessionm.ListByPath(projB), 1, "project-scoped stop must not touch another project")

	d.StopAllResources(context.Background())
	require.Eventually(t, func() bool { return d.sessionm.ActiveCount() == 0 },
		5*time.Second, 10*time.Millisecond, "global stop must stop every automation session")
}
