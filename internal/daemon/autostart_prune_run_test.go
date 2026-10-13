package daemon

import (
	"context"
	"testing"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Autostart prunes registry entries the config no longer declares. A process
// started by the RUN verb is in the registry and in no config, and autostart
// runs asynchronously after SESSION REGISTER, so an agent's first `run` can be
// registered before the prune. The prune must leave it alone (it was removing
// the entry and its RUN mark, leaving a live process the registry no longer
// tracked) while still removing a genuinely stale entry.
func TestAutostartPrune_KeepsRunStartedEntries(t *testing.T) {
	d, _ := newBootedDaemon(t)
	dir := normalizePath(t.TempDir())

	runCfg := scriptConfigToEntry(&config.ScriptConfig{Command: "sh"})
	runEntry, err := d.registerScriptEntry("dev", dir, runCfg)
	require.NoError(t, err)
	d.runLaunches.Store(runEntry.ProcessID, runLaunch{})

	staleCfg := scriptConfigToEntry(&config.ScriptConfig{Command: "sh"})
	_, err = d.registerScriptEntry("removed-from-config", dir, staleCfg)
	require.NoError(t, err)

	d.registerAndStartScripts(context.Background(), &config.AgntConfig{}, dir, &AutostartResult{}, nil)

	_, kept := d.scriptRegistry.Get("dev", dir)
	assert.True(t, kept, "a RUN-started process keeps its registry entry across autostart")
	_, marked := d.runLaunches.Load(runEntry.ProcessID)
	assert.True(t, marked, "and its RUN mark")

	_, stale := d.scriptRegistry.Get("removed-from-config", dir)
	assert.False(t, stale, "an entry that is neither in config nor RUN-started is still pruned")
}
