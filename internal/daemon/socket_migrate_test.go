package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A stale legacy socket file (nothing listening behind it) is removed, so the
// next startup does not keep re-inspecting it.
func TestMigrateLegacySocket_RemovesStaleFile(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "legacy.sock")
	require.NoError(t, os.WriteFile(legacy, nil, 0o600))

	d := NewForTest(t, DaemonConfig{SocketPath: filepath.Join(dir, "new.sock")})
	d.migrateLegacySocketFrom(legacy)

	_, err := os.Stat(legacy)
	assert.True(t, os.IsNotExist(err), "stale legacy socket was not removed")
}

// Nothing at the legacy path means nothing to do — and no error.
func TestMigrateLegacySocket_NoLegacySocketIsNoop(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "absent.sock")

	d := NewForTest(t, DaemonConfig{SocketPath: filepath.Join(dir, "new.sock")})
	d.migrateLegacySocketFrom(legacy) // must not panic or create anything

	_, err := os.Stat(legacy)
	assert.True(t, os.IsNotExist(err))
}

// The migration must not run at all when the daemon was pointed at an explicit
// socket path: that caller owns the socket layout, legacy path included.
func TestMigrateLegacySocket_SkippedForCustomSocketPath(t *testing.T) {
	dir := t.TempDir()
	d := NewForTest(t, DaemonConfig{SocketPath: filepath.Join(dir, "custom.sock")})

	// Guard fires before any filesystem access, so a non-existent legacy path is
	// irrelevant: the assertion is that nothing panics and nothing is created.
	d.migrateLegacySocket()

	_, err := os.Stat(LegacySocketPath())
	if err == nil {
		t.Skipf("a real legacy socket exists at %s; cannot assert absence", LegacySocketPath())
	}
	assert.True(t, os.IsNotExist(err))
}

// A live daemon at the legacy path is shut down, not left orphaned holding its
// processes and ports where the new client can never find it.
func TestMigrateLegacySocket_StopsLiveLegacyDaemon(t *testing.T) {
	dir := shortTempDir(t)
	legacy := filepath.Join(dir, "legacy.sock")

	// NewForTest boots the hub, so this daemon is already listening on `legacy`.
	_ = NewForTest(t, DaemonConfig{SocketPath: legacy})
	require.True(t, IsRunning(legacy), "legacy daemon should be listening")

	newer := NewForTest(t, DaemonConfig{SocketPath: filepath.Join(dir, "new.sock")})
	newer.migrateLegacySocketFrom(legacy)

	assert.False(t, IsRunning(legacy), "legacy daemon still listening after migration")
	_, err := os.Stat(legacy)
	assert.True(t, os.IsNotExist(err), "legacy socket file left behind")
}

// LegacySocketPath must be the pre-0.13.32 form: the socket directly in /tmp,
// not inside the per-uid directory the hardened bind now requires.
func TestLegacySocketPath_IsTheOldFlatForm(t *testing.T) {
	legacy := LegacySocketPath()
	assert.Equal(t, "/tmp", filepath.Dir(legacy), "legacy socket lived directly in /tmp")
	assert.NotEqual(t, DefaultSocketPath(), legacy, "legacy and current paths must differ")
	assert.Equal(t, filepath.Join("/tmp", SocketName+"-"+strconv.Itoa(os.Getuid())+".sock"), legacy)
}
