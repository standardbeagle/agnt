//go:build e2e

package daemon

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/standardbeagle/go-cli-server/script"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_ReconcileRemovesScriptLive verifies that removing a script from
// `.agnt.kdl` and reconciling stops the running process in place — no daemon
// or session restart. This is the live-edit teardown path (piece D).
func TestE2E_ReconcileRemovesScriptLive(t *testing.T) {
	env := setupDaemonForE2E(t)
	port, pidFilePath, processID := runSingleScriptAutostart(t, env, "dev", "fake-pnpm-watch.sh")
	pid := verifyRunningDataStructures(t, env, "dev", processID, port, pidFilePath)

	// Rewrite config to an empty scripts block — "dev" is gone.
	writeAgntKDL(t, env.ProjectDir, "scripts {\n}\n")

	plan, err := env.Daemon.ReconcileProjectConfig(context.Background(), env.ProjectDir)
	require.NoError(t, err)
	assert.Equal(t, []string{"dev"}, plan.StopScripts, "removed script must be in StopScripts")
	assert.Empty(t, plan.RestartScripts)

	// The process must die and the registry entry must be pruned.
	assertProcessDead(t, pid, 10*time.Second)
	_, ok := env.Daemon.ScriptRegistry().Get("dev", env.ProjectDir)
	assert.False(t, ok, "removed script must be pruned from the registry")
}

// TestE2E_ReconcileRestartsChangedScriptLive verifies that changing a script's
// command in `.agnt.kdl` and reconciling relaunches it with the new command,
// in place.
func TestE2E_ReconcileRestartsChangedScriptLive(t *testing.T) {
	env := setupDaemonForE2E(t)
	port, pidFilePath, processID := runSingleScriptAutostart(t, env, "dev", "fake-pnpm-watch.sh")
	pid1 := verifyRunningDataStructures(t, env, "dev", processID, port, pidFilePath)

	// Rewrite "dev" to run a different fixture on a different port — a real
	// launch-config change.
	newPort := freePort(t)
	env.TrackPort(newPort)
	newPidFile := pidFilePath + ".v2"
	fixturePath := testdataPath(t, "fake-vitest.sh")
	writeAgntKDL(t, env.ProjectDir, fmt.Sprintf(`scripts {
    dev {
        run "bash %s %d %s"
        autostart true
        ports %d
    }
}
`, fixturePath, newPort, newPidFile, newPort))

	plan, err := env.Daemon.ReconcileProjectConfig(context.Background(), env.ProjectDir)
	require.NoError(t, err)
	assert.Equal(t, []string{"dev"}, plan.RestartScripts, "changed script must be in RestartScripts")
	assert.Empty(t, plan.StopScripts)

	// Old process dies; a new one comes up on the new port with the new config.
	assertProcessDead(t, pid1, 10*time.Second)
	require.NoError(t, waitForPort(newPort, 10*time.Second), "new port not bound after reconcile")

	require.Eventually(t, func() bool {
		entry, ok := env.Daemon.ScriptRegistry().Get("dev", env.ProjectDir)
		return ok && entry.State() == script.StateRunning
	}, 10*time.Second, 50*time.Millisecond, "changed script must be running again")
}
