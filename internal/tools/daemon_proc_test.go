package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPopulateLastExitFields verifies that a daemon response map
// containing last_exit_* keys populates the ProcOutput correctly. This
// is the wire-level contract for the death record surfaced through
// proc status.
func TestPopulateLastExitFields(t *testing.T) {
	t.Run("full record", func(t *testing.T) {
		resp := map[string]interface{}{
			"last_exit_at":     "2026-04-13T16:30:00Z",
			"last_exit_code":   float64(1), // JSON numbers decode as float64
			"last_exit_reason": "crash",
			"last_uptime":      "12m34s",
			"last_stderr_tail": "[vite] Internal server error",
		}

		var out ProcOutput
		populateLastExitFields(&out, resp)

		assert.Equal(t, "2026-04-13T16:30:00Z", out.LastExitAt)
		require.NotNil(t, out.LastExitCode, "LastExitCode must be populated")
		assert.Equal(t, 1, *out.LastExitCode)
		assert.Equal(t, "crash", out.LastExitReason)
		assert.Equal(t, "12m34s", out.LastUptime)
		assert.Equal(t, "[vite] Internal server error", out.LastStderrTail)
	})

	t.Run("missing last_exit_code leaves pointer nil", func(t *testing.T) {
		resp := map[string]interface{}{
			"last_exit_at": "2026-04-13T16:30:00Z",
		}
		var out ProcOutput
		populateLastExitFields(&out, resp)
		assert.Nil(t, out.LastExitCode, "absent field must leave pointer nil (distinguishes from a real 0)")
		assert.Equal(t, "2026-04-13T16:30:00Z", out.LastExitAt)
	})

	t.Run("zero exit code is still populated when key present", func(t *testing.T) {
		resp := map[string]interface{}{
			"last_exit_code":   float64(0),
			"last_exit_reason": "stopped",
		}
		var out ProcOutput
		populateLastExitFields(&out, resp)
		require.NotNil(t, out.LastExitCode)
		assert.Equal(t, 0, *out.LastExitCode, "zero is distinguishable from absent via pointer")
		assert.Equal(t, "stopped", out.LastExitReason)
	})

	t.Run("empty response is a no-op", func(t *testing.T) {
		var out ProcOutput
		populateLastExitFields(&out, map[string]interface{}{})
		assert.Empty(t, out.LastExitAt)
		assert.Nil(t, out.LastExitCode)
	})
}

// TestPopulateLastExitFieldsEntry verifies the per-entry variant used by
// proc list (operates on ProcEntry instead of ProcOutput).
func TestPopulateLastExitFieldsEntry(t *testing.T) {
	resp := map[string]interface{}{
		"last_exit_at":     "2026-04-13T16:30:00Z",
		"last_exit_code":   float64(42),
		"last_exit_reason": "crash",
		"last_uptime":      "5s",
		"last_stderr_tail": "panic: runtime error",
	}
	var entry ProcEntry
	populateLastExitFieldsEntry(&entry, resp)
	assert.Equal(t, "2026-04-13T16:30:00Z", entry.LastExitAt)
	require.NotNil(t, entry.LastExitCode)
	assert.Equal(t, 42, *entry.LastExitCode)
	assert.Equal(t, "crash", entry.LastExitReason)
	assert.Equal(t, "5s", entry.LastUptime)
	assert.Equal(t, "panic: runtime error", entry.LastStderrTail)
}
