package daemon

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/platform"
	"github.com/standardbeagle/agnt/internal/selflog"
	"github.com/standardbeagle/go-cli-server/script"
)

// defaultCleanupGracePeriod is used when config has no explicit value.
const defaultCleanupGracePeriod = 5 * time.Second

// sessionPGIDGracePeriod is how long we give the PTY child session pgid
// to respond to SIGTERM before escalating to SIGKILL. Kept short because
// this fires AFTER the agnt run process has already exited — anything
// still in the pgid at this point is an orphaned background job (e.g.
// a `npm run dev &` the coding agent forgot to stop), not an interactive
// process that might print a shutdown banner.
const sessionPGIDGracePeriod = 2 * time.Second

// killSessionPGID reaps every process that inherited the session pgid
// from the PTY child. No-op on Windows (SessionPGID is always 0) or when
// the client didn't report a pgid at registration time. Self-exclusion
// protects the daemon process in the (defensive) case where they
// accidentally share a pgid.
func (d *Daemon) killSessionPGID(session *Session) {
	session.mu.RLock()
	pgid := session.SessionPGID
	code := session.Code
	session.mu.RUnlock()

	if pgid <= 1 {
		return
	}

	debug.Log("daemon", "session %s: killing pgid %d (grace=%s)", code, pgid, sessionPGIDGracePeriod)

	// Record the kill where the user can find it. This reaps the session's
	// whole process group — including the coding agent itself — so from the
	// user's seat the agent is killed by a signal with no explanation, and
	// `agnt run`'s shutdown banner can only report "terminated by a signal"
	// without being able to name the sender. debug.Log alone does not surface
	// it (debug file, off by default), which made agnt's own kill the one
	// cause a user could never diagnose.
	selflog.Record("daemon", "reaped session %s process group (pgid %d) on session cleanup — any agent running in it was terminated", code, pgid)

	if err := platform.KillSessionPGID(pgid, os.Getpid(), sessionPGIDGracePeriod, false); err != nil {
		debug.Warn("daemon", "session %s: killpg(%d) failed: %v", code, pgid, err)
		d.daemonStartupLog("warning", "session_pgid_kill_failed",
			fmt.Sprintf("session %s: failed to reap pgid %d: %v", code, pgid, err))
	}
}

// killSessionJobObject is the Windows equivalent of killSessionPGID: it
// terminates every process assigned to the PTY child's Job Object and
// closes the handle. No-op on Unix (SessionJobHandle is always 0 — the
// stub in platform/sessionjob_other.go returns nil) or when the client
// didn't report a handle.
//
// This coexists with killSessionPGID at the dispatch level: on Unix
// only pgid is active, on Windows only the job handle is active. Both
// are called unconditionally from doCleanup because each no-ops on the
// wrong platform.
//
// Handle lifetime caveat: Windows job handles are per-process. The
// handle reported by `agnt run` is only valid in the daemon process if
// the daemon and `agnt run` are the same process (unusual) or if the
// handle has been duplicated explicitly across processes. In the
// common case an explicit TerminateJobObject from the daemon will
// fail with ERROR_INVALID_HANDLE — we log that as a warning but do
// NOT treat it as fatal because the owning `agnt run` process still
// has JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE as a backstop when its
// handle is finally closed.
func (d *Daemon) killSessionJobObject(session *Session) {
	session.mu.RLock()
	handle := session.SessionJobHandle
	code := session.Code
	session.mu.RUnlock()

	if handle == 0 {
		return
	}

	debug.Log("daemon", "session %s: terminating job object 0x%x", code, handle)

	if err := platform.KillSessionJobObject(uintptr(handle)); err != nil {
		debug.Warn("daemon", "session %s: TerminateJobObject(0x%x) failed: %v", code, handle, err)
		d.daemonStartupLog("warning", "session_job_kill_failed",
			fmt.Sprintf("session %s: failed to terminate job 0x%x: %v", code, handle, err))
	}
}

func (d *Daemon) cleanupGracePeriod() time.Duration {
	if d.config.CleanupGracePeriod > 0 {
		return d.config.CleanupGracePeriod
	}
	return defaultCleanupGracePeriod
}

