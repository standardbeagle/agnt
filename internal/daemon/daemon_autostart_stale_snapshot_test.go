//go:build unix

package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/go-cli-server/script"
	"github.com/stretchr/testify/require"
)

// TestRunAutostart_DoesNotStampNewConfigOverStillRunningScript pins that
// RunAutostart never overwrites a script's cached config snapshot
// (d.scriptConfigs, read by reconcile_apply.go's signature diff) with an
// edited .agnt.kdl while the actually-running process is still the OLD
// config. RunAutostart is called on every session connect and every
// reconcile, not just the first start; startOneScript's idempotent
// already-running fast path deliberately skips relaunching a script that is
// still Running/Starting. Before this fix, the config snapshot was stored
// unconditionally, ahead of that check, so a second RunAutostart call after
// an edit (with the old process never actually stopped/relaunched) stamped
// the new config as "running" — which then made reconcile's signature diff
// see desired==running and report no change forever, while the stale old
// command kept running underneath.
func TestRunAutostart_DoesNotStampNewConfigOverStillRunningScript(t *testing.T) {
	d := NewForTest(t, DaemonConfig{})
	dir := t.TempDir()
	writeConfig(t, dir, `
scripts {
    web {
        run "`+stayAliveCmd()+`"
        autostart true
    }
}
`)
	ctx := context.Background()
	result := d.RunAutostart(ctx, dir)
	require.Empty(t, result.Errors)
	require.Eventually(t, func() bool {
		entry, ok := d.scriptRegistry.Get("web", dir)
		return ok && entry.State() == script.StateRunning
	}, 3*time.Second, 20*time.Millisecond, "script never reached Running")

	processID := makeProcessID(dir, "web")
	v1, ok := d.scriptConfigs.Load(processID)
	require.True(t, ok)
	sigV1 := scriptSignature(v1.(*config.ScriptConfig))

	// Edit the config while "web" is still running under the old config —
	// nothing stops the running process here, mirroring a session
	// reconnect / periodic reconcile racing an in-flight edit.
	writeConfig(t, dir, `
scripts {
    web {
        run "`+stayAliveCmd()+`"
        autostart true
        env {
            FOO "bar"
        }
    }
}
`)
	result = d.RunAutostart(ctx, dir)
	require.Empty(t, result.Errors)

	v2, ok := d.scriptConfigs.Load(processID)
	require.True(t, ok)
	sigV2 := scriptSignature(v2.(*config.ScriptConfig))
	require.Equal(t, sigV1, sigV2,
		"scriptConfigs snapshot must still reflect the actually-running (old) config, not the unapplied edit")
}
