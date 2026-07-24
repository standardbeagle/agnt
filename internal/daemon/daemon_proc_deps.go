// Package daemon — PROC RUN dependency wait + group orchestration.
//
// This file is the daemon-side implementation of the `depends_on` contract
// exposed by the `proc {action:"run", depends_on:[...]}` and
// `proc {action:"run_group", processes:[...]}` MCP tool actions.
//
// Single-process flow (PROC RUN with deps):
//
//	hubHandleProcRun (deps != nil)
//	  → StartProcessWithDeps(name, scriptCfg, projectPath, deps, timeout)
//	    → pendingProcs.Register(...)
//	    → goroutine waits on readySignaler.WaitReadyCtx for each dep
//	      → dep ready: pendingProcs.MarkReady(processID, dep)
//	      → dep timeout: pendingProcs.MarkFailed(processID, dep) + script
//	        registry StateFailed + abort
//	    → all deps ready: StartScriptExplicit + pendingProcs.Remove
//
// Group flow (PROC RUN-GROUP):
//
//	hubHandleProcRunGroup
//	  → cycle detection via config.TopologicalSort (returns error if cyclic)
//	  → for each script in deterministic order:
//	      → register pending entry (if has deps)
//	      → spawn StartProcessWithDeps goroutine
//	  → return all process IDs immediately
//
// Cycle detection runs BEFORE any process is launched. This is the
// "Dependency cycle → immediate error before any process starts"
// acceptance criterion.

package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/go-cli-server/script"
)

// DefaultDependsOnTimeout is the per-process default deadline applied
// when a PROC RUN payload supplies `depends_on` without an explicit
// `depends_on_timeout` value. Matches the spec's documented 30s default.
//
// This default applies only to the MCP tool surface — the autostart
// (.agnt.kdl) path continues to default to "wait indefinitely" per
// .claude/rules/daemon-lifecycle.md, which is correct for that path
// because the parent context is the session lifetime. The MCP tool
// surface needs a hard upper bound so an agent that mistypes a
// dependency name does not strand the dependent forever.
const DefaultDependsOnTimeout = 30 * time.Second

// StartProcessResult captures the outcome of a deferred PROC RUN launch
// kicked off by StartProcessWithDeps.
//
// Used by RunGroup to report per-process kickoff status. The actual
// launch happens asynchronously — this struct only reports what
// happened up to the point where StartProcessWithDeps returned.
type StartProcessResult struct {
	// ProcessID is the daemon-side identifier (project_path + name).
	ProcessID string
	// State is the immediate state observed by the caller:
	//   "starting" — no deps, launch was synchronous and succeeded.
	//   "pending"  — deps exist, launch is deferred until all deps ready.
	//   "failed"   — synchronous launch failed (returned in Err).
	State string
	// WaitingFor is populated when State == "pending"; the set of dep
	// names the launcher will block on.
	WaitingFor []string
	// Err is non-nil only when the synchronous launch path failed
	// (no-deps case). Async failures land on the script registry's
	// StateFailed and the pending tracker's PendingFailed.
	Err error
}

// StartProcessWithDeps starts a process via PROC RUN, optionally gated on
// `depends_on` dependencies.
//
// If deps is empty: delegates synchronously to StartScriptExplicit.
// If deps is non-empty: registers a pending entry, spawns a wait goroutine,
// and returns immediately with State == "pending".
//
// timeout applies to the entire dependency wait window. Zero means use
// DefaultDependsOnTimeout. Negative means wait indefinitely (parent ctx
// only) — only intended for callers that explicitly want autostart-style
// "wait until session dies" semantics.
//
// The launch goroutine resolves dependencies in declaration order. The
// per-dep wait uses a derived context that is cancelled by either the
// per-process deadline or the parent ctx. On dependency timeout, the
// pending entry is marked PendingFailed with reason
// "dependency_timeout:<dep>" and the script registry entry transitions to
// StateFailed before the goroutine exits. The dependent process is NOT
// launched on timeout.
func (d *Daemon) StartProcessWithDeps(
	ctx context.Context,
	name string,
	scriptCfg *config.ScriptConfig,
	projectPath string,
	deps []string,
	timeout time.Duration,
) StartProcessResult {
	processID := makeProcessID(projectPath, name)

	// Fast path: no dependencies — delegate to StartScriptExplicit.
	if len(deps) == 0 {
		if err := d.StartScriptExplicit(ctx, name, scriptCfg, projectPath, nil); err != nil {
			return StartProcessResult{ProcessID: processID, State: "failed", Err: err}
		}
		return StartProcessResult{ProcessID: processID, State: "starting"}
	}

	// Dep wait: register the pending entry FIRST so PROC STATUS /
	// PROC LIST can report `waiting_for` before the goroutine is
	// scheduled.
	cmdStr := resolveCommandString(scriptCfg)
	deadline := time.Time{}
	effectiveTimeout := timeout
	if effectiveTimeout == 0 {
		effectiveTimeout = DefaultDependsOnTimeout
	}
	if effectiveTimeout > 0 {
		deadline = time.Now().Add(effectiveTimeout)
	}

	d.pendingProcs.Register(PendingProcess{
		ProcessID:   processID,
		Name:        name,
		ProjectPath: projectPath,
		Command:     cmdStr,
		Deadline:    deadline,
	}, deps)

	// NOTE: deliberately do NOT pre-register the script registry entry
	// as Starting here. StartScriptExplicit has an idempotency check
	// that skips the launch if the script entry is already in
	// StateStarting (the "already running, skipping" fast path), which
	// would mean the post-dep launch becomes a no-op and the process
	// never enters ProcessManager. The pending tracker is the single
	// source of truth for "waiting on deps"; SCRIPT LIST surfaces it
	// via the merge in hubHandleProcList.
	go d.runProcessAfterDeps(ctx, name, scriptCfg, projectPath, deps, effectiveTimeout)

	return StartProcessResult{
		ProcessID:  processID,
		State:      "pending",
		WaitingFor: append([]string(nil), deps...),
	}
}