// cancelPendingCleanup cancels any deferred cleanup for the given session code.
// Called when a session is re-registered (reconnection) to prevent killing
// processes that are still running.
func (d *Daemon) cancelPendingCleanup(sessionCode string) {
	if val, ok := d.pendingCleanups.LoadAndDelete(sessionCode); ok {
		val.(*time.Timer).Stop()
		debug.Log("daemon", "cancelled pending cleanup for session %s (re-registered)", sessionCode)
	}
}

// drainPendingCleanups stops every scheduled deferred-cleanup timer and clears
// the map. Called once during Stop() before the managers those timers' callbacks
// touch are torn down. A timer that already fired (Stop returns false) is
// additionally short-circuited by the isShuttingDown() guard in its callback.
func (d *Daemon) drainPendingCleanups() {
	d.pendingCleanups.Range(func(key, val any) bool {
		if t, ok := val.(*time.Timer); ok {
			t.Stop()
		}
		d.pendingCleanups.Delete(key)
		return true
	})
}

// CleanupSessionResources performs immediate session resource cleanup.
// Used for explicit UNREGISTER and direct calls. For connection drops, use
// CleanupSessionResourcesDeferred instead.
func (d *Daemon) CleanupSessionResources(sessionCode string) {
	d.forwardMappings.Delete(sessionCode)
	// Cancel any pending deferred cleanup first
	d.cancelPendingCleanup(sessionCode)
	d.doCleanup(sessionCode)
}

// CleanupSessionResourcesDeferred schedules resource cleanup with a grace period.
// Called when a connection drops unexpectedly. The ResilientClient may reconnect
// and re-register the same session within seconds — the grace period prevents
// killing processes during that window. If the session is re-registered before
// the timer fires, the cleanup is cancelled entirely.
func (d *Daemon) CleanupSessionResourcesDeferred(sessionCode string) {
	// Get session to find project path
	session, ok := d.sessionRegistry.Get(sessionCode)
	if !ok {
		debug.Log("daemon", "session %s not found for deferred cleanup", sessionCode)
		return
	}

	// A session-host session's PTY child is reaped only by SESSION-HOST KILL, by
	// the child exiting, or by the startup orphan scan. A dropped connection is
	// explicitly not one of those cases — surviving client disconnect is the
	// entire point of session-host. doCleanup guards this too, but the
	// no-project branch below reaps the pgid without going through it.
	if session.Kind == SessionKindSessionHost {
		debug.Log("daemon", "session %s is session-host: skipping deferred cleanup (explicit-kill-only)", sessionCode)
		return
	}

	projectPath := session.ProjectPath
	grace := d.cleanupGracePeriod()

	if projectPath == "" {
		// A session with no project path still owns its PTY child's pgid and any
		// backgrounded jobs under it, so the kill cannot be skipped. But it must
		// not be immediate either: this path is a dropped connection, not an
		// unregister, and an acp one-shot or cooked REPL that reconnects within
		// the grace window would have had its process tree reaped out from under
		// it — while every project-scoped session gets that window.
		//
		// Reaping inline also blocked the hub's disconnect callback for the full
		// SIGTERM→SIGKILL escalation (up to 2s).
		debug.Log("daemon", "deferring pgid-only cleanup for session %s (no project, grace: %s)", sessionCode, grace)
		d.cancelPendingCleanup(sessionCode)
		timer := time.AfterFunc(grace, func() {
			d.pendingCleanups.Delete(sessionCode)

			d.shutdownMu.Lock()
			if d.shutdown {
				d.shutdownMu.Unlock()
				return
			}
			d.wg.Add(1)
			d.shutdownMu.Unlock()
			defer d.wg.Done()

			s, ok := d.sessionRegistry.Get(sessionCode)
			if !ok {
				return
			}
			if s.Kind == SessionKindSessionHost {
				return // re-registered as a session-host session; explicit-kill-only
			}
			if time.Since(s.GetLastSeen()) < grace {
				debug.Log("daemon", "skipping deferred cleanup for session %s — re-registered during grace period", sessionCode)
				return
			}
			// Both calls no-op on the wrong platform. Mirrors the
			// empty-projectPath branch in doCleanup.
			d.killSessionPGID(s)
			d.killSessionJobObject(s)
			d.sessionRegistry.Unregister(sessionCode)
		})
		d.pendingCleanups.Store(sessionCode, timer)
		return
	}

	debug.Log("daemon", "deferring cleanup for session %s (project: %s, grace: %s)", sessionCode, projectPath, grace)

	// Cancel any previously scheduled cleanup for this session (e.g., rapid
	// reconnect/disconnect cycles).
	d.cancelPendingCleanup(sessionCode)

	// Schedule the actual cleanup. If the session is re-registered before the
	// timer fires (ResilientClient reconnect → OnReconnect → SessionRegister),
	// cancelPendingCleanup will cancel this timer and processes stay alive.
	timer := time.AfterFunc(grace, func() {
		d.pendingCleanups.Delete(sessionCode)

		// Register with the shutdown waitgroup so the process cannot exit while a
		// cleanup is in flight. This does NOT order the cleanup before the
		// managers it touches: Stop()'s d.wg.Wait() runs after hub.Stop() and
		// proxym.Shutdown(), so a cleanup that started just before Stop runs
		// concurrently with that teardown. It is safe because each manager is
		// individually shutdown-safe, not because the waitgroup sequences them.
		//
		// The wg.Add must be atomic with the shutdown check: Stop() sets
		// d.shutdown under shutdownMu before it calls d.wg.Wait(), so taking the
		// same lock guarantees we either bail (shutting down — Stop reaps
		// resources itself) or Add(1) strictly before Wait, never Add-after-Wait.
		d.shutdownMu.Lock()
		if d.shutdown {
			d.shutdownMu.Unlock()
			return
		}
		d.wg.Add(1)
		d.shutdownMu.Unlock()
		defer d.wg.Done()

		if s, ok := d.sessionRegistry.Get(sessionCode); ok {
			if time.Since(s.GetLastSeen()) < grace {
				debug.Log("daemon", "skipping deferred cleanup for session %s — re-registered during grace period", sessionCode)
				return
			}
		}

		d.doCleanup(sessionCode)
	})
	d.pendingCleanups.Store(sessionCode, timer)
}

