package daemon

import (
	"context"
	"testing"
	"time"

	goprocess "github.com/standardbeagle/go-cli-server/process"
	"github.com/stretchr/testify/require"
)

// startURLPrintingProcess runs a command that prints a URL and waits for it to
// exit, so its output is fully buffered before the tracker scans it.
func startURLPrintingProcess(t *testing.T) (*goprocess.ProcessManager, *goprocess.ManagedProcess) {
	t.Helper()
	pm := goprocess.NewProcessManager(goprocess.ManagerConfig{})
	t.Cleanup(func() { _ = pm.Shutdown(context.Background()) })

	proc, err := pm.StartCommand(context.Background(), goprocess.ProcessConfig{
		ID:      "urlproc",
		Command: "sh",
		Args:    []string{"-c", "echo 'Local:   http://localhost:3000'"},
	})
	require.NoError(t, err)

	select {
	case <-proc.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("process never exited")
	}
	return pm, proc
}

// onURLDetected reaches the readiness signaler, the process manager, and the
// proxy-event channel. None of that belongs under the tracker's lock, and the
// tracker's own accessors take that lock — so a callback that reads back the
// URLs it was just told about would deadlock against a non-reentrant RWMutex.
//
// scanProcess registered the notify defer *after* the unlock defer. Defers run
// LIFO, so the callback ran first: with the lock still held, exactly the
// opposite of what the code's comment claimed.
func TestURLTracker_CallbackRunsWithoutHoldingTheLock(t *testing.T) {
	_, proc := startURLPrintingProcess(t)

	tracker := NewURLTracker(nil, DefaultURLTrackerConfig())
	tracker.SetURLMatchers(proc.ID, []string{`Local:\s*{url}`})

	var seen []string
	tracker.onURLDetected = func(processID, url string) {
		// Re-enter the tracker, as a real callback reaching back for state would.
		// Under the writer lock this blocks forever.
		seen = append(seen, url)
		_ = tracker.GetURLs(processID)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		tracker.scanProcess(proc)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("scanProcess deadlocked: onURLDetected ran while holding the tracker lock")
	}

	require.Equal(t, []string{"http://localhost:3000"}, seen, "callback should report the detected URL")
	require.Equal(t, []string{"http://localhost:3000"}, tracker.GetURLs(proc.ID))
}

// The callback must fire exactly once per new URL, and not at all for a rescan
// that finds nothing new.
func TestURLTracker_CallbackFiresOncePerURL(t *testing.T) {
	_, proc := startURLPrintingProcess(t)

	tracker := NewURLTracker(nil, DefaultURLTrackerConfig())
	tracker.SetURLMatchers(proc.ID, []string{`Local:\s*{url}`})

	var calls int
	tracker.onURLDetected = func(string, string) { calls++ }

	tracker.scanProcess(proc)
	require.Equal(t, 1, calls, "first scan should report the URL once")

	tracker.scanProcess(proc)
	require.Equal(t, 1, calls, "rescan found no new URL, so it must not notify again")
}
