package proxy

import (
	"encoding/base64"
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

// --- screenshot cwd-containment tests ---
//
// saveScreenshot/savePNGBytes funnel through GetAuditDir, which used to read
// os.Getwd(). ps.Path is the working directory the proxy was created for, and
// the daemon's own cwd is unrelated to it, so a capture for one project landed
// in another project's tree. These assertions are POSITIVE: they prove the PNG
// bytes really exist under ps.Path, not merely that nothing showed up in cwd.

// onePixelPNG is a minimal valid PNG payload, used so the assertions can compare
// exact bytes rather than only checking for a non-empty file.
var onePixelPNG = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89,
}

func TestSaveScreenshot_LandsUnderProxyProjectRootNotCwd(t *testing.T) {
	projectRoot := t.TempDir()
	sentinelCwd := t.TempDir()
	t.Chdir(sentinelCwd)

	ps := &ProxyServer{ID: "test-proxy", Path: projectRoot}
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(onePixelPNG)

	filePath, err := ps.saveScreenshot("panel-capture", dataURL)
	require.NoError(t, err)

	// POSITIVE containment: exact bytes, at the proxy's project root.
	require.Equal(t,
		filepath.Join(projectRoot, ".agnt", AuditDirName, "screenshots"),
		filepath.Dir(filePath))
	got, err := os.ReadFile(filePath)
	require.NoError(t, err, "screenshot must exist under the proxy's project root")
	require.NotEmpty(t, got)
	require.Equal(t, onePixelPNG, got)

	_, statErr := os.Stat(filepath.Join(sentinelCwd, ".agnt"))
	require.True(t, os.IsNotExist(statErr), "no .agnt tree may be created relative to the process cwd")
}

func TestSavePNGBytes_LandsUnderProxyProjectRootNotCwd(t *testing.T) {
	projectRoot := t.TempDir()
	sentinelCwd := t.TempDir()
	t.Chdir(sentinelCwd)

	ps := &ProxyServer{ID: "test-proxy", Path: projectRoot}

	filePath, err := ps.savePNGBytes("area-42", onePixelPNG)
	require.NoError(t, err)

	require.Equal(t,
		filepath.Join(projectRoot, ".agnt", AuditDirName, "screenshots"),
		filepath.Dir(filePath))
	got, err := os.ReadFile(filePath)
	require.NoError(t, err, "screenshot must exist under the proxy's project root")
	require.Equal(t, onePixelPNG, got)

	_, statErr := os.Stat(filepath.Join(sentinelCwd, ".agnt"))
	require.True(t, os.IsNotExist(statErr))
}

// TestSavePNGBytes_NoProjectRootFallsBackToTempNotCwd covers a proxy with no
// project path. GetAuditDir fails loud; saveScreenshot/savePNGBytes keep their
// pre-existing temp-dir fallback (the path is returned to the caller, so the
// capture stays locatable) — but the fallback must never be the process cwd.
func TestSavePNGBytes_NoProjectRootFallsBackToTempNotCwd(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("TMPDIR", tempDir)
	sentinelCwd := t.TempDir()
	t.Chdir(sentinelCwd)

	ps := &ProxyServer{ID: "rootless-proxy"}

	filePath, err := ps.savePNGBytes("area-1", onePixelPNG)
	require.NoError(t, err)

	// POSITIVE: the bytes exist, under the temp dir, not under the cwd.
	got, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.Equal(t, onePixelPNG, got)
	rel, err := filepath.Rel(tempDir, filePath)
	require.NoError(t, err)
	require.NotContains(t, rel, "..", "rootless capture must stay inside the temp dir")

	_, statErr := os.Stat(filepath.Join(sentinelCwd, ".agnt"))
	require.True(t, os.IsNotExist(statErr), "a missing project root must never resolve to the process cwd")
}
