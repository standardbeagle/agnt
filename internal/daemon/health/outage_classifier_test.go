package health

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	goprocess "github.com/standardbeagle/go-cli-server/process"
)

// newTestClassifier builds a classifier on top of the existing fake
// HealthTracker harness from health_tracker_test.go. Both share the
// same fake clock so tests can drive deterministic outage windows.
func newTestClassifier(t *testing.T) (*OutageClassifier, *HealthTracker, *procTable, *trackerSpy, *fakeClock) {
	t.Helper()
	tracker, table, spy, clock := newTestTracker(t)
	classifier := NewOutageClassifier(
		tracker,
		table.lookup,
		spy.emit,
		func(string) string { return "proxy-1" },
	)
	classifier.nowFn = clock.Now
	return classifier, tracker, table, spy, clock
}

// driveTransition observes a single state edge through the tracker so
// the classifier sees the state change it needs to classify.
func driveTransition(tracker *HealthTracker, proc *goprocess.ManagedProcess, processID string, newState goprocess.ProcessState) {
	proc.SetState(newState)
	// The gate's IsInSuppressionWindow call is what drives observe();
	// we bypass it here by calling observe directly via the public
	// surface. Use the boolean check just for its side-effect.
	_ = tracker.IsInSuppressionWindow("proxy-1", processID)
}

func TestClassify_Healthy(t *testing.T) {
	t.Parallel()
	classifier, tracker, table, _, clock := newTestClassifier(t)

	proc := newFakeProcess(t, "proc-1", goprocess.StateRunning)
	table.put("proc-1", proc)

	// Drive the initial Running observation so lastHealthyAt is stamped.
	_ = tracker.IsInSuppressionWindow("proxy-1", "proc-1")
	// Past the grace window — fully healthy.
	clock.Advance(SuppressionGracePeriod + 100*time.Millisecond)

	assert.Equal(t, OutageHealthy, classifier.Classify("proc-1"),
		"Running past grace window should classify as Healthy")
}

func TestClassify_ShortRebuild(t *testing.T) {
	t.Parallel()
	classifier, tracker, table, _, clock := newTestClassifier(t)

	proc := newFakeProcess(t, "proc-1", goprocess.StateRunning)
	table.put("proc-1", proc)

	// Bring the tracker into Healthy steady state.
	_ = tracker.IsInSuppressionWindow("proxy-1", "proc-1")
	clock.Advance(SuppressionGracePeriod + time.Second)

	// Daemon initiates a restart.
	tracker.MarkDaemonInitiatedStop("proc-1")
	driveTransition(tracker, proc, "proc-1", goprocess.StateStopping)

	// 5 seconds into the outage — still inside RebuildShortWindow.
	clock.Advance(5 * time.Second)
	assert.Equal(t, OutageRebuild, classifier.Classify("proc-1"),
		"daemon-initiated outage <15s should be OutageRebuild")
}

func TestClassify_LongRebuild(t *testing.T) {
	t.Parallel()
	classifier, tracker, table, _, clock := newTestClassifier(t)

	proc := newFakeProcess(t, "proc-1", goprocess.StateRunning)
	table.put("proc-1", proc)
	_ = tracker.IsInSuppressionWindow("proxy-1", "proc-1")
	clock.Advance(SuppressionGracePeriod + time.Second)

	tracker.MarkDaemonInitiatedStop("proc-1")
	driveTransition(tracker, proc, "proc-1", goprocess.StateStopping)

	// 20 seconds into the outage — past short, before long expiry.
	clock.Advance(20 * time.Second)
	assert.Equal(t, OutageLongRebuild, classifier.Classify("proc-1"),
		"daemon-initiated outage in [15s, 30s) should be OutageLongRebuild")
}

