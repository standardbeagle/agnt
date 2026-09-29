package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/overlay"
)

// TestSetupAlertScanner_InvalidPatternConfigNotifiesAndDisables proves a bad
// `alerts.patterns` entry in .agnt.kdl never fails silently. Parse-time
// validation rejects the file, and setupAlertScanner must say so through
// notifyUser (debug.Log alone never reaches the user — AGENTS.md "No silent
// failures") and skip the scanner rather than run it on substituted defaults.
func TestSetupAlertScanner_InvalidPatternConfigNotifiesAndDisables(t *testing.T) {
	dir := t.TempDir()
	kdl := `alerts {
    patterns {
        "bad-regex" {
            pattern "["
            severity "error"
        }
    }
}
`
	if err := os.WriteFile(filepath.Join(dir, ".agnt.kdl"), []byte(kdl), 0o644); err != nil {
		t.Fatal(err)
	}

	var notified []string
	restore := setUserNotifier(overlay.NotifierFunc(func(n overlay.Notification) {
		notified = append(notified, n.Text)
	}))
	defer restore()

	scanner := setupAlertScanner(dir, "test-session", nil, nil, func() overlay.ActivityState {
		return overlay.ActivityIdle
	})
	if scanner != nil {
		scanner.Stop()
		t.Fatal("a config that failed to load must not produce a scanner running on defaults")
	}
	if len(notified) != 1 {
		t.Fatalf("want exactly 1 user notification, got %d: %v", len(notified), notified)
	}
	if !strings.Contains(notified[0], "alerts disabled") || !strings.Contains(notified[0], "bad-regex") {
		t.Errorf("notification must say alerts are off and name the offending pattern; got %q", notified[0])
	}
}

// TestAlertScannerConfigFromAlerts_ReportsEveryInvalidPattern covers the
// builder's own guard, which still matters for configs that never passed
// through ParseAgntConfig (tests, programmatic construction): each bad entry
// is reported by ID. An uncompilable regex is dropped; an unknown severity is
// kept at error, so a typo'd severity still alerts rather than going quiet.
func TestAlertScannerConfigFromAlerts_ReportsEveryInvalidPattern(t *testing.T) {
	cfg := &config.AlertsConfig{Patterns: map[string]*config.AlertPatternConfig{
		"bad-regex":    {Pattern: "[", Severity: "error"},
		"bad-severity": {Pattern: "SOMETHING_WENT_WRONG", Severity: "catastrophic"},
		"good":         {Pattern: "OK_PATTERN", Severity: "warning"},
	}}
	reported := map[string]string{}
	out := overlay.AlertScannerConfigFromAlerts(cfg, func(id, reason string) {
		reported[id] = reason
	})
	if len(reported) != 2 || reported["bad-regex"] == "" || reported["bad-severity"] == "" {
		t.Fatalf("want both invalid patterns reported by ID, got %v", reported)
	}
	got := map[string]overlay.AlertSeverity{}
	for _, p := range out.Patterns {
		got[p.ID] = p.Severity
	}
	want := map[string]overlay.AlertSeverity{
		"good":         overlay.AlertSeverityWarning,
		"bad-severity": overlay.AlertSeverityError,
	}
	if len(got) != len(want) || got["good"] != want["good"] || got["bad-severity"] != want["bad-severity"] {
		t.Fatalf("want %v compiled, got %v", want, got)
	}
}
