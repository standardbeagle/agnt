package debug

import (
	"os"
	"testing"
)

func TestEnableDisable(t *testing.T) {
	// Initially should be disabled (unless AGNT_DEBUG is set)
	origEnabled := IsEnabled()

	Enable()
	if !IsEnabled() {
		t.Error("Expected debug to be enabled after Enable()")
	}

	Disable()
	if IsEnabled() {
		t.Error("Expected debug to be disabled after Disable()")
	}

	// Restore original state
	if origEnabled {
		Enable()
	}
}

func TestLogDoesNotPanicWhenDisabled(t *testing.T) {
	Disable()
	// Should not panic
	Log("test", "message %s", "value")
	LogWithTimestamp("test", "message %s", "value")
	Trace("test", "message %s", "value")
}

func TestLogOutputsWhenEnabled(t *testing.T) {
	Enable()
	defer Disable()

	// These should not panic and should output to stderr
	Log("test", "test message")
	LogWithTimestamp("test", "timestamped message")
	Trace("test", "trace message")
	Error("test", "error message")
	Warn("test", "warn message")
	Info("test", "info message")
}

func TestEnvVarEnabled(t *testing.T) {
	// Save and restore original env
	origEnv := os.Getenv("AGNT_DEBUG")
	defer os.Setenv("AGNT_DEBUG", origEnv)

	// The init() function checks AGNT_DEBUG, but we can't re-run init
	// So just test that setting the env and calling Enable works
	os.Setenv("AGNT_DEBUG", "1")

	// Manually trigger enable as init() already ran
	if os.Getenv("AGNT_DEBUG") != "" {
		Enable()
	}

	if !IsEnabled() {
		t.Error("Expected debug to be enabled when AGNT_DEBUG is set")
	}
}

func TestSetLogFile(t *testing.T) {
	// Test setting log file
	err := SetLogFile("test-debug.log")
	if err != nil {
		t.Errorf("SetLogFile failed: %v", err)
	}

	path := GetLogFilePath()
	if path == "" {
		t.Error("Expected log file path to be set")
	}

	// Clean up
	SetLogFile("")
	Close()

	// Remove test log file
	if path != "" {
		os.Remove(path)
	}
}
