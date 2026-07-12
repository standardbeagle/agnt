package overlay

// Regression test for the protected-only batch filter used by the OnAlert
// forwarding-pause gate in cmd/agnt.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAlertBatch_ProtectedOnly pins the filter used by the OnAlert pause
// gate: protected user content survives, auto-alerts and the suppressed
// throttle note (which belongs to the gated auto-alert stream) do not.
func TestAlertBatch_ProtectedOnly(t *testing.T) {
	protected := &AlertMatch{
		Pattern:      &AlertPattern{ID: "user:panel_message", Severity: AlertSeverityInfo},
		Line:         "user says hi",
		ScriptID:     "proxy:dev",
		Source:       AlertSourceUser,
		RenderedText: "user says hi",
		Protected:    true,
	}
	auto := errMatch(1)

	mixed := &AlertBatch{
		Matches:    []*AlertMatch{auto, protected},
		ScriptID:   "proxy:dev",
		Suppressed: 7,
	}
	got := mixed.ProtectedOnly()
	require.NotNil(t, got)
	require.Len(t, got.Matches, 1)
	assert.Same(t, protected, got.Matches[0])
	assert.Equal(t, "proxy:dev", got.ScriptID)
	assert.Equal(t, 0, got.Suppressed, "throttle note not carried into the protected subset")

	autoOnly := &AlertBatch{Matches: []*AlertMatch{auto}, ScriptID: "svc"}
	assert.Nil(t, autoOnly.ProtectedOnly(), "no protected content: nothing to deliver while paused")
}
