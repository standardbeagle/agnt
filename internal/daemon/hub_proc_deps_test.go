//go:build unix

package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/daemonclient"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStartProcessWithDeps_NoDepsFastPath: when deps is empty,
// StartProcessWithDeps must delegate synchronously to StartScriptExplicit
// and return State="starting".
func TestStartProcessWithDeps_NoDepsFastPath(t *testing.T) {
	// No t.Parallel(): starts real sleep process; PID-reuse kills it under high concurrency.
	daemon, _, tmpDir := newHubProcTestDaemon(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := daemon.StartProcessWithDeps(ctx, "solo", &config.ScriptConfig{Run: longRunningCmd()}, tmpDir, nil, 0)
	require.NoError(t, result.Err)
	assert.Equal(t, "starting", result.State)
	assert.Empty(t, result.WaitingFor)

	processID := makeProcessID(tmpDir, "solo")
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = daemon.hub.ProcessManager().Stop(stopCtx, processID)
	})

	// ProcessManager should know about the process immediately.
	_, err := daemon.hub.ProcessManager().Get(processID)
	require.NoError(t, err)

	// Pending tracker must NOT have an entry — fast path skips it.
	_, ok := daemon.pendingProcs.Get(processID)
	assert.False(t, ok, "no-deps path must not register a pending entry")
}

// TestStartProcessWithDeps_PendingThenReady: a process with deps stays
// in pending state until SignalReady fires for each dep, then launches.
func TestStartProcessWithDeps_PendingThenReady(t *testing.T) {
	// No t.Parallel(): starts real sleep process; PID-reuse kills it under high concurrency.
	daemon, _, tmpDir := newHubProcTestDaemon(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	deps := []string{"db", "redis"}
	result := daemon.StartProcessWithDeps(ctx, "api",
		&config.ScriptConfig{Run: longRunningCmd()},
		tmpDir, deps, 30*time.Second)
	require.NoError(t, result.Err)
	assert.Equal(t, "pending", result.State)
	assert.ElementsMatch(t, deps, result.WaitingFor)

	processID := makeProcessID(tmpDir, "api")

	// Pending tracker entry exists with both deps.
	pending, ok := daemon.pendingProcs.Get(processID)
	require.True(t, ok)
	assert.Equal(t, []string{"db", "redis"}, pending.WaitingFor) // sorted

	// Signal one dep — the other should remain.
	daemon.readySignaler.SignalReady(makeProcessID(tmpDir, "db"))
	require.Eventually(t, func() bool {
		p, ok := daemon.pendingProcs.Get(processID)
		return ok && len(p.WaitingFor) == 1 && p.WaitingFor[0] == "redis"
	}, 2*time.Second, 20*time.Millisecond, "tracker should drop db once SignalReady fires")

	// Signal the second dep — process should launch and tracker should clear.
	daemon.readySignaler.SignalReady(makeProcessID(tmpDir, "redis"))
	require.Eventually(t, func() bool {
		_, err := daemon.hub.ProcessManager().Get(processID)
		return err == nil
	}, 5*time.Second, 100*time.Millisecond, "process must launch once all deps clear")

	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = daemon.hub.ProcessManager().Stop(stopCtx, processID)
	})

	// Pending entry must be removed after successful launch.
	require.Eventually(t, func() bool {
		_, ok := daemon.pendingProcs.Get(processID)
		return !ok
	}, 5*time.Second, 100*time.Millisecond,
		"pending tracker should clear once the post-dep StartScriptExplicit returns")
}