// doCleanup performs the actual session resource cleanup.
func (d *Daemon) doCleanup(sessionCode string) {
	d.forwardMappings.Delete(sessionCode)
	session, ok := d.sessionRegistry.Get(sessionCode)
	if !ok {
		debug.Log("daemon", "session %s not found for cleanup", sessionCode)
		return
	}

	// Session-host sessions are explicit-kill-only (spec §2.2 invariant 11,
	// .claude/rules/daemon-architecture.md § Session Containment). Nothing
	// should route a session-host session's code into doCleanup in the
	// first place (SESSION-HOST ATTACH never calls conn.SetSessionCode), but
	// this guard is belt-and-braces: if it ever does, doCleanup must not
	// reap the PTY pgid or tear down project resources — only
	// SESSION-HOST KILL (hub_sessionhost.go) may do that.
	if session.Kind == SessionKindSessionHost {
		debug.Log("daemon", "session %s is session-host: skipping doCleanup teardown (explicit-kill-only)", sessionCode)
		return
	}

	projectPath := session.ProjectPath
	if projectPath == "" {
		debug.Log("daemon", "session %s has no project path, skipping resource cleanup", sessionCode)
		// Still try to reap descendants on both platforms — the pgid
		// path no-ops on Windows and the job path no-ops on Unix, so
		// invoking both is always safe.
		d.killSessionPGID(session)
		d.killSessionJobObject(session)
		d.sessionRegistry.Unregister(sessionCode)
		return
	}

	debug.Log("daemon", "cleaning up resources for session %s (project: %s)", sessionCode, projectPath)

	// Reap every process in the PTY child's session pgid / Job Object
	// BEFORE we touch managed processes or proxies. This catches
	// background jobs the coding agent spawned through non-interactive
	// bash (e.g. `npm run dev &`) that the daemon has no explicit
	// handle on. Managed `proc run` children live in their own pgids
	// and are stopped separately below via ProcessManager — losing
	// these calls would leak anything the agent started outside the
	// daemon's view.
	//
	// Both calls are issued unconditionally: pgid cleanup no-ops on
	// Windows (SessionPGID is always 0 there) and job-object cleanup
	// no-ops on Unix (SessionJobHandle is always 0 there). The platform
	// package's build-tagged stubs guarantee cross-platform safety.
	d.killSessionPGID(session)
	d.killSessionJobObject(session)

	// Handle script ownership transfer or cleanup.
	// Remove the session as observer first, then check ownership.
	var orphanedProcessIDs []string
	for _, entry := range d.scriptRegistry.List(projectPath) {
		entry.RemoveSession(sessionCode)

		if entry.Owner() != sessionCode {
			continue
		}

		// This session owns this script — transfer or stop
		newOwner := entry.TransferOwnership()
		if newOwner != "" {
			debug.Log("daemon", "transferred ownership of script %s from %s to %s", entry.Name, sessionCode, newOwner)
		} else {
			debug.Log("daemon", "no observers for script %s, marking for stop", entry.Name)
			entry.SetState(script.StateStopped)
			orphanedProcessIDs = append(orphanedProcessIDs, entry.ProcessID)
		}
	}

	// Use a reasonable timeout for cleanup
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Check if any other sessions remain for this project
	hasOtherSessions := false
	for _, s := range d.sessionRegistry.List(projectPath, false) {
		if s.Code != sessionCode {
			hasOtherSessions = true
			break
		}
	}

	var wg sync.WaitGroup

	if !hasOtherSessions {
		// Last session for this project — stop all proxies and browsers
		wg.Add(1)
		go func() {
			defer wg.Done()
			stoppedIDs, err := d.proxym.StopByProjectPath(ctx, projectPath)
			if err != nil {
				debug.Log("daemon", "error stopping proxies for project %s: %v", projectPath, err)
				d.daemonStartupLog("warning", "proxy_stop_failed",
					fmt.Sprintf("error stopping proxies for project %s: %v", projectPath, err))
			}
			if len(stoppedIDs) > 0 {
				debug.Log("daemon", "stopped proxies: %v", stoppedIDs)
				if d.stateMgr != nil {
					for _, id := range stoppedIDs {
						d.stateMgr.RemoveProxy(id)
					}
				}
			}
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			stoppedIDs, err := d.browserm.StopByProjectPath(ctx, projectPath)
			if err != nil {
				debug.Log("daemon", "error stopping browsers for project %s: %v", projectPath, err)
				d.daemonStartupLog("warning", "stop_failed",
					fmt.Sprintf("error stopping browsers for project %s: %v", projectPath, err))
			}
			if len(stoppedIDs) > 0 {
				debug.Log("daemon", "stopped browsers: %v", stoppedIDs)
			}
		}()
	}

	// Stop only orphaned script processes (no remaining observers)
	if len(orphanedProcessIDs) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pm := d.hub.ProcessManager()
			for _, pid := range orphanedProcessIDs {
				if err := pm.Stop(ctx, pid); err != nil {
					debug.Log("daemon", "error stopping orphaned process %s: %v", pid, err)
					d.recordStartupEntry(pid, "", "warning", "stop_failed",
						fmt.Sprintf("error stopping orphaned process: %v", err), 0)
				} else {
					debug.Log("daemon", "stopped orphaned process %s", pid)
					if d.autoRestarter != nil {
						d.autoRestarter.Unregister(pid)
					}
				}
			}
		}()
	} else if !hasOtherSessions {
		// No orphaned scripts but last session — stop all remaining processes
		wg.Add(1)
		go func() {
			defer wg.Done()
			stoppedIDs, err := d.hub.ProcessManager().StopByProjectPath(ctx, projectPath)
			if err != nil {
				debug.Log("daemon", "error stopping processes for project %s: %v", projectPath, err)
				d.daemonStartupLog("warning", "stop_failed",
					fmt.Sprintf("error stopping processes for project %s: %v", projectPath, err))
			}
			if len(stoppedIDs) > 0 {
				debug.Log("daemon", "stopped processes: %v", stoppedIDs)
				if d.autoRestarter != nil {
					for _, id := range stoppedIDs {
						d.autoRestarter.Unregister(id)
					}
				}
			}
		}()
	}

	wg.Wait()

	// Remove orphaned script entries from the registry.
	// This must happen after processes are stopped to avoid race conditions.
	if !hasOtherSessions {
		// Last session: remove ALL script entries for this project.
		// The next session will re-register from current .agnt.kdl config.
		for _, entry := range d.scriptRegistry.List(projectPath) {
			d.scriptRegistry.Remove(entry.Name, projectPath)
			d.scriptConfigs.Delete(entry.ProcessID)
		}

		// Drop any PROC RUN pending entries for this project. The
		// dependent goroutines exit on the next ctx.Done() / dep wait
		// failure; the tracker entry is the only durable artefact and
		// would otherwise leak into the next session's PROC LIST.
		if d.pendingProcs != nil {
			for _, pending := range d.pendingProcs.ListByProject(projectPath) {
				d.pendingProcs.Remove(pending.ProcessID)
			}
		}

		// Clear proxy-kind admin entries registered by handleExplicitStart.
		// StopByProjectPath already stopped the underlying proxies and removed
		// them from stateMgr above — this sweep drops the admin-surface
		// projection so SCRIPT LIST on the next session doesn't render a
		// phantom indicator for a proxy that no longer exists.
		//
		// Also drops any script→proxy reverse-index entries populated by
		// handleExplicitStart for script-linked explicit proxies, so
		// clearScriptProxies doesn't leak a stale mapping across sessions.
		if d.proxyEntries != nil {
			for _, pe := range d.proxyEntries.List(projectPath) {
				d.proxyEntries.Remove(pe.ProjectPath(), pe.Name())
				if scriptID := d.linkedScriptForProxy(pe.ProxyID()); scriptID != "" {
					d.clearScriptProxies(scriptID)
				}
			}
		}

		debug.Log("daemon", "cleared script registry for project %s (last session)", projectPath)

		// Clear this project's startup log — next session starts fresh.
		// Scoped by the project's ProcessID prefix so tearing down this
		// project's last session does not wipe other projects' startup logs
		// from the shared ring buffer.
		d.startupErrorStore.ClearByPrefix(makeProcessID(projectPath, ""))

		// Retention trigger 3: clear the project's alert-ring entries too —
		// the next session must not inherit errors from processes that were
		// just torn down. Pinned errors survive (they live outside the ring).
		d.maybeRetireOnSessionEnd(projectPath)

		// Cancel any in-flight autostart run for this project, then drop the
		// handle so the next session triggers a fresh autostart. Matching the
		// "script registry is ephemeral" rule: autostart state does not carry
		// over between sessions. Cancel must precede Remove — Cancel(projectPath)
		// looks the handle up by key and is a no-op once the entry is gone.
		if d.autostartManager != nil {
			d.autostartManager.Cancel(projectPath)
			d.autostartManager.Remove(projectPath)
		}
	} else {
		// Other sessions remain: only remove entries that were orphaned (no observers).
		for _, pid := range orphanedProcessIDs {
			if entry, ok := d.scriptRegistry.GetByProcessID(pid); ok {
				d.scriptRegistry.Remove(entry.Name, entry.ProjectPath)
				d.scriptConfigs.Delete(pid)
			}
		}
	}

	// Unregister the session
	if err := d.sessionRegistry.Unregister(sessionCode); err != nil {
		debug.Log("daemon", "error unregistering session %s: %v", sessionCode, err)
	}

	if d.incidentBus != nil {
		d.incidentBus.RemoveSession(sessionCode)
	}
	d.forwardingPaused.Delete(sessionCode)

	debug.Log("daemon", "session %s cleanup complete", sessionCode)
}
