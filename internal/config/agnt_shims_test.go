package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseShimsBlock(t *testing.T) {
	t.Parallel()
	cfg, err := ParseAgntConfig(`shims {
    enabled true
    watch-script "serve"
    rules {
        build-restart {
            match "npm run build"
            action "restart-watch"
        }
        out-of-tree {
            match "go build*"
            action "reroute"
            dir "./.agnt-build"
        }
    }
}`)
	require.NoError(t, err)
	require.NotNil(t, cfg.Shims)
	require.NotNil(t, cfg.Shims.Enabled)
	assert.True(t, *cfg.Shims.Enabled)
	assert.Equal(t, "serve", cfg.Shims.WatchScript)
	require.Len(t, cfg.Shims.Rules, 2)
	assert.Equal(t, "npm run build", cfg.Shims.Rules["build-restart"].Match)
	assert.Equal(t, "restart-watch", cfg.Shims.Rules["build-restart"].Action)
	assert.Equal(t, "./.agnt-build", cfg.Shims.Rules["out-of-tree"].Dir)
}

func TestShimsEnabledDefaults(t *testing.T) {
	t.Parallel()
	// No shims block → enabled.
	cfg, err := ParseAgntConfig(`scripts {}`)
	require.NoError(t, err)
	assert.True(t, cfg.ShimsEnabled())

	// Explicit disable.
	cfg, err = ParseAgntConfig(`shims {
    enabled false
}`)
	require.NoError(t, err)
	assert.False(t, cfg.ShimsEnabled())

	// Nil config → enabled (fail-open design).
	var nilCfg *AgntConfig
	assert.True(t, nilCfg.ShimsEnabled())
}
