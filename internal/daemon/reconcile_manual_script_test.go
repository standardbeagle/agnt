//go:build unix

package daemon

import (
	"context"
	"testing"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/go-cli-server/script"
	"github.com/stretchr/testify/require"
)

// TestReconcileProjectConfig_DoesNotStopManuallyStartedScript pins that
// ReconcileProjectConfig never stops a script the agent started by hand (MCP
// PROC RUN, StartScriptExplicit under an arbitrary name) just because it is
// not declared in .agnt.kdl. The desired set only ever contains
// autostart-eligible scripts (cfg.GetAutostartScripts()), but the running
// set previously listed EVERY registered script for the project regardless
// of whether .agnt.kdl declares it at all — so a manually started process
// diffed as "running, not desired" and reconcile stopped it out from under
// the agent. Reconcile must manage only config-declared scripts (autostart
// or not); an undeclared name is untouched.
func TestReconcileProjectConfig_DoesNotStopManuallyStartedScript(t *testing.T) {
	d := NewForTest(t, DaemonConfig{})
	dir := t.TempDir()
	writeConfig(t, dir, "") // no scripts declared at all

	ctx := context.Background()
	scriptCfg := &config.ScriptConfig{Run: stayAliveCmd()}
	require.NoError(t, d.StartScriptExplicit(ctx, "manual", scriptCfg, dir, nil))

	plan, err := d.ReconcileProjectConfig(ctx, dir)
	require.NoError(t, err)
	require.True(t, plan.IsEmpty(), "an undeclared manually started script must produce no plan, got %+v", plan)

	entry, ok := d.scriptRegistry.Get("manual", dir)
	require.True(t, ok, "manually started script must still be registered after reconcile")
	require.NotEqual(t, script.StateStopped, entry.State(), "manually started script must not be stopped by reconcile")
}
