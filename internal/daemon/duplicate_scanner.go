package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/platform"
)

// devServerCommands lists executable basenames that are common dev servers.
// When multiple instances of the same command run under the same project,
// the oldest is kept and duplicates are killed.
var devServerCommands = map[string]string{
	// Node.js
	"node": "node", "npm": "npm", "npx": "npx", "vite": "vite",
	"next": "next", "webpack": "webpack", "ts-node": "ts-node",
	"nodemon": "nodemon", "turbo": "turbo",
	// Go
	"go": "go", "air": "air",
	// .NET
	"dotnet": "dotnet",
	// Python
	"flask": "flask", "uvicorn": "uvicorn", "gunicorn": "gunicorn",
	"python": "python", "python3": "python3", "django": "django",
	// Rust
	"cargo": "cargo",
	// Ruby
	"rails": "rails", "foreman": "foreman", "bundle": "bundle",
	// Java
	"java": "java", "mvn": "mvn", "gradle": "gradle",
}

// DuplicateGroup holds processes grouped by (command, projectPath).
type DuplicateGroup struct {
	Command string
	Path    string
	Procs   []platform.ProcInfo
}

// CleanupResult describes what happened during a cleanup pass.
type CleanupResult struct {
	Killed []KilledProcess
}

// KilledProcess records a single killed duplicate.
type KilledProcess struct {
	PID     int
	Command string
	Reason  string
}

// DuplicateScanner detects and kills duplicate dev server processes
// that are not managed by the daemon.
type DuplicateScanner struct {
	daemon *Daemon

	// Notification callback — sends a message to the AI agent via PTY overlay.
	// Set by the caller (pty_common.go integration point).
	OnNotify func(message string)

	mu         sync.Mutex
	lastScanAt time.Time
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
}

// NewDuplicateScanner creates a new scanner bound to a daemon instance.
func NewDuplicateScanner(d *Daemon) *DuplicateScanner {
	ctx, cancel := context.WithCancel(d.ctx)
	return &DuplicateScanner{
		daemon: d,
		ctx:    ctx,
		cancel: cancel,
	}
}

// Start begins the periodic duplicate scan (every 30 seconds).
func (s *DuplicateScanner) Start() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				result := s.ScanAndCleanup("")
				if len(result.Killed) > 0 {
					s.notify(result)
				}
			}
		}
	}()
}

// Stop cancels the periodic scan and waits for the goroutine to finish.
func (s *DuplicateScanner) Stop() {
	s.cancel()
	s.wg.Wait()
}

// LastScanAt returns the timestamp of the most recent completed scan.
func (s *DuplicateScanner) LastScanAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastScanAt
}

// ScanForProject runs a scan for a specific project path.
// Returns the cleanup result. The caller is responsible for notification.
func (s *DuplicateScanner) ScanForProject(projectPath string) *CleanupResult {
	return s.ScanAndCleanup(projectPath)
}

// ScanAndCleanup scans for duplicates and kills them.
// If projectPath is non-empty, only processes under that path are considered.
// If projectPath is empty, all active project paths are scanned.
//
// The lock is held only during the scan phase (process listing + duplicate
// detection). Kills happen outside the lock to avoid blocking concurrent
// callers during SIGTERM + grace period waits.
func (s *DuplicateScanner) ScanAndCleanup(projectPath string) *CleanupResult {
	// Phase 1: Scan under lock — collect process list and identify duplicates.
	groups, managedPIDs := s.scanDuplicates(projectPath)
	if len(groups) == 0 {
		return &CleanupResult{}
	}

	// Phase 2: Kill outside lock — SIGTERM + wait happens without holding mu.
	var result CleanupResult
	for _, group := range groups {
		killed := s.killDuplicates(group, managedPIDs)
		result.Killed = append(result.Killed, killed...)
	}

	// Phase 3: Update scan timestamp under lock.
	s.mu.Lock()
	s.lastScanAt = time.Now()
	s.mu.Unlock()

	return &result
}

// scanDuplicates holds the lock while scanning processes and detecting
// duplicates. Returns the duplicate groups and managed PID set for use
// by the caller outside the lock.
func (s *DuplicateScanner) scanDuplicates(projectPath string) ([]DuplicateGroup, map[int]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	paths := s.resolveProjectPaths(projectPath)
	if len(paths) == 0 {
		return nil, nil
	}

	// Gather all running processes
	allProcs, err := platform.Scan()
	if err != nil {
		debug.Log("dup-scanner", "platform scan failed: %v", err)
		return nil, nil
	}

	// If WSL, also scan Windows-side processes
	if platform.IsWSL() {
		winProcs, werr := platform.ScanWindows()
		if werr != nil {
			debug.Log("dup-scanner", "WSL Windows scan failed: %v", werr)
		} else {
			allProcs = append(allProcs, winProcs...)
		}
	}

	managedPIDs := s.daemon.collectManagedPIDs()
	var groups []DuplicateGroup

	for _, pp := range paths {
		found := s.findDuplicates(allProcs, pp, managedPIDs)
		groups = append(groups, found...)
	}

	return groups, managedPIDs
}

