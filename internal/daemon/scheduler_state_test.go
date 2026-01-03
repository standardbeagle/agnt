package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSchedulerStateManager_SaveTask(t *testing.T) {
	tmpDir := t.TempDir()

	sm := NewSchedulerStateManager()

	// Test saving a task
	task := &ScheduledTask{
		ID:          "test-task-1",
		ProjectPath: tmpDir,
		SessionCode: "test-session",
		Message:     "test message",
		DeliverAt:   time.Now().Add(1 * time.Hour),
		Status:      TaskStatusPending,
	}

	err := sm.SaveTask(task)
	if err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}

	// Verify the state file was created
	statePath := filepath.Join(tmpDir, SchedulerStateDir, SchedulerStateFile)
	if _, err := os.Stat(statePath); os.IsNotExist(err) {
		t.Error("State file should exist after save")
	}
}

func TestSchedulerStateManager_SaveTaskEmptyPath(t *testing.T) {
	sm := NewSchedulerStateManager()

	task := &ScheduledTask{
		ID:          "test-task",
		ProjectPath: "", // Empty path should fail
		Message:     "test",
	}

	err := sm.SaveTask(task)
	if err == nil {
		t.Error("Expected error for empty project path")
	}
}

func TestSchedulerStateManager_LoadTasks(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewSchedulerStateManager()

	// Load from non-existent path should return empty
	tasks, err := sm.LoadTasks(tmpDir)
	if err != nil {
		t.Fatalf("LoadTasks failed: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("Expected 0 tasks, got %d", len(tasks))
	}

	// Save a task first
	task := &ScheduledTask{
		ID:          "load-test-1",
		ProjectPath: tmpDir,
		SessionCode: "session-1",
		Message:     "test load",
		DeliverAt:   time.Now().Add(1 * time.Hour),
		Status:      TaskStatusPending,
	}
	if err := sm.SaveTask(task); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}

	// Now load should return the task
	tasks, err = sm.LoadTasks(tmpDir)
	if err != nil {
		t.Fatalf("LoadTasks failed: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("Expected 1 task, got %d", len(tasks))
	}
	if tasks[0].ID != "load-test-1" {
		t.Errorf("Expected task ID load-test-1, got %s", tasks[0].ID)
	}
}

func TestSchedulerStateManager_RemoveTask(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewSchedulerStateManager()

	// Save a task first
	task := &ScheduledTask{
		ID:          "remove-test-1",
		ProjectPath: tmpDir,
		SessionCode: "session-1",
		Message:     "to be removed",
		DeliverAt:   time.Now().Add(1 * time.Hour),
		Status:      TaskStatusPending,
	}
	if err := sm.SaveTask(task); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}

	// Remove the task
	err := sm.RemoveTask("remove-test-1", tmpDir)
	if err != nil {
		t.Fatalf("RemoveTask failed: %v", err)
	}

	// Verify task was removed
	tasks, err := sm.LoadTasks(tmpDir)
	if err != nil {
		t.Fatalf("LoadTasks failed: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("Expected 0 tasks after remove, got %d", len(tasks))
	}
}

func TestSchedulerStateManager_RemoveTaskEmptyPath(t *testing.T) {
	sm := NewSchedulerStateManager()

	err := sm.RemoveTask("task-id", "")
	if err == nil {
		t.Error("Expected error for empty project path")
	}
}

func TestSchedulerStateManager_RemoveTaskNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewSchedulerStateManager()

	// Remove from non-existent state should succeed (no-op)
	err := sm.RemoveTask("nonexistent-task", tmpDir)
	if err != nil {
		t.Fatalf("RemoveTask from non-existent path should not error: %v", err)
	}
}

func TestSchedulerStateManager_ScanForProjects(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewSchedulerStateManager()

	// Create a state directory with a state file
	stateDir := filepath.Join(tmpDir, SchedulerStateDir)
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatalf("Failed to create state dir: %v", err)
	}

	// Create an empty state file
	statePath := filepath.Join(stateDir, SchedulerStateFile)
	if err := os.WriteFile(statePath, []byte(`{"version":1,"tasks":[]}`), 0644); err != nil {
		t.Fatalf("Failed to create state file: %v", err)
	}

	// Scan should discover this project
	sm.ScanForProjects([]string{tmpDir})

	// List projects should return the discovered project
	projects := sm.ListProjectsWithTasks()
	if len(projects) != 1 {
		t.Errorf("Expected 1 project, got %d", len(projects))
	}
	if len(projects) > 0 && projects[0] != tmpDir {
		t.Errorf("Expected project %s, got %s", tmpDir, projects[0])
	}
}

func TestSchedulerStateManager_RegisterProject(t *testing.T) {
	sm := NewSchedulerStateManager()

	// Register a project
	sm.RegisterProject("/test/project")

	// Should be in the list
	projects := sm.ListProjectsWithTasks()
	found := false
	for _, p := range projects {
		if p == "/test/project" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected registered project to be in list")
	}
}