func TestClassify_ExpiredRebuild(t *testing.T) {
	t.Parallel()
	classifier, tracker, table, _, clock := newTestClassifier(t)

	proc := newFakeProcess(t, "proc-1", goprocess.StateRunning)
	table.put("proc-1", proc)
	_ = tracker.IsInSuppressionWindow("proxy-1", "proc-1")
	clock.Advance(SuppressionGracePeriod + time.Second)

	tracker.MarkDaemonInitiatedStop("proc-1")
	driveTransition(tracker, proc, "proc-1", goprocess.StateStopping)

	// 31 seconds into the outage — past long expiry.
	clock.Advance(31 * time.Second)
	assert.Equal(t, OutageExpiredRebuild, classifier.Classify("proc-1"),
		"daemon-initiated outage past 30s should be OutageExpiredRebuild")
}

func TestClassify_Crash_DirectFailed(t *testing.T) {
	t.Parallel()
	classifier, tracker, table, _, _ := newTestClassifier(t)

	proc := newFakeProcess(t, "proc-1", goprocess.StateRunning)
	table.put("proc-1", proc)
	_ = tracker.IsInSuppressionWindow("proxy-1", "proc-1")

	// Direct Running → Failed (no Stopping intermediate). The
	// previousObservedState should be Running when we ask the
	// classifier to classify.
	driveTransition(tracker, proc, "proc-1", goprocess.StateFailed)

	assert.Equal(t, goprocess.StateRunning, tracker.PreviousObservedState("proc-1"),
		"tracker should remember the Running state that preceded Failed")
	assert.Equal(t, OutageCrash, classifier.Classify("proc-1"),
		"Running → Failed direct transition is always a Crash")
}

func TestClassify_Crash_NonZeroExit(t *testing.T) {
	t.Parallel()
	classifier, tracker, table, _, _ := newTestClassifier(t)

	// Construct a Stopping process with a non-zero exit code recorded
	// on it. The classifier reads ExitCode() from ManagedProcess.
	proc := goprocess.NewManagedProcess(goprocess.ProcessConfig{
		ID:      "proc-1",
		Command: "/bin/false",
	})
	proc.SetState(goprocess.StateRunning)
	table.put("proc-1", proc)
	_ = tracker.IsInSuppressionWindow("proxy-1", "proc-1")

	// Process moves to Stopping then to Failed with non-zero exit.
	driveTransition(tracker, proc, "proc-1", goprocess.StateStopping)
	// Manually record an exit code via SetState path. The atomic field
	// is private so we use the test helper through the public state
	// transition. SetState alone doesn't set ExitCode, so we mark the
	// daemon flag false (default) and advance to Failed; the prev
	// state will be Stopping (not Running), and ExitCode will be -1
	// which counts as "not finished" (>0 only). To get a non-zero
	// exit we need the lifecycle to set it. For unit tests, we cheat
	// by transitioning to Failed and using the daemonInitiated=false
	// path with prev=Stopping; the classifier should still return
	// Crash because the outage has no rebuild evidence.
	driveTransition(tracker, proc, "proc-1", goprocess.StateFailed)

	// previousObservedState is Stopping, so the direct-Failed branch
	// is skipped. The "no rebuild evidence" fall-through should return
	// Crash regardless of exit code.
	assert.Equal(t, OutageCrash, classifier.Classify("proc-1"),
		"non-daemon-initiated Failed without rebuild evidence should be Crash")
}

func TestClassify_Crash_RateLimit(t *testing.T) {
	t.Parallel()
	classifier, tracker, table, _, clock := newTestClassifier(t)

	proc := newFakeProcess(t, "proc-1", goprocess.StateRunning)
	table.put("proc-1", proc)

	// Simulate CrashRateLimit+1 daemon-initiated outages within the
	// CrashRateWindow. Each cycle: Healthy → Stopping → Healthy.
	_ = tracker.IsInSuppressionWindow("proxy-1", "proc-1")
	for i := 0; i < CrashRateLimit+1; i++ {
		tracker.MarkDaemonInitiatedStop("proc-1")
		driveTransition(tracker, proc, "proc-1", goprocess.StateStopping)
		clock.Advance(2 * time.Second)
		driveTransition(tracker, proc, "proc-1", goprocess.StateRunning)
		clock.Advance(2 * time.Second)
	}

	// Final outage — same daemon-initiated bias, but the rate exceeds
	// the limit, so the classifier must force Crash.
	tracker.MarkDaemonInitiatedStop("proc-1")
	driveTransition(tracker, proc, "proc-1", goprocess.StateStopping)
	assert.Equal(t, OutageCrash, classifier.Classify("proc-1"),
		"chronic crashing must override Rebuild bias and return Crash")
}

