//go:build unix

package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/stretchr/testify/require"
)

// TestRunLifecycleHookAsync_ReportsFailure pins that a failing async
// lifecycle hook invokes the onFailure callback (in addition to the
// always-on debug.Warn, which by itself never reaches the agent/session
// log — daemon-architecture.md § Silent Failure Prohibition). Production
// callers use this to record the failure on the project's startup log; see
// the on-start hook wiring in daemon_autostart.go's StartScriptExplicit.
func TestRunLifecycleHookAsync_ReportsFailure(t *testing.T) {
	scriptCfg := &config.ScriptConfig{}
	done := make(chan error, 1)
	runLifecycleHookAsync("exit 1", "web", "start", scriptCfg, 0, func(err error) {
		done <- err
	})
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("onFailure callback was never invoked for a failing hook")
	}
}

// TestRunLifecycleHookAsync_NoCallbackOnSuccess pins the converse: a
// succeeding hook never fires onFailure.
func TestRunLifecycleHookAsync_NoCallbackOnSuccess(t *testing.T) {
	scriptCfg := &config.ScriptConfig{}
	called := make(chan struct{}, 1)
	runLifecycleHookAsync("true", "web", "start", scriptCfg, 0, func(error) {
		called <- struct{}{}
	})
	select {
	case <-called:
		t.Fatal("onFailure invoked for a hook that succeeded")
	case <-time.After(300 * time.Millisecond):
	}
}

// TestStartScriptExplicit_OnStartHookFailureReachesStartupLog is the
// end-to-end regression for the on-start hook wiring in
// StartScriptExplicit (daemon_autostart.go): a failing on-start hook must
// produce a visible "hook_failed" entry on the project's startup log, not
// just a debug.Warn nobody but a developer tailing the debug file would see.
func TestStartScriptExplicit_OnStartHookFailureReachesStartupLog(t *testing.T) {
	d := NewForTest(t, DaemonConfig{})
	dir := t.TempDir()

	scriptCfg := &config.ScriptConfig{
		Run: stayAliveCmd(),
		Hooks: &config.ScriptLifecycleHooks{
			OnStart: "exit 1",
		},
	}
	require.NoError(t, d.StartScriptExplicit(context.Background(), "web", scriptCfg, dir, nil))

	require.Eventually(t, func() bool {
		entries := d.startupErrorStore.Query(StartupLogFilter{ProjectPath: dir})
		for _, e := range entries {
			if e.EventType == "hook_failed" && e.ScriptName == "web" {
				return true
			}
		}
		return false
	}, 3*time.Second, 20*time.Millisecond, "expected a hook_failed startup-log entry for the failing on-start hook")
}
