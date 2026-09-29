//go:build unix

package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/stretchr/testify/require"
)

// TestReconcile_AppliesAlertsAndMaxSessionsOnEmptyPlan pins that editing
// only alerts/automation.max-sessions in .agnt.kdl — fields that are not
// part of any script/proxy signature — still takes effect via reconcile.
// ReconcileProjectConfig used to call RunAutostart (the only path that
// applied these) only when the script/proxy plan was non-empty, so an
// alerts-only or max-sessions-only edit produced an empty plan and the
// early return skipped RunAutostart entirely: the edit silently never
// applied.
func TestReconcile_AppliesAlertsAndMaxSessionsOnEmptyPlan(t *testing.T) {
	d := NewForTest(t, DaemonConfig{})
	dir := t.TempDir()

	writeConfig(t, dir, `
automation {
    max-sessions 2
}
`)
	ctx := context.Background()
	plan, err := d.ReconcileProjectConfig(ctx, dir)
	require.NoError(t, err)
	require.True(t, plan.IsEmpty(), "no scripts/proxies declared, plan must be empty")
	require.Equal(t, 2, d.sessionm.MaxSessions())

	writeConfig(t, dir, `
automation {
    max-sessions 7
}
`)
	plan, err = d.ReconcileProjectConfig(ctx, dir)
	require.NoError(t, err)
	require.True(t, plan.IsEmpty(), "still no scripts/proxies declared, plan must be empty")
	require.Equal(t, 7, d.sessionm.MaxSessions(), "automation.max-sessions edit must apply even on an empty reconcile plan")
}

// TestReconcile_HotReloadsURLMatchersAndAutoRestartWithoutRestart pins the
// cheap hot-reload path: editing only url-matchers or auto-restart for an
// already-running, otherwise-unchanged script must not appear in
// RestartScripts (those fields are deliberately excluded from
// scriptSignature) and must be applied live to the running process's
// URLTracker / ProcessAutoRestarter registration.
func TestReconcile_HotReloadsURLMatchersAndAutoRestartWithoutRestart(t *testing.T) {
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
	plan, err := d.ReconcileProjectConfig(ctx, dir)
	require.NoError(t, err)
	require.Equal(t, []string{"web"}, plan.StartScripts)

	processID := makeProcessID(dir, "web")
	require.Eventually(t, func() bool {
		_, ok := d.scriptConfigs.Load(processID)
		return ok
	}, 3*time.Second, 20*time.Millisecond)

	writeConfig(t, dir, `
scripts {
    web {
        run "`+stayAliveCmd()+`"
        autostart true
        url-matchers "Listening on {url}"
        auto-restart true
    }
}
`)
	plan, err = d.ReconcileProjectConfig(ctx, dir)
	require.NoError(t, err)
	require.Empty(t, plan.RestartScripts, "url-matchers/auto-restart edits must not trigger a restart")
	require.Empty(t, plan.NeedsRestart, "url-matchers/auto-restart are hot-reloadable, not needs-restart")

	require.True(t, d.autoRestarter.IsRegistered(processID), "auto-restart true must register the live process")

	stored, ok := d.scriptConfigs.Load(processID)
	require.True(t, ok)
	require.Equal(t, []string{"Listening on {url}"}, stored.(*config.ScriptConfig).URLMatchers)
}

// TestReconcile_FlagsPortsChangeAsNeedsRestart pins that editing `ports` on
// an already-running, signature-unchanged script — a field that only feeds
// pre-flight cleanup at start time, so it cannot take effect on a live
// process — is surfaced on plan.NeedsRestart instead of being silently
// dropped.
func TestReconcile_FlagsPortsChangeAsNeedsRestart(t *testing.T) {
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
	plan, err := d.ReconcileProjectConfig(ctx, dir)
	require.NoError(t, err)
	require.Equal(t, []string{"web"}, plan.StartScripts)

	processID := makeProcessID(dir, "web")
	require.Eventually(t, func() bool {
		_, ok := d.scriptConfigs.Load(processID)
		return ok
	}, 3*time.Second, 20*time.Millisecond)

	writeConfig(t, dir, `
scripts {
    web {
        run "`+stayAliveCmd()+`"
        autostart true
        ports 9999
    }
}
`)
	plan, err = d.ReconcileProjectConfig(ctx, dir)
	require.NoError(t, err)
	require.Empty(t, plan.RestartScripts, "ports alone is not part of scriptSignature")
	require.Len(t, plan.NeedsRestart, 1)
	require.Contains(t, plan.NeedsRestart[0], "web")
	require.Contains(t, plan.NeedsRestart[0], "ports")
}
