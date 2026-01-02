package daemon

import (
	"context"
	"testing"
	"time"
)

func setupSchedulerTest(t *testing.T) (*Scheduler, *SessionRegistry, func()) {
	t.Helper()
	registry := NewSessionRegistry(60 * time.Second)
	config := DefaultSchedulerConfig()
	config.TickInterval = 100 * time.Millisecond // Faster for tests
	scheduler := NewScheduler(config, registry, nil)

	// Register a test session
	session := &Session{
		Code:        "test-session",
		OverlayPath: "/tmp/test-overlay.sock",
		ProjectPath: "/project",
		Command:     "claude",
		StartedAt:   time.Now(),
		Status:      SessionStatusActive,
		LastSeen:    time.Now(),
	}
	_ = registry.Register(session)

	cleanup := func() {
		scheduler.Stop()
	}
	return scheduler, registry, cleanup
}

func TestScheduler_Schedule(t *testing.T) {
	scheduler, _, cleanup := setupSchedulerTest(t)
	defer cleanup()

	task, err := scheduler.Schedule("test-session", 5*time.Minute, "Test message", "/project")
	if err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}

	if task.ID == "" {
		t.Error("Schedule() returned task with empty ID")
	}
	if task.SessionCode != "test-session" {
		t.Errorf("Schedule() SessionCode = %v, want test-session", task.SessionCode)
	}
	if task.Message != "Test message" {
		t.Errorf("Schedule() Message = %v, want 'Test message'", task.Message)
	}
	if task.Status != TaskStatusPending {
		t.Errorf("Schedule() Status = %v, want pending", task.Status)
	}
	if task.DeliverAt.Before(time.Now()) {
		t.Error("Schedule() DeliverAt should be in the future")
	}
}

func TestScheduler_Schedule_EmptySessionCode(t *testing.T) {
	scheduler, _, cleanup := setupSchedulerTest(t)
	defer cleanup()

	_, err := scheduler.Schedule("", 5*time.Minute, "Test message", "/project")
	if err == nil {
		t.Error("Schedule() should return error for empty session code")
	}
}

func TestScheduler_Schedule_EmptyMessage(t *testing.T) {
	scheduler, _, cleanup := setupSchedulerTest(t)
	defer cleanup()

	_, err := scheduler.Schedule("test-session", 5*time.Minute, "", "/project")
	if err == nil {
		t.Error("Schedule() should return error for empty message")
	}
}

func TestScheduler_Schedule_NegativeDuration(t *testing.T) {
	scheduler, _, cleanup := setupSchedulerTest(t)
	defer cleanup()

	_, err := scheduler.Schedule("test-session", -5*time.Minute, "Test message", "/project")
	if err == nil {
		t.Error("Schedule() should return error for negative duration")
	}
}

func TestScheduler_Schedule_SessionNotFound(t *testing.T) {
	scheduler, _, cleanup := setupSchedulerTest(t)
	defer cleanup()

	_, err := scheduler.Schedule("nonexistent-session", 5*time.Minute, "Test message", "/project")
	if err == nil {
		t.Error("Schedule() should return error for nonexistent session")
	}
}

func TestScheduler_GetTask(t *testing.T) {
	scheduler, _, cleanup := setupSchedulerTest(t)
	defer cleanup()

	task, _ := scheduler.Schedule("test-session", 5*time.Minute, "Test message", "/project")

	got, found := scheduler.GetTask(task.ID)
	if !found {
		t.Fatal("GetTask() returned false, expected true")
	}
	if got.ID != task.ID {
		t.Errorf("GetTask() ID = %v, want %v", got.ID, task.ID)
	}
}

func TestScheduler_GetTask_NotFound(t *testing.T) {
	scheduler, _, cleanup := setupSchedulerTest(t)
	defer cleanup()

	_, found := scheduler.GetTask("nonexistent")
	if found {
		t.Error("GetTask() should return false for nonexistent task")
	}
}

func TestScheduler_Cancel(t *testing.T) {
	scheduler, _, cleanup := setupSchedulerTest(t)
	defer cleanup()

	task, _ := scheduler.Schedule("test-session", 5*time.Minute, "Test message", "/project")

	err := scheduler.Cancel(task.ID)
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}

	// Task should be removed after cancellation
	_, found := scheduler.GetTask(task.ID)
	if found {
		t.Error("GetTask() should return false after Cancel()")
	}
}

func TestScheduler_Cancel_NotFound(t *testing.T) {
	scheduler, _, cleanup := setupSchedulerTest(t)
	defer cleanup()

	err := scheduler.Cancel("nonexistent")
	if err == nil {
		t.Error("Cancel() should return error for nonexistent task")
	}
}

func TestScheduler_ListTasks(t *testing.T) {
	scheduler, _, cleanup := setupSchedulerTest(t)
	defer cleanup()

	// Add multiple tasks
	scheduler.Schedule("test-session", 5*time.Minute, "Message 1", "/project")
	scheduler.Schedule("test-session", 10*time.Minute, "Message 2", "/project")
	scheduler.Schedule("test-session", 15*time.Minute, "Message 3", "/project")

	tasks := scheduler.ListTasks("", true) // global list
	if len(tasks) != 3 {
		t.Errorf("ListTasks() returned %d tasks, want 3", len(tasks))
	}
}

