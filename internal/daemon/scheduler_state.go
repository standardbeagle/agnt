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

// SchedulerStateManager handles persisting scheduled tasks per-project.
type SchedulerStateManager struct {
	mu sync.RWMutex

	// Cache of known project directories with tasks
	knownProjects sync.Map // map[string]bool
}

// NewSchedulerStateManager creates a new scheduler state manager.
func NewSchedulerStateManager() *SchedulerStateManager {
	return &SchedulerStateManager{}
}

// getStatePath returns the path to the state file for a project.
func (m *SchedulerStateManager) getStatePath(projectPath string) string {
	return filepath.Join(projectPath, SchedulerStateDir, SchedulerStateFile)
}

// SaveTask saves or updates a task in the project's state file.
func (m *SchedulerStateManager) SaveTask(task *ScheduledTask) error {
	if task.ProjectPath == "" {
		return fmt.Errorf("task has no project path")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	statePath := m.getStatePath(task.ProjectPath)
	stateDir := filepath.Dir(statePath)

	// Ensure state directory exists
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	// Load existing state
	state, err := m.loadStateLocked(statePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to load state: %w", err)
	}
	if state == nil {
		state = &PersistedTaskState{Version: 1}
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

	// Save state
	if err := m.saveStateLocked(statePath, state); err != nil {
		return fmt.Errorf("failed to save state: %w", err)
	}

	// Track this project
	m.knownProjects.Store(task.ProjectPath, true)

	return nil
}

// RemoveTask removes a task from the project's state file.
func (m *SchedulerStateManager) RemoveTask(taskID string, projectPath string) error {
	if projectPath == "" {
		return fmt.Errorf("project path is required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	statePath := m.getStatePath(projectPath)

	// Load existing state
	state, err := m.loadStateLocked(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No state file, nothing to remove
		}
		return fmt.Errorf("failed to load state: %w", err)
	}

	// Remove task
	for i, t := range state.Tasks {
		if t.ID == taskID {
			state.Tasks = append(state.Tasks[:i], state.Tasks[i+1:]...)
			break
		}
	}

	// Save state (or remove file if empty)
	if len(state.Tasks) == 0 {
		os.Remove(statePath)
		return nil
	}

	return m.saveStateLocked(statePath, state)
}

// LoadTasks loads all tasks for a specific project.
func (m *SchedulerStateManager) LoadTasks(projectPath string) ([]*ScheduledTask, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	statePath := m.getStatePath(projectPath)
	state, err := m.loadStateLocked(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	// Track this project
	m.knownProjects.Store(projectPath, true)

	return state.Tasks, nil
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

// loadStateLocked loads state from a file (caller must hold lock).
func (m *SchedulerStateManager) loadStateLocked(statePath string) (*PersistedTaskState, error) {
	data, err := os.ReadFile(statePath)
	if err != nil {
		return nil, err
	}

	var state PersistedTaskState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse state: %w", err)
	}

	return &state, nil
}

// saveStateLocked saves state to a file (caller must hold lock).
func (m *SchedulerStateManager) saveStateLocked(statePath string, state *PersistedTaskState) error {
	state.UpdatedAt = time.Now().Format(time.RFC3339)

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	// Write atomically via temp file
	tmpPath := statePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}

	if err := os.Rename(tmpPath, statePath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename state file: %w", err)
	}

	return nil
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
	defer m.mu.Unlock()

	statePath := m.getStatePath(projectPath)
	if err := os.Remove(statePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove state file: %w", err)
	}

	m.knownProjects.Delete(projectPath)
	return nil
}
