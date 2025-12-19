package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPIDTracker_BasicOperations(t *testing.T) {
	tmpDir := t.TempDir()
	trackerPath := filepath.Join(tmpDir, "pids.json")

	tracker := NewPIDTracker(trackerPath)

	// Test Add
	err := tracker.Add("dev", 1234, 1234, "/home/user/project")
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// Test Load
	tracking := tracker.Load()
	if len(tracking.Processes) != 1 {
		t.Fatalf("Expected 1 process, got %d", len(tracking.Processes))
	}

	proc := tracking.Processes[0]
	if proc.ID != "dev" || proc.PID != 1234 || proc.PGID != 1234 || proc.ProjectPath != "/home/user/project" {
		t.Fatalf("Process data mismatch: %+v", proc)
	}

	// Test Add with same ID replaces
	err = tracker.Add("dev", 5678, 5678, "/home/user/project")
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	tracking = tracker.Load()
	if len(tracking.Processes) != 1 {
		t.Fatalf("Expected 1 process after replace, got %d", len(tracking.Processes))
	}
	if tracking.Processes[0].PID != 5678 {
		t.Fatalf("Expected PID 5678, got %d", tracking.Processes[0].PID)
	}

	// Test Remove
	err = tracker.Remove("dev", "/home/user/project")
	if err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	tracking = tracker.Load()
	if len(tracking.Processes) != 0 {
		t.Fatalf("Expected 0 processes after remove, got %d", len(tracking.Processes))
	}

	// Test Clear
	tracker.Add("dev", 1234, 1234, "/home/user/project")
	err = tracker.Clear()
	if err != nil {
		t.Fatalf("Clear failed: %v", err)
	}

	if _, err := os.Stat(trackerPath); !os.IsNotExist(err) {
		t.Fatal("Tracking file should not exist after Clear")
	}
}

func TestPIDTracker_SetDaemonPID(t *testing.T) {
	tmpDir := t.TempDir()
	trackerPath := filepath.Join(tmpDir, "pids.json")

	tracker := NewPIDTracker(trackerPath)

	// Set daemon PID
	err := tracker.SetDaemonPID(9999)
	if err != nil {
		t.Fatalf("SetDaemonPID failed: %v", err)
	}

	// Load and verify
	tracking := tracker.Load()
	if tracking.DaemonPID != 9999 {
		t.Fatalf("Expected daemon PID 9999, got %d", tracking.DaemonPID)
	}
}

func TestPIDTracker_CleanupOrphans_SameDaemon(t *testing.T) {
	tmpDir := t.TempDir()
	trackerPath := filepath.Join(tmpDir, "pids.json")

	tracker := NewPIDTracker(trackerPath)

	// Set current daemon PID
	currentPID := os.Getpid()
	tracker.SetDaemonPID(currentPID)

	// Add some processes
	tracker.Add("dev", 1234, 1234, "/home/user/project")
	tracker.Add("test", 5678, 5678, "/home/user/project")

	// Cleanup with same daemon PID should not kill anything
	killedCount, err := tracker.CleanupOrphans(currentPID)
	if err != nil {
		t.Fatalf("CleanupOrphans failed: %v", err)
	}

	if killedCount != 0 {
		t.Fatalf("Expected 0 killed, got %d (same daemon should not cleanup)", killedCount)
	}

	// Processes should still be tracked
	tracking := tracker.Load()
	if len(tracking.Processes) != 2 {
		t.Fatalf("Expected 2 processes still tracked, got %d", len(tracking.Processes))
	}
}

func TestPIDTracker_CleanupOrphans_DifferentDaemon(t *testing.T) {
	tmpDir := t.TempDir()
	trackerPath := filepath.Join(tmpDir, "pids.json")

	tracker := NewPIDTracker(trackerPath)

	// Set old daemon PID (not current process)
	oldDaemonPID := 9999
	tracker.SetDaemonPID(oldDaemonPID)

	// Add fake processes (using PIDs that don't exist)
	tracker.Add("dev", 99991, 99991, "/home/user/project")
	tracker.Add("test", 99992, 99992, "/home/user/project")

	// Cleanup with different daemon PID should try to kill
	currentPID := os.Getpid()
	killedCount, err := tracker.CleanupOrphans(currentPID)
	if err != nil {
		t.Fatalf("CleanupOrphans failed: %v", err)
	}

	// Since PIDs don't exist, kill count should be 0
	if killedCount != 0 {
		t.Fatalf("Expected 0 killed (PIDs don't exist), got %d", killedCount)
	}

	// Processes should be cleared
	tracking := tracker.Load()
	if len(tracking.Processes) != 0 {
		t.Fatalf("Expected 0 processes after cleanup, got %d", len(tracking.Processes))
	}

	// Daemon PID should be updated
	if tracking.DaemonPID != currentPID {
		t.Fatalf("Expected daemon PID %d, got %d", currentPID, tracking.DaemonPID)
	}
}

func TestPIDTracker_MultipleProjects(t *testing.T) {
	tmpDir := t.TempDir()
	trackerPath := filepath.Join(tmpDir, "pids.json")

	tracker := NewPIDTracker(trackerPath)

	// Add processes from different projects
	tracker.Add("dev", 1234, 1234, "/home/user/project1")
	tracker.Add("dev", 5678, 5678, "/home/user/project2")
	tracker.Add("test", 9012, 9012, "/home/user/project1")

	// Should have 3 processes
	tracking := tracker.Load()
	if len(tracking.Processes) != 3 {
		t.Fatalf("Expected 3 processes, got %d", len(tracking.Processes))
	}

	// Remove dev from project1
	tracker.Remove("dev", "/home/user/project1")

	// Should have 2 processes
	tracking = tracker.Load()
	if len(tracking.Processes) != 2 {
		t.Fatalf("Expected 2 processes after remove, got %d", len(tracking.Processes))
	}

	// Verify correct process was removed
	found := false
	for _, p := range tracking.Processes {
		if p.ID == "dev" && p.ProjectPath == "/home/user/project1" {
			found = true
			break
		}
	}
	if found {
		t.Fatal("Process from project1 should have been removed")
	}
}

func TestPIDTracker_Persistence(t *testing.T) {
	tmpDir := t.TempDir()
	trackerPath := filepath.Join(tmpDir, "pids.json")

	// Create first tracker and add data
	tracker1 := NewPIDTracker(trackerPath)
	tracker1.Add("dev", 1234, 1234, "/home/user/project")
	tracker1.SetDaemonPID(9999)

	// Create second tracker (simulates daemon restart)
	tracker2 := NewPIDTracker(trackerPath)

	// Load should restore data
	tracking := tracker2.Load()
	if len(tracking.Processes) != 1 {
		t.Fatalf("Expected 1 process after reload, got %d", len(tracking.Processes))
	}
	if tracking.DaemonPID != 9999 {
		t.Fatalf("Expected daemon PID 9999 after reload, got %d", tracking.DaemonPID)
	}
}

func TestPIDTracker_UpdatedAt(t *testing.T) {
	tmpDir := t.TempDir()
	trackerPath := filepath.Join(tmpDir, "pids.json")

	tracker := NewPIDTracker(trackerPath)

	before := time.Now()
	tracker.Add("dev", 1234, 1234, "/home/user/project")
	after := time.Now()

	tracking := tracker.Load()
	if tracking.UpdatedAt.Before(before) || tracking.UpdatedAt.After(after) {
		t.Fatalf("UpdatedAt timestamp out of range: %v (expected between %v and %v)",
			tracking.UpdatedAt, before, after)
	}
}
