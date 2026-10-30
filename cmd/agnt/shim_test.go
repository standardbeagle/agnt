package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/shims"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chdirForTest switches into dir and restores the original cwd on cleanup.
// Without the restore, removing the temp dir would break getwd for every
// later test in the process.
func chdirForTest(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

// TestShimProjectPath covers the cheap client-side gate: outside a managed
// context the shim must resolve to "" (passthrough) without ever touching
// the daemon.
func TestShimProjectPath(t *testing.T) {
	t.Run("env wins", func(t *testing.T) {
		t.Setenv("AGNT_PROJECT_PATH", "/some/project")
		assert.Equal(t, "/some/project", shimProjectPath())
	})

	t.Run("no config falls back empty", func(t *testing.T) {
		t.Setenv("AGNT_PROJECT_PATH", "")
		chdirForTest(t, t.TempDir())
		assert.Equal(t, "", shimProjectPath())
	})

	t.Run("config present uses cwd", func(t *testing.T) {
		t.Setenv("AGNT_PROJECT_PATH", "")
		dir, err := filepath.EvalSymlinks(t.TempDir())
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".agnt.kdl"), []byte("scripts {}\n"), 0o644))
		chdirForTest(t, dir)
		assert.Equal(t, dir, shimProjectPath())
	})

	t.Run("shims disabled falls back empty", func(t *testing.T) {
		t.Setenv("AGNT_PROJECT_PATH", "")
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".agnt.kdl"), []byte("shims {\n    enabled false\n}\n"), 0o644))
		chdirForTest(t, dir)
		assert.Equal(t, "", shimProjectPath())
	})
}

// TestShimWatchClaimsItsSlotBeforeTheSupersededCheck: the daemon records the
// watcher's pid only after starting it, so the manifest the new watcher first
// reads still names the previous, dead watcher. The watcher used to take that
// for "superseded" and exit at birth, leaving a zombie in the daemon and no
// watcher at all. It must claim the slot, keep watching, and still step aside
// when a later watcher claims it.
func TestShimWatchClaimsItsSlotBeforeTheSupersededCheck(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	orig := shimWatchPollInterval
	shimWatchPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { shimWatchPollInterval = orig })

	const staleWatcher = 1<<22 + 7 // above any pid_max default: never alive
	require.NoError(t, shims.SaveManifest(&shims.Manifest{
		Projects: map[string]*shims.ManifestEntry{
			"/proj/demo": {BinDir: filepath.Join(t.TempDir(), "bin"), Commands: []string{"npm"}},
		},
		WatcherPID: staleWatcher,
	}))

	exited := make(chan int, 1)
	go func() { exited <- runShimWatch(filepath.Join(t.TempDir(), "no-daemon.sock")) }()

	require.Eventually(t, func() bool {
		return shims.LoadManifest().WatcherPID == os.Getpid()
	}, 5*time.Second, 5*time.Millisecond, "watcher never claimed the manifest slot")
	select {
	case code := <-exited:
		t.Fatalf("watcher exited (code %d) while it owned the slot and the daemon grace had not run out", code)
	case <-time.After(100 * time.Millisecond): // twenty polls
	}

	require.NoError(t, shims.WithManifest(func(m *shims.Manifest) { m.WatcherPID = staleWatcher }))
	select {
	case code := <-exited:
		assert.Equal(t, 0, code, "a superseded watcher exits cleanly")
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not step aside after another watcher claimed the slot")
	}
	assert.Len(t, shims.LoadManifest().Projects, 1, "a superseded watcher must not tear down shims")
}