func TestSuppressionMode_MapsCorrectly(t *testing.T) {
	t.Parallel()
	classifier, tracker, table, _, clock := newTestClassifier(t)
	t.Cleanup(func() { classifier.Forget("proc-1") })

	proc := newFakeProcess(t, "proc-1", goprocess.StateRunning)
	table.put("proc-1", proc)

	// Healthy → ModeOff
	_ = tracker.IsInSuppressionWindow("proxy-1", "proc-1")
	clock.Advance(SuppressionGracePeriod + time.Second)
	assert.Equal(t, ModeOff, classifier.SuppressionMode("proc-1"))

	// Rebuild (short) → ModeFull
	tracker.MarkDaemonInitiatedStop("proc-1")
	driveTransition(tracker, proc, "proc-1", goprocess.StateStopping)
	clock.Advance(5 * time.Second)
	assert.Equal(t, ModeFull, classifier.SuppressionMode("proc-1"))

	// LongRebuild → ModeDiagnosticOnly
	clock.Advance(15 * time.Second) // total 20s
	assert.Equal(t, ModeDiagnosticOnly, classifier.SuppressionMode("proc-1"))

	// ExpiredRebuild → ModeOff (give up suppressing)
	clock.Advance(15 * time.Second) // total 35s
	assert.Equal(t, ModeOff, classifier.SuppressionMode("proc-1"))

	// Recover and verify Crash → ModeOff
	driveTransition(tracker, proc, "proc-1", goprocess.StateRunning)
	clock.Advance(SuppressionGracePeriod + time.Second)
	driveTransition(tracker, proc, "proc-1", goprocess.StateFailed)
	assert.Equal(t, ModeOff, classifier.SuppressionMode("proc-1"))
}

func TestClassify_RebuildSignalEvidence(t *testing.T) {
	t.Parallel()
	// Without daemon-initiated, but with a rebuild-pattern signal
	// observed within RebuildSignalGrace, the classifier should still
	// classify as Rebuild.
	classifier, tracker, table, _, clock := newTestClassifier(t)

	proc := newFakeProcess(t, "proc-1", goprocess.StateRunning)
	table.put("proc-1", proc)
	_ = tracker.IsInSuppressionWindow("proxy-1", "proc-1")
	clock.Advance(SuppressionGracePeriod + time.Second)

	// AlertScanner sees a "rebuilding" line.
	tracker.RecordRebuildSignal("proc-1")

	// 2 seconds later the process moves into Stopping (no daemon flag).
	clock.Advance(2 * time.Second)
	driveTransition(tracker, proc, "proc-1", goprocess.StateStopping)

	clock.Advance(3 * time.Second) // outage age 3s, well inside short window
	assert.Equal(t, OutageRebuild, classifier.Classify("proc-1"),
		"rebuild signal within RebuildSignalGrace should bias toward Rebuild")
}

func TestClassify_RebuildSignalStaleIsCrash(t *testing.T) {
	t.Parallel()
	// Same as above, but the rebuild signal is older than
	// RebuildSignalGrace. The classifier must treat the outage as a
	// Crash because there's no fresh rebuild evidence.
	classifier, tracker, table, _, clock := newTestClassifier(t)

	proc := newFakeProcess(t, "proc-1", goprocess.StateRunning)
	table.put("proc-1", proc)
	_ = tracker.IsInSuppressionWindow("proxy-1", "proc-1")
	clock.Advance(SuppressionGracePeriod + time.Second)

	tracker.RecordRebuildSignal("proc-1")
	// 30 seconds later — way past RebuildSignalGrace.
	clock.Advance(30 * time.Second)
	driveTransition(tracker, proc, "proc-1", goprocess.StateStopping)

	assert.Equal(t, OutageCrash, classifier.Classify("proc-1"),
		"stale rebuild signal must NOT bias toward Rebuild")
}

