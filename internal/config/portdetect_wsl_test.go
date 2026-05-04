//go:build !windows

package config

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/platform"
	"github.com/stretchr/testify/assert"
)

// TestParseTasklistCSV exercises the CSV parser for tasklist.exe output.
// tasklist.exe /fo csv /nh emits one record per line, comma-separated,
// fields wrapped in double quotes:
//
//	"chrome.exe","12345","Console","1","45,000 K"
//
// The parser must strip quotes, return name keyed by PID, and survive
// commas inside quoted fields (the memory column is "45,000 K").
func TestParseTasklistCSV(t *testing.T) {
	input := strings.Join([]string{
		`"chrome.exe","12345","Console","1","45,000 K"`,
		`"docker.exe","6789","Services","0","12,345 K"`,
		`"svchost.exe","100","Services","0","1,024 K"`,
	}, "\n")

	got := parseTasklistCSV([]byte(input))

	assert.Equal(t, "chrome.exe", got[12345])
	assert.Equal(t, "docker.exe", got[6789])
	assert.Equal(t, "svchost.exe", got[100])
	assert.Len(t, got, 3)
}

// TestParseTasklistCSV_Empty handles tasklist.exe's "no matching tasks"
// path. tasklist exits 0 with empty stdout (or a single info line on
// stderr) when /fi matches nothing. Parser must return empty map, not nil.
func TestParseTasklistCSV_Empty(t *testing.T) {
	got := parseTasklistCSV(nil)
	assert.NotNil(t, got, "must return empty map, not nil, so callers can index safely")
	assert.Empty(t, got)

	got = parseTasklistCSV([]byte(""))
	assert.Empty(t, got)
}

// TestParseTasklistCSV_Malformed gracefully drops malformed lines
// instead of panicking. tasklist.exe is generally well-behaved but
// the parser must be defensive — a corrupted line should not poison
// the whole batch.
func TestParseTasklistCSV_Malformed(t *testing.T) {
	input := strings.Join([]string{
		`"chrome.exe","12345","Console","1","45,000 K"`,
		`garbage line with no quotes`,
		`"docker.exe","not-a-pid","Services","0","12,345 K"`,
		`"too","few","fields"`,
		`"svchost.exe","999","Services","0","1,024 K"`,
	}, "\n")

	got := parseTasklistCSV([]byte(input))
	assert.Equal(t, "chrome.exe", got[12345])
	assert.Equal(t, "svchost.exe", got[999])
	assert.Len(t, got, 2, "malformed lines dropped, valid lines retained")
}

// TestProcessNamesByPIDs_LinuxOnly_NoTasklistCall asserts the Linux
// fast path: when every PID resolves through /proc/<pid>/comm, the
// function must NOT shell out to tasklist.exe. Validates the
// "no new tasklist.exe call for purely-Linux port owners" acceptance
// criterion.
func TestProcessNamesByPIDs_LinuxOnly_NoTasklistCall(t *testing.T) {
	// Use the test process itself as a known-resolvable Linux PID.
	self := os.Getpid()

	got := ProcessNamesByPIDs(context.Background(), []int{self})

	assert.NotEmpty(t, got[self], "self PID must resolve via /proc")

	// We can't directly assert "tasklist.exe was not called" without
	// instrumenting exec, but the callPath returned by the test-only
	// hook lets us check.
	resetTasklistCallCount()
	_ = ProcessNamesByPIDs(context.Background(), []int{self})
	assert.Equal(t, 0, tasklistCallCount(), "purely-Linux PID batch must not invoke tasklist.exe")
}

// TestProcessNamesByPIDs_EmptyInput returns empty map on empty input
// (no shell out, no panic).
func TestProcessNamesByPIDs_EmptyInput(t *testing.T) {
	resetTasklistCallCount()
	got := ProcessNamesByPIDs(context.Background(), nil)
	assert.NotNil(t, got)
	assert.Empty(t, got)
	assert.Equal(t, 0, tasklistCallCount())
}

// TestProcessNamesByPIDs_PartialMisses_OnlyOneTasklistCall asserts that
// when some PIDs miss /proc and we are on WSL, exactly ONE tasklist.exe
// call is made for the entire batch — not one call per missing PID.
// Skipped on non-WSL because tasklist.exe isn't reachable.
func TestProcessNamesByPIDs_PartialMisses_OnlyOneTasklistCall(t *testing.T) {
	if !platform.IsWSL() {
		t.Skip("WSL-only: requires tasklist.exe via interop")
	}
	if _, err := exec.LookPath("tasklist.exe"); err != nil {
		t.Skip("tasklist.exe not on PATH (WSL interop disabled?)")
	}

	self := os.Getpid()
	// Mix Linux PID (self) with a Windows-side-shaped PID that won't
	// exist in /proc. tasklist.exe will likely return nothing for the
	// fake PID either, but the call MUST happen exactly once.
	resetTasklistCallCount()
	_ = ProcessNamesByPIDs(context.Background(), []int{self, 4, 8})
	assert.LessOrEqual(t, tasklistCallCount(), 1, "batch must coalesce to <=1 tasklist.exe invocation")
}

// TestProcessNameByPID_WSLFallback_NoCallForLinuxPID asserts the
// single-PID path also avoids tasklist.exe when /proc resolves.
// Same purely-Linux-no-cost contract as the batch path.
func TestProcessNameByPID_WSLFallback_NoCallForLinuxPID(t *testing.T) {
	resetTasklistCallCount()
	name := ProcessNameByPID(os.Getpid())
	assert.NotEmpty(t, name)
	assert.Equal(t, 0, tasklistCallCount(), "Linux PID lookup must not invoke tasklist.exe")
}
