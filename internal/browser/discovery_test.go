package browser

import (
	"os"
	"os/exec"
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

	// Try to get Chrome version (just to verify it runs)
	cmd := exec.Command(chromePath, "--version")
	output, err := cmd.Output()
	if err != nil {
		t.Logf("Warning: could not get Chrome version: %v", err)
	} else {
		t.Logf("Chrome version: %s", string(output))
	}
}
