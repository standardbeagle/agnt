package main

import (
	"os"
	"path/filepath"
	"testing"

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
