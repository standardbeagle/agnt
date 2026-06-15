package replaytest

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreSaveLoadList(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	s := &Scenario{Name: "alpha", Version: 1}
	require.NoError(t, st.SaveScenario(s))
	assert.FileExists(t, filepath.Join(dir, ".agnt", "replaytests", "alpha.json"))

	got, err := st.LoadScenario("alpha")
	require.NoError(t, err)
	assert.Equal(t, "alpha", got.Name)

	names, err := st.List()
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha"}, names)

	_, err = st.LoadScenario("missing")
	assert.Error(t, err)
}