func TestSchedulerStateManager_ListProjectsWithTasks(t *testing.T) {
	sm := NewSchedulerStateManager()

	// Initially empty
	projects := sm.ListProjectsWithTasks()
	if len(projects) != 0 {
		t.Errorf("Expected 0 projects, got %d", len(projects))
	}

	// Register some projects
	sm.RegisterProject("/project1")
	sm.RegisterProject("/project2")

	projects = sm.ListProjectsWithTasks()
	if len(projects) != 2 {
		t.Errorf("Expected 2 projects, got %d", len(projects))
	}
}

func TestSchedulerStateManager_ClearProject(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewSchedulerStateManager()

	// Save a task first
	task := &ScheduledTask{
		ID:          "clear-test-1",
		ProjectPath: tmpDir,
		SessionCode: "session-1",
		Message:     "to be cleared",
		DeliverAt:   time.Now().Add(1 * time.Hour),
		Status:      TaskStatusPending,
	}
	if err := sm.SaveTask(task); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}

	// Clear the project
	err := sm.ClearProject(tmpDir)
	if err != nil {
		t.Fatalf("ClearProject failed: %v", err)
	}

	// State file should be gone
	statePath := filepath.Join(tmpDir, SchedulerStateDir, SchedulerStateFile)
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Error("State file should not exist after clear")
	}

	// Project should be removed from list
	projects := sm.ListProjectsWithTasks()
	for _, p := range projects {
		if p == tmpDir {
			t.Error("Project should not be in list after clear")
		}
	}
}

func TestSchedulerStateManager_LoadAllTasks(t *testing.T) {
	tmpDir1 := t.TempDir()
	tmpDir2 := t.TempDir()
	sm := NewSchedulerStateManager()

	// Save tasks in two projects
	task1 := &ScheduledTask{
		ID:          "all-test-1",
		ProjectPath: tmpDir1,
		SessionCode: "session-1",
		Message:     "task 1",
		DeliverAt:   time.Now().Add(1 * time.Hour),
		Status:      TaskStatusPending,
	}
	if err := sm.SaveTask(task1); err != nil {
		t.Fatalf("SaveTask 1 failed: %v", err)
	}

	task2 := &ScheduledTask{
		ID:          "all-test-2",
		ProjectPath: tmpDir2,
		SessionCode: "session-2",
		Message:     "task 2",
		DeliverAt:   time.Now().Add(2 * time.Hour),
		Status:      TaskStatusPending,
	}
	if err := sm.SaveTask(task2); err != nil {
		t.Fatalf("SaveTask 2 failed: %v", err)
	}

	// Load all tasks
	allTasks := sm.LoadAllTasks()
	if len(allTasks) != 2 {
		t.Errorf("Expected 2 tasks, got %d", len(allTasks))
	}
}

func TestSchedulerStateManager_UpdateExistingTask(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewSchedulerStateManager()

	// Save a task
	task := &ScheduledTask{
		ID:          "update-test-1",
		ProjectPath: tmpDir,
		SessionCode: "session-1",
		Message:     "original message",
		DeliverAt:   time.Now().Add(1 * time.Hour),
		Status:      TaskStatusPending,
	}
	if err := sm.SaveTask(task); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}

	// Update the task
	task.Message = "updated message"
	task.Status = TaskStatusDelivered
	if err := sm.SaveTask(task); err != nil {
		t.Fatalf("SaveTask (update) failed: %v", err)
	}

	// Load and verify update
	tasks, err := sm.LoadTasks(tmpDir)
	if err != nil {
		t.Fatalf("LoadTasks failed: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("Expected 1 task (should update, not add), got %d", len(tasks))
	}
	if tasks[0].Message != "updated message" {
		t.Errorf("Expected updated message, got %s", tasks[0].Message)
	}
	if tasks[0].Status != TaskStatusDelivered {
		t.Errorf("Expected status delivered, got %s", tasks[0].Status)
	}
}

func TestSchedulerStateManager_ClearNonExistentProject(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewSchedulerStateManager()

	// Clear a project that doesn't have a state file
	err := sm.ClearProject(tmpDir)
	if err != nil {
		t.Fatalf("ClearProject should succeed for non-existent state: %v", err)
	}
}

func TestSchedulerStateManager_RemoveLastTask(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewSchedulerStateManager()

	// Save one task
	task := &ScheduledTask{
		ID:          "last-task",
		ProjectPath: tmpDir,
		SessionCode: "session-1",
		Message:     "only task",
		DeliverAt:   time.Now().Add(1 * time.Hour),
		Status:      TaskStatusPending,
	}
	if err := sm.SaveTask(task); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}

	// Verify state file exists
	statePath := filepath.Join(tmpDir, SchedulerStateDir, SchedulerStateFile)
	if _, err := os.Stat(statePath); os.IsNotExist(err) {
		t.Fatal("State file should exist after save")
	}

	// Remove the only task
	if err := sm.RemoveTask("last-task", tmpDir); err != nil {
		t.Fatalf("RemoveTask failed: %v", err)
	}

	// State file should be removed since no tasks remain
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Error("State file should be removed when last task is deleted")
	}
}
