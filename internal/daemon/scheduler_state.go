// Package daemon provides the background daemon for persistent state management.
package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SchedulerStateFile is the name of the per-project task state file.
const SchedulerStateFile = "scheduled-tasks.json"

// SchedulerStateDir is the directory within each project for agnt state.
const SchedulerStateDir = ".agnt"

// PersistedTaskState represents the structure of the task state file.
type PersistedTaskState struct {
	Version   int              `json:"version"`
	Tasks     []*ScheduledTask `json:"tasks"`
	UpdatedAt string           `json:"updated_at"`
}

// schedulerWriteReq is a request to write a project's state to disk.
type schedulerWriteReq struct {
	projectPath string
}

// SchedulerStateManager handles persisting scheduled tasks per-project.
// State is cached in memory per-project. Reads are served from cache.
// Mutations update cache under lock, then enqueue async disk writes
// via a write-behind channel.
type SchedulerStateManager struct {
	mu sync.RWMutex

	// In-memory cache of per-project state. Keyed by project path.
	cache map[string]*PersistedTaskState

	// Cache of known project directories with tasks
	knownProjects sync.Map // map[string]bool

	// Write-behind: pending projects needing a disk write
	pendingWrites map[string]struct{}
	writeCh       chan struct{}
	flushCh       chan chan error
	stopCh        chan struct{}
	stopped       chan struct{}
	closeOnce     sync.Once
	saveInterval  time.Duration
}

// NewSchedulerStateManager creates a new scheduler state manager.
func NewSchedulerStateManager() *SchedulerStateManager {
	return NewSchedulerStateManagerWithInterval(1 * time.Second)
}

// NewSchedulerStateManagerWithInterval creates a scheduler state manager
// with a custom debounce interval.
func NewSchedulerStateManagerWithInterval(interval time.Duration) *SchedulerStateManager {
	m := &SchedulerStateManager{
		cache:         make(map[string]*PersistedTaskState),
		pendingWrites: make(map[string]struct{}),
		writeCh:       make(chan struct{}, 1),
		flushCh:       make(chan chan error),
		stopCh:        make(chan struct{}),
		stopped:       make(chan struct{}),
		saveInterval:  interval,
	}
	go m.writeLoop()
	return m
}

// getStatePath returns the path to the state file for a project.
func (m *SchedulerStateManager) getStatePath(projectPath string) string {
	return filepath.Join(projectPath, SchedulerStateDir, SchedulerStateFile)
}

// ensureCached loads a project's state from disk into cache if not present.
// Caller must hold at least a read lock. Returns the cached state (may be nil).
func (m *SchedulerStateManager) ensureCachedLocked(projectPath string) (*PersistedTaskState, error) {
	if state, ok := m.cache[projectPath]; ok {
		return state, nil
	}

	// Not in cache — need to read from disk (only happens once per project)
	statePath := m.getStatePath(projectPath)
	data, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, err
		}
		return nil, fmt.Errorf("failed to read state: %w", err)
	}

	var state PersistedTaskState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse state: %w", err)
	}

	m.cache[projectPath] = &state
	return &state, nil
}

// enqueueProjectWrite marks a project as needing a disk write and signals the writer.
// Caller must hold the write lock.
func (m *SchedulerStateManager) enqueueProjectWrite(projectPath string) {
	m.pendingWrites[projectPath] = struct{}{}
	select {
	case m.writeCh <- struct{}{}:
	default:
	}
}

