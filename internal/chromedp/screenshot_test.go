package chromedp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGetScreenshotDir_WritesUnderSuppliedRootNotProcessCwd is the containment
// assertion for the cwd-resolution defect. It is deliberately POSITIVE: it
// proves a screenshot really was written AND that it landed under the
// caller-supplied project root. An "no artifact appeared in the cwd" check
// alone would pass just as happily if the code under test never ran at all.
//
// The process cwd is pointed at a sentinel directory for the duration, so the
// second half of the assertion has teeth: if GetScreenshotDir went back to
// resolving os.Getwd(), the PNG would appear under the sentinel instead.
func TestGetScreenshotDir_WritesUnderSuppliedRootNotProcessCwd(t *testing.T) {
	projectRoot := t.TempDir()
	sentinelCwd := t.TempDir()
	t.Chdir(sentinelCwd)

	dir, err := GetScreenshotDir(projectRoot)
	require.NoError(t, err)

	// The directory is the caller's root, not the process cwd.
	require.Equal(t, filepath.Join(projectRoot, ".agnt", "audit", ScreenshotDirName), dir)

	// POSITIVE containment: the write happens and the bytes are there.
	written := filepath.Join(dir, "viewport-containment.png")
	payload := []byte("\x89PNG\r\n\x1a\n-not-a-real-png-but-non-empty")
	require.NoError(t, os.WriteFile(written, payload, 0644))

	got, err := os.ReadFile(written)
	require.NoError(t, err, "artifact must exist at the supplied root")
	require.NotEmpty(t, got, "artifact must be non-empty at the supplied root")
	require.Equal(t, payload, got)

	// NEGATIVE half: nothing was created relative to the process cwd.
	_, err = os.Stat(filepath.Join(sentinelCwd, ".agnt"))
	require.True(t, os.IsNotExist(err), "no .agnt tree may be created relative to the process cwd")
}

// TestGetScreenshotDir_EmptyRootFailsLoud pins the Silent Failure Prohibition:
// a caller with no project root gets an error, never a cwd fallback.
func TestGetScreenshotDir_EmptyRootFailsLoud(t *testing.T) {
	sentinelCwd := t.TempDir()
	t.Chdir(sentinelCwd)

	dir, err := GetScreenshotDir("")
	require.Error(t, err)
	require.Empty(t, dir)
	require.Contains(t, err.Error(), "project root is required")

	// The failure must not have quietly created the cwd-relative tree either.
	_, statErr := os.Stat(filepath.Join(sentinelCwd, ".agnt"))
	require.True(t, os.IsNotExist(statErr))
}

// TestGenerateScreenshotPath_ResolvesSessionProjectRoot proves the Capture*
// entry points derive their destination from the session's project path — the
// data the daemon already threads through SessionConfig — rather than from the
// daemon's own working directory, which belongs to whichever project happened
// to start it.
func TestGenerateScreenshotPath_ResolvesSessionProjectRoot(t *testing.T) {
	projectRoot := t.TempDir()
	sentinelCwd := t.TempDir()
	t.Chdir(sentinelCwd)

	session := NewSession(SessionConfig{ID: "containment-session", Path: projectRoot})

	path, err := generateScreenshotPath(session, "viewport", "label", "desktop")
	require.NoError(t, err)

	wantDir := filepath.Join(projectRoot, ".agnt", "audit", ScreenshotDirName)
	require.Equal(t, wantDir, filepath.Dir(path))
	require.True(t, strings.HasPrefix(filepath.Base(path), "viewport-label-desktop-"))
	require.True(t, strings.HasSuffix(path, ".png"))

	// POSITIVE containment: the generated path is writable and holds the bytes.
	require.NoError(t, os.WriteFile(path, []byte("png-bytes"), 0644))
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, []byte("png-bytes"), got)

	_, statErr := os.Stat(filepath.Join(sentinelCwd, ".agnt"))
	require.True(t, statErr != nil && os.IsNotExist(statErr), "nothing may be written relative to the process cwd")
}

// TestGenerateScreenshotPath_NoProjectRootFailsLoud covers a session started
// without a project path: the capture fails with an actionable message naming
// the session instead of silently dumping PNGs into the daemon's cwd.
func TestGenerateScreenshotPath_NoProjectRootFailsLoud(t *testing.T) {
	sentinelCwd := t.TempDir()
	t.Chdir(sentinelCwd)

	session := NewSession(SessionConfig{ID: "rootless-session"})

	path, err := generateScreenshotPath(session, "viewport", "", "")
	require.Error(t, err)
	require.Empty(t, path)
	require.Contains(t, err.Error(), "rootless-session")
	require.Contains(t, err.Error(), "no project path")

	_, statErr := os.Stat(filepath.Join(sentinelCwd, ".agnt"))
	require.True(t, os.IsNotExist(statErr))
}
