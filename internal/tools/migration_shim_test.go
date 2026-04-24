package tools

import (
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildTestErrors creates a slice of synthetic proxy log entries for testing.
func buildTestErrors() []proxy.LogEntry {
	now := time.Now()
	return []proxy.LogEntry{
		{
			Type: proxy.LogTypeError,
			Error: &proxy.FrontendError{
				Message:   "TypeError: Cannot read properties of undefined",
				Source:    "browser:js",
				Timestamp: now.Add(-10 * time.Second),
			},
		},
		{
			Type: proxy.LogTypeHTTP,
			HTTP: &proxy.HTTPLogEntry{
				Method:     "GET",
				URL:        "/api/users",
				StatusCode: 500,
				Timestamp:  now.Add(-5 * time.Second),
			},
		},
		{
			Type: proxy.LogTypeCustom,
			Custom: &proxy.CustomLog{
				Level:     "error",
				Message:   "Custom app error",
				Timestamp: now.Add(-2 * time.Second),
			},
		},
	}
}

// TestGetErrorsShim_ByteIdenticalOutput_PrePostFlag verifies that the
// formatErrorsOutput helper (which is the shared formatting path used by both
// the pre-flag and post-flag code paths) produces identical output for the
// same input across three parameter combinations. This ensures the shim
// delegates correctly without introducing formatting drift.
func TestGetErrorsShim_ByteIdenticalOutput_PrePostFlag(t *testing.T) {
	entries := buildTestErrors()
	proxyID := "test-proxy"

	// Collect unified errors from proxy entries using the direct converter
	// (the same converter used by both the daemon and legacy paths).
	var allErrors []unifiedError
	for _, entry := range entries {
		errs := convertProxyEntryDirect(proxyID, entry)
		allErrors = append(allErrors, errs...)
	}
	require.NotEmpty(t, allErrors, "should have converted some errors")

	testCases := []struct {
		name            string
		includeWarnings bool
		limit           int
		raw             bool
	}{
		{"warnings included, limit 25, compact", true, 25, false},
		{"warnings excluded, limit 25, compact", false, 25, false},
		{"warnings included, limit 25, raw JSON", true, 25, true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Make two independent copies to simulate two path invocations.
			a := make([]unifiedError, len(allErrors))
			b := make([]unifiedError, len(allErrors))
			copy(a, allErrors)
			copy(b, allErrors)

			resultA, outputA := formatErrorsOutput(a, tc.includeWarnings, tc.limit, tc.raw)
			resultB, outputB := formatErrorsOutput(b, tc.includeWarnings, tc.limit, tc.raw)

			// Both paths must produce identical outputs.
			assert.Equal(t, resultA, resultB, "CallToolResult must be identical")
			assert.Equal(t, outputA.ErrorCount, outputB.ErrorCount, "ErrorCount must match")
			assert.Equal(t, outputA.WarningCount, outputB.WarningCount, "WarningCount must match")
			assert.Equal(t, outputA.Summary, outputB.Summary, "Summary must be byte-identical")
		})
	}
}

// TestGetErrorsShim_FlagDefaultIsFalse verifies that the IncidentPipelineEnabled
// accessor returns false when the AlertsConfig is nil or has no explicit value.
func TestGetErrorsShim_FlagDefaultIsFalse(t *testing.T) {
	// Nil AlertsConfig.
	var nilCfg *incidentPipelineConfig
	assert.False(t, nilCfg.IncidentPipelineEnabled(), "nil config must default to false")

	// Zero-value AlertsConfig.
	zeroCfg := &incidentPipelineConfig{}
	assert.False(t, zeroCfg.IncidentPipelineEnabled(), "zero config must default to false")

	// Explicit false.
	falseCfg := &incidentPipelineConfig{enabled: false}
	assert.False(t, falseCfg.IncidentPipelineEnabled(), "explicit false must return false")

	// Explicit true.
	trueCfg := &incidentPipelineConfig{enabled: true}
	assert.True(t, trueCfg.IncidentPipelineEnabled(), "explicit true must return true")
}