// TestStartProcessWithDeps_DependencyTimeout: when a dep never signals
// ready within the configured timeout, the dependent transitions to
// failed with reason "dependency_timeout:<dep>".
func TestStartProcessWithDeps_DependencyTimeout(t *testing.T) {
	t.Parallel()
	daemon, _, tmpDir := newHubProcTestDaemon(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Use a 100ms timeout so the test runs fast.
	result := daemon.StartProcessWithDeps(ctx, "api",
		&config.ScriptConfig{Run: longRunningCmd()},
		tmpDir, []string{"db"}, 100*time.Millisecond)
	require.NoError(t, result.Err)
	assert.Equal(t, "pending", result.State)

	processID := makeProcessID(tmpDir, "api")

	// Wait for the timeout to fire.
	require.Eventually(t, func() bool {
		p, ok := daemon.pendingProcs.Get(processID)
		return ok && p.State == PendingFailed
	}, 2*time.Second, 25*time.Millisecond, "tracker should mark failed after dep timeout")

	pending, ok := daemon.pendingProcs.Get(processID)
	require.True(t, ok)
	assert.Equal(t, "dependency_timeout:db", pending.FailureReason)

	// ProcessManager must NOT have a registered entry — the dependent
	// never launched.
	_, err := daemon.hub.ProcessManager().Get(processID)
	assert.Error(t, err, "dependent must not launch when dep times out")

	// scriptRegistry should reflect the failure.
	if entry, ok := daemon.scriptRegistry.Get("api", tmpDir); ok {
		assert.Contains(t, entry.LastError(), "dependency_timeout:db")
	}
}

// TestStartProcessGroup_CycleDetection: a self-loop or A↔B cycle must
// short-circuit before any process is launched.
func TestStartProcessGroup_CycleDetection(t *testing.T) {
	t.Parallel()
	daemon, _, tmpDir := newHubProcTestDaemon(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cases := []struct {
		name      string
		processes []protocol.GroupProcess
	}{
		{
			name: "self_loop",
			processes: []protocol.GroupProcess{
				{Name: "a", Run: longRunningCmd(), DependsOn: []string{"a"}},
			},
		},
		{
			name: "two_way_cycle",
			processes: []protocol.GroupProcess{
				{Name: "a", Run: longRunningCmd(), DependsOn: []string{"b"}},
				{Name: "b", Run: longRunningCmd(), DependsOn: []string{"a"}},
			},
		},
		{
			name: "three_way_cycle",
			processes: []protocol.GroupProcess{
				{Name: "a", Run: longRunningCmd(), DependsOn: []string{"c"}},
				{Name: "b", Run: longRunningCmd(), DependsOn: []string{"a"}},
				{Name: "c", Run: longRunningCmd(), DependsOn: []string{"b"}},
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			result := daemon.StartProcessGroup(ctx, tmpDir, tc.processes, 0)
			require.Error(t, result.Err, "cycle detection must error")
			assert.Contains(t, result.Err.Error(), "circular dependency")
			assert.Empty(t, result.Processes, "no process must launch on cycle")

			// Verify no process was actually launched.
			for _, gp := range tc.processes {
				processID := makeProcessID(tmpDir, gp.Name)
				_, err := daemon.hub.ProcessManager().Get(processID)
				assert.Error(t, err, "process %q must not be launched on cycle detection", gp.Name)
			}
		})
	}
}

// TestStartProcessGroup_UnknownDep: depending on a process that doesn't
// exist in the group is a fatal cycle-detection error.
func TestStartProcessGroup_UnknownDep(t *testing.T) {
	t.Parallel()
	daemon, _, tmpDir := newHubProcTestDaemon(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := daemon.StartProcessGroup(ctx, tmpDir, []protocol.GroupProcess{
		{Name: "api", Run: longRunningCmd(), DependsOn: []string{"missing"}},
	}, 0)
	require.Error(t, result.Err)
	assert.Contains(t, result.Err.Error(), "unknown")
}

// TestStartProcessGroup_DuplicateName: two processes with the same name
// is a validation error before topological sort.
func TestStartProcessGroup_DuplicateName(t *testing.T) {
	t.Parallel()
	daemon, _, tmpDir := newHubProcTestDaemon(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := daemon.StartProcessGroup(ctx, tmpDir, []protocol.GroupProcess{
		{Name: "a", Run: longRunningCmd()},
		{Name: "a", Run: longRunningCmd()},
	}, 0)
	require.Error(t, result.Err)
	assert.Contains(t, result.Err.Error(), "duplicate")
}

// TestStartProcessGroup_FiveProcessStack: end-to-end acceptance — five
// processes, declared layer order honoured via SignalReady fan-out from
// the readySignaler. Validates the spec example: db, redis → api, worker
// → web. Layer-zero processes (db, redis) launch immediately; api/worker
// launch when both db and redis signal ready; web launches when api
// signals ready.
func TestStartProcessGroup_FiveProcessStack(t *testing.T) {
	// No t.Parallel(): starts five real sleep processes; PID-reuse kills them under high concurrency.
	daemon, _, tmpDir := newHubProcTestDaemon(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := daemon.StartProcessGroup(ctx, tmpDir, []protocol.GroupProcess{
		{Name: "db", Run: longRunningCmd()},
		{Name: "redis", Run: longRunningCmd()},
		{Name: "api", Run: longRunningCmd(), DependsOn: []string{"db", "redis"}},
		{Name: "worker", Run: longRunningCmd(), DependsOn: []string{"db", "redis"}},
		{Name: "web", Run: longRunningCmd(), DependsOn: []string{"api"}},
	}, 30*time.Second)

	require.NoError(t, result.Err)
	require.Len(t, result.Processes, 5)

	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		for _, name := range []string{"db", "redis", "api", "worker", "web"} {
			_ = daemon.hub.ProcessManager().Stop(stopCtx, makeProcessID(tmpDir, name))
		}
	})

	// Layer 0 (db, redis) should be starting immediately.
	stateByName := map[string]string{}
	for _, r := range result.Processes {
		// process_id format is "tmpDir:name"; extract name.
		name := r.ProcessID
		if idx := lastColon(name); idx >= 0 {
			name = name[idx+1:]
		}
		stateByName[name] = r.State
	}
	assert.Equal(t, "starting", stateByName["db"], "layer 0 db should be starting")
	assert.Equal(t, "starting", stateByName["redis"], "layer 0 redis should be starting")
	assert.Equal(t, "pending", stateByName["api"], "layer 1 api should be pending")
	assert.Equal(t, "pending", stateByName["worker"], "layer 1 worker should be pending")
	assert.Equal(t, "pending", stateByName["web"], "layer 2 web should be pending")

	// Signal db + redis ready → api + worker launch.
	daemon.readySignaler.SignalReady(makeProcessID(tmpDir, "db"))
	daemon.readySignaler.SignalReady(makeProcessID(tmpDir, "redis"))

	require.Eventually(t, func() bool {
		_, errAPI := daemon.hub.ProcessManager().Get(makeProcessID(tmpDir, "api"))
		_, errWorker := daemon.hub.ProcessManager().Get(makeProcessID(tmpDir, "worker"))
		return errAPI == nil && errWorker == nil
	}, 3*time.Second, 25*time.Millisecond, "api + worker must launch when both deps clear")

	// web is still pending until api fires.
	_, err := daemon.hub.ProcessManager().Get(makeProcessID(tmpDir, "web"))
	assert.Error(t, err, "web must remain pending until api signals ready")

	// Signal api → web launches.
	daemon.readySignaler.SignalReady(makeProcessID(tmpDir, "api"))
	require.Eventually(t, func() bool {
		_, err := daemon.hub.ProcessManager().Get(makeProcessID(tmpDir, "web"))
		return err == nil
	}, 3*time.Second, 25*time.Millisecond, "web must launch when api signals ready")
}

// TestStartProcessGroup_EmptyProcesses: empty processes list is a
// validation error.
func TestStartProcessGroup_EmptyProcesses(t *testing.T) {
	t.Parallel()
	daemon, _, tmpDir := newHubProcTestDaemon(t)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	result := daemon.StartProcessGroup(ctx, tmpDir, nil, 0)
	require.Error(t, result.Err)
	assert.Contains(t, result.Err.Error(), "empty")
}

// TestStartProcessGroup_MissingCommand: a process without `run` or
// `command` is a validation error.
func TestStartProcessGroup_MissingCommand(t *testing.T) {
	t.Parallel()
	daemon, _, tmpDir := newHubProcTestDaemon(t)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	result := daemon.StartProcessGroup(ctx, tmpDir, []protocol.GroupProcess{
		{Name: "broken"},
	}, 0)
	require.Error(t, result.Err)
	assert.Contains(t, result.Err.Error(), "requires `run` or `command`")
}

// TestHubHandleProcRun_WithDepsExposesWaitingFor: end-to-end PROC RUN
// over the wire. PROC RUN with deps returns state="pending" + the
// dep list, and PROC STATUS reflects the same waiting_for entry.
func TestHubHandleProcRun_WithDepsExposesWaitingFor(t *testing.T) {
	// No t.Parallel(): starts real sleep process via dep resolution
	// signal; PID-reuse kills it under high concurrency.
	daemon, client, tmpDir := newHubProcTestDaemon(t)

	resp, err := client.ProcRun("api", daemonclient.ProcRunConfig{
		Run:              longRunningCmd(),
		ProjectPath:      tmpDir,
		DependsOn:        []string{"db"},
		DependsOnTimeout: 30,
	})
	require.NoError(t, err)
	assert.Equal(t, "pending", resp["state"])

	waitingForRaw, ok := resp["waiting_for"].([]interface{})
	require.True(t, ok, "waiting_for must be present in PROC RUN response")
	require.Len(t, waitingForRaw, 1)
	assert.Equal(t, "db", waitingForRaw[0])

	// PROC STATUS must surface the same waiting_for.
	status, err := client.ProcStatus(makeProcessID(tmpDir, "api"))
	require.NoError(t, err)
	assert.Equal(t, "pending", status["state"])
	statusWaiting, ok := status["waiting_for"].([]interface{})
	require.True(t, ok)
	require.Len(t, statusWaiting, 1)
	assert.Equal(t, "db", statusWaiting[0])

	// Resolve the dep and ensure status flips.
	daemon.readySignaler.SignalReady(makeProcessID(tmpDir, "db"))

	require.Eventually(t, func() bool {
		s, err := client.ProcStatus(makeProcessID(tmpDir, "api"))
		if err != nil {
			return false
		}
		return s["state"] == "running"
	}, 3*time.Second, 50*time.Millisecond)

	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = daemon.hub.ProcessManager().Stop(stopCtx, makeProcessID(tmpDir, "api"))
	})
}

// TestHubHandleProcRunGroup_CycleReturnsError: end-to-end PROC RUN-GROUP
// with a cycle returns an error response and starts no process.
func TestHubHandleProcRunGroup_CycleReturnsError(t *testing.T) {
	t.Parallel()
	daemon, client, tmpDir := newHubProcTestDaemon(t)

	resp, err := client.ProcRunGroup(daemonclient.ProcRunGroupConfig{
		ProjectPath: tmpDir,
		Processes: []protocol.GroupProcess{
			{Name: "a", Run: longRunningCmd(), DependsOn: []string{"b"}},
			{Name: "b", Run: longRunningCmd(), DependsOn: []string{"a"}},
		},
	})
	require.Error(t, err, "PROC RUN-GROUP must return error on cycle (got resp=%+v)", resp)
	assert.Contains(t, err.Error(), "circular dependency")

	// Verify nothing was started.
	for _, name := range []string{"a", "b"} {
		_, gerr := daemon.hub.ProcessManager().Get(makeProcessID(tmpDir, name))
		assert.Error(t, gerr, "process %q must not exist after cycle detection", name)
	}
}

// TestHubHandleProcList_IncludesPendingEntries: PROC LIST must surface
// pending processes that haven't launched yet so the agent can see
// `waiting_for` for them.
func TestHubHandleProcList_IncludesPendingEntries(t *testing.T) {
	t.Parallel()
	daemon, client, tmpDir := newHubProcTestDaemon(t)

	// Register one pending process directly via the tracker — avoids the
	// goroutine race with launching real processes for this test.
	processID := makeProcessID(tmpDir, "pending-api")
	daemon.pendingProcs.Register(PendingProcess{
		ProcessID:   processID,
		Name:        "pending-api",
		ProjectPath: tmpDir,
		Command:     "go run ./cmd/api",
	}, []string{"db", "redis"})
	t.Cleanup(func() { daemon.pendingProcs.Remove(processID) })

	// Use Global to bypass session lookup.
	resp, err := client.ProcList(protocol.DirectoryFilter{Global: true})
	require.NoError(t, err)

	procs, ok := resp["processes"].([]interface{})
	require.True(t, ok)

	var found map[string]interface{}
	for _, p := range procs {
		if pm, ok := p.(map[string]interface{}); ok {
			if pm["id"] == processID {
				found = pm
				break
			}
		}
	}
	require.NotNil(t, found, "PROC LIST must include pending entry %q", processID)
	assert.Equal(t, "pending", found["state"])
	waiting, ok := found["waiting_for"].([]interface{})
	require.True(t, ok)
	assert.ElementsMatch(t, []interface{}{"db", "redis"}, waiting)
}

// TestStartProcessWithDeps_DefaultTimeoutApplied: zero timeout means
// use DefaultDependsOnTimeout (30s). We verify by inspecting the
// pending entry's deadline rather than waiting 30s.
func TestStartProcessWithDeps_DefaultTimeoutApplied(t *testing.T) {
	t.Parallel()
	daemon, _, tmpDir := newHubProcTestDaemon(t)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	before := time.Now()
	result := daemon.StartProcessWithDeps(ctx, "api",
		&config.ScriptConfig{Run: longRunningCmd()},
		tmpDir, []string{"db"}, 0) // 0 → default
	require.NoError(t, result.Err)

	pending, ok := daemon.pendingProcs.Get(result.ProcessID)
	require.True(t, ok)
	deadline := pending.Deadline
	require.False(t, deadline.IsZero(), "deadline must be set for default-timeout case")
	assert.WithinDuration(t, before.Add(DefaultDependsOnTimeout), deadline, 1*time.Second,
		"deadline should be ~30s from now")

	t.Cleanup(func() { daemon.pendingProcs.Remove(result.ProcessID) })
}

// TestStartProcessWithDeps_ParentCtxCancelDoesNotMarkFailed: when the
// parent context is cancelled (e.g. session shutdown), the dependent
// must not be marked PendingFailed — that would surface as
// "dependency_timeout" in the agent UI even though the agent never
// asked for cancellation. Cancellation is silent.
func TestStartProcessWithDeps_ParentCtxCancelDoesNotMarkFailed(t *testing.T) {
	t.Parallel()
	daemon, _, tmpDir := newHubProcTestDaemon(t)

	ctx, cancel := context.WithCancel(context.Background())

	// Use a long timeout (negative = unbounded for the dep wait, so
	// only the parent ctx can stop us).
	result := daemon.StartProcessWithDeps(ctx, "api",
		&config.ScriptConfig{Run: longRunningCmd()},
		tmpDir, []string{"db"}, -1*time.Second)
	require.NoError(t, result.Err)
	assert.Equal(t, "pending", result.State)

	// Cancel the parent ctx before the dep ever signals ready.
	cancel()

	// Give the goroutine a moment to react.
	time.Sleep(100 * time.Millisecond)

	// The pending entry should remain in PendingWaiting, NOT
	// PendingFailed.
	pending, ok := daemon.pendingProcs.Get(result.ProcessID)
	require.True(t, ok)
	assert.Equal(t, PendingWaiting, pending.State,
		"parent-ctx cancellation must not transition the entry to Failed")
	assert.Empty(t, pending.FailureReason)

	t.Cleanup(func() { daemon.pendingProcs.Remove(result.ProcessID) })
}

// lastColon returns the index of the last ':' or -1.
func lastColon(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			return i
		}
	}
	return -1
}