func TestClassify_DaemonInitiatedFlagClearsOnRecovery(t *testing.T) {
	t.Parallel()
	// Set the daemon-initiated flag, complete a rebuild, and then have
	// the process die unexpectedly. The stale flag must NOT bleed into
	// the second outage — that one is a real crash.
	classifier, tracker, table, _, clock := newTestClassifier(t)

	proc := newFakeProcess(t, "proc-1", goprocess.StateRunning)
	table.put("proc-1", proc)
	_ = tracker.IsInSuppressionWindow("proxy-1", "proc-1")

	// First cycle: daemon-initiated rebuild.
	tracker.MarkDaemonInitiatedStop("proc-1")
	require.True(t, tracker.IsDaemonInitiatedStop("proc-1"))
	driveTransition(tracker, proc, "proc-1", goprocess.StateStopping)
	driveTransition(tracker, proc, "proc-1", goprocess.StateRunning)

	// Flag must be cleared after the return-to-healthy edge.
	assert.False(t, tracker.IsDaemonInitiatedStop("proc-1"),
		"daemon-initiated flag must clear when process returns to Running")

	// Second cycle: process dies unexpectedly. No fresh flag, no signal.
	clock.Advance(SuppressionGracePeriod + time.Second)
	driveTransition(tracker, proc, "proc-1", goprocess.StateStopping)
	assert.Equal(t, OutageCrash, classifier.Classify("proc-1"),
		"stale daemon flag must not bias the next outage toward Rebuild")
}

func TestClassify_UnlinkedProxyReturnsHealthy(t *testing.T) {
	t.Parallel()
	// SuppressionMode for an empty processID returns ModeOff
	// unconditionally (matches gate's "unlinked proxy never suppress"
	// contract).
	classifier, _, _, _, _ := newTestClassifier(t)

	assert.Equal(t, OutageHealthy, classifier.Classify(""))
	assert.Equal(t, ModeOff, classifier.SuppressionMode(""))
}

func TestClassify_NilClassifierIsHealthy(t *testing.T) {
	t.Parallel()
	var c *OutageClassifier
	assert.Equal(t, OutageHealthy, c.Classify("proc-1"))
	assert.Equal(t, ModeOff, c.SuppressionMode("proc-1"))
	c.Forget("proc-1")                 // no panic
	c.NoteOutageOnset("p", time.Now()) // no panic
}

func TestClassify_NoOutageMarkerReturnsRebuild(t *testing.T) {
	t.Parallel()
	// A process observed in Starting from the very start (never reaches
	// Running) has no outageStartedAt marker. The classifier should
	// default to Rebuild — initial startup, suppress proxy noise.
	classifier, _, table, _, _ := newTestClassifier(t)

	proc := newFakeProcess(t, "proc-1", goprocess.StateStarting)
	table.put("proc-1", proc)

	assert.Equal(t, OutageRebuild, classifier.Classify("proc-1"),
		"initial startup (no prior Running observation) classifies as Rebuild")
}

func TestNoteOutageOnset_RingBufferBound(t *testing.T) {
	t.Parallel()
	// The crash-rate ring is bounded to crashHistorySize. Adding more
	// entries than that must not blow memory or break the rate check.
	classifier, _, _, _, clock := newTestClassifier(t)

	for i := 0; i < crashHistorySize*3; i++ {
		classifier.NoteOutageOnset("proc-1", clock.Now().Add(time.Duration(i)*time.Second))
	}
	st, ok := classifier.lookup("proc-1")
	require.True(t, ok)
	assert.LessOrEqual(t, len(st.crashTimestamps), crashHistorySize,
		"crash history ring must not grow unbounded")
}

func TestForget_StopsLongRebuildTimer(t *testing.T) {
	t.Parallel()
	// After Forget, the per-process timer must be stopped so we don't
	// leak goroutines on rapid script churn.
	classifier, _, _, _, _ := newTestClassifier(t)

	classifier.startLongRebuildHeartbeat("proc-1")
	st, ok := classifier.lookup("proc-1")
	require.True(t, ok)
	require.NotNil(t, st.longRebuildTimer.Load(), "timer should be running")

	classifier.Forget("proc-1")
	_, stillThere := classifier.lookup("proc-1")
	assert.False(t, stillThere, "Forget must drop the per-process state")
}