// SaveTask saves or updates a task in the project's state file.
func (m *SchedulerStateManager) SaveTask(task *ScheduledTask) error {
	if task.ProjectPath == "" {
		return fmt.Errorf("task has no project path")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	state, err := m.ensureCachedLocked(task.ProjectPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to load state: %w", err)
	}
	if state == nil {
		state = &PersistedTaskState{Version: 1}
		m.cache[task.ProjectPath] = state
	}

	// Update or add task
	found := false
	for i, t := range state.Tasks {
		if t.ID == task.ID {
			state.Tasks[i] = task
			found = true
			break
		}
	}
	if !found {
		state.Tasks = append(state.Tasks, task)
	}

	// Track this project
	m.knownProjects.Store(task.ProjectPath, true)

	// Enqueue async write
	m.enqueueProjectWrite(task.ProjectPath)

	return nil
}

// RemoveTask removes a task from the project's state file.
func (m *SchedulerStateManager) RemoveTask(taskID string, projectPath string) error {
	if projectPath == "" {
		return fmt.Errorf("project path is required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	state, err := m.ensureCachedLocked(projectPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No state file, nothing to remove
		}
		return fmt.Errorf("failed to load state: %w", err)
	}
	if state == nil {
		return nil
	}

	// Remove task
	for i, t := range state.Tasks {
		if t.ID == taskID {
			state.Tasks = append(state.Tasks[:i], state.Tasks[i+1:]...)
			break
		}
	}

	// If empty, remove from cache and delete file
	if len(state.Tasks) == 0 {
		delete(m.cache, projectPath)
		statePath := m.getStatePath(projectPath)
		os.Remove(statePath)
		return nil
	}

	m.enqueueProjectWrite(projectPath)
	return nil
}

// LoadTasks loads all tasks for a specific project from the in-memory cache.
func (m *SchedulerStateManager) LoadTasks(projectPath string) ([]*ScheduledTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, err := m.ensureCachedLocked(projectPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if state == nil {
		return nil, nil
	}

	// Track this project
	m.knownProjects.Store(projectPath, true)

	// Return a copy of the tasks slice
	result := make([]*ScheduledTask, len(state.Tasks))
	copy(result, state.Tasks)
	return result, nil
}

// LoadAllTasks loads all tasks from all known project directories.
func (m *SchedulerStateManager) LoadAllTasks() []*ScheduledTask {
	var result []*ScheduledTask

	m.knownProjects.Range(func(key, value interface{}) bool {
		projectPath := key.(string)
		tasks, err := m.LoadTasks(projectPath)
		if err == nil {
			result = append(result, tasks...)
		}
		return true
	})

	return result
}

// ScanForProjects scans directories for existing task state files.
// This is called at daemon startup to discover persisted tasks.
func (m *SchedulerStateManager) ScanForProjects(basePaths []string) {
	for _, basePath := range basePaths {
		statePath := m.getStatePath(basePath)
		if _, err := os.Stat(statePath); err == nil {
			m.knownProjects.Store(basePath, true)
		}
	}
}

// RegisterProject registers a project directory for task tracking.
func (m *SchedulerStateManager) RegisterProject(projectPath string) {
	m.knownProjects.Store(projectPath, true)
}

// ListProjectsWithTasks returns all known project directories with tasks.
func (m *SchedulerStateManager) ListProjectsWithTasks() []string {
	var result []string
	m.knownProjects.Range(func(key, value interface{}) bool {
		result = append(result, key.(string))
		return true
	})
	return result
}

// ClearProject removes all tasks for a project.
func (m *SchedulerStateManager) ClearProject(projectPath string) error {
	m.mu.Lock()
	delete(m.cache, projectPath)
	delete(m.pendingWrites, projectPath)
	m.mu.Unlock()

	statePath := m.getStatePath(projectPath)
	if err := os.Remove(statePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove state file: %w", err)
	}

	m.knownProjects.Delete(projectPath)
	return nil
}

// Flush ensures all pending writes are persisted to disk immediately.
func (m *SchedulerStateManager) Flush() error {
	done := make(chan error, 1)
	select {
	case m.flushCh <- done:
		return <-done
	case <-m.stopped:
		// Writer already stopped — do direct write
		return m.doWriteAll()
	}
}

// Close stops the background writer after flushing pending state.
func (m *SchedulerStateManager) Close() error {
	var err error
	m.closeOnce.Do(func() {
		close(m.stopCh)
		<-m.stopped
	})
	return err
}

// writeLoop is the background goroutine that coalesces and writes state.
func (m *SchedulerStateManager) writeLoop() {
	defer close(m.stopped)

	for {
		select {
		case <-m.writeCh:
			timer := time.NewTimer(m.saveInterval)
			select {
			case <-timer.C:
			case done := <-m.flushCh:
				timer.Stop()
				done <- m.doWriteAll()
				continue
			case <-m.stopCh:
				timer.Stop()
				m.doWriteAll()
				return
			}
			m.drainWriteCh()
			m.doWriteAll()

		case done := <-m.flushCh:
			done <- m.doWriteAll()

		case <-m.stopCh:
			m.drainWriteCh()
			m.doWriteAll()
			return
		}
	}
}

func (m *SchedulerStateManager) drainWriteCh() {
	select {
	case <-m.writeCh:
	default:
	}
}

// doWriteAll writes all pending project states to disk.
func (m *SchedulerStateManager) doWriteAll() error {
	// Snapshot pending writes and their state under lock
	m.mu.Lock()
	pending := m.pendingWrites
	m.pendingWrites = make(map[string]struct{})

	type writeJob struct {
		path string
		data []byte
	}
	var jobs []writeJob

	for projectPath := range pending {
		state, ok := m.cache[projectPath]
		if !ok {
			continue
		}
		// Clone state for serialization
		clone := *state
		clone.UpdatedAt = time.Now().Format(time.RFC3339)
		tasks := make([]*ScheduledTask, len(state.Tasks))
		copy(tasks, state.Tasks)
		clone.Tasks = tasks

		data, err := json.MarshalIndent(&clone, "", "  ")
		if err != nil {
			m.mu.Unlock()
			return fmt.Errorf("failed to marshal state for %s: %w", projectPath, err)
		}
		jobs = append(jobs, writeJob{path: m.getStatePath(projectPath), data: data})
	}
	m.mu.Unlock()

	// Write all files without holding the lock
	for _, job := range jobs {
		stateDir := filepath.Dir(job.path)
		if err := os.MkdirAll(stateDir, 0755); err != nil {
			return fmt.Errorf("failed to create state directory: %w", err)
		}

		tmpPath := job.path + ".tmp"
		if err := os.WriteFile(tmpPath, job.data, 0644); err != nil {
			return fmt.Errorf("failed to write state file: %w", err)
		}

		if err := os.Rename(tmpPath, job.path); err != nil {
			os.Remove(tmpPath)
			return fmt.Errorf("failed to rename state file: %w", err)
		}
	}

	return nil
}
