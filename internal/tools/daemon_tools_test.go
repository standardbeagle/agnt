package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/daemon"
)

func TestGetProjectPath_FromEnvironment(t *testing.T) {
	// Save original env value
	originalEnv := os.Getenv("AGNT_PROJECT_PATH")
	defer func() {
		if originalEnv != "" {
			os.Setenv("AGNT_PROJECT_PATH", originalEnv)
		} else {
			os.Unsetenv("AGNT_PROJECT_PATH")
		}
	}()

	// Test with absolute path
	testPath := "/home/test/project"
	os.Setenv("AGNT_PROJECT_PATH", testPath)

	result := getProjectPath()
	if result != testPath {
		t.Errorf("Expected %q, got %q", testPath, result)
	}
}

func TestGetProjectPath_FromEnvironment_RelativePath(t *testing.T) {
	// Save original env value
	originalEnv := os.Getenv("AGNT_PROJECT_PATH")
	defer func() {
		if originalEnv != "" {
			os.Setenv("AGNT_PROJECT_PATH", originalEnv)
		} else {
			os.Unsetenv("AGNT_PROJECT_PATH")
		}
	}()

	// Test with relative path - should be converted to absolute
	os.Setenv("AGNT_PROJECT_PATH", ".")

	result := getProjectPath()
	cwd, _ := os.Getwd()
	expected, _ := filepath.Abs(".")

	if result != expected {
		t.Errorf("Expected absolute path %q, got %q (cwd: %q)", expected, result, cwd)
	}
}

func TestGetProjectPath_FallbackToCwd(t *testing.T) {
	// Save original env value
	originalEnv := os.Getenv("AGNT_PROJECT_PATH")
	defer func() {
		if originalEnv != "" {
			os.Setenv("AGNT_PROJECT_PATH", originalEnv)
		} else {
			os.Unsetenv("AGNT_PROJECT_PATH")
		}
	}()

	// Clear the environment variable
	os.Unsetenv("AGNT_PROJECT_PATH")

	result := getProjectPath()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get cwd: %v", err)
	}

	if result != cwd {
		t.Errorf("Expected cwd %q, got %q", cwd, result)
	}
}

func TestGetProjectPath_EmptyEnvFallbackToCwd(t *testing.T) {
	// Save original env value
	originalEnv := os.Getenv("AGNT_PROJECT_PATH")
	defer func() {
		if originalEnv != "" {
			os.Setenv("AGNT_PROJECT_PATH", originalEnv)
		} else {
			os.Unsetenv("AGNT_PROJECT_PATH")
		}
	}()

	// Set empty environment variable
	os.Setenv("AGNT_PROJECT_PATH", "")

	result := getProjectPath()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get cwd: %v", err)
	}

	if result != cwd {
		t.Errorf("Expected cwd %q when env is empty, got %q", cwd, result)
	}
}

func TestGetProjectPath_WithTempDir(t *testing.T) {
	// Save original env value
	originalEnv := os.Getenv("AGNT_PROJECT_PATH")
	defer func() {
		if originalEnv != "" {
			os.Setenv("AGNT_PROJECT_PATH", originalEnv)
		} else {
			os.Unsetenv("AGNT_PROJECT_PATH")
		}
	}()

	// Create a temp directory and use it as project path
	tmpDir := t.TempDir()
	os.Setenv("AGNT_PROJECT_PATH", tmpDir)

	result := getProjectPath()
	if result != tmpDir {
		t.Errorf("Expected %q, got %q", tmpDir, result)
	}
}

func TestGetProjectPath_DifferentFromCwd(t *testing.T) {
	// Save original env value and cwd
	originalEnv := os.Getenv("AGNT_PROJECT_PATH")
	originalCwd, _ := os.Getwd()
	defer func() {
		os.Chdir(originalCwd)
		if originalEnv != "" {
			os.Setenv("AGNT_PROJECT_PATH", originalEnv)
		} else {
			os.Unsetenv("AGNT_PROJECT_PATH")
		}
	}()

	// Create two temp directories
	projectDir := t.TempDir()
	workDir := t.TempDir()

	// Set project path to one directory
	os.Setenv("AGNT_PROJECT_PATH", projectDir)

	// Change cwd to different directory
	os.Chdir(workDir)

	// Verify getProjectPath returns env var, not cwd
	result := getProjectPath()
	cwd, _ := os.Getwd()

	// On Windows, both paths will be lowercased
	expectedProject := projectDir
	if isWindows() {
		expectedProject = strings.ToLower(projectDir)
		cwd = strings.ToLower(cwd)
	}

	if result == cwd {
		t.Errorf("Expected project path %q to differ from cwd %q", result, cwd)
	}

	if result != expectedProject {
		t.Errorf("Expected project path %q, got %q", expectedProject, result)
	}
}

