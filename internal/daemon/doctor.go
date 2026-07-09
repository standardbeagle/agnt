package daemon

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/scope"
	goprocess "github.com/standardbeagle/go-cli-server/process"
	"github.com/standardbeagle/go-cli-server/script"
)

// Status constants for health check results.
const (
	StatusOK      = "ok"
	StatusWarning = "warning"
	StatusError   = "error"
)

// CheckResult is a single health check outcome.
type CheckResult struct {
	Name    string      `json:"name"`
	Status  string      `json:"status"`
	Message string      `json:"message"`
	Details interface{} `json:"details,omitempty"`
	Fix     string      `json:"fix,omitempty"`
}

// DoctorReport is the aggregate of all health checks.
type DoctorReport struct {
	Status string        `json:"status"`
	Checks []CheckResult `json:"checks"`
}

// doctorCheck pairs a name with its check function.
type doctorCheck struct {
	name string
	fn   func(ctx context.Context) CheckResult
}

// RunDoctor runs all health checks concurrently and returns a structured report.
func (d *Daemon) RunDoctor(ctx context.Context, projectPath string) *DoctorReport {
	checks := d.buildChecks(projectPath)
	return runDoctorWithChecks(ctx, checks)
}

// buildChecks assembles the list of checks for this daemon instance.
func (d *Daemon) buildChecks(projectPath string) []doctorCheck {
	return []doctorCheck{
		{name: "orphan_processes", fn: func(ctx context.Context) CheckResult {
			return checkOrphanProcesses(ctx, d.pidTracker, d.hub.ProcessManager())
		}},
		{name: "port_conflicts", fn: func(ctx context.Context) CheckResult {
			return checkPortConflicts(ctx, projectPath, d.hub.ProcessManager())
		}},
		{name: "daemon_health", fn: func(ctx context.Context) CheckResult {
			return checkDaemonHealthStatic(Version, time.Since(d.started), d.hub.ClientCount())
		}},
		{name: "script_health", fn: func(ctx context.Context) CheckResult {
			return checkScriptHealth(ctx, d.scriptRegistry, projectPath)
		}},
		{name: "proxy_health", fn: func(ctx context.Context) CheckResult {
			return checkProxyHealth(ctx, d.proxym, projectPath)
		}},
		{name: "session_health", fn: func(ctx context.Context) CheckResult {
			return checkSessionHealth(ctx, d.sessionRegistry)
		}},
		{name: "config_health", fn: func(ctx context.Context) CheckResult {
			return checkConfigHealth(ctx, projectPath)
		}},
		{name: "startup_errors", fn: func(ctx context.Context) CheckResult {
			return checkStartupErrors(ctx, d.startupErrorStore)
		}},
		{name: "process_tree", fn: func(ctx context.Context) CheckResult {
			return checkProcessTree(ctx, d.pidTracker, d.hub.ProcessManager())
		}},
	}
}

// runDoctorWithChecks executes the given checks concurrently under the context deadline.
func runDoctorWithChecks(ctx context.Context, checks []doctorCheck) *DoctorReport {
	if len(checks) == 0 {
		return &DoctorReport{Status: StatusOK}
	}

	results := make([]CheckResult, len(checks))
	var wg sync.WaitGroup
	wg.Add(len(checks))

	for i, chk := range checks {
		go func(idx int, c doctorCheck) {
			defer wg.Done()
			results[idx] = c.fn(ctx)
		}(i, chk)
	}

	wg.Wait()

	overall := StatusOK
	for _, r := range results {
		overall = worstStatus(overall, r.Status)
	}

	return &DoctorReport{
		Status: overall,
		Checks: results,
	}
}

