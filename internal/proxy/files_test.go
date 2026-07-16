package proxy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSaveLargeResult_ExecIDCannotEscapeTempDir(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("TMPDIR", tempDir)

	ps := &ProxyServer{ID: "test-proxy"}
	// Before execID sanitization, the prefixed first component plus these
	// traversal segments resolve outside tempDir. Create that component so the
	// vulnerable write can succeed and the regression test proves containment.
	require.NoError(t, os.Mkdir(filepath.Join(tempDir, "agnt-result-test-proxy-.."), 0755))

	result := string(make([]byte, LargeResultThreshold))
	filePath, err := ps.saveLargeResult("../../../escaped", result)
	require.NoError(t, err)

	rel, err := filepath.Rel(tempDir, filePath)
	require.NoError(t, err)
	require.NotEqual(t, "..", rel)
	require.NotContains(t, rel, ".."+string(filepath.Separator))
	require.Equal(t, filepath.Base(filePath), rel, "large results must remain directly in the temp directory")
}