func TestScheduler_ListTasksByProject(t *testing.T) {
	registry := NewSessionRegistry(60 * time.Second)
	config := DefaultSchedulerConfig()
	scheduler := NewScheduler(config, registry, nil)
	defer scheduler.Stop()

	// Register sessions in different projects
	session1 := &Session{
		Code:        "session-a",
		OverlayPath: "/tmp/a.sock",
		ProjectPath: "/project-a",
		Command:     "claude",
		StartedAt:   time.Now(),
		Status:      SessionStatusActive,
		LastSeen:    time.Now(),
	}
	session2 := &Session{
		Code:        "session-b",
		OverlayPath: "/tmp/b.sock",
		ProjectPath: "/project-b",
		Command:     "claude",
		StartedAt:   time.Now(),
		Status:      SessionStatusActive,
		LastSeen:    time.Now(),
	}
	_ = registry.Register(session1)
	_ = registry.Register(session2)

	// Add tasks in different projects
	scheduler.Schedule("session-a", 5*time.Minute, "Message 1", "/project-a")
	scheduler.Schedule("session-a", 10*time.Minute, "Message 2", "/project-a")
	scheduler.Schedule("session-b", 15*time.Minute, "Message 3", "/project-b")

	// List tasks in project-a
	tasks := scheduler.ListTasks("/project-a", false)
	if len(tasks) != 2 {
		t.Errorf("ListTasks() for project-a returned %d tasks, want 2", len(tasks))
	}

	// List tasks in project-b
	tasks = scheduler.ListTasks("/project-b", false)
	if len(tasks) != 1 {
		t.Errorf("ListTasks() for project-b returned %d tasks, want 1", len(tasks))
	}
}

func TestScheduler_ListPendingTasks(t *testing.T) {
	scheduler, _, cleanup := setupSchedulerTest(t)
	defer cleanup()

	// Add tasks
	task1, _ := scheduler.Schedule("test-session", 5*time.Minute, "Message 1", "/project")
	scheduler.Schedule("test-session", 10*time.Minute, "Message 2", "/project")

	// Cancel one
	scheduler.Cancel(task1.ID)

	pending := scheduler.ListPendingTasks("", true)
	if len(pending) != 1 {
		t.Errorf("ListPendingTasks() returned %d tasks, want 1", len(pending))
	}
}

func TestScheduler_Info(t *testing.T) {
	scheduler, _, cleanup := setupSchedulerTest(t)
	defer cleanup()

	scheduler.Schedule("test-session", 5*time.Minute, "Message 1", "/project")
	task, _ := scheduler.Schedule("test-session", 10*time.Minute, "Message 2", "/project")
	scheduler.Cancel(task.ID)

	info := scheduler.Info()
	if info.TotalScheduled != 2 {
		t.Errorf("Info() TotalScheduled = %d, want 2", info.TotalScheduled)
	}
	if info.TotalCancelled != 1 {
		t.Errorf("Info() TotalCancelled = %d, want 1", info.TotalCancelled)
	}
	if info.PendingCount != 1 {
		t.Errorf("Info() PendingCount = %d, want 1", info.PendingCount)
	}
}

func TestScheduler_StartStop(t *testing.T) {
	scheduler, _, cleanup := setupSchedulerTest(t)
	defer cleanup()

	ctx := context.Background()

	// Start scheduler
	err := scheduler.Start(ctx)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	// Start again should fail
	err = scheduler.Start(ctx)
	if err == nil {
		t.Error("Second Start() should return error")
	}

	// Stop scheduler
	scheduler.Stop()

	// Stop again should be safe
	scheduler.Stop() // Should not panic
}

func TestScheduledTask_ToJSON(t *testing.T) {
	now := time.Now()
	task := &ScheduledTask{
		ID:          "task-1",
		SessionCode: "test-session",
		Message:     "Test message",
		DeliverAt:   now.Add(5 * time.Minute),
		CreatedAt:   now,
		ProjectPath: "/project",
		Status:      TaskStatusPending,
		Attempts:    0,
	}

	json := task.ToJSON()
	if json["id"] != "task-1" {
		t.Errorf("ToJSON() id = %v, want task-1", json["id"])
	}
	if json["session_code"] != "test-session" {
		t.Errorf("ToJSON() session_code = %v, want test-session", json["session_code"])
	}
	if json["status"] != "pending" {
		t.Errorf("ToJSON() status = %v, want pending", json["status"])
	}
}

func TestDefaultSchedulerConfig(t *testing.T) {
	config := DefaultSchedulerConfig()

	if config.TickInterval != 1*time.Second {
		t.Errorf("DefaultSchedulerConfig() TickInterval = %v, want 1s", config.TickInterval)
	}
	if config.MaxRetries != 3 {
		t.Errorf("DefaultSchedulerConfig() MaxRetries = %d, want 3", config.MaxRetries)
	}
	if config.RetryDelay != 5*time.Second {
		t.Errorf("DefaultSchedulerConfig() RetryDelay = %v, want 5s", config.RetryDelay)
	}
}

func TestNewScheduler_WithEmptyConfig(t *testing.T) {
	registry := NewSessionRegistry(60 * time.Second)

	// Create scheduler with empty config - should use defaults
	scheduler := NewScheduler(SchedulerConfig{}, registry, nil)

	if scheduler == nil {
		t.Fatal("NewScheduler returned nil")
	}
	if scheduler.config.TickInterval != 1*time.Second {
		t.Errorf("NewScheduler() with empty config should use default TickInterval, got %v", scheduler.config.TickInterval)
	}
}

func TestScheduler_Start_AlreadyStarted(t *testing.T) {
	registry := NewSessionRegistry(60 * time.Second)
	scheduler := NewScheduler(DefaultSchedulerConfig(), registry, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// First start should succeed
	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("First Start() failed: %v", err)
	}
	defer scheduler.Stop()

	// Second start should fail
	err := scheduler.Start(ctx)
	if err == nil {
		t.Error("Second Start() should return error for already started scheduler")
	}
}
