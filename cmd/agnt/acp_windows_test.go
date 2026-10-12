//go:build windows

package main

import (
	"errors"
	"testing"
)

// A stub that printed help and returned nil reported success for a command that
// ran nothing. It must fail, so the exit code is non-zero.
func TestACPCommand_FailsLoudlyOnWindows(t *testing.T) {
	err := acpCmd.RunE(acpCmd, []string{"gemini"})
	if !errors.Is(err, errACPUnsupportedOnWindows) {
		t.Fatalf("agnt acp on Windows returned %v, want errACPUnsupportedOnWindows", err)
	}
}
