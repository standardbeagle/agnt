package replaytest

import (
	"os"
	"path/filepath"
	"strings"
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

// TestStoreRejectsTraversalNames pins path containment: a caller-supplied name
// must never read or write outside .agnt/replaytests.
func TestStoreRejectsTraversalNames(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	victim := filepath.Join(dir, "victim.json")
	require.NoError(t, os.WriteFile(victim, []byte("original"), 0o644))

	for _, name := range []string{
		"../../victim", `..\..\victim`, "/abs", `C:\abs`, "a/b", `a\b`,
		"..", ".hidden", "", "x.report", strings.Repeat("a", 129),
	} {
		assert.Error(t, st.SaveScenario(&Scenario{Name: name}), "save %q", name)
		_, err := st.LoadScenario(name)
		assert.Error(t, err, "load %q", name)
		assert.Error(t, st.SaveReport(name, []byte("{}")), "report %q", name)
	}

	got, err := os.ReadFile(victim)
	require.NoError(t, err)
	assert.Equal(t, "original", string(got), "a file outside the store was overwritten")
}
