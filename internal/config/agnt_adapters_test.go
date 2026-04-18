package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseAgntConfig_AIAdapters exercises the `ai.adapters` block that
// feeds per-agent overrides into internal/agentadapter at run time. The
// block must be fully optional (absent adapters inherit default
// behavior) and must support all three override knobs: disabled,
// flag-name, and stdin-delay-ms.
func TestParseAgntConfig_AIAdapters(t *testing.T) {
	input := `ai {
    adapters {
        claude {
            flag-name "--system-prompt"
        }
        aider {
            stdin-delay-ms 1500
        }
        gemini {
            disabled true
        }
    }
}`
	cfg, err := ParseAgntConfig(input)
	require.NoError(t, err)
	require.NotNil(t, cfg.AI)
	require.NotNil(t, cfg.AI.Adapters)

	claude := cfg.AI.Adapters["claude"]
	require.NotNil(t, claude, "claude override should be parsed")
	assert.Equal(t, "--system-prompt", claude.FlagName)
	assert.False(t, claude.Disabled)
	assert.Equal(t, 0, claude.StdinDelayMs)

	aider := cfg.AI.Adapters["aider"]
	require.NotNil(t, aider)
	assert.Equal(t, 1500, aider.StdinDelayMs)

	gemini := cfg.AI.Adapters["gemini"]
	require.NotNil(t, gemini)
	assert.True(t, gemini.Disabled)
}

func TestParseAgntConfig_AIAdaptersAbsent(t *testing.T) {
	input := `ai {
    skill "debugging"
}`
	cfg, err := ParseAgntConfig(input)
	require.NoError(t, err)
	require.NotNil(t, cfg.AI)
	assert.Empty(t, cfg.AI.Adapters, "no adapters block should yield empty map")
}
