package daemon

import (
	"context"
	"testing"
	"time"

	goprocess "github.com/standardbeagle/go-cli-server/process"
	"github.com/stretchr/testify/require"
)

// runToExit starts a process under id and waits for it to exit, so its output
// is fully buffered before the tracker scans it.
func runToExit(t *testing.T, pm *goprocess.ProcessManager, id, command string, args []string) *goprocess.ManagedProcess {
	t.Helper()
	proc, err := pm.StartCommand(context.Background(), goprocess.ProcessConfig{
		ID: id, ProjectPath: t.TempDir(), Command: command, Args: args,
	})
	require.NoError(t, err)
	select {
	case <-proc.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("process never exited")
	}
	return proc
}

func newTestProcessManager(t *testing.T) *goprocess.ProcessManager {
	t.Helper()
	pm := goprocess.NewProcessManager(goprocess.ManagerConfig{})
	t.Cleanup(func() { _ = pm.Shutdown(context.Background()) })
	return pm
}

// A process that prints nothing left both scan offsets at 0, so every tick
// counted as its "first scan" and reloaded .agnt.kdl through
// onProcessFirstSeen — every 500ms for as long as it stayed silent.
func TestURLTracker_SilentProcessLoadsConfigOnce(t *testing.T) {
	pm := newTestProcessManager(t)
	cmd, args := failExec() // exits without writing anything
	proc := runToExit(t, pm, "silent", cmd, args)

	tracker := NewURLTracker(pm, DefaultURLTrackerConfig())
	var loads int
	tracker.onProcessFirstSeen = func(string) { loads++ }

	for range 5 {
		tracker.scanProcess(proc)
	}
	require.Equal(t, 1, loads, "config must load once per process lifetime, not once per tick")
}

// Per-process scan state was retired only for processes that had found a
// URL, so a removed process that never printed one kept its entries forever.
func TestURLTracker_RemovedURLlessProcessIsRetired(t *testing.T) {
	pm := newTestProcessManager(t)
	cmd, args := echoExec("compiling, no url here")
	proc := runToExit(t, pm, "quiet", cmd, args)

	tracker := NewURLTracker(pm, DefaultURLTrackerConfig())
	tracker.SetURLMatchers(proc.ID, []string{`Local:\s*{url}`})
	var stopped []string
	tracker.onProcessStopped = func(id string) { stopped = append(stopped, id) }

	tracker.scanProcess(proc)
	tracker.mu.RLock()
	_, scanned := tracker.scannedStdout[proc.ID]
	tracker.mu.RUnlock()
	require.True(t, scanned, "precondition: the scan recorded state for the process")

	require.True(t, pm.Remove(proc.ID))
	tracker.scanAllProcesses()

	tracker.mu.RLock()
	defer tracker.mu.RUnlock()
	require.NotContains(t, tracker.scannedStdout, proc.ID)
	require.NotContains(t, tracker.scannedStderr, proc.ID)
	require.NotContains(t, tracker.urlMatchers, proc.ID)
	require.NotContains(t, tracker.lifetimes, proc.ID)
	require.Empty(t, stopped, "a process that never announced a URL has no proxy to stop")
}

// Scan state belongs to one process lifetime. A new process under the same id
// must be scanned from offset 0, not from where the previous one stopped.
func TestURLTracker_NewLifetimeUnderSameIDRescansFromStart(t *testing.T) {
	pm := newTestProcessManager(t)
	cmd, args := echoExec("a long startup banner that is printed before anything else happens")
	old := runToExit(t, pm, "web", cmd, args)

	tracker := NewURLTracker(pm, DefaultURLTrackerConfig())
	tracker.scanProcess(old)
	require.True(t, pm.Remove(old.ID))

	cmd, args = echoExec("Local: http://localhost:3000")
	fresh := runToExit(t, pm, "web", cmd, args)
	require.NotSame(t, old, fresh)

	tracker.scanProcess(fresh)
	require.Equal(t, []string{"http://localhost:3000"}, tracker.GetURLs(fresh.ID))
}
