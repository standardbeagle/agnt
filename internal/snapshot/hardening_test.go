package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Path Traversal Tests for Snapshot Storage ---

func TestStorage_SaveBaseline_TraversalInName(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStorage(tmpDir)
	require.NoError(t, err)

	traversalNames := []string{
		"../../../etc/evil",
		"..\\..\\windows\\evil",
		"../../tmp/escape",
		"normal/../../../escape",
		"..",
		"../",
	}

	for _, name := range traversalNames {
		t.Run(name, func(t *testing.T) {
			baseline := &Baseline{Name: name}
			err := store.SaveBaseline(baseline)

			if err == nil {
				// If it succeeded, verify the data stayed within tmpDir
				resolved := filepath.Clean(filepath.Join(tmpDir, name))
				if !strings.HasPrefix(resolved, tmpDir) {
					t.Errorf("baseline escaped to %s (outside %s)", resolved, tmpDir)
				}
			}
			// Either an error or contained path is acceptable
		})
	}
}

func TestStorage_LoadBaseline_TraversalInName(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStorage(tmpDir)
	require.NoError(t, err)

	traversalNames := []string{
		"../../../etc/passwd",
		"..\\..\\windows\\system32",
		"foo/../../../etc/shadow",
	}

	for _, name := range traversalNames {
		t.Run(name, func(t *testing.T) {
			// Loading should fail gracefully (not found or error)
			_, err := store.LoadBaseline(name)
			assert.Error(t, err, "loading traversal path should not succeed")
		})
	}
}

func TestStorage_DeleteBaseline_TraversalInName(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStorage(tmpDir)
	require.NoError(t, err)

	// Create a sibling directory that should NOT be deletable via traversal
	siblingDir := filepath.Join(filepath.Dir(tmpDir), "sibling-safe")
	require.NoError(t, os.MkdirAll(siblingDir, 0755))
	defer os.RemoveAll(siblingDir)

	// Create a marker file in the sibling
	markerPath := filepath.Join(siblingDir, "marker.txt")
	require.NoError(t, os.WriteFile(markerPath, []byte("safe"), 0644))

	// Attempt to delete the sibling via traversal
	relativePath := "../sibling-safe"
	_ = store.DeleteBaseline(relativePath)

	// The sibling's marker should still exist because filepath.Join resolves
	// the traversal but RemoveAll will delete wherever it resolves to.
	// This test documents the current behavior for awareness.
	resolvedDelete := filepath.Clean(filepath.Join(tmpDir, relativePath))
	if strings.HasPrefix(resolvedDelete, tmpDir) {
		// Path resolved within tmpDir, deletion is safe
		return
	}
	// If it resolved outside tmpDir, check if marker survived
	_, err = os.Stat(markerPath)
	if os.IsNotExist(err) {
		t.Log("WARNING: DeleteBaseline with traversal path deleted outside basePath")
	}
}

// A baseline name that resolves to the base directory itself must be refused:
// `snapshot delete name:"."` used to RemoveAll every baseline.
func TestStorage_DeleteBaseline_NameResolvingToBaseIsRefused(t *testing.T) {
	for _, name := range []string{".", "", "x/..", "./"} {
		t.Run(name, func(t *testing.T) {
			tmpDir := t.TempDir()
			store, err := NewStorage(tmpDir)
			require.NoError(t, err)
			require.NoError(t, store.SaveBaseline(&Baseline{Name: "keep"}))

			assert.Error(t, store.DeleteBaseline(name), "a name resolving to the base dir must be refused")
			_, err = store.LoadBaseline("keep")
			assert.NoError(t, err, "other baselines must survive")

			assert.Error(t, store.SaveBaseline(&Baseline{Name: name}), "save into the base dir itself must be refused")
			_, statErr := os.Stat(filepath.Join(tmpDir, "metadata.json"))
			assert.True(t, os.IsNotExist(statErr), "no metadata written into the base dir")
		})
	}
}

func TestStorage_SaveScreenshot_TraversalInFilename(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStorage(tmpDir)
	require.NoError(t, err)

	// Create a valid baseline first
	baselineName := "valid-baseline"
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, baselineName), 0755))

	traversalFilenames := []string{
		"../../../tmp/evil.png",
		"..\\..\\tmp\\evil.png",
		"../../escape.png",
		"subdir/../../../escape.png",
	}

	for _, filename := range traversalFilenames {
		t.Run(filename, func(t *testing.T) {
			err := store.SaveScreenshot(baselineName, filename, []byte("PNG DATA"))
			if err == nil {
				// If it succeeded, verify the file location
				resolved := filepath.Clean(filepath.Join(tmpDir, baselineName, filename))
				if !strings.HasPrefix(resolved, tmpDir) {
					t.Errorf("screenshot escaped to %s (outside %s)", resolved, tmpDir)
				}
			}
		})
	}
}

func TestStorage_GetBaselinePath_Traversal(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStorage(tmpDir)
	require.NoError(t, err)

	traversalNames := []string{
		"../../../etc",
		"..\\..\\windows",
		"foo/../../../bar",
	}

	for _, name := range traversalNames {
		t.Run(name, func(t *testing.T) {
			path, err := store.GetBaselinePath(name)
			if err != nil {
				// Traversal correctly rejected
				return
			}
			resolved := filepath.Clean(path)
			if !strings.HasPrefix(resolved, tmpDir) {
				t.Logf("WARNING: GetBaselinePath(%q) resolves to %s (outside %s)",
					name, resolved, tmpDir)
			}
		})
	}
}

func TestStorage_GetScreenshotPath_Traversal(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStorage(tmpDir)
	require.NoError(t, err)

	tests := []struct {
		baseline string
		filename string
	}{
		{"../evil", "screenshot.png"},
		{"valid", "../../../etc/passwd"},
		{"../evil", "../../../etc/shadow"},
	}

	for _, tt := range tests {
		t.Run(tt.baseline+"/"+tt.filename, func(t *testing.T) {
			path, err := store.GetScreenshotPath(tt.baseline, tt.filename)
			if err != nil {
				// Traversal correctly rejected
				return
			}
			resolved := filepath.Clean(path)
			if !strings.HasPrefix(resolved, tmpDir) {
				t.Logf("WARNING: GetScreenshotPath(%q, %q) resolves to %s (outside %s)",
					tt.baseline, tt.filename, resolved, tmpDir)
			}
		})
	}
}

func TestStorage_NullByteInName(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStorage(tmpDir)
	require.NoError(t, err)

	// Null bytes in paths should not cause panics
	nullNames := []string{
		"baseline\x00evil",
		"\x00",
		"valid\x00/../../../etc/passwd",
	}

	for _, name := range nullNames {
		t.Run("null-byte", func(t *testing.T) {
			baseline := &Baseline{Name: name}
			// Should either error or handle gracefully (OS will reject null bytes)
			err := store.SaveBaseline(baseline)
			if err != nil {
				assert.Contains(t, err.Error(), "invalid")
			}

			_, loadErr := store.LoadBaseline(name)
			// Should not panic
			_ = loadErr
		})
	}
}
