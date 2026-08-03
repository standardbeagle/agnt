package daemon

import (
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
)

// Error retention: when the daemon retires stale errors so the agent's error
// view reflects the code that is actually running, not history.
//
// Three automatic triggers, each config-gated (alerts { retention { ... } },
// all default-on):
//
//  1. Build success — the AlertScanner's "rebuild-build-success" pattern
//     means the process rebuilt cleanly, so errors stamped at or before the
//     success line are retired. Timestamp-bounded: anything emitted after the
//     success signal survives.
//  2. Explicit PROC STOP / RESTART — an intentional lifecycle action starts a
//     fresh error slate for that process. Crash restarts never trigger this;
//     crash evidence must survive for diagnosis.
//  3. Last-session disconnect — doCleanup clears the project's alert-ring
//     entries alongside the startup log it already clears.
//
// Incidents the agent pinned (INCIDENTS PIN) are exempt from the inbox side of
// these triggers; the alert ring has no pin concept.

// buildSuccessPatternID is the classify pattern that certifies a clean build.
// Start-of-build signals (rebuild-generic etc.) intentionally do not clear.
const buildSuccessPatternID = "rebuild-build-success"

// SetRetentionConfig atomically installs the retention gate config.
// Follows the ApplyAlertsConfig pattern: applied on session connect,
// daemon-wide, last writer wins.
func (d *Daemon) SetRetentionConfig(cfg *config.RetentionConfig) {
	d.retentionCfg.Store(cfg)
}

func (d *Daemon) retention() *config.RetentionConfig {
	return d.retentionCfg.Load() // nil is valid: all getters default to enabled
}

// retireProcessErrors clears processID's errors stamped at or before the
// boundary from the alert ring and every session's incident inbox. Pinned
// copies are unaffected. The clear is logged with its trigger so retention
// activity is diagnosable, and the removed count is returned for surfacing
// in command responses (never a silent skip).
func (d *Daemon) retireProcessErrors(processID string, before time.Time, trigger string) int {
	if processID == "" {
		return 0
	}
	removed := d.alertStore.ClearProcessBefore(processID, before)
	if d.incidentBus != nil {
		// The bus boundary is trigger time, not the match-line time: incident
		// events are stamped ReceivedAt at publish, so a backdated match ts
		// would miss events already in flight. FIFO on the inbound channel
		// guarantees everything published before this call sorts below the
		// boundary, and anything published after it is stamped later.
		busBoundary := time.Now()
		if before.After(busBoundary) {
			busBoundary = before
		}
		d.incidentBus.ClearProcessBefore(processID, busBoundary)
	}
	if removed > 0 {
		debug.Log("retention", "retired %d alert(s) for process %s (trigger: %s)", removed, processID, trigger)
	}
	return removed
}

// maybeRetireOnBuildSuccess fires trigger 1 when the matched pattern is the
// build-success signal. ts is the success line's timestamp — the boundary.
func (d *Daemon) maybeRetireOnBuildSuccess(patternID, processID string, ts time.Time) {
	if patternID != buildSuccessPatternID || processID == "" {
		return
	}
	if !d.retention().ClearOnBuildSuccess() {
		return
	}
	d.retireProcessErrors(processID, ts, "build-success")
}

// maybeRetireOnProcStop fires trigger 2 for explicit PROC STOP / RESTART.
func (d *Daemon) maybeRetireOnProcStop(processID string) int {
	if !d.retention().ClearOnProcStop() {
		return 0
	}
	return d.retireProcessErrors(processID, time.Now(), "proc-stop")
}

// maybeRetireOnSessionEnd fires trigger 3 from doCleanup's last-session path.
func (d *Daemon) maybeRetireOnSessionEnd(projectPath string) {
	if projectPath == "" || !d.retention().ClearOnSessionEnd() {
		return
	}
	if removed := d.alertStore.ClearProject(projectPath); removed > 0 {
		debug.Log("retention", "retired %d alert(s) for project %s (trigger: session-end)", removed, projectPath)
	}
}
