package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeAlertsConfig writes a minimal .agnt.kdl with an `alerts` block to dir.
func writeAlertsConfig(t *testing.T, dir, alertsBlock string) {
	t.Helper()
	body := "alerts {\n" + alertsBlock + "\n}\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".agnt.kdl"), []byte(body), 0o644))
}

// TestBuildProjectAlertScanner_HonorsEnabledFlag proves the daemon's
// process-output scanner now reads a project's `alerts { enabled false }`
// instead of ignoring it (the gap: daemon.go previously built one
// hardcoded-default AlertScanner at daemon startup, before any project was
// even known, so no .agnt.kdl could ever reach it).
func TestBuildProjectAlertScanner_HonorsEnabledFlag(t *testing.T) {
	// No t.Parallel(): touches package-level default pattern registration
	// indirectly through NewAlertScanner.
	d := &Daemon{}
	dir := shortTempDir(t)
	writeAlertsConfig(t, dir, "    enabled false")

	scanner := d.buildProjectAlertScanner(dir)
	t.Cleanup(scanner.Stop)

	// A disabled scanner's ProcessLineWithLifetime returns before pattern
	// matching even runs, so a guaranteed-match line must never show up in
	// the ring buffer of recent matches (populated pre-dedup, pre-batch).
	scanner.ProcessLine("panic: this must never be scanned", "proc-1")
	time.Sleep(100 * time.Millisecond)
	assert.Empty(t, scanner.RecentMatches(time.Time{}), "a disabled project scanner must not process lines at all")
}

// TestBuildProjectAlertScanner_HonorsCustomPatternAndDisableList proves
// alerts.patterns / alerts.disable reach the daemon's own scanner (they
// previously reached only the PTY-side scanner in
// cmd/agnt/pty_common.go::setupAlertScanner).
func TestBuildProjectAlertScanner_HonorsCustomPatternAndDisableList(t *testing.T) {
	// Real, fully-wired daemon: the match below fires OnAlert (batch window
	// 1s), which calls through to d.ingestProcessAlert -> d.alertStore /
	// d.hub / d.healthTracker, all of which need to be real.
	d, _ := newBootedDaemon(t)
	dir := shortTempDir(t)
	writeAlertsConfig(t, dir, `    batch-window 1
    disable "generic-error"
    patterns {
        "my-custom" {
            pattern "MY_APP_ERROR:"
            severity "error"
        }
    }`)

	scanner := d.buildProjectAlertScanner(dir)
	t.Cleanup(scanner.Stop)

	scanner.ProcessLine("MY_APP_ERROR: something broke", "proc-custom")

	require.Eventually(t, func() bool {
		return len(scanner.RecentMatches(time.Time{})) > 0
	}, 3*time.Second, 20*time.Millisecond, "custom pattern from .agnt.kdl never matched")

	matches := scanner.RecentMatches(time.Time{})
	require.NotEmpty(t, matches)
	assert.Equal(t, "my-custom", matches[len(matches)-1].Pattern.ID,
		"the project's custom pattern must be the one that matched")
}

// TestApplyAlertsConfig_RebuildsProjectScanner proves an edited `alerts` block
// reaches the per-project scanner. The scanner is built once, on the project's
// first output line; ApplyAlertsConfig runs on session connect and on every
// AUTOSTART RECONCILE, so without retiring the cached scanner there, a
// `.agnt.kdl` edit changed retention/push/hold but left the scanner on the
// config it was built with until the daemon restarted.
func TestApplyAlertsConfig_RebuildsProjectScanner(t *testing.T) {
	d, _ := newBootedDaemon(t)
	dir := shortTempDir(t)
	// No .agnt.kdl yet: defaults, scanner enabled.

	before := d.alertScannerFor(dir)
	assert.Same(t, before, d.alertScannerFor(dir+string(filepath.Separator)),
		"lookup must key on the normalized path, or the retire below misses the cached scanner")

	writeAlertsConfig(t, dir, "    enabled false")
	d.ApplyAlertsConfig(dir, loadAlertsForTest(t, dir))

	after := d.alertScannerFor(dir)
	t.Cleanup(after.Stop)
	require.NotSame(t, before, after, "an applied alerts block must retire the cached scanner")

	after.ProcessLine("panic: must not scan after enabled false", "proc-1")
	before.ProcessLine("panic: retired scanner must not scan either", "proc-1")
	time.Sleep(100 * time.Millisecond)
	assert.Empty(t, after.RecentMatches(time.Time{}), "rebuilt scanner must carry the new enabled=false")
	assert.Empty(t, before.RecentMatches(time.Time{}), "retired scanner must be stopped")

	// Another project's scanner is untouched by this project's apply.
	other := shortTempDir(t)
	otherScanner := d.alertScannerFor(other)
	t.Cleanup(otherScanner.Stop)
	d.ApplyAlertsConfig(dir, loadAlertsForTest(t, dir))
	assert.Same(t, otherScanner, d.alertScannerFor(other), "apply must retire only its own project's scanner")
}

func loadAlertsForTest(t *testing.T, dir string) *config.AlertsConfig {
	t.Helper()
	cfg, err := config.LoadAgntConfig(dir)
	require.NoError(t, err)
	return cfg.Alerts
}

// TestAlertScannerFor_IsolatesPerProject proves two projects with
// conflicting alerts config (one disabled, one enabled) each get their own
// scanner rather than sharing the single daemon-wide default the old
// construction produced.
func TestAlertScannerFor_IsolatesPerProject(t *testing.T) {
	d, _ := newBootedDaemon(t)
	dirA := shortTempDir(t)
	dirB := shortTempDir(t)
	writeAlertsConfig(t, dirA, "    enabled false")
	// dirB gets no .agnt.kdl at all — defaults (enabled).

	scannerA := d.alertScannerFor(dirA)
	scannerB := d.alertScannerFor(dirB)
	t.Cleanup(scannerA.Stop)
	t.Cleanup(scannerB.Stop)

	require.NotSame(t, scannerA, scannerB, "each project must get its own scanner instance")

	// Same instance returned on a second lookup (lazy singleton per project).
	assert.Same(t, scannerA, d.alertScannerFor(dirA))

	scannerB.ProcessLine("panic: proj-b error", "proc-b")
	require.Eventually(t, func() bool {
		return len(scannerB.RecentMatches(time.Time{})) > 0
	}, 3*time.Second, 20*time.Millisecond, "enabled project's scanner never matched")

	scannerA.ProcessLine("panic: proj-a error", "proc-a")
	time.Sleep(100 * time.Millisecond)
	assert.Empty(t, scannerA.RecentMatches(time.Time{}), "disabled project's scanner must not scan despite a guaranteed-match line")
}
