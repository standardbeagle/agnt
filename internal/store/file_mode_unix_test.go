//go:build !windows

package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The global scope holds secret entries in plaintext, so a store file must be
// readable by its owner only — including when a world-readable temp file was
// left behind by an interrupted write (WriteFile keeps an existing mode).
func TestStoreFile_IsPrivateToOwner(t *testing.T) {
	base := t.TempDir()
	path := getStorePath(base, ScopeGlobal, "")
	require.NoError(t, ensureStoreDir(base))
	require.NoError(t, os.WriteFile(path+".tmp", []byte("stale"), 0o644))
	require.NoError(t, os.Chmod(path+".tmp", 0o644))

	mgr := NewStoreManager()
	require.NoError(t, mgr.Set(base, ScopeGlobal, "", "API_TOKEN", "s3cr3t-value", map[string]any{MetaSecret: true}))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "store file holding a secret must be 0600")

	fresh := t.TempDir()
	require.NoError(t, ensureStoreDir(fresh))
	dirInfo, err := os.Stat(filepath.Dir(getStorePath(fresh, ScopeGlobal, "")))
	require.NoError(t, err)
	assert.Zero(t, dirInfo.Mode().Perm()&0o077, "a new store directory is private to its owner")
}
