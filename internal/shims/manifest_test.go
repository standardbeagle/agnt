package shims

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManifestRoundTrip(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	require.NoError(t, RecordInstall("/proj/a", "/proj/a/.agnt/bin", "sess-1"))
	require.NoError(t, RecordInstall("/proj/a", "/proj/a/.agnt/bin", "sess-2"))
	require.NoError(t, RecordInstall("/proj/b", "/proj/b/.agnt/bin", ""))

	m := LoadManifest()
	require.Len(t, m.Projects, 2)
	a := m.Projects["/proj/a"]
	require.NotNil(t, a)
	assert.Equal(t, []string{"sess-1", "sess-2"}, a.Sessions)
	assert.Equal(t, CommandNames(), a.Commands)
	assert.Empty(t, m.Projects["/proj/b"].Sessions)

	// Releasing one session keeps the entry.
	stillUsed, err := ReleaseSession("/proj/a", "sess-1")
	require.NoError(t, err)
	assert.True(t, stillUsed)
	m = LoadManifest()
	require.Len(t, m.Projects, 2)

	// Releasing the last session detaches it but KEEPS the entry — only
	// DropProject (after the bin dir is gone) removes it, so crash
	// recovery never loses track of an installed dir.
	stillUsed, err = ReleaseSession("/proj/a", "sess-2")
	require.NoError(t, err)
	assert.False(t, stillUsed)
	m = LoadManifest()
	require.Len(t, m.Projects, 2)
	assert.Empty(t, m.Projects["/proj/a"].Sessions)

	require.NoError(t, DropProject("/proj/a"))
	require.NoError(t, DropProject("/proj/b"))
	m = LoadManifest()
	assert.Empty(t, m.Projects)
}

func TestLoadManifestMissingFile(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m := LoadManifest()
	require.NotNil(t, m)
	assert.Empty(t, m.Projects)
}

func TestLoadManifestCorruptFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	require.NoError(t, saveManifestTo(manifestPath(), &Manifest{Projects: map[string]*ManifestEntry{}}))
	// Corrupt it.
	require.NoError(t, os.WriteFile(manifestPath(), []byte("{not json"), 0o600))
	m := LoadManifest()
	require.NotNil(t, m)
	assert.Empty(t, m.Projects)
}
