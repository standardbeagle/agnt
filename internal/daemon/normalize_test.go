package daemon

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNormalizePath_Empty(t *testing.T) {
	result := normalizePath("")
	if result != "." {
		t.Errorf("normalizePath(\"\") = %q, want \".\"", result)
	}
}

func TestNormalizePath_Dot(t *testing.T) {
	result := normalizePath(".")
	if result != "." {
		t.Errorf("normalizePath(\".\") = %q, want \".\"", result)
	}
}

func TestNormalizePath_AbsolutePath(t *testing.T) {
	tmpDir := t.TempDir()

	result := normalizePath(tmpDir)

	// On Windows, should be lowercased
	expected := tmpDir
	if runtime.GOOS == "windows" {
		expected = strings.ToLower(tmpDir)
	}

	if result != expected {
		t.Errorf("normalizePath(%q) = %q, want %q", tmpDir, result, expected)
	}
}

func TestNormalizePath_RelativePath(t *testing.T) {
	result := normalizePath("./relative/path")

	// Should be converted to absolute path
	if !filepath.IsAbs(result) {
		t.Errorf("normalizePath(\"./relative/path\") = %q, expected absolute path", result)
	}

	// Should contain the relative portion
	if !strings.HasSuffix(result, filepath.Join("relative", "path")) &&
		!strings.HasSuffix(strings.ToLower(result), strings.ToLower(filepath.Join("relative", "path"))) {
		t.Errorf("normalizePath(\"./relative/path\") = %q, expected to end with 'relative/path'", result)
	}
}

func TestNormalizePath_PathWithSpaces(t *testing.T) {
	tmpDir := t.TempDir()
	pathWithSpaces := filepath.Join(tmpDir, "path with spaces")

	result := normalizePath(pathWithSpaces)

	// Should preserve spaces
	expected := pathWithSpaces
	if runtime.GOOS == "windows" {
		expected = strings.ToLower(pathWithSpaces)
	}

	if result != expected {
		t.Errorf("normalizePath(%q) = %q, want %q", pathWithSpaces, result, expected)
	}
}

func TestNormalizePath_PathWithSpecialChars(t *testing.T) {
	tmpDir := t.TempDir()
	specialPath := filepath.Join(tmpDir, "path-with_special.chars@123")

	result := normalizePath(specialPath)

	// Should preserve special characters
	expected := specialPath
	if runtime.GOOS == "windows" {
		expected = strings.ToLower(specialPath)
	}

	if result != expected {
		t.Errorf("normalizePath(%q) = %q, want %q", specialPath, result, expected)
	}
}

// TestNormalizePath_WindowsCaseInsensitive verifies that on Windows,
// paths are normalized to lowercase for case-insensitive comparison.
func TestNormalizePath_WindowsCaseInsensitive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Case normalization test only runs on Windows")
	}

	// Test uppercase path
	result := normalizePath("C:\\Users\\TestUser\\Project")
	expected := "c:\\users\\testuser\\project"

	if result != expected {
		t.Errorf("normalizePath uppercase = %q, want %q", result, expected)
	}

	// Test mixed case path
	result2 := normalizePath("C:\\USERS\\testUser\\PROJECT")
	if result2 != expected {
		t.Errorf("normalizePath mixed case = %q, want %q", result2, expected)
	}
}

// TestNormalizePath_WindowsUNCPath verifies that UNC paths are handled correctly.
func TestNormalizePath_WindowsUNCPath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("UNC path test only runs on Windows")
	}

	// UNC path
	result := normalizePath("\\\\server\\share\\path")

	// Should be lowercased but preserve UNC format
	if !strings.HasPrefix(result, "\\\\") {
		t.Errorf("normalizePath UNC = %q, expected to start with \\\\", result)
	}

	expected := "\\\\server\\share\\path"
	if result != expected {
		t.Errorf("normalizePath UNC = %q, want %q", result, expected)
	}
}

// TestNormalizePath_Comparison tests that paths normalized the same way can be compared.
func TestNormalizePath_Comparison(t *testing.T) {
	tmpDir := t.TempDir()

	// Normalize the same path twice
	path1 := normalizePath(tmpDir)
	path2 := normalizePath(tmpDir)

	if path1 != path2 {
		t.Errorf("Same path normalized differently: %q vs %q", path1, path2)
	}

	// On Windows, also test different cases
	if runtime.GOOS == "windows" {
		path3 := normalizePath(strings.ToUpper(tmpDir))
		path4 := normalizePath(strings.ToLower(tmpDir))

		if path3 != path4 {
			t.Errorf("Different case paths should normalize to same: %q vs %q", path3, path4)
		}
	}
}
