package process

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TrackedProcess represents a process being tracked for orphan cleanup.
type TrackedProcess struct {
	ID          string `json:"id"`
	PID         int    `json:"pid"`
	PGID        int    `json:"pgid"`
	ProjectPath string `json:"project_path"`
	// Identity is an opaque token capturing the OS-level identity of the
	// process at Add time (on Linux: "<starttime>:<uid>" from /proc). It is
	// re-checked before any orphan kill so that a PID which the kernel has
	// since recycled into an unrelated process is NOT killed. Empty on
	// platforms where identity cannot be captured (falls back to a liveness
	// check only).
	Identity       string    `json:"identity,omitempty"`
	StartedAt      time.Time `json:"started_at"`
	DescendantPIDs []int     `json:"descendants,omitempty"`
	// DescendantIdentities stores OS identity tokens for descendant PIDs captured
	// during the same scan. Kill paths use it to avoid killing a recycled PID.
	DescendantIdentities map[int]string `json:"descendant_identities,omitempty"`
	LastScanAt           time.Time      `json:"last_scan_at,omitempty"`
}

// PIDTracking holds the complete tracking state.
type PIDTracking struct {
	DaemonPID int              `json:"daemon_pid"`
	Processes []TrackedProcess `json:"processes"`
	UpdatedAt time.Time        `json:"updated_at"`
}

// FilePIDTracker manages process ID tracking for orphan cleanup.
// It persists PIDs to disk so that if the daemon crashes, we can
// clean up orphaned processes on the next startup.
//
// FilePIDTracker implements the PIDTracker interface.
type FilePIDTracker struct {
	path string
	mu   sync.Mutex
}

// Ensure FilePIDTracker implements PIDTracker and DescendantTracker interfaces.
var _ PIDTracker = (*FilePIDTracker)(nil)
var _ DescendantTracker = (*FilePIDTracker)(nil)

// FilePIDTrackerConfig configures the file-based PID tracker.
type FilePIDTrackerConfig struct {
	// Path is the file path for persisting PID tracking state.
	// If empty, uses default based on XDG_STATE_HOME.
	Path string
	// AppName is used for the default path. Default: "cli-server"
	AppName string
}

// NewFilePIDTracker creates a new file-based PID tracker with the given configuration.
func NewFilePIDTracker(config FilePIDTrackerConfig) *FilePIDTracker {
	path := config.Path
	if path == "" {
		appName := config.AppName
		if appName == "" {
			appName = "cli-server"
		}
		path = defaultPIDTrackingPath(appName)
	}
	return &FilePIDTracker{path: path}
}

// defaultPIDTrackingPath returns the default path for PID tracking file.
func defaultPIDTrackingPath(appName string) string {
	// Use XDG_STATE_HOME if available
	if stateHome := os.Getenv("XDG_STATE_HOME"); stateHome != "" {
		return filepath.Join(stateHome, appName, "pids.json")
	}

	// Fall back to ~/.local/state
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", appName, "pids.json")
	}

	// Last resort: temp directory
	return filepath.Join(os.TempDir(), appName+"-pids.json")
}

// Path returns the tracking file path.
func (pt *FilePIDTracker) Path() string {
	return pt.path
}

// Load reads the current tracking state from disk.
func (pt *FilePIDTracker) Load() PIDTracking {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	return pt.loadLocked()
}

// loadLocked loads tracking state without acquiring the lock.
func (pt *FilePIDTracker) loadLocked() PIDTracking {
	var tracking PIDTracking

	data, err := os.ReadFile(pt.path)
	if err != nil {
		// File doesn't exist or can't be read - return empty tracking
		return tracking
	}

	if err := json.Unmarshal(data, &tracking); err != nil {
		// A corrupt tracking file would otherwise be silently treated as "no
		// tracked processes", and the next saveLocked would overwrite it —
		// permanently forgetting (leaking) every orphan it recorded. Preserve
		// the bytes in a sidecar for diagnosis and start from empty state.
		_ = os.Rename(pt.path, pt.path+".corrupt")
		return PIDTracking{}
	}
	return tracking
}

