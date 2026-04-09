package daemon

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/go-cli-server/script"
)

func (d *Daemon) CleanupSessionResources(sessionCode string) {
	// Get session to find project path
	session, ok := d.sessionRegistry.Get(sessionCode)
	if !ok {
		debug.Log("daemon", "session %s not found for cleanup", sessionCode)
		return
	}

	projectPath := session.ProjectPath
	if projectPath == "" {
		debug.Log("daemon", "session %s has no project path, skipping resource cleanup", sessionCode)
		// Still unregister the session
		d.sessionRegistry.Unregister(sessionCode)
		return
	}

	debug.Log("daemon", "cleaning up resources for session %s (project: %s)", sessionCode, projectPath)

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

// NOTE: acceptLoop is now handled by Hub - removed from Daemon.
// Session cleanup is registered with Hub via SetSessionCleanup() in Start().
