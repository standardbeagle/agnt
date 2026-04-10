package daemon

import (
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/go-cli-server/script"
)

// reconcileScriptStates verifies that all script entries for a project path
// have accurate state by probing the OS for PID liveness. Scripts in
// StateRunning or StateStarting whose managed process is gone (no
// ManagedProcess or dead PID) are transitioned to StateStopped and a
// ScriptStopped proxy event is emitted to clean up associated proxies.
//
// Returns the list of entries that were transitioned.
func (d *Daemon) reconcileScriptStates(projectPath string) []*script.Entry {
	var reconciled []*script.Entry

	for _, entry := range d.scriptRegistry.List(projectPath) {
		state := entry.State()
		if state != script.StateRunning && state != script.StateStarting {
			continue
		}

		// Look up the managed process by the script's ProcessID
		proc, err := d.hub.ProcessManager().Get(entry.ProcessID)
		if err != nil {
			// No managed process — the process was never started or was cleaned up.
			// Transition to Stopped.
			debug.Log("daemon", "reconcile: script %s (%s) has no managed process, transitioning %s -> stopped",
				entry.Name, entry.ProcessID, state)
			if entry.CompareAndSwapState(state, script.StateStopped) {
				reconciled = append(reconciled, entry)
				d.emitScriptStopped(entry.ProcessID)
			}
			continue
		}

		pid := proc.PID()
		if pid <= 0 {
			// Process registered but never actually started
			debug.Log("daemon", "reconcile: script %s (%s) has PID %d, transitioning %s -> stopped",
				entry.Name, entry.ProcessID, pid, state)
			if entry.CompareAndSwapState(state, script.StateStopped) {
				reconciled = append(reconciled, entry)
				d.emitScriptStopped(entry.ProcessID)
			}
			continue
		}

		if !pidAlive(pid) {
			debug.Log("daemon", "reconcile: script %s (PID %d) is dead, transitioning %s -> stopped",
				entry.Name, pid, state)
			if entry.CompareAndSwapState(state, script.StateStopped) {
				reconciled = append(reconciled, entry)
				d.emitScriptStopped(entry.ProcessID)
			}
		}
	}

	return reconciled
}

// emitScriptStopped sends a ScriptStopped proxy event for the given scriptID.
// Uses a non-blocking send — if the event channel is full, the event is dropped
// with a warning log.
func (d *Daemon) emitScriptStopped(scriptID string) {
	select {
	case d.proxyEvents <- ProxyEvent{
		Type:     ScriptStopped,
		ScriptID: scriptID,
	}:
	default:
		debug.Warn("daemon", "reconcile: proxy event channel full, dropping ScriptStopped for %s", scriptID)
	}
}