// resolveProjectPaths returns the set of project paths to scan.
func (s *DuplicateScanner) resolveProjectPaths(projectPath string) []string {
	if projectPath != "" {
		return []string{normalizePath(projectPath)}
	}

	// Collect all active project paths from session registry
	seen := make(map[string]bool)
	var paths []string
	for _, session := range s.daemon.SessionRegistry().List("", true) {
		pp := normalizePath(session.ProjectPath)
		if pp != "" && !seen[pp] {
			seen[pp] = true
			paths = append(paths, pp)
		}
	}
	return paths
}

// findDuplicates groups processes by command basename under the given project path.
// Processes that are daemon-managed are included in groups but not killed.
// Returns groups where there are more than one process (managed + unmanaged).
func (s *DuplicateScanner) findDuplicates(procs []platform.ProcInfo, projectPath string, managedPIDs map[int]bool) []DuplicateGroup {
	// Group by (command basename, project path)
	groups := make(map[string][]platform.ProcInfo) // key = command

	for _, p := range procs {
		cmdBase := strings.ToLower(p.Command)
		if _, isDev := devServerCommands[cmdBase]; !isDev {
			continue
		}

		// Filter by project path: CWD must match or be a child of the project path.
		if !pathMatches(projectPath, p.Cwd) {
			continue
		}

		// Skip our own daemon process
		if p.PID == 0 || p.PID == 1 {
			continue
		}

		groups[cmdBase] = append(groups[cmdBase], p)
	}

	var duplicates []DuplicateGroup
	for cmd, procs := range groups {
		// Only care about groups with more than one process
		if len(procs) < 2 {
			continue
		}

		// Count unmanaged processes (managed ones are the ones we started)
		unmanagedCount := 0
		for _, p := range procs {
			if !managedPIDs[p.PID] {
				unmanagedCount++
			}
		}

		// If all are managed, no action needed
		if unmanagedCount == 0 {
			continue
		}

		// Sort by PID ascending (lower PID = older process = keep)
		sort.Slice(procs, func(i, j int) bool {
			return procs[i].PID < procs[j].PID
		})

		duplicates = append(duplicates, DuplicateGroup{
			Command: cmd,
			Path:    projectPath,
			Procs:   procs,
		})
	}

	return duplicates
}

// killDuplicates kills duplicate processes, keeping the oldest unmanaged one.
// Daemon-managed processes (and their children) are never killed.
func (s *DuplicateScanner) killDuplicates(group DuplicateGroup, managedPIDs map[int]bool) []KilledProcess {
	var killed []KilledProcess
	keptUnmanaged := false

	for _, p := range group.Procs {
		// Never kill daemon-managed processes or their children
		if managedPIDs[p.PID] {
			continue
		}

		// Keep the first (oldest) unmanaged process
		if !keptUnmanaged {
			keptUnmanaged = true
			continue
		}

		// Kill the duplicate
		if err := platform.KillPID(p.PID, 3); err != nil {
			debug.Log("dup-scanner", "failed to kill PID %d (%s): %v", p.PID, group.Command, err)
			continue
		}

		reason := fmt.Sprintf("duplicate %s (PID %d) was running alongside %s instance",
			group.Command, p.PID, group.Command)

		debug.Log("dup-scanner", "killed %s", reason)

		killed = append(killed, KilledProcess{
			PID:     p.PID,
			Command: group.Command,
			Reason:  reason,
		})
	}

	return killed
}

// notify sends a message about killed duplicates to the AI agent.
func (s *DuplicateScanner) notify(result *CleanupResult) {
	if len(result.Killed) == 0 {
		return
	}

	// Group by command for a compact message
	byCmd := make(map[string][]int)
	for _, k := range result.Killed {
		byCmd[k.Command] = append(byCmd[k.Command], k.PID)
	}

	var parts []string
	for cmd, pids := range byCmd {
		pidStrs := make([]string, len(pids))
		for i, p := range pids {
			pidStrs[i] = fmt.Sprintf("PID %d", p)
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", cmd, strings.Join(pidStrs, ", ")))
	}
	sort.Strings(parts)

	total := len(result.Killed)
	msg := fmt.Sprintf("[agnt] cleaned up %d duplicate process(es): %s",
		total, strings.Join(parts, ", "))

	// Log to startup error store
	if s.daemon != nil && s.daemon.startupErrorStore != nil {
		s.daemon.startupErrorStore.Add(&StartupLogEntry{
			Level:     "info",
			EventType: "duplicate_cleanup",
			Message:   msg,
			Timestamp: time.Now(),
		})
	}

	// Send to PTY overlay if callback is registered
	if s.OnNotify != nil {
		s.OnNotify(msg)
	}
}

// pathMatches returns true if procCwd is within or equal to projectPath.
// Both are normalized (lowercased on Windows) before comparison.
func pathMatches(projectPath, procCwd string) bool {
	if procCwd == "" || projectPath == "" {
		return false
	}
	procCwd = normalizePath(procCwd)
	// Exact match or procCwd is a subdirectory
	if procCwd == projectPath {
		return true
	}
	return strings.HasPrefix(procCwd, projectPath+string(filepath.Separator))
}
