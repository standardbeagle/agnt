//go:build linux || darwin

package platform

import (
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The real process table, not an injected source: Scan must find this process
// with its real parent on every OS that claims an implementation. On macOS the
// scan used to read /proc, which does not exist there, so it always failed.
func TestScan_FindsThisProcess(t *testing.T) {
	procs, err := Scan()
	require.NoError(t, err)
	require.NotEmpty(t, procs)

	self := os.Getpid()
	var found *ProcInfo
	for i := range procs {
		if procs[i].PID == self {
			found = &procs[i]
			break
		}
	}
	require.NotNil(t, found, "Scan did not return this process (pid %d) among %d processes", self, len(procs))
	assert.Equal(t, os.Getppid(), found.PPID)
	assert.NotEmpty(t, found.Command)
}

// Session containment asks which processes belong to a process group and
// refuses to reap when it cannot tell. That enumeration is built on Scan, so
// it must work wherever Scan does.
func TestMembersOfPGID_FindsThisProcess(t *testing.T) {
	pgid, err := syscall.Getpgid(os.Getpid())
	require.NoError(t, err)

	members := MembersOfPGID(pgid)
	require.NotNil(t, members, "enumeration failed for our own process group %d", pgid)
	assert.Contains(t, members, os.Getpid())
}
