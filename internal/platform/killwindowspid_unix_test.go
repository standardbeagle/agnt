//go:build !windows

package platform

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestKillWindowsPID_InvalidPID asserts that non-positive PIDs are rejected
// at the front of the function with a clear error, never reaching exec.
// Runs everywhere because no exec is performed.
func TestKillWindowsPID_InvalidPID(t *testing.T) {
	for _, pid := range []int{0, -1, -1234} {
		err := KillWindowsPID(pid)
		assert.Error(t, err, "pid %d must be rejected", pid)
		assert.Contains(t, err.Error(), "invalid pid", "error must call out invalid pid (got: %v)", err)
	}
}

// TestKillWindowsPID_NotWSL asserts that non-WSL hosts get a clear refusal
// rather than silently calling an absent taskkill.exe. The error must mention
// WSL so callers can route it to the right diagnostic channel.
// Skipped on WSL hosts where this branch is unreachable.
func TestKillWindowsPID_NotWSL(t *testing.T) {
	if IsWSL() {
		t.Skip("non-WSL coverage only — WSL host reaches the taskkill.exe path")
	}
	err := KillWindowsPID(1234)
	if err == nil {
		t.Fatalf("non-WSL host must return error, got nil")
	}
	if !strings.Contains(err.Error(), "WSL") {
		t.Fatalf("error must mention WSL for routing, got: %v", err)
	}
}

// TestKillWindowsPID_NonExistentPID exercises the WSL-only path against a PID
// that is guaranteed not to exist (max int32 - 1). taskkill.exe returns exit 1
// with "ERROR: The process ... not found"; we assert the error surfaces
// rather than silently swallowing it. Skipped on non-WSL hosts.
func TestKillWindowsPID_NonExistentPID(t *testing.T) {
	if !IsWSL() {
		t.Skip("WSL-only smoke test")
	}
	if _, err := exec.LookPath("taskkill.exe"); err != nil {
		t.Skip("taskkill.exe not on PATH (WSL interop disabled?)")
	}
	// 2147483646 (max int32 - 1) is well above any realistic Windows PID
	// and is guaranteed to be absent from the tasklist.
	err := KillWindowsPID(2147483646)
	assert.Error(t, err, "non-existent PID must surface taskkill failure, not nil")
}