// runProcessAfterDeps is the goroutine spawned by StartProcessWithDeps
// when the process has at least one dependency. It blocks on each dep's
// readiness signal in declaration order, then invokes
// StartScriptExplicit. On timeout, it marks the pending entry Failed and
// transitions the script registry entry to StateFailed.
//
// This function never returns to a caller — its only side effects are
// (a) the script registry / process manager (via StartScriptExplicit)
// and (b) the pending tracker (Remove on success, MarkFailed on
// timeout). Errors are surfaced via startupErrorStore for visibility in
// PROC STATUS warnings.
func (d *Daemon) runProcessAfterDeps(
	ctx context.Context,
	name string,
	scriptCfg *config.ScriptConfig,
	projectPath string,
	deps []string,
	timeout time.Duration,
) {
	processID := makeProcessID(projectPath, name)

	// Build a wait context bounded by the per-process timeout.
	waitCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	for _, dep := range deps {
		depProcessID := makeProcessID(projectPath, dep)
		if err := d.readySignaler.WaitReadyCtx(depProcessID, waitCtx); err != nil {
			// Distinguish wait-context-cancelled (timeout) from
			// parent-cancelled (shutdown).
			parentDone := ctx.Err() != nil
			d.failPendingProcess(processID, name, projectPath, dep, parentDone, err)
			return
		}
		d.pendingProcs.MarkReady(processID, dep)
	}

	// All deps ready — kick off the actual start.
	if err := d.StartScriptExplicit(ctx, name, scriptCfg, projectPath, nil); err != nil {
		d.recordStartupEntry(processID, name, "error", "proc_run_failed",
			fmt.Sprintf("PROC RUN failed after deps: %v", err), 0)
		// Mark the pending entry failed so PROC STATUS reflects the
		// final state. Use a synthetic reason — the real error lives
		// in startupErrorStore.
		d.pendingProcs.MarkFailed(processID, "start")
		debug.Warn("daemon", "PROC RUN %s post-deps start failed: %v", name, err)
		return
	}
	d.pendingProcs.Remove(processID)
}

// failPendingProcess records a dep-wait failure for a PROC RUN process.
// Sets the pending tracker entry to PendingFailed, transitions the
// script registry entry to StateFailed with a structured LastError, and
// surfaces a startup-log entry so the failure is visible to PROC STATUS
// and the overlay.
//
// parentCancelled distinguishes "agent cancelled / daemon shutting down"
// from "dep wait timed out". Only the latter writes
// "dependency_timeout:<dep>" — the former is logged as a debug exit.
func (d *Daemon) failPendingProcess(processID, name, projectPath, dep string, parentCancelled bool, waitErr error) {
	if parentCancelled {
		debug.Log("daemon", "PROC RUN %s dep-wait cancelled by parent: %v", name, waitErr)
		// Leave the pending entry in place; the next session-cleanup
		// pass will remove it. We don't mark Failed because the
		// agent / daemon may be shutting down deliberately.
		return
	}
	d.pendingProcs.MarkFailed(processID, dep)

	if entry, ok := d.scriptRegistry.Get(name, projectPath); ok {
		entry.SetState(script.StateFailed)
		entry.SetLastError(fmt.Sprintf("dependency_timeout:%s", dep))
		entry.IncrementFailCount()
	}

	d.recordStartupEntry(processID, name, "error", "dependency_timeout",
		fmt.Sprintf("dependency_timeout:%s — %v", dep, waitErr), 0)
}

