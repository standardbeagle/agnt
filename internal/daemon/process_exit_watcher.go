// Package daemon — process_exit_watcher.go
//
// Every managed process is watched from start to exit. When a process
// transitions Running → Stopped or Running → Failed, the watcher captures
// exit metadata (code, reason, uptime, stderr tail) and publishes it two
// ways:
//
//  1. A process:lifecycle AlertEntry is pushed into the daemon's alertStore
//     so get_errors surfaces the death alongside proxy-side effects. This
//     closes the diagnostic gap where an agent sees proxy 502s but no
//     breadcrumb that the upstream dev server actually died.
//
//  2. A ProcessExitInfo record is stored in an in-memory, TTL-bounded side
//     map keyed by processID. proc status and proc list read from this map
//     and include last_exit_at / last_exit_code / last_exit_reason /
//     last_stderr_tail fields so a simple status call reveals the death.
//
// Retention is in-memory only — 10 minutes, or until the process restarts
// (whichever comes first). This is a deliberate scope decision: persistence
// across daemon restart would require a real event store, which is way out
// of scope for closing the immediate diagnostic gap.
//
// The watcher goroutine reads process state lock-free via the vendored
// ManagedProcess atomics. Exit info storage uses a small map protected
// by an RWMutex — sufficient for an in-memory store keyed by process ID
// that only grows on exit and is read by proc status/list handlers. No
// new mutex is introduced on the ManagedProcess struct itself.

package daemon

import (
	"fmt"
	"strconv"
	"sync"
	"time"

	goprocess "github.com/standardbeagle/go-cli-server/process"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
)

// CategoryProcessLifecycle is the AlertEntry category used for
// process-death events. Keeping it distinct from the AlertScanner-derived
// "process error" / "compile error" / etc. categories lets dedup in
// get_errors collapse a single death into exactly one unified error rather
// than merging it with unrelated output.
const CategoryProcessLifecycle = "process_lifecycle"

// defaultExitInfoRetention bounds how long a death record stays queryable
// in proc status / proc list after the process exits. Short enough to
// avoid stale data confusing the next session, long enough for an agent
// to correlate downstream proxy errors with the upstream death.
const defaultExitInfoRetention = 10 * time.Minute

// stderrTailLines is the number of lines of stderr captured on exit. Small
// enough to fit inside a status response, large enough to carry the panic
// / error stack that the agent needs.
const stderrTailLines = 15

// ProcessExitInfo is a snapshot of a process death. It is held in the
// daemon's in-memory exit-info store and rendered into proc status /
// proc list responses.
type ProcessExitInfo struct {
	ProcessID  string    `json:"process_id"`
	ExitCode   int       `json:"exit_code"`
	Reason     string    `json:"reason"` // "stopped" | "crash" | "signal"
	StartedAt  time.Time `json:"started_at,omitempty"`
	EndedAt    time.Time `json:"ended_at"`
	Uptime     time.Duration
	StderrTail string `json:"stderr_tail,omitempty"`
}

// processExitInfoStore is a TTL-bounded in-memory store of death records
// keyed by processID. Safe for concurrent use.
type processExitInfoStore struct {
	mu        sync.RWMutex
	entries   map[string]*ProcessExitInfo
	retention time.Duration
}

func newProcessExitInfoStore(retention time.Duration) *processExitInfoStore {
	if retention <= 0 {
		retention = defaultExitInfoRetention
	}
	return &processExitInfoStore{
		entries:   make(map[string]*ProcessExitInfo),
		retention: retention,
	}
}

// Set records a death. Overwrites any prior record for the same
// processID — matching the "10 min or until next start" contract.
func (s *processExitInfoStore) Set(info *ProcessExitInfo) {
	if s == nil || info == nil {
		return
	}
	s.mu.Lock()
	s.entries[info.ProcessID] = info
	s.mu.Unlock()
}

// Get returns the death record for processID if one is within the
// retention window. Evicts stale records on read. Returns (nil, false) if
// no record exists or it is too old.
func (s *processExitInfoStore) Get(processID string) (*ProcessExitInfo, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.RLock()
	info, ok := s.entries[processID]
	s.mu.RUnlock()
	if !ok || info == nil {
		return nil, false
	}
	if time.Since(info.EndedAt) > s.retention {
		s.mu.Lock()
		// Re-check under the write lock to avoid racing with a fresh Set.
		if cur, ok := s.entries[processID]; ok && cur == info {
			delete(s.entries, processID)
		}
		s.mu.Unlock()
		return nil, false
	}
	return info, true
}

