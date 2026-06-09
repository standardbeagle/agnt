package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/overlay"
	"github.com/stretchr/testify/require"
)

// TestIngestProcessAlert_ReachesIncidentInbox pins that a process AlertScanner
// match is published to the incident bus (get_incidents), not only the legacy
// alertStore (get_errors) and the browser toast (EventHub.Deliver). Regression
// guard for the report that process errors popped as proxy toasts but never
// reached an agent running the incident pipeline — FromAlertMatch existed and
// was unit-tested but was never wired into a production publish.
func TestIngestProcessAlert_ReachesIncidentInbox(t *testing.T) {
	d := NewForTest(t, DaemonConfig{})
	require.NotNil(t, d.incidentBus, "incident bus must exist")
	d.addIncidentSession("sess-proc")

	m := &overlay.AlertMatch{
		Pattern: &overlay.AlertPattern{
			ID:          "go-panic",
			Severity:    overlay.AlertSeverityError,
			Category:    "go",
			Description: "runtime panic",
		},
		Line: "panic: runtime error: invalid memory address",
	}

	d.ingestProcessAlert(m, time.Now())

	// Bus dispatch is async on a single goroutine; poll the session inbox.
	require.Eventually(t, func() bool {
		entries, _ := d.incidentBus.QuerySession("sess-proc", incident.QueryFilter{
			Severities: []incident.Severity{incident.SeverityError},
		})
		for _, e := range entries {
			if e.Sample != nil &&
				e.Sample.Source == incident.SourceProcessAlert &&
				strings.Contains(e.Sample.Summary, "panic") {
				return true
			}
		}
		return false
	}, 3*time.Second, 20*time.Millisecond,
		"process alert never reached the incident inbox")

	// It must still also land in the legacy alertStore (get_errors), so the
	// fix adds the incident path without removing the existing one.
	require.NotZero(t, d.alertStore.Len(),
		"process alert must also reach the legacy alertStore")
}

// TestIngestProcessAlert_BuildPatternMapsToBuildFail pins that build-system
// patterns route to SourceBuildFail rather than SourceProcessAlert, so the
// remediation routing table hands the agent build-specific guidance.
func TestIngestProcessAlert_BuildPatternMapsToBuildFail(t *testing.T) {
	d := NewForTest(t, DaemonConfig{})
	d.addIncidentSession("sess-build")

	m := &overlay.AlertMatch{
		Pattern: &overlay.AlertPattern{
			ID:          "vite-fail",
			Severity:    overlay.AlertSeverityError,
			Category:    "vite",
			Description: "build failed",
		},
		Line: "[vite] Internal server error: Failed to resolve import",
	}

	d.ingestProcessAlert(m, time.Now())

	require.Eventually(t, func() bool {
		entries, _ := d.incidentBus.QuerySession("sess-build", incident.QueryFilter{})
		for _, e := range entries {
			if e.Sample != nil && e.Sample.Source == incident.SourceBuildFail {
				return true
			}
		}
		return false
	}, 3*time.Second, 20*time.Millisecond,
		"vite build failure should map to SourceBuildFail in the inbox")
}