// worstStatus returns the more severe of two statuses.
func worstStatus(a, b string) string {
	rank := map[string]int{StatusOK: 0, StatusWarning: 1, StatusError: 2}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// --- Individual checks ---

func checkOrphanProcesses(ctx context.Context, tracker *goprocess.FilePIDTracker, pm *goprocess.ProcessManager) CheckResult {
	name := "orphan_processes"
	if tracker == nil {
		return CheckResult{Name: name, Status: StatusOK, Message: "PID tracker not configured"}
	}

	tracked := tracker.ListTracked()
	if len(tracked) == 0 {
		return CheckResult{Name: name, Status: StatusOK, Message: "no tracked processes"}
	}

	var orphans []map[string]interface{}
	for _, tp := range tracked {
		if !pidAlive(tp.PID) {
			continue
		}
		if pm.IsManagedPID(tp.PID) {
			continue
		}
		orphans = append(orphans, map[string]interface{}{
			"id":  tp.ID,
			"pid": tp.PID,
		})
	}

	if len(orphans) == 0 {
		return CheckResult{Name: name, Status: StatusOK, Message: fmt.Sprintf("%d tracked, all accounted for", len(tracked))}
	}

	return CheckResult{
		Name:    name,
		Status:  StatusWarning,
		Message: fmt.Sprintf("%d orphan process(es) alive but not managed", len(orphans)),
		Details: orphans,
		Fix:     "proc {action: \"cleanup_port\"} or restart daemon",
	}
}

func checkPortConflicts(ctx context.Context, projectPath string, pm *goprocess.ProcessManager) CheckResult {
	name := "port_conflicts"

	cfg, err := config.LoadAgntConfig(projectPath)
	if err != nil {
		return CheckResult{Name: name, Status: StatusOK, Message: "no config to check"}
	}

	type rawConflict struct {
		script string
		port   int
		pids   []int
	}
	var raws []rawConflict
	var allPIDs []int
	pidSeen := make(map[int]struct{})
	for scriptName, sc := range cfg.Scripts {
		for _, port := range sc.Ports {
			pids := config.FindPIDsByPort(ctx, port)
			if len(pids) == 0 {
				continue
			}
			managed := false
			for _, pid := range pids {
				if pm.IsManagedPID(pid) {
					managed = true
					break
				}
			}
			if managed {
				continue
			}
			raws = append(raws, rawConflict{script: scriptName, port: port, pids: pids})
			for _, pid := range pids {
				if _, dup := pidSeen[pid]; !dup {
					pidSeen[pid] = struct{}{}
					allPIDs = append(allPIDs, pid)
				}
			}
		}
	}

	// Resolve all rogue PIDs in a single batch — on WSL this coalesces
	// to one tasklist.exe call regardless of how many Windows-side PIDs
	// are holding our declared ports. Linux PIDs resolve via /proc with
	// no shell-out cost.
	names := config.ProcessNamesByPIDs(ctx, allPIDs)

	var conflicts []map[string]interface{}
	for _, raw := range raws {
		pidNames := make([]string, 0, len(raw.pids))
		for _, pid := range raw.pids {
			if n := names[pid]; n != "" {
				pidNames = append(pidNames, n)
			}
		}
		entry := map[string]interface{}{
			"script": raw.script,
			"port":   raw.port,
			"pids":   raw.pids,
		}
		if len(pidNames) > 0 {
			entry["process_names"] = pidNames
		}
		conflicts = append(conflicts, entry)
	}

	if len(conflicts) == 0 {
		return CheckResult{Name: name, Status: StatusOK, Message: "no port conflicts"}
	}

	return CheckResult{
		Name:    name,
		Status:  StatusError,
		Message: fmt.Sprintf("%d port conflict(s) with unmanaged processes", len(conflicts)),
		Details: conflicts,
		Fix:     "proc {action: \"cleanup_port\", port: N}",
	}
}

func checkDaemonHealthStatic(version string, uptime time.Duration, clientCount int64) CheckResult {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	return CheckResult{
		Name:    "daemon_health",
		Status:  StatusOK,
		Message: fmt.Sprintf("v%s, up %s", version, uptime.Truncate(time.Second)),
		Details: map[string]interface{}{
			"version":       version,
			"uptime_sec":    int(uptime.Seconds()),
			"clients":       clientCount,
			"heap_alloc_mb": int(mem.HeapAlloc / 1024 / 1024),
			"goroutines":    runtime.NumGoroutine(),
		},
	}
}

func checkScriptHealth(ctx context.Context, registry *script.Registry, projectPath string) CheckResult {
	name := "script_health"
	if registry == nil {
		return CheckResult{Name: name, Status: StatusOK, Message: "no script registry"}
	}

	entries := registry.List(projectPath)
	if len(entries) == 0 {
		return CheckResult{Name: name, Status: StatusOK, Message: "no scripts registered"}
	}

	var failed []map[string]interface{}
	var crashLooping []map[string]interface{}

	for _, e := range entries {
		state := e.State()
		if state == script.StateFailed {
			failed = append(failed, map[string]interface{}{
				"name":       e.Name,
				"process_id": e.ProcessID,
				"fails":      e.FailCount(),
			})
		}
		if e.FailCount() > 0 && e.FailCount() > e.StartCount()/2 {
			crashLooping = append(crashLooping, map[string]interface{}{
				"name":        e.Name,
				"process_id":  e.ProcessID,
				"start_count": e.StartCount(),
				"fail_count":  e.FailCount(),
			})
		}
	}

	if len(crashLooping) > 0 {
		return CheckResult{
			Name:    name,
			Status:  StatusError,
			Message: fmt.Sprintf("%d script(s) crash-looping", len(crashLooping)),
			Details: crashLooping,
			Fix:     "proc {action: \"stop\", process_id: \"...\"} then investigate",
		}
	}
	if len(failed) > 0 {
		return CheckResult{
			Name:    name,
			Status:  StatusWarning,
			Message: fmt.Sprintf("%d script(s) in failed state", len(failed)),
			Details: failed,
			Fix:     "proc {action: \"restart\", process_id: \"...\"}",
		}
	}

	return CheckResult{Name: name, Status: StatusOK, Message: fmt.Sprintf("%d script(s) healthy", len(entries))}
}

func checkProxyHealth(ctx context.Context, pm *proxy.ProxyManager, projectPath string) CheckResult {
	name := "proxy_health"
	if pm == nil {
		return CheckResult{Name: name, Status: StatusOK, Message: "no proxy manager"}
	}

	// Scope to the project under inspection. An empty projectPath means a
	// daemon-wide doctor run (audited via Unscoped); otherwise only this
	// project's proxies are checked.
	sc := scope.Project(projectPath)
	if projectPath == "" {
		sc = scope.Unscoped("doctor: health check across all projects")
	}
	filtered := pm.ListScoped(sc)
	if len(filtered) == 0 {
		return CheckResult{Name: name, Status: StatusOK, Message: "no proxies for this project"}
	}

	client := &http.Client{Timeout: 2 * time.Second}
	var unreachable []map[string]interface{}

	for _, p := range filtered {
		if !p.IsRunning() {
			unreachable = append(unreachable, map[string]interface{}{
				"id":     p.ID,
				"reason": "not running",
			})
			continue
		}
		url := "http://" + p.ListenAddr
		resp, err := client.Get(url)
		if err != nil {
			unreachable = append(unreachable, map[string]interface{}{
				"id":     p.ID,
				"addr":   p.ListenAddr,
				"reason": err.Error(),
			})
			continue
		}
		resp.Body.Close()
	}

	if len(unreachable) > 0 {
		return CheckResult{
			Name:    name,
			Status:  StatusError,
			Message: fmt.Sprintf("%d/%d proxy(ies) unreachable", len(unreachable), len(filtered)),
			Details: unreachable,
			Fix:     "proxy {action: \"restart\", proxy_id: \"...\"}",
		}
	}

	return CheckResult{Name: name, Status: StatusOK, Message: fmt.Sprintf("%d proxy(ies) healthy", len(filtered))}
}

func checkSessionHealth(ctx context.Context, registry *SessionRegistry) CheckResult {
	name := "session_health"
	if registry == nil {
		return CheckResult{Name: name, Status: StatusOK, Message: "no session registry"}
	}

	sessions := registry.List("", true)
	if len(sessions) == 0 {
		return CheckResult{Name: name, Status: StatusOK, Message: "no sessions"}
	}

	// Derive the staleness threshold from the registry's heartbeat timeout
	// rather than a parallel hardcoded constant, so the two can't drift.
	threshold := registry.HeartbeatTimeout()
	now := time.Now()
	var stale []map[string]interface{}

	for _, s := range sessions {
		// GetLastSeen reads under the session lock; a bare s.LastSeen races the
		// heartbeat writer and can observe a torn time.Time.
		lastSeen := s.GetLastSeen()
		if s.GetStatus() == SessionStatusActive && now.Sub(lastSeen) > threshold {
			stale = append(stale, map[string]interface{}{
				"code":          s.Code,
				"last_seen_ago": now.Sub(lastSeen).Truncate(time.Second).String(),
			})
		}
	}

	if len(stale) > 0 {
		return CheckResult{
			Name:    name,
			Status:  StatusWarning,
			Message: fmt.Sprintf("%d stale session(s) (active but no heartbeat >60s)", len(stale)),
			Details: stale,
		}
	}

	return CheckResult{Name: name, Status: StatusOK, Message: fmt.Sprintf("%d session(s) healthy", len(sessions))}
}

func checkConfigHealth(ctx context.Context, projectPath string) CheckResult {
	name := "config_health"
	if projectPath == "" {
		return CheckResult{Name: name, Status: StatusOK, Message: "no project path specified"}
	}

	configPath := config.FindAgntConfigFile(projectPath)
	if configPath == "" {
		return CheckResult{Name: name, Status: StatusOK, Message: "no .agnt.kdl found"}
	}

	cfg, err := config.LoadAgntConfigFile(configPath)
	if err != nil {
		return CheckResult{
			Name:    name,
			Status:  StatusError,
			Message: fmt.Sprintf("config parse error: %v", err),
			Fix:     "fix .agnt.kdl syntax",
		}
	}

	var missingDirs []string
	for scriptName, sc := range cfg.Scripts {
		if sc.Cwd == "" {
			continue
		}
		cwd := sc.Cwd
		if !filepath.IsAbs(cwd) {
			cwd = filepath.Join(projectPath, cwd)
		}
		if _, err := os.Stat(cwd); os.IsNotExist(err) {
			missingDirs = append(missingDirs, fmt.Sprintf("%s (script: %s)", cwd, scriptName))
		}
	}

	if len(missingDirs) > 0 {
		return CheckResult{
			Name:    name,
			Status:  StatusWarning,
			Message: fmt.Sprintf("%d script cwd director(ies) missing", len(missingDirs)),
			Details: missingDirs,
			Fix:     "create the missing directories or update .agnt.kdl",
		}
	}

	return CheckResult{Name: name, Status: StatusOK, Message: "config valid"}
}

func checkStartupErrors(ctx context.Context, store *StartupLogStore) CheckResult {
	name := "startup_errors"
	if store == nil {
		return CheckResult{Name: name, Status: StatusOK, Message: "no startup log store"}
	}

	recent := store.Recent(30*time.Minute, 0)
	var errorCount int
	var errorMsgs []string

	for _, entry := range recent {
		if entry.Level == "error" {
			errorCount++
			errorMsgs = append(errorMsgs, fmt.Sprintf("[%s] %s: %s", entry.ScriptName, entry.EventType, entry.Message))
		}
	}

	if errorCount > 0 {
		return CheckResult{
			Name:    name,
			Status:  StatusWarning,
			Message: fmt.Sprintf("%d error(s) in last 30 minutes", errorCount),
			Details: errorMsgs,
		}
	}

	return CheckResult{Name: name, Status: StatusOK, Message: "no recent startup errors"}
}

func checkProcessTree(ctx context.Context, tracker *goprocess.FilePIDTracker, pm *goprocess.ProcessManager) CheckResult {
	name := "process_tree"
	if tracker == nil {
		return CheckResult{Name: name, Status: StatusOK, Message: "PID tracker not configured"}
	}

	tracked := tracker.ListTracked()
	if len(tracked) == 0 {
		return CheckResult{Name: name, Status: StatusOK, Message: "no tracked processes"}
	}

	var escaped []map[string]interface{}
	allTrackedPIDs := make(map[int]bool)
	for _, tp := range tracked {
		allTrackedPIDs[tp.PID] = true
		for _, d := range tp.DescendantPIDs {
			allTrackedPIDs[d] = true
		}
	}

	for _, tp := range tracked {
		if !pidAlive(tp.PID) {
			continue
		}
		liveDescendants := scanLiveDescendants(tp.PID)
		for _, dpid := range liveDescendants {
			if !allTrackedPIDs[dpid] && pidAlive(dpid) {
				escaped = append(escaped, map[string]interface{}{
					"parent_id":   tp.ID,
					"parent_pid":  tp.PID,
					"escaped_pid": dpid,
				})
			}
		}
	}

	if len(escaped) > 0 {
		return CheckResult{
			Name:    name,
			Status:  StatusWarning,
			Message: fmt.Sprintf("%d escaped descendant process(es) not in tracker", len(escaped)),
			Details: escaped,
		}
	}

	return CheckResult{Name: name, Status: StatusOK, Message: "process tree consistent"}
}

// scanLiveDescendants finds all live descendant PIDs for a process.
func scanLiveDescendants(pid int) []int {
	return findDescendants(pid)
}

// findDescendants finds all descendant PIDs for a given process.
func findDescendants(pid int) []int {
	var result []int
	children := directChildren(pid)
	for _, child := range children {
		result = append(result, child)
		result = append(result, findDescendants(child)...)
	}
	return result
}

// directChildren reads direct child PIDs from /proc on Linux.
func directChildren(pid int) []int {
	if runtime.GOOS != "linux" {
		return nil
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", pid, pid))
	if err != nil {
		return nil
	}

	var children []int
	for _, field := range splitFields(data) {
		var child int
		if _, err := fmt.Sscanf(field, "%d", &child); err == nil {
			children = append(children, child)
		}
	}
	return children
}

// splitFields splits whitespace-separated byte data into string fields.
func splitFields(data []byte) []string {
	var fields []string
	start := -1
	for i, b := range data {
		if b == ' ' || b == '\t' || b == '\n' || b == '\r' {
			if start >= 0 {
				fields = append(fields, string(data[start:i]))
				start = -1
			}
		} else if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		fields = append(fields, string(data[start:]))
	}
	return fields
}
