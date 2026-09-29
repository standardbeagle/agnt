package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/overlay"
)

// TestSetupAlertScanner_InvalidPatternAndUnknownSeverityNotifyUser proves a
// bad `alerts.patterns` entry no longer fails silently. Before this,
// setupAlertScanner (cmd/agnt/pty_common.go) only called debug.Log for an
// invalid regex, and an unrecognized severity string silently became
// "error" — debug.Log alone never reaches the agent or the user (see
// AGENTS.md "No silent failures"), so both cases must also go through
// notifyUser.
func TestSetupAlertScanner_InvalidPatternAndUnknownSeverityNotifyUser(t *testing.T) {
	dir := t.TempDir()
	kdl := `alerts {
    patterns {
        "bad-regex" {
            pattern "["
            severity "error"
        }
        "bad-severity" {
            pattern "SOMETHING_WENT_WRONG"
            severity "catastrophic"
        }
        "good" {
            pattern "OK_PATTERN"
            severity "warning"
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
	if scanner == nil {
		t.Fatal("expected a scanner (alerts not disabled)")
	}
	t.Cleanup(scanner.Stop)

	if len(notified) != 2 {
		t.Fatalf("want 2 user notifications (bad regex + unknown severity), got %d: %v", len(notified), notified)
	}
	foundRegex, foundSeverity := false, false
	for _, msg := range notified {
		if strings.Contains(msg, "bad-regex") {
			foundRegex = true
		}
		if strings.Contains(msg, "bad-severity") {
			foundSeverity = true
		}
	}
	if !foundRegex {
		t.Errorf("no notification named the invalid-regex pattern; got %v", notified)
	}
	if !foundSeverity {
		t.Errorf("no notification named the unknown-severity pattern; got %v", notified)
	}
}
