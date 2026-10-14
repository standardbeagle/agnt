package daemon

import (
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// No t.Parallel in this file: the tests start real OS processes.

func newReviewBatchDaemon(t *testing.T) *Daemon {
	t.Helper()
	return NewForTest(t, DaemonConfig{
		SocketPath:            shortSockPath(t),
		MaxClients:            4,
		WriteTimeout:          2 * time.Second,
		StartupMonitorTimeout: 150 * time.Millisecond,
	})
}

// ApplyAlertsConfig runs on every session connect and AUTOSTART RECONCILE. It
// replaced the daemon-wide hold buffer every time and the old one dropped
// what it held.
func TestApplyAlertsConfig_KeepsBufferWhenUnchanged(t *testing.T) {
	d := newReviewBatchDaemon(t)
	dir := normalizePath(shortTempDir(t))
	mk := func(window int) *config.AlertsConfig {
		return &config.AlertsConfig{OutageHold: &config.OutageHoldConfig{WindowMs: window}}
	}

	d.ApplyAlertsConfig(dir, mk(60000))
	first := d.holdBufferForProject(dir)
	require.NotNil(t, first)

	d.ApplyAlertsConfig(dir, mk(60000))
	assert.Same(t, first, d.holdBufferForProject(dir), "an unchanged config must keep the running buffer")

	d.ApplyAlertsConfig(dir, mk(30000))
	assert.NotSame(t, first, d.holdBufferForProject(dir), "a changed config installs a new buffer")
}

// One project's alerts block governed every proxy in the daemon: the last
// project to connect set the hold window, cascade patterns, transport
// thresholds and retention gates for all of them.
func TestApplyAlertsConfig_IsPerProject(t *testing.T) {
	d := newReviewBatchDaemon(t)
	a := normalizePath(shortTempDir(t))
	b := normalizePath(shortTempDir(t))
	off := false

	d.ApplyAlertsConfig(a, &config.AlertsConfig{
		OutageHold: &config.OutageHoldConfig{WindowMs: 1000, JSCascadePatterns: []string{"only-in-a"}},
		Retention:  &config.RetentionConfig{OnBuildSuccess: &off},
	})
	d.ApplyAlertsConfig(b, &config.AlertsConfig{
		OutageHold: &config.OutageHoldConfig{WindowMs: 9000, JSCascadePatterns: []string{"only-in-b"}},
	})

	hbA, hbB, def := d.holdBufferForProject(a), d.holdBufferForProject(b), d.holdBuffer.Load()
	require.NotNil(t, hbA)
	require.NotNil(t, hbB)
	assert.NotSame(t, hbA, hbB)
	assert.NotSame(t, def, hbA, "the default buffer must not take a project's settings")
	assert.True(t, hbA.MatchesJSCascade("x only-in-a"))
	assert.False(t, hbA.MatchesJSCascade("x only-in-b"), "b's patterns must not reach a's proxies")
	assert.Same(t, def, d.holdBufferForProject(normalizePath(shortTempDir(t))),
		"a project without an alerts block uses the default buffer")

	assert.False(t, d.retention(a).ClearOnBuildSuccess())
	assert.True(t, d.retention(b).ClearOnBuildSuccess(), "a's retention gates must not reach b")

	// The last session leaving retires the project's settings.
	d.retireProjectAlertsConfig(a)
	assert.Same(t, d.holdBuffer.Load(), d.holdBufferForProject(a))
	assert.True(t, d.retention(a).ClearOnBuildSuccess())
}
