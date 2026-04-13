package daemon

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/platform"
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

	if err := platform.KillSessionPGID(pgid, os.Getpid(), sessionPGIDGracePeriod, false); err != nil {
		debug.Warn("daemon", "session %s: killpg(%d) failed: %v", code, pgid, err)
		d.startupErrorStore.Add(&StartupLogEntry{
			Level:     "warning",
			EventType: "session_pgid_kill_failed",
			Message:   fmt.Sprintf("session %s: failed to reap pgid %d: %v", code, pgid, err),
			Timestamp: time.Now(),
		})
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

// CleanupSessionResources performs immediate session resource cleanup.
// Used for explicit UNREGISTER and direct calls. For connection drops, use
// CleanupSessionResourcesDeferred instead.
func (d *Daemon) CleanupSessionResources(sessionCode string) {
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

	projectPath := session.ProjectPath
	if projectPath == "" {
		debug.Log("daemon", "session %s has no project path, skipping deferred cleanup", sessionCode)
		d.sessionRegistry.Unregister(sessionCode)
		return
	}

	grace := d.cleanupGracePeriod()

	debug.Log("daemon", "deferring cleanup for session %s (project: %s, grace: %s)", sessionCode, projectPath, grace)

	// Cancel any previously scheduled cleanup for this session (e.g., rapid
	// reconnect/disconnect cycles).
	d.cancelPendingCleanup(sessionCode)

	// Schedule the actual cleanup. If the session is re-registered before the
	// timer fires (ResilientClient reconnect → OnReconnect → SessionRegister),
	// cancelPendingCleanup will cancel this timer and processes stay alive.
	timer := time.AfterFunc(grace, func() {
		d.pendingCleanups.Delete(sessionCode)

		if s, ok := d.sessionRegistry.Get(sessionCode); ok {
			if time.Since(s.LastSeen) < grace {
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
	session, ok := d.sessionRegistry.Get(sessionCode)
	if !ok {
		debug.Log("daemon", "session %s not found for cleanup", sessionCode)
		return
	}

	projectPath := session.ProjectPath
	if projectPath == "" {
		debug.Log("daemon", "session %s has no project path, skipping resource cleanup", sessionCode)
		d.killSessionPGID(session) // still try to reap descendants
		d.sessionRegistry.Unregister(sessionCode)
		return
	}

	debug.Log("daemon", "cleaning up resources for session %s (project: %s)", sessionCode, projectPath)

	// Reap every process in the PTY child's session pgid BEFORE we touch
	// managed processes or proxies. This catches background jobs the
	// coding agent spawned through non-interactive bash (e.g.
	// `npm run dev &`) that the daemon has no explicit handle on.
	// Managed `proc run` children live in their own pgids and are
	// stopped separately below via ProcessManager — losing this call
	// would leak anything the agent started outside the daemon's view.
	d.killSessionPGID(session)

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
				d.startupErrorStore.Add(&StartupLogEntry{
					ProcessID: "",
					Level:     "warning",
					EventType: "proxy_stop_failed",
					Message:   fmt.Sprintf("error stopping proxies for project %s: %v", projectPath, err),
					Timestamp: time.Now(),
				})
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
				d.startupErrorStore.Add(&StartupLogEntry{
					ProcessID: "",
					Level:     "warning",
					EventType: "stop_failed",
					Message:   fmt.Sprintf("error stopping browsers for project %s: %v", projectPath, err),
					Timestamp: time.Now(),
				})
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
					d.startupErrorStore.Add(&StartupLogEntry{
						ProcessID: pid,
						Level:     "warning",
						EventType: "stop_failed",
						Message:   fmt.Sprintf("error stopping orphaned process: %v", err),
						Timestamp: time.Now(),
					})
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
				d.startupErrorStore.Add(&StartupLogEntry{
					ProcessID: "",
					Level:     "warning",
					EventType: "stop_failed",
					Message:   fmt.Sprintf("error stopping processes for project %s: %v", projectPath, err),
					Timestamp: time.Now(),
				})
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
		debug.Log("daemon", "cleared script registry for project %s (last session)", projectPath)

		// Clear session log — next session starts fresh
		d.startupErrorStore.Clear()

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

	debug.Log("daemon", "session %s cleanup complete", sessionCode)
}