// Add adds a process to the tracking file.
func (pt *FilePIDTracker) Add(id string, pid int, pgid int, projectPath string) error {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	tracking := pt.loadLocked()

	// Remove existing entry with same ID and project path
	for i := len(tracking.Processes) - 1; i >= 0; i-- {
		p := tracking.Processes[i]
		if p.ID == id && p.ProjectPath == projectPath {
			tracking.Processes = append(tracking.Processes[:i], tracking.Processes[i+1:]...)
		}
	}

	// Add new entry, capturing the process identity so a later orphan sweep can
	// tell the original process from a recycled PID.
	tracking.Processes = append(tracking.Processes, TrackedProcess{
		ID:          id,
		PID:         pid,
		PGID:        pgid,
		ProjectPath: projectPath,
		Identity:    processIdentity(pid),
		StartedAt:   time.Now(),
	})

	return pt.saveLocked(tracking)
}

// Remove removes a process from the tracking file.
func (pt *FilePIDTracker) Remove(id string, projectPath string) error {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	tracking := pt.loadLocked()

	// Remove matching entries
	for i := len(tracking.Processes) - 1; i >= 0; i-- {
		p := tracking.Processes[i]
		if p.ID == id && p.ProjectPath == projectPath {
			tracking.Processes = append(tracking.Processes[:i], tracking.Processes[i+1:]...)
		}
	}

	return pt.saveLocked(tracking)
}

// SetDaemonPID sets the current daemon PID in the tracking file.
func (pt *FilePIDTracker) SetDaemonPID(pid int) error {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	tracking := pt.loadLocked()
	tracking.DaemonPID = pid
	return pt.saveLocked(tracking)
}

// Clear removes the tracking file completely.
func (pt *FilePIDTracker) Clear() error {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	err := os.Remove(pt.path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// saveLocked saves tracking state to disk without acquiring the lock.
func (pt *FilePIDTracker) saveLocked(tracking PIDTracking) error {
	tracking.UpdatedAt = time.Now()

	data, err := json.MarshalIndent(tracking, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal tracking data: %w", err)
	}

	// Ensure directory exists
	dir := filepath.Dir(pt.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create tracking directory: %w", err)
	}

	// Write atomically via temp file
	tmpPath := pt.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write tracking file: %w", err)
	}

	if err := os.Rename(tmpPath, pt.path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename tracking file: %w", err)
	}

	return nil
}

// identityMatches reports whether the live process at proc.PID is still the
// same one we tracked. If we captured an identity at Add time it must match the
// current /proc identity exactly; a mismatch means the PID was recycled and the
// process must NOT be killed. When no identity was captured (empty — e.g. an
// older tracking file, or a platform that can't read it) we conservatively
// allow the kill, preserving the prior liveness-only behavior.
func identityMatches(proc TrackedProcess) bool {
	if proc.Identity == "" {
		return true
	}
	return processIdentity(proc.PID) == proc.Identity
}

func descendantIdentityMatches(proc TrackedProcess, pid int) bool {
	if proc.DescendantIdentities == nil {
		return false
	}
	identity := proc.DescendantIdentities[pid]
	if identity == "" {
		return false
	}
	return processIdentity(pid) == identity
}

// CleanupOrphans checks for orphaned processes from a previous daemon crash
// and kills them. This should be called on daemon startup.
func (pt *FilePIDTracker) CleanupOrphans(currentDaemonPID int) (killedCount int, err error) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	tracking := pt.loadLocked()

	// If daemon PID matches current process, this is a clean restart
	// or the tracking file was already cleaned up
	if tracking.DaemonPID == currentDaemonPID {
		// Update timestamp but don't clean anything
		return 0, pt.saveLocked(tracking)
	}

	// Different daemon PID means we're recovering from a crash.
	// Check each tracked process and kill if still alive AND still the same
	// process we tracked — a bare liveness check would SIGKILL whatever
	// unrelated process the kernel has since recycled the PID into.
	for _, proc := range tracking.Processes {
		// Only act when the tracked parent is verifiably still OUR process (alive
		// AND identity match). Stored descendant PIDs carry no identity token, so
		// chasing them after the parent is gone would SIGKILL whatever unrelated
		// process the kernel has since recycled those hours-old PIDs into.
		if !(isProcessAlive(proc.PID) && identityMatches(proc)) {
			continue
		}

		// Kill stored descendants first (catches processes reparented since the
		// parent died). A descendant PID is a single process, not a group leader,
		// so pass 0 as pgid — killOrphanProcess only signals the group when
		// pid == pgid.
		for _, dpid := range proc.DescendantPIDs {
			if isProcessAlive(dpid) && descendantIdentityMatches(proc, dpid) {
				killOrphanProcess(dpid, 0)
				killedCount++
			}
		}

		killOrphanProcess(proc.PID, proc.PGID)
		killedCount++
		time.Sleep(10 * time.Millisecond)
	}

	// Clear old tracking and set new daemon PID
	tracking.Processes = nil
	tracking.DaemonPID = currentDaemonPID

	return killedCount, pt.saveLocked(tracking)
}