func TestGetProjectPath_PathWithSpaces(t *testing.T) {
	// Save original env value
	originalEnv := os.Getenv("AGNT_PROJECT_PATH")
	defer func() {
		if originalEnv != "" {
			os.Setenv("AGNT_PROJECT_PATH", originalEnv)
		} else {
			os.Unsetenv("AGNT_PROJECT_PATH")
		}
	}()

	// Create a temp directory with spaces in the name
	tmpBase := t.TempDir()
	pathWithSpaces := filepath.Join(tmpBase, "path with spaces", "sub dir")
	if err := os.MkdirAll(pathWithSpaces, 0755); err != nil {
		t.Fatalf("Failed to create directory with spaces: %v", err)
	}

	os.Setenv("AGNT_PROJECT_PATH", pathWithSpaces)

	result := getProjectPath()

	// On Windows, result will be lowercased
	expected := pathWithSpaces
	if isWindows() {
		expected = strings.ToLower(pathWithSpaces)
	}

	if result != expected {
		t.Errorf("Expected %q, got %q", expected, result)
	}
}

func TestGetProjectPath_PathWithSpecialChars(t *testing.T) {
	// Save original env value
	originalEnv := os.Getenv("AGNT_PROJECT_PATH")
	defer func() {
		if originalEnv != "" {
			os.Setenv("AGNT_PROJECT_PATH", originalEnv)
		} else {
			os.Unsetenv("AGNT_PROJECT_PATH")
		}
	}()

	// Create a temp directory with special characters
	// Note: Some characters are not valid in Windows paths, so we use a portable set
	tmpBase := t.TempDir()
	specialPath := filepath.Join(tmpBase, "path-with_special.chars", "sub@dir#123")
	if err := os.MkdirAll(specialPath, 0755); err != nil {
		t.Fatalf("Failed to create directory with special chars: %v", err)
	}

	os.Setenv("AGNT_PROJECT_PATH", specialPath)

	result := getProjectPath()

	// On Windows, result will be lowercased
	expected := specialPath
	if isWindows() {
		expected = strings.ToLower(specialPath)
	}

	if result != expected {
		t.Errorf("Expected %q, got %q", expected, result)
	}
}

func TestIsWindows(t *testing.T) {
	result := isWindows()
	expected := runtime.GOOS == "windows"
	if result != expected {
		t.Errorf("isWindows() = %v, expected %v (runtime.GOOS=%s)", result, expected, runtime.GOOS)
	}
}

// TestGetProjectPath_WindowsCaseNormalization verifies that on Windows,
// paths with different cases are normalized to lowercase.
// This test only verifies behavior on Windows.
func TestGetProjectPath_WindowsCaseNormalization(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Case normalization test only runs on Windows")
	}

	// Save original env value
	originalEnv := os.Getenv("AGNT_PROJECT_PATH")
	defer func() {
		if originalEnv != "" {
			os.Setenv("AGNT_PROJECT_PATH", originalEnv)
		} else {
			os.Unsetenv("AGNT_PROJECT_PATH")
		}
	}()

	// Test with uppercase path
	os.Setenv("AGNT_PROJECT_PATH", "C:\\Users\\TestUser\\Project")
	result := getProjectPath()

	// Should be lowercased
	if result != "c:\\users\\testuser\\project" {
		t.Errorf("Expected lowercase path, got %q", result)
	}
}

// TestDaemonTools_SessionCode tests the session code getter and setter.
func TestDaemonTools_SessionCode(t *testing.T) {
	dt := NewDaemonTools(dummyAutoStartConfig(), "1.0.0")

	// Initially should be empty
	if code := dt.SessionCode(); code != "" {
		t.Errorf("Expected empty session code, got %q", code)
	}

	// Set session code
	dt.SetSessionCode("claude-1")
	if code := dt.SessionCode(); code != "claude-1" {
		t.Errorf("Expected 'claude-1', got %q", code)
	}

	// Clear session code
	dt.SetSessionCode("")
	if code := dt.SessionCode(); code != "" {
		t.Errorf("Expected empty session code after clear, got %q", code)
	}
}

// TestDaemonTools_SetNoAutoAttach tests the auto-attach toggle.
func TestDaemonTools_SetNoAutoAttach(t *testing.T) {
	dt := NewDaemonTools(dummyAutoStartConfig(), "1.0.0")

	// By default, auto-attach should be enabled (noAutoAttach = false)
	// We can only test this indirectly since the field is private

	// Test that we can toggle without panic
	dt.SetNoAutoAttach(true)
	dt.SetNoAutoAttach(false)
	dt.SetNoAutoAttach(true)
}

// TestDaemonTools_SessionCode_ConcurrentAccess tests concurrent access to session code.
func TestDaemonTools_SessionCode_ConcurrentAccess(t *testing.T) {
	dt := NewDaemonTools(dummyAutoStartConfig(), "1.0.0")

	// Run concurrent readers and writers
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(n int) {
			for j := 0; j < 100; j++ {
				if j%2 == 0 {
					dt.SetSessionCode("test-session")
				} else {
					dt.SessionCode()
				}
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines to complete
	for i := 0; i < 10; i++ {
		<-done
	}
}

// dummyAutoStartConfig returns a minimal config for testing.
func dummyAutoStartConfig() daemon.AutoStartConfig {
	return daemon.AutoStartConfig{
		SocketPath: "/tmp/test.sock",
	}
}
