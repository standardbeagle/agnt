package browser

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDefaultDiscoverer(t *testing.T) {
	d := DefaultDiscoverer()
	if d == nil {
		t.Fatal("DefaultDiscoverer returned nil")
	}
}

func TestFindInPath(t *testing.T) {
	// Test finding a command that should exist
	path := findInPath("sh")
	if path == "" {
		t.Skip("sh not found in PATH")
	}

	// Test finding a command that shouldn't exist
	path = findInPath("nonexistent-command-12345")
	if path != "" {
		t.Errorf("findInPath should return empty for nonexistent command, got %q", path)
	}
}

func TestFindFirst(t *testing.T) {
	// Create a temp file to test with
	tmpFile, err := os.CreateTemp("", "chrome-test-*")
	if err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	// Test finding the first existing path
	paths := []string{"/nonexistent/path1", tmpFile.Name(), "/nonexistent/path2"}
	found := findFirst(paths)
	if found != tmpFile.Name() {
		t.Errorf("findFirst() = %q, want %q", found, tmpFile.Name())
	}

	// Test with no existing paths
	paths = []string{"/nonexistent/path1", "/nonexistent/path2"}
	found = findFirst(paths)
	if found != "" {
		t.Errorf("findFirst() should return empty for nonexistent paths, got %q", found)
	}
}

func TestFileExists(t *testing.T) {
	// Create a temp file
	tmpFile, err := os.CreateTemp("", "browser-test-*")
	if err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	if !fileExists(tmpFile.Name()) {
		t.Error("fileExists() should return true for existing file")
	}

	if fileExists("/nonexistent/file/path") {
		t.Error("fileExists() should return false for nonexistent file")
	}

	// Test with a directory (should return false)
	tmpDir, err := os.MkdirTemp("", "browser-test-dir-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	if fileExists(tmpDir) {
		t.Error("fileExists() should return false for directory")
	}
}

func TestDiscovererIntegration(t *testing.T) {
	discoverer := DefaultDiscoverer()
	chromePath := discoverer.Find()

	if chromePath == "" {
		t.Log("Chrome not found on this system (this is expected in CI)")
		return
	}

	t.Logf("Found Chrome at: %s", chromePath)

	// Verify it's actually executable
	_, err := exec.LookPath(chromePath)
	if err != nil {
		t.Errorf("Found Chrome path %q is not executable: %v", chromePath, err)
	}

	// Read the version through the product's probe rather than a private
	// `chrome --version` here: on Windows that spelling launches a browser
	// that never exits, which hung this test for the whole suite timeout.
	if info := CheckVersionAt(chromePath); info.FullVersion == "" {
		t.Logf("Warning: could not get Chrome version")
	} else {
		t.Logf("Chrome version: %s", info.FullVersion)
	}
}

// playwrightTree lays out fake Playwright browser installs under a temp cache
// root and returns the root. Each entry is "<revision dir>/<platform dir>".
func playwrightTree(t *testing.T, binary string, installs ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, in := range installs {
		dir := filepath.Join(root, filepath.FromSlash(in))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, binary), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestNewestPlaywrightChromium_ComparesRevisionsNumerically: "chromium-999"
// sorts after "chromium-1208" as a string, so the string sort this replaced
// picked the oldest install once revisions crossed a digit boundary.
func TestNewestPlaywrightChromium_ComparesRevisionsNumerically(t *testing.T) {
	root := playwrightTree(t, "chrome.exe",
		"chromium-999/chrome-win",
		"chromium-1208/chrome-win64",
		"chromium-1179/chrome-win",
	)
	got := newestPlaywrightChromium(filepath.Join(root, "chromium-*", "chrome-win*", "chrome.exe"))
	want := filepath.Join(root, "chromium-1208", "chrome-win64", "chrome.exe")
	if got != want {
		t.Errorf("newestPlaywrightChromium = %q, want %q", got, want)
	}
}

// TestNewestPlaywrightChromium_MatchesRenamedPlatformDirs: Playwright renamed
// its platform directories (chrome-win → chrome-win64, chrome-linux →
// chrome-linux64, chrome-mac → chrome-mac-arm64). A pattern that names one
// spelling finds nothing after an upgrade; the wildcard finds each.
func TestNewestPlaywrightChromium_MatchesRenamedPlatformDirs(t *testing.T) {
	for _, tc := range []struct{ platformGlob, dir string }{
		{"chrome-win*", "chrome-win"},
		{"chrome-win*", "chrome-win64"},
		{"chrome-linux*", "chrome-linux"},
		{"chrome-linux*", "chrome-linux64"},
		{"chrome-mac*", "chrome-mac"},
		{"chrome-mac*", "chrome-mac-arm64"},
	} {
		root := playwrightTree(t, "chrome", "chromium-1208/"+tc.dir)
		got := newestPlaywrightChromium(filepath.Join(root, "chromium-*", tc.platformGlob, "chrome"))
		want := filepath.Join(root, "chromium-1208", tc.dir, "chrome")
		if got != want {
			t.Errorf("%s under %s: got %q, want %q", tc.dir, tc.platformGlob, got, want)
		}
	}
}

// TestNewestPlaywrightChromium_IgnoresOtherBrowsersAndMisses: the headless
// shell lives beside chromium-N and must not be picked; no install is "".
func TestNewestPlaywrightChromium_IgnoresOtherBrowsersAndMisses(t *testing.T) {
	root := playwrightTree(t, "chrome.exe",
		"chromium_headless_shell-1300/chrome-win64",
		"chromium-1100/chrome-win64",
	)
	got := newestPlaywrightChromium(filepath.Join(root, "chromium-*", "chrome-win*", "chrome.exe"))
	want := filepath.Join(root, "chromium-1100", "chrome-win64", "chrome.exe")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	empty := t.TempDir()
	if got := newestPlaywrightChromium(filepath.Join(empty, "chromium-*", "chrome-win*", "chrome.exe")); got != "" {
		t.Errorf("no installs: got %q, want empty", got)
	}
	// Several patterns: the best across all of them wins.
	multi := newestPlaywrightChromium(
		filepath.Join(empty, "chromium-*", "chrome-win*", "chrome.exe"),
		filepath.Join(root, "chromium-*", "chrome-win*", "chrome.exe"),
	)
	if multi != want {
		t.Errorf("multi-pattern: got %q, want %q", multi, want)
	}
}
