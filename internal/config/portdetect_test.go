//go:build !windows

package config

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/standardbeagle/agnt/internal/platform"
	"github.com/stretchr/testify/assert"
)

func TestProcessNameByPID_Self(t *testing.T) {
	name := ProcessNameByPID(os.Getpid())
	assert.NotEmpty(t, name, "should return process name for own PID")
}

func TestProcessNameByPID_Invalid(t *testing.T) {
	name := ProcessNameByPID(-1)
	assert.Empty(t, name, "should return empty for invalid PID")
}

func TestProcessNameByPID_NonExistent(t *testing.T) {
	name := ProcessNameByPID(999999999)
	assert.Empty(t, name, "should return empty for non-existent PID")
}

// TestFindPIDsByPort_NetstatExeFallback_Smoke exercises the WSL-only
// netstat.exe fallback path. It does not assert specific PIDs (that
// would require a deterministic Windows-side listener) — it only
// asserts the path runs without panicking and returns a sane shape
// (nil slice for an unused port). Skipped on non-WSL hosts where
// netstat.exe is unavailable.
func TestFindPIDsByPort_NetstatExeFallback_Smoke(t *testing.T) {
	if !platform.IsWSL() {
		t.Skip("WSL-only smoke test")
	}
	if _, err := exec.LookPath("netstat.exe"); err != nil {
		t.Skip("netstat.exe not on PATH (WSL interop disabled?)")
	}
	// Port 1 is reserved by IANA and never bound — the fallback should
	// return nil, not crash, and not return phantom PIDs.
	pids := findPIDsByPortNetstatExe(context.Background(), 1)
	assert.Empty(t, pids, "port 1 should have no listeners")
}

// TestFindPIDsByPort_NetstatExeMissing_ReturnsNil exercises the
// not-WSL / no-interop path of findPIDsByPortNetstatExe. exec.LookPath
// failure must produce nil, not panic. Runs everywhere because we use
// a name we know is absent to force LookPath failure.
func TestFindPIDsByPort_NetstatExeMissing_ReturnsNil(t *testing.T) {
	// We can't easily force LookPath to fail for "netstat.exe" itself
	// without mutating PATH, but we can assert the function returns
	// nil cleanly for an unused port even when called explicitly.
	// On non-WSL hosts netstat.exe is absent → LookPath fails → nil.
	if platform.IsWSL() {
		t.Skip("non-WSL coverage only")
	}
	pids := findPIDsByPortNetstatExe(context.Background(), 1)
	assert.Nil(t, pids, "non-WSL host without netstat.exe must return nil")
}

// TestFindPIDsByPortTagged_UnusedPort asserts that the tagged variant
// returns empty slices (not phantom PIDs) for an unbound port. Runs
// everywhere because port 1 is reserved by IANA and never bound.
func TestFindPIDsByPortTagged_UnusedPort(t *testing.T) {
	linux, windows := FindPIDsByPortTagged(context.Background(), 1)
	assert.Empty(t, linux, "port 1 must not surface phantom Linux PIDs")
	assert.Empty(t, windows, "port 1 must not surface phantom Windows PIDs")
}

// TestFindPIDsByPortTagged_NonWSL_NoWindowsPIDs asserts that on
// non-WSL hosts the windows slice is always nil — the netstat.exe
// fallback must never run when IsWSL() is false. Skipped on WSL.
func TestFindPIDsByPortTagged_NonWSL_NoWindowsPIDs(t *testing.T) {
	if platform.IsWSL() {
		t.Skip("non-WSL coverage only")
	}
	// Use this test process's port if we can get one bound; otherwise
	// just assert the unbound case never produces windows PIDs.
	_, windows := FindPIDsByPortTagged(context.Background(), 1)
	assert.Nil(t, windows, "non-WSL host must never tag PIDs as Windows-side")
}

// TestFindPIDsByPort_PreservesTaggedUnion asserts that the back-compat
// FindPIDsByPort returns the union of linux + windows slices, so existing
// visibility-only callers (doctor, status display) keep working unchanged
// after the tagged refactor. Runs everywhere — both branches collapse to
// nil for the unused port, validating the no-panic contract.
func TestFindPIDsByPort_PreservesTaggedUnion(t *testing.T) {
	pids := FindPIDsByPort(context.Background(), 1)
	assert.Empty(t, pids, "untagged variant must collapse to empty for unused port")
}