// ListTracked returns all currently tracked processes.
func (pt *FilePIDTracker) ListTracked() []TrackedProcess {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	tracking := pt.loadLocked()
	return tracking.Processes
}

// UpdateDescendants updates the descendant PIDs for a tracked process.
func (pt *FilePIDTracker) UpdateDescendants(pid int, descendants []int) error {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	tracking := pt.loadLocked()
	now := time.Now()

	for i := range tracking.Processes {
		if tracking.Processes[i].PID == pid {
			tracking.Processes[i].DescendantPIDs = descendants
			tracking.Processes[i].DescendantIdentities = descendantIdentities(descendants)
			tracking.Processes[i].LastScanAt = now
			return pt.saveLocked(tracking)
		}
	}
	return nil
}

func descendantIdentities(descendants []int) map[int]string {
	if len(descendants) == 0 {
		return nil
	}
	identities := make(map[int]string, len(descendants))
	for _, pid := range descendants {
		if identity := processIdentity(pid); identity != "" {
			identities[pid] = identity
		}
	}
	if len(identities) == 0 {
		return nil
	}
	return identities
}

// ListAllPIDs returns a flat list of all tracked PIDs and their descendants.
func (pt *FilePIDTracker) ListAllPIDs() []int {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	tracking := pt.loadLocked()
	seen := make(map[int]struct{})
	var result []int

	for _, proc := range tracking.Processes {
		if _, ok := seen[proc.PID]; !ok {
			seen[proc.PID] = struct{}{}
			result = append(result, proc.PID)
		}
		for _, dpid := range proc.DescendantPIDs {
			if _, ok := seen[dpid]; !ok {
				seen[dpid] = struct{}{}
				result = append(result, dpid)
			}
		}
	}
	return result
}

// GetDescendants returns the stored descendant PIDs for a given process PID.
func (pt *FilePIDTracker) GetDescendants(pid int) []int {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	tracking := pt.loadLocked()
	for _, proc := range tracking.Processes {
		if proc.PID == pid {
			return proc.DescendantPIDs
		}
	}
	return nil
}

// GetVerifiedDescendants returns stored descendant PIDs whose current process
// identity still matches the identity captured during the last scan.
func (pt *FilePIDTracker) GetVerifiedDescendants(pid int) []int {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	tracking := pt.loadLocked()
	for _, proc := range tracking.Processes {
		if proc.PID != pid {
			continue
		}
		var result []int
		for _, dpid := range proc.DescendantPIDs {
			if descendantIdentityMatches(proc, dpid) {
				result = append(result, dpid)
			}
		}
		return result
	}
	return nil
}

// StartDescendantScanner starts a background goroutine that periodically
// scans the descendant tree of each tracked process and persists results.
// The scanner stops when the provided context is cancelled.
func (pt *FilePIDTracker) StartDescendantScanner(ctx context.Context, interval time.Duration) {
	go pt.descendantScanLoop(ctx, interval)
}

// descendantScanLoop runs the periodic scan until ctx is cancelled.
func (pt *FilePIDTracker) descendantScanLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pt.scanDescendants()
		}
	}
}

// scanDescendants scans all tracked processes and updates their descendants.
func (pt *FilePIDTracker) scanDescendants() {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	tracking := pt.loadLocked()
	if len(tracking.Processes) == 0 {
		return
	}

	now := time.Now()
	changed := false

	for i := range tracking.Processes {
		pid := tracking.Processes[i].PID
		if !isProcessAlive(pid) {
			continue
		}
		descendants := scanDescendantPIDs(pid)
		tracking.Processes[i].DescendantPIDs = descendants
		tracking.Processes[i].DescendantIdentities = descendantIdentities(descendants)
		tracking.Processes[i].LastScanAt = now
		changed = true
	}

	if changed {
		_ = pt.saveLocked(tracking)
	}
}