// Clear removes any death record for processID. Called on the next start
// so proc status does not show a stale exit after a restart.
func (s *processExitInfoStore) Clear(processID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.entries, processID)
	s.mu.Unlock()
}

// classifyExitReason maps an exit code to a reason string.
//
//	0           → "stopped"  (normal shutdown, any Stop call)
//	>0          → "crash"    (process died with a non-zero code)
//	<0          → "signal"   (Go's exec.Wait reports -1 on signal-kill
//	                          or otherwise unexpected termination)
func classifyExitReason(exitCode int) string {
	switch {
	case exitCode == 0:
		return "stopped"
	case exitCode < 0:
		return "signal"
	default:
		return "crash"
	}
}

// buildLifecycleAlert converts an exit info snapshot into an AlertEntry
// that flows through the existing alertStore → collectProcessAlerts path
// in get_errors. A crash or signal is an error; a clean stop is info so
// it does not pollute the error list.
func buildLifecycleAlert(info *ProcessExitInfo) *AlertEntry {
	if info == nil {
		return nil
	}

	severity := "error"
	if info.ExitCode == 0 || info.Reason == "stopped" {
		severity = "info"
	}

	description := fmt.Sprintf(
		"process %s exited (code %d, reason %s, uptime %s)",
		info.ProcessID, info.ExitCode, info.Reason, formatExitUptime(info.Uptime),
	)

	// The "line" field is rendered as the error message in get_errors —
	// we want agents to see WHY it died, so prefer the last stderr line.
	line := info.StderrTail
	if line == "" {
		line = description
	}

	// PatternID is stable per death event (timestamp-encoded) so multiple
	// queries of the same death collapse via dedup, while two deaths of
	// the same process at different times stay distinct.
	patternID := "process_lifecycle:" + info.ProcessID + ":" + strconv.FormatInt(info.EndedAt.UnixNano(), 10)

	return &AlertEntry{
		PatternID:   patternID,
		Severity:    severity,
		Category:    CategoryProcessLifecycle,
		Description: description,
		Line:        line,
		ScriptID:    info.ProcessID,
		Timestamp:   info.EndedAt,
	}
}

// formatExitUptime is a compact human-readable duration used in the
// lifecycle alert description.
func formatExitUptime(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) - m*60
		if s == 0 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm%ds", m, s)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) - h*60
	return fmt.Sprintf("%dh%dm", h, m)
}

