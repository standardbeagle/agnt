package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestShellQuote(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple word",
			input:    "hello",
			expected: "hello",
		},
		{
			name:     "word with space",
			input:    "hello world",
			expected: "'hello world'",
		},
		{
			name:     "word with single quote",
			input:    "it's",
			expected: "'it'\\''s'",
		},
		{
			name:     "word with double quote",
			input:    `say "hello"`,
			expected: `'say "hello"'`,
		},
		{
			name:     "word with dollar sign",
			input:    "$HOME",
			expected: "'$HOME'",
		},
		{
			name:     "word with backtick",
			input:    "`whoami`",
			expected: "'`whoami`'",
		},
		{
			name:     "word with semicolon",
			input:    "cmd;rm -rf",
			expected: "'cmd;rm -rf'",
		},
		{
			name:     "word with pipe",
			input:    "cmd|cat",
			expected: "'cmd|cat'",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "flag with equals",
			input:    "--flag=value",
			expected: "--flag=value",
		},
		{
			name:     "flag with space in value",
			input:    "--message=hello world",
			expected: "'--message=hello world'",
		},
		{
			name:     "complex path",
			input:    "/path/to/file",
			expected: "/path/to/file",
		},
		{
			name:     "path with space",
			input:    "/path/to/my file",
			expected: "'/path/to/my file'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shellQuote(tt.input)
			if result != tt.expected {
				t.Errorf("shellQuote(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// findAgntBinary finds the agnt binary for testing.
func findAgntBinary(t *testing.T) string {
	wd, _ := os.Getwd()
	// Navigate from cmd/agnt to project root
	projectRoot := filepath.Join(wd, "..", "..")
	agntPath := filepath.Join(projectRoot, "agnt")

	if _, err := os.Stat(agntPath); os.IsNotExist(err) {
		t.Skipf("agnt binary not found at %s - run 'make build' first", agntPath)
	}

	return agntPath
}

// TestRunCommand_SetsProjectPathEnv verifies that `agnt run` sets AGNT_PROJECT_PATH
// environment variable for the child process.
//
// Note: This test requires a real TTY and will skip in automated test environments.
// To test manually: go test -v -run TestRunCommand_SetsProjectPathEnv
func TestRunCommand_SetsProjectPathEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agnt run requires PTY support, skipping on Windows")
	}

	agntPath := findAgntBinary(t)

	// Create a test directory to run from
	testDir := t.TempDir()

	// Create a simple script that prints AGNT_PROJECT_PATH
	scriptPath := filepath.Join(testDir, "print_env.sh")
	script := `#!/bin/sh
echo "AGNT_PROJECT_PATH=$AGNT_PROJECT_PATH"
exit 0
`
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("Failed to create test script: %v", err)
	}

	// Run agnt run with the test script from the test directory
	cmd := exec.Command(agntPath, "run", "--no-overlay", scriptPath)
	cmd.Dir = testDir

	// Capture output with timeout
	done := make(chan struct{})
	var output []byte
	var cmdErr error

	go func() {
		output, cmdErr = cmd.CombinedOutput()
		close(done)
	}()

	select {
	case <-done:
		// Command completed
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatal("Command timed out")
	}

	outputStr := string(output)

	// Skip if we couldn't get a TTY (common in CI environments)
	if strings.Contains(outputStr, "failed to set raw mode") ||
		strings.Contains(outputStr, "inappropriate ioctl") {
		t.Skip("Test requires TTY - skipping in non-interactive environment")
	}

	// Check if AGNT_PROJECT_PATH was set to the test directory
	expectedEnv := "AGNT_PROJECT_PATH=" + testDir
	if !strings.Contains(outputStr, expectedEnv) {
		t.Errorf("Expected output to contain %q, got:\n%s\nerr: %v", expectedEnv, outputStr, cmdErr)
	}
}

// TestRunCommand_ProjectPathIsAbsolute verifies that AGNT_PROJECT_PATH is always absolute.
//
// Note: This test requires a real TTY and will skip in automated test environments.
// To test manually: go test -v -run TestRunCommand_ProjectPathIsAbsolute
func TestRunCommand_ProjectPathIsAbsolute(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agnt run requires PTY support, skipping on Windows")
	}

	agntPath := findAgntBinary(t)

	// Create a test directory
	testDir := t.TempDir()

	// Create a script that prints if path is absolute
	scriptPath := filepath.Join(testDir, "check_absolute.sh")
	script := `#!/bin/sh
case "$AGNT_PROJECT_PATH" in
  /*) echo "PATH_IS_ABSOLUTE=true" ;;
  *)  echo "PATH_IS_ABSOLUTE=false" ;;
esac
exit 0
`
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("Failed to create test script: %v", err)
	}

	cmd := exec.Command(agntPath, "run", "--no-overlay", scriptPath)
	cmd.Dir = testDir

	done := make(chan struct{})
	var output []byte

	go func() {
		output, _ = cmd.CombinedOutput()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatal("Command timed out")
	}

	outputStr := string(output)

	// Skip if we couldn't get a TTY (common in CI environments)
	if strings.Contains(outputStr, "failed to set raw mode") ||
		strings.Contains(outputStr, "inappropriate ioctl") {
		t.Skip("Test requires TTY - skipping in non-interactive environment")
	}

	if !strings.Contains(outputStr, "PATH_IS_ABSOLUTE=true") {
		t.Errorf("Expected AGNT_PROJECT_PATH to be absolute, output:\n%s", outputStr)
	}
}