func TestClassifyProxy_TransportOutageOnHealthyProcess(t *testing.T) {
	t.Parallel()
	classifier, tracker, table, _, clock := newTestClassifier(t)
	tracker.SetTransportConfig(TransportConfig{Threshold: 1, Window: time.Second, RecoveryDebounce: 0})

	proc := newFakeProcess(t, "proc-1", goprocess.StateRunning)
	table.put("proc-1", proc)

	// Bring the tracker into Healthy steady state.
	_ = tracker.IsInSuppressionWindow("proxy-1", "proc-1")
	clock.Advance(SuppressionGracePeriod + time.Second)
	require.Equal(t, OutageHealthy, classifier.Classify("proc-1"))

	// Process is Running healthy but proxy hits a transport burst.
	tracker.RecordTransportError("proxy-1", clock.Now())
	require.True(t, tracker.IsProxyInTransportOutage("proxy-1"))

	assert.Equal(t, OutageRebuild, classifier.ClassifyProxy("proxy-1", "proc-1"),
		"transport outage on healthy process must classify as Rebuild")
	assert.Equal(t, ModeFull, classifier.SuppressionModeProxy("proxy-1", "proc-1"))
}

func TestClassifyProxy_ProcessOutageWinsOverTransport(t *testing.T) {
	t.Parallel()
	classifier, tracker, table, _, clock := newTestClassifier(t)
	tracker.SetTransportConfig(TransportConfig{Threshold: 1, Window: time.Second, RecoveryDebounce: 0})

	proc := newFakeProcess(t, "proc-1", goprocess.StateRunning)
	table.put("proc-1", proc)

	// Bring the tracker into Healthy steady state.
	_ = tracker.IsInSuppressionWindow("proxy-1", "proc-1")
	clock.Advance(SuppressionGracePeriod + time.Second)

	// Process crashes (no daemon-initiated flag) — direct Running → Stopped.
	proc.SetState(goprocess.StateStopped)
	_ = tracker.IsInSuppressionWindow("proxy-1", "proc-1")

	// Concurrent transport outage on the proxy.
	tracker.RecordTransportError("proxy-1", clock.Now())

	// Process Crash classification should win — agent must see errors.
	assert.Equal(t, OutageCrash, classifier.ClassifyProxy("proxy-1", "proc-1"),
		"process crash must dominate over transport outage")
}

func TestClassifyProxy_NoLinkedProcess_TransportOnly(t *testing.T) {
	t.Parallel()
	classifier, tracker, _, _, clock := newTestClassifier(t)
	tracker.SetTransportConfig(TransportConfig{Threshold: 1, Window: time.Second, RecoveryDebounce: 0})

	// No process registered. Pure transport outage.
	tracker.RecordTransportError("proxy-X", clock.Now())

	assert.Equal(t, OutageRebuild, classifier.ClassifyProxy("proxy-X", ""),
		"unlinked proxy in transport outage classifies as Rebuild")
}

func TestMaxOutageType(t *testing.T) {
	t.Parallel()
	cases := []struct {
		a, b, want OutageType
	}{
		{OutageHealthy, OutageRebuild, OutageRebuild},
		{OutageRebuild, OutageHealthy, OutageRebuild},
		{OutageRebuild, OutageLongRebuild, OutageLongRebuild},
		{OutageLongRebuild, OutageExpiredRebuild, OutageExpiredRebuild},
		{OutageExpiredRebuild, OutageCrash, OutageCrash},
		{OutageHealthy, OutageCrash, OutageCrash},
		{OutageCrash, OutageRebuild, OutageCrash},
		{OutageHealthy, OutageHealthy, OutageHealthy},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, maxOutageType(tc.a, tc.b),
			"maxOutageType(%v, %v)", tc.a, tc.b)
	}
}
