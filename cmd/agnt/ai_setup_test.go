package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAiSetupSystemPrompt asserts the `ai`-verb first-run gate: an unconfigured
// project opens in setup mode exactly once per re-nudge TTL, and a configured
// project never does.
func TestAiSetupSystemPrompt(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	// No config, no marker → setup prompt returned, nudge recorded.
	unconfigured := t.TempDir()
	got := aiSetupSystemPrompt(unconfigured)
	require.NotEmpty(t, got, "unconfigured project must open in setup mode")
	assert.Contains(t, got, "first-run setup")

	// Second call within the TTL is suppressed by the recorded marker.
	assert.Empty(t, aiSetupSystemPrompt(unconfigured),
		"setup must not re-fire within the re-nudge TTL")

	// A project that already has a config never enters setup.
	configured := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(configured, ".agnt.kdl"), []byte("project {}\n"), 0o644))
	assert.Empty(t, aiSetupSystemPrompt(configured),
		"configured project must never enter setup")
}
