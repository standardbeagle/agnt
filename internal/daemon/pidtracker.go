package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TrackedProcess represents a process being tracked for orphan cleanup.
type TrackedProcess struct {
	ID          string    `json:"id"`
	PID         int       `json:"pid"`
	PGID        int       `json:"pgid"`
	ProjectPath string    `json:"project_path"`
	StartedAt   time.Time `json:"started_at"`
}

// PIDTracking holds the complete tracking state.
type PIDTracking struct {
	DaemonPID int              `json:"daemon_pid"`
	Processes []TrackedProcess `json:"processes"`
	UpdatedAt time.Time        `json:"updated_at"`
}

// PIDTracker manages process ID tracking for orphan cleanup.
// It persists PIDs to disk so that if the daemon crashes, we can
// clean up orphaned processes on the next startup.
type PIDTracker struct {
	path string
	mu   sync.Mutex
}

// NewPIDTracker creates a new PID tracker with the given file path.
func NewPIDTracker(path string) *PIDTracker {
	if path == "" {
		path = defaultPIDTrackingPath()
	}
	return &PIDTracker{path: path}
}

// defaultPIDTrackingPath returns the default path for PID tracking file.
func defaultPIDTrackingPath() string {
	// Use XDG_STATE_HOME if available
	if stateHome := os.Getenv("XDG_STATE_HOME"); stateHome != "" {
		return filepath.Join(stateHome, "devtool-mcp", "pids.json")
	}

	// Fall back to ~/.local/state
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", "devtool-mcp", "pids.json")
	}

	// Last resort: temp directory
	return filepath.Join(os.TempDir(), "devtool-mcp-pids.json")
}

// Load reads the current tracking state from disk.
func (pt *PIDTracker) Load() PIDTracking {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	return pt.loadLocked()
}

// loadLocked loads tracking state without acquiring the lock.
func (pt *PIDTracker) loadLocked() PIDTracking {
	var tracking PIDTracking

	data, err := os.ReadFile(pt.path)
	if err != nil {
		// File doesn't exist or can't be read - return empty tracking
		return tracking
	}

	// Best effort unmarshal - ignore errors
	_ = json.Unmarshal(data, &tracking)
	return tracking
}

// Add adds a process to the tracking file.
func (pt *PIDTracker) Add(id string, pid int, pgid int, projectPath string) error {
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

	// Add new entry
	tracking.Processes = append(tracking.Processes, TrackedProcess{
		ID:          id,
		PID:         pid,
		PGID:        pgid,
		ProjectPath: projectPath,
		StartedAt:   time.Now(),
	})

	return pt.saveLocked(tracking)
}

// Remove removes a process from the tracking file.
func (pt *PIDTracker) Remove(id string, projectPath string) error {
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
func (pt *PIDTracker) SetDaemonPID(pid int) error {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	tracking := pt.loadLocked()
	tracking.DaemonPID = pid
	return pt.saveLocked(tracking)
}

// Clear removes the tracking file completely.
func (pt *PIDTracker) Clear() error {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	err := os.Remove(pt.path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// saveLocked saves tracking state to disk without acquiring the lock.
func (pt *PIDTracker) saveLocked(tracking PIDTracking) error {
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

// CleanupOrphans checks for orphaned processes from a previous daemon crash
// and kills them. This should be called on daemon startup.
func (pt *PIDTracker) CleanupOrphans(currentDaemonPID int) (killedCount int, err error) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	tracking := pt.loadLocked()

	// If daemon PID matches current process, this is a clean restart
	// or the tracking file was already cleaned up
	if tracking.DaemonPID == currentDaemonPID {
		// Update timestamp but don't clean anything
		return 0, pt.saveLocked(tracking)
	}

	// Different daemon PID means we're recovering from a crash
	// Check each tracked process and kill if still alive
	for _, proc := range tracking.Processes {
		if isProcessAlive(proc.PID) {
			// Kill the orphan process (platform-specific)
			killOrphanProcess(proc.PID, proc.PGID)

			killedCount++

			// Brief wait for process death
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Clear old tracking and set new daemon PID
	tracking.Processes = nil
	tracking.DaemonPID = currentDaemonPID

	return killedCount, pt.saveLocked(tracking)
}