// resolveCommandString returns a best-effort human-readable command
// string for a ScriptConfig, used to populate PendingProcess.Command
// for visibility in PROC LIST output. Mirrors the behaviour of
// StartScriptExplicit's command resolution but does not invoke project
// detection (which would be expensive and pointless for display only).
func resolveCommandString(scriptCfg *config.ScriptConfig) string {
	if scriptCfg == nil {
		return ""
	}
	if scriptCfg.Run != "" {
		return scriptCfg.Run
	}
	if scriptCfg.Command != "" {
		if len(scriptCfg.Args) == 0 {
			return scriptCfg.Command
		}
		return scriptCfg.Command + " " + strings.Join(scriptCfg.Args, " ")
	}
	return ""
}

// GroupResult captures the outcome of a PROC RUN-GROUP launch.
type GroupResult struct {
	// Processes lists per-process kickoff results, in declaration order.
	Processes []StartProcessResult
	// Err is set when group launch failed before any process started
	// (cycle detection, missing deps). When non-nil, Processes is empty.
	Err error
}

// StartProcessGroup orchestrates a PROC RUN-GROUP launch.
//
// Performs cycle detection FIRST via config.TopologicalSort. If a cycle
// is detected, returns immediately with Err set — no process is launched.
// If a process declares a dep that does not exist in the group, that is
// also a fatal error reported through Err.
//
// Otherwise, all processes are kicked off via StartProcessWithDeps.
// Layer-zero processes (no deps) start synchronously; processes with
// deps spawn goroutines that wait on the readySignaler. The function
// returns when all kickoffs have been initiated — actual readiness is
// observed via PROC STATUS polling.
//
// timeout applies as the per-process default depends-on timeout when a
// protocol.GroupProcess does not specify its own. Pass 0 to use
// DefaultDependsOnTimeout.
func (d *Daemon) StartProcessGroup(
	ctx context.Context,
	projectPath string,
	processes []protocol.GroupProcess,
	timeout time.Duration,
) GroupResult {
	if len(processes) == 0 {
		return GroupResult{Err: errors.New("PROC RUN-GROUP: processes list cannot be empty")}
	}

	// Build a name → ScriptConfig map for cycle detection. The map is
	// keyed by the protocol.GroupProcess.Name field, which is what `depends_on`
	// references.
	scripts := make(map[string]*config.ScriptConfig, len(processes))
	for i := range processes {
		gp := &processes[i]
		if gp.Name == "" {
			return GroupResult{Err: fmt.Errorf("PROC RUN-GROUP: process[%d] missing name", i)}
		}
		if _, dup := scripts[gp.Name]; dup {
			return GroupResult{Err: fmt.Errorf("PROC RUN-GROUP: duplicate process name %q", gp.Name)}
		}
		if gp.Run == "" && gp.Command == "" {
			return GroupResult{Err: fmt.Errorf("PROC RUN-GROUP: process %q requires `run` or `command`", gp.Name)}
		}

		depList := make(config.DependsOnList, 0, len(gp.DependsOn))
		for _, dep := range gp.DependsOn {
			if dep == "" {
				continue
			}
			depList = append(depList, config.ScriptDependency{Name: dep})
		}
		scripts[gp.Name] = &config.ScriptConfig{
			Run:         gp.Run,
			Command:     gp.Command,
			Args:        gp.Args,
			Cwd:         gp.Cwd,
			Env:         gp.Env,
			URLMatchers: gp.URLMatchers,
			AutoRestart: gp.AutoRestart,
			DependsOn:   depList,
		}
	}

	// Cycle detection. TopologicalSort also fails on unknown-dependency
	// names, which subsumes the "depends on a process not in the group"
	// case. We discard the layer ordering — StartProcessWithDeps handles
	// per-process gating via the ready signaler, so the group does not
	// need a layer-by-layer wait.
	if _, err := config.TopologicalSort(scripts); err != nil {
		return GroupResult{Err: fmt.Errorf("PROC RUN-GROUP: %v", err)}
	}

	// All clear — kick off every process. Use a wait group only for
	// scratch correctness: we want the no-deps processes to have their
	// synchronous StartScriptExplicit complete before we return so the
	// caller observes "starting" / "running" rather than racing against
	// the goroutine.
	results := make([]StartProcessResult, len(processes))
	var wg sync.WaitGroup
	for i := range processes {
		gp := processes[i]
		idx := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[idx] = d.StartProcessWithDeps(
				ctx, gp.Name, scripts[gp.Name], projectPath,
				gp.DependsOn, timeout,
			)
		}()
	}
	wg.Wait()

	return GroupResult{Processes: results}
}
