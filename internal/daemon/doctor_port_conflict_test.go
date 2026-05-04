//go:build unix

package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	goprocess "github.com/standardbeagle/go-cli-server/process"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckPortConflicts_IncludesProcessNames verifies the doctor command's
// port_conflicts check resolves rogue PIDs to process names via the
// ProcessNamesByPIDs batch path. The names are needed so the doctor
// output is actionable: "blocked by chrome.exe pid=12345" rather than
// just "blocked by pid=12345".
//
// On WSL this exercises the tasklist.exe batch resolution path
// (when the rogue PID is Windows-side); on pure Linux it exercises
// the /proc resolution path. We use the test binary's own PID — which
// is always /proc-resolvable — as a stand-in for any unmanaged
// listener, satisfying the "names propagate into the doctor output"
// contract.
func TestCheckPortConflicts_IncludesProcessNames(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	dir := t.TempDir()
	configBody := fmt.Sprintf(`scripts {
    api {
        run "echo placeholder"
        ports %d
    }
}`, port)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".agnt.kdl"), []byte(configBody), 0o644))

	pm := goprocess.NewProcessManager(goprocess.DefaultManagerConfig())
	defer pm.Shutdown(context.Background())

	result := checkPortConflicts(context.Background(), dir, pm)
	require.Equal(t, StatusError, result.Status, "port held by unmanaged listener must report error")

	details, ok := result.Details.([]map[string]interface{})
	require.True(t, ok, "details must be []map[string]interface{}")
	require.Len(t, details, 1)

	entry := details[0]
	assert.Equal(t, "api", entry["script"])
	assert.Equal(t, port, entry["port"])

	pids, ok := entry["pids"].([]int)
	require.True(t, ok, "pids must be []int")
	assert.Contains(t, pids, os.Getpid())

	names, ok := entry["process_names"].([]string)
	require.True(t, ok, "process_names must be []string when at least one PID resolves")
	require.NotEmpty(t, names, "own PID must resolve via /proc, so names must not be empty")
	for _, name := range names {
		assert.NotEmpty(t, name, "every entry in process_names must be non-empty")
	}
}