// watchProcessExit launches a goroutine that waits for a managed process
// to exit, then captures its death metadata and publishes it. Idempotent —
// caller is responsible for not double-registering the same process.
//
// The watcher lives for exactly one process lifetime. On restart, the
// next start path calls Clear() + watchProcessExit() to begin a fresh
// watch against the new ManagedProcess instance.
//
// Must be called AFTER a successful start so proc.Done() is wired up.
func (d *Daemon) watchProcessExit(proc *goprocess.ManagedProcess) {
	if d == nil || proc == nil {
		return
	}
	if d.processExitInfo == nil {
		return
	}

	// Clear any prior death record for this process — we're starting
	// fresh and the next status call should reflect the NEW run, not the
	// previous death.
	d.processExitInfo.Clear(proc.ID)

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()

		select {
		case <-proc.Done():
			// Process exited normally — capture its metadata.
		case <-d.ctx.Done():
			// Daemon shutting down. Don't record a spurious "exit".
			return
		}

		info := captureExitInfo(proc)
		if info == nil {
			return
		}

		// Guard against the autorestart race: by the time we publish,
		// the daemon may have already replaced this ManagedProcess with
		// a fresh instance under the same ID. Only claim the exit-info
		// slot if the ProcessManager still points at the same instance
		// (or has removed it entirely — in which case we're the last
		// record, which is what the user wants). If a DIFFERENT instance
		// is registered, the new instance's start path already ran
		// Clear() and its own watcher will publish eventually; skip.
		if current, err := d.hub.ProcessManager().Get(proc.ID); err == nil && current != proc {
			return
		}

		// Fire on-stop or on-crash lifecycle hook if configured.
		if cfgVal, ok := d.scriptConfigs.Load(proc.ID); ok {
			if scriptCfg, ok := cfgVal.(*config.ScriptConfig); ok && scriptCfg.Hooks != nil {
				scriptName := proc.ID
				if entry, ok := d.scriptRegistry.GetByProcessID(proc.ID); ok {
					scriptName = entry.Name
				}
				if info.Reason == "stopped" && scriptCfg.Hooks.OnStop != "" {
					if err := RunLifecycleHook(scriptCfg.Hooks.OnStop, scriptName, "stop", scriptCfg, info.ExitCode); err != nil {
						debug.Warn("daemon", "on-stop hook for %s: %v", scriptName, err)
					}
				} else if info.Reason != "stopped" && scriptCfg.Hooks.OnCrash != "" {
					if err := RunLifecycleHook(scriptCfg.Hooks.OnCrash, scriptName, "crash", scriptCfg, info.ExitCode); err != nil {
						debug.Warn("daemon", "on-crash hook for %s: %v", scriptName, err)
					}
				}
			}
		}

		d.processExitInfo.Set(info)

		// Only push an alert for real deaths. A clean exit (reason
		// "stopped") is always either user-initiated or an expected
		// short-lived script — pushing those as lifecycle entries
		// would pollute get_errors with noise. Clean exits are still
		// recorded in processExitInfo so proc status can show them.
		if d.alertStore != nil && info.Reason != "stopped" {
			if alert := buildLifecycleAlert(info); alert != nil {
				d.alertStore.Add(alert)
			}
		}

		debug.Log("daemon", "process %s exited: code=%d reason=%s uptime=%s",
			proc.ID, info.ExitCode, info.Reason, info.Uptime)
	}()
}

// exitInfoToResponse merges a ProcessExitInfo into a response map used by
// hub_proc.go handlers for proc status / proc list. The caller decides
// whether the record exists; this helper just adds the keys.
//
// Keys added:
//
//	last_exit_at       RFC3339 timestamp of the exit
//	last_exit_code     integer exit code
//	last_exit_reason   "stopped" | "crash" | "signal"
//	last_uptime        human-readable uptime before exit
//	last_uptime_ms     uptime in milliseconds
//	last_stderr_tail   up to stderrTailLines lines of stderr at exit
func exitInfoToResponse(dst map[string]interface{}, info *ProcessExitInfo) {
	if dst == nil || info == nil {
		return
	}
	dst["last_exit_at"] = info.EndedAt.Format(time.RFC3339)
	dst["last_exit_code"] = info.ExitCode
	dst["last_exit_reason"] = info.Reason
	dst["last_uptime"] = formatExitUptime(info.Uptime)
	dst["last_uptime_ms"] = info.Uptime.Milliseconds()
	if info.StderrTail != "" {
		dst["last_stderr_tail"] = info.StderrTail
	}
}

// captureExitInfo snapshots the metadata of a just-exited process into a
// ProcessExitInfo. Reads atomics from the vendored ManagedProcess and
// grabs a stderr tail from the ring buffer. Returns nil if the process
// does not have end-time metadata yet (should not happen after Done() has
// fired, but defensive).
func captureExitInfo(proc *goprocess.ManagedProcess) *ProcessExitInfo {
	if proc == nil {
		return nil
	}

	endedAt := time.Now()
	if et := proc.EndTime(); et != nil && !et.IsZero() {
		endedAt = *et
	}

	startedAt := time.Time{}
	if st := proc.StartTime(); st != nil {
		startedAt = *st
	}

	exitCode := proc.ExitCode()

	// Prefer stderr tail because panics and compile errors usually land
	// on stderr. Fall back to combined output if stderr is empty.
	tailBytes, _ := proc.Stderr()
	tail := lastLines(string(tailBytes), stderrTailLines)
	if tail == "" {
		combinedBytes, _ := proc.CombinedOutput()
		tail = lastLines(string(combinedBytes), stderrTailLines)
	}

	return &ProcessExitInfo{
		ProcessID:  proc.ID,
		ExitCode:   exitCode,
		Reason:     classifyExitReason(exitCode),
		StartedAt:  startedAt,
		EndedAt:    endedAt,
		Uptime:     proc.Runtime(),
		StderrTail: tail,
	}
}
