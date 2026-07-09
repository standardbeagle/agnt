package daemon

import (
	"path/filepath"
	"testing"

	"github.com/standardbeagle/go-cli-server/script"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// go-cli-server v0.3.13 made script.Registry.Register reject a re-register whose
// config differs (v0.3.9 returned the existing entry). Editing a script's `run`
// in .agnt.kdl therefore left the stale entry behind and every subsequent start
// of that script failed with "already registered ... with different config" —
// which is exactly what live reconcile does. registerScriptEntry replaces the
// entry instead.
func TestRegisterScriptEntry_ReplacesOnChangedConfig(t *testing.T) {
	d := NewForTest(t, DaemonConfig{SocketPath: filepath.Join(t.TempDir(), "d.sock")})
	project := t.TempDir()

	first, err := d.registerScriptEntry("dev", project, &script.Config{Run: "npm run dev"})
	require.NoError(t, err)
	require.NotNil(t, first)

	// Sanity: the vendored registry really does reject the changed re-register.
	_, rawErr := d.scriptRegistry.Register("dev", project, &script.Config{Run: "npm run dev -- --port 3001"})
	require.Error(t, rawErr, "vendor semantics changed; this helper may no longer be needed")

	replaced, err := d.registerScriptEntry("dev", project, &script.Config{Run: "npm run dev -- --port 3001"})
	require.NoError(t, err, "a changed config must replace the entry, not fail the start")
	assert.Equal(t, "npm run dev -- --port 3001", replaced.Config.Run)

	got, ok := d.scriptRegistry.Get("dev", project)
	require.True(t, ok)
	assert.Equal(t, "npm run dev -- --port 3001", got.Config.Run, "registry holds the new config")
}

func TestRegisterScriptEntry_IdempotentOnUnchangedConfig(t *testing.T) {
	d := NewForTest(t, DaemonConfig{SocketPath: filepath.Join(t.TempDir(), "d.sock")})
	project := t.TempDir()
	cfg := func() *script.Config {
		return &script.Config{Command: "go", Args: []string{"run", "."}, Env: map[string]string{"A": "1"}}
	}

	first, err := d.registerScriptEntry("api", project, cfg())
	require.NoError(t, err)
	first.SetState(script.StateRunning)

	again, err := d.registerScriptEntry("api", project, cfg())
	require.NoError(t, err)
	assert.Same(t, first, again, "an equal config must reuse the entry, preserving runtime state")
	assert.Equal(t, script.StateRunning, again.State())
}

// Session observers and ownership are what CleanupSessionResources uses to decide
// whether a script still has a live watcher. Losing them on a config edit would
// let the next session disconnect tear down a script another session still owns.
func TestRegisterScriptEntry_CarriesSessionsAndOwnerAcrossReplacement(t *testing.T) {
	d := NewForTest(t, DaemonConfig{SocketPath: filepath.Join(t.TempDir(), "d.sock")})
	project := t.TempDir()

	entry, err := d.registerScriptEntry("web", project, &script.Config{Run: "vite"})
	require.NoError(t, err)
	entry.AddSession("sess-a")
	entry.AddSession("sess-b")
	entry.SetOwner("sess-a")

	replaced, err := d.registerScriptEntry("web", project, &script.Config{Run: "vite --host"})
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"sess-a", "sess-b"}, replaced.ListSessions())
	assert.Equal(t, "sess-a", replaced.Owner())
	assert.Equal(t, 2, replaced.ObserverCount())
}

func TestRegisterScriptEntry_RejectsInvalidKey(t *testing.T) {
	d := NewForTest(t, DaemonConfig{SocketPath: filepath.Join(t.TempDir(), "d.sock")})

	_, err := d.registerScriptEntry("", t.TempDir(), &script.Config{Run: "x"})
	assert.Error(t, err, "empty name is unaddressable in the registry")

	_, err = d.registerScriptEntry("dev", "", &script.Config{Run: "x"})
	assert.Error(t, err, "empty project path is unaddressable in the registry")
}
