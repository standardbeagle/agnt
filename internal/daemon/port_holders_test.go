//go:build linux

package daemon

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/standardbeagle/agnt/internal/platform"
)

// The test process itself is a live process with known cwd, so the described
// fields can be checked against ground truth rather than just non-empty.
func TestPortHolderEntries_DescribesLiveProcess(t *testing.T) {
	self := os.Getpid()
	d, ok := platform.DescribePID(self)
	require.True(t, ok)

	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.Equal(t, cwd, d.Cwd)
	require.Greater(t, d.RSSBytes, uint64(0))
	require.False(t, d.StartTime.IsZero())
	require.Greater(t, d.Uptime.Nanoseconds(), int64(0))
	require.Greater(t, d.Threads, 0)

	entries := portHolderEntries([]platform.ProcDetails{d, {PID: self + 1}}, []int{self})
	require.Len(t, entries, 1, "only requested pids are rendered")
	require.Equal(t, self, entries[0]["pid"])
	require.Equal(t, cwd, entries[0]["cwd"])
	require.Contains(t, entries[0], "uptime")
	require.Contains(t, entries[0], "memory_bytes")

	text := formatPortHolders([]platform.ProcDetails{d}, []int{self})
	require.Contains(t, text, cwd)
	require.Contains(t, text, "mem ")
}

func TestFormatPortHolders_UnreadableHolderStillNamedByPID(t *testing.T) {
	text := formatPortHolders([]platform.ProcDetails{{PID: 4242}}, []int{4242})
	require.Equal(t, "  pid 4242", text)
}
