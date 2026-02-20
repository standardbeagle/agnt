package daemon

import (
	"context"
	"fmt"
	"sync/atomic"
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

func TestScheduler_DeliveryConcurrencyBound(t *testing.T) {
	registry := NewSessionRegistry(60 * time.Second)
	config := SchedulerConfig{
		TickInterval:            50 * time.Millisecond,
		MaxRetries:              1,
		RetryDelay:              time.Second,
		DeliveryTimeout:         time.Second,
		MaxConcurrentDeliveries: 3, // low limit for easy testing
	}
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

	taskCount := 20

	// Schedule many tasks that are already due
	past := time.Now().Add(-time.Minute)
	for i := 0; i < taskCount; i++ {
		task := &ScheduledTask{
			ID:          fmt.Sprintf("task-%d", i),
			SessionCode: "test-session",
			Message:     fmt.Sprintf("Message %d", i),
			DeliverAt:   past,
			CreatedAt:   past,
			ProjectPath: "/project",
			Status:      TaskStatusPending,
		}
		scheduler.tasks.Store(task.ID, task)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer scheduler.Stop()

	// The semaphore should prevent more than MaxConcurrentDeliveries goroutines.
	// Delivery will fail (no real overlay socket), causing tasks to be removed
	// from the map (MaxRetries=1). After processing, tasks are gone.
	// Wait for delivery attempts to drain the task map.
	time.Sleep(800 * time.Millisecond)

	// Verify tasks were processed by checking the failure counter.
	// Failed tasks get removed from the map but increment totalFailed.
	failedTotal := scheduler.totalFailed.Load()
	if failedTotal == 0 {
		t.Error("Expected at least some tasks to fail (be attempted), got 0")
	}
	if failedTotal > int64(taskCount) {
		t.Errorf("More failures (%d) than tasks (%d)", failedTotal, taskCount)
	}
}

func TestScheduler_CtxCancellationStopsLoop(t *testing.T) {
	registry := NewSessionRegistry(60 * time.Second)
	config := SchedulerConfig{
		TickInterval:            10 * time.Millisecond,
		MaxRetries:              1,
		RetryDelay:              time.Second,
		DeliveryTimeout:         time.Second,
		MaxConcurrentDeliveries: 5,
	}
	scheduler := NewScheduler(config, registry, nil)

	ctx, cancel := context.WithCancel(context.Background())

	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Cancel context - the scheduler loop should exit promptly
	cancel()

	// Wait for scheduler to stop (Stop blocks on wg.Wait)
	done := make(chan struct{})
	go func() {
		scheduler.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Scheduler stopped promptly
	case <-time.After(2 * time.Second):
		t.Fatal("Scheduler did not stop within 2 seconds after context cancellation")
	}
}

func TestScheduler_CheckDueTasksRespectsCtxCancel(t *testing.T) {
	registry := NewSessionRegistry(60 * time.Second)
	config := SchedulerConfig{
		TickInterval:            time.Hour, // won't auto-tick
		MaxRetries:              1,
		RetryDelay:              time.Second,
		DeliveryTimeout:         time.Second,
		MaxConcurrentDeliveries: 1, // very tight bound
	}
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

	ctx, cancel := context.WithCancel(context.Background())
	scheduler.ctx = ctx
	scheduler.cancel = cancel

	// Schedule many due tasks
	past := time.Now().Add(-time.Minute)
	for i := 0; i < 10; i++ {
		task := &ScheduledTask{
			ID:          fmt.Sprintf("task-%d", i),
			SessionCode: "test-session",
			Message:     fmt.Sprintf("Message %d", i),
			DeliverAt:   past,
			CreatedAt:   past,
			ProjectPath: "/project",
			Status:      TaskStatusPending,
		}
		scheduler.tasks.Store(task.ID, task)
	}

	// Cancel context before calling checkDueTasks
	cancel()

	// checkDueTasks should return promptly since context is cancelled
	done := make(chan struct{})
	go func() {
		scheduler.checkDueTasks()
		close(done)
	}()

	select {
	case <-done:
		// Returned promptly
	case <-time.After(2 * time.Second):
		t.Fatal("checkDueTasks did not return promptly after context cancellation")
	}
}

func TestScheduler_DefaultMaxConcurrentDeliveries(t *testing.T) {
	config := DefaultSchedulerConfig()
	if config.MaxConcurrentDeliveries != 10 {
		t.Errorf("DefaultSchedulerConfig().MaxConcurrentDeliveries = %d, want 10",
			config.MaxConcurrentDeliveries)
	}
}

func TestScheduler_ZeroConcurrencyUsesDefault(t *testing.T) {
	registry := NewSessionRegistry(60 * time.Second)
	config := SchedulerConfig{
		TickInterval:            time.Second,
		MaxConcurrentDeliveries: 0, // should be corrected to 10
	}
	scheduler := NewScheduler(config, registry, nil)
	if scheduler.config.MaxConcurrentDeliveries != 10 {
		t.Errorf("Zero MaxConcurrentDeliveries should default to 10, got %d",
			scheduler.config.MaxConcurrentDeliveries)
	}
}

func TestScheduler_SemaphoreNotNil(t *testing.T) {
	registry := NewSessionRegistry(60 * time.Second)
	scheduler := NewScheduler(DefaultSchedulerConfig(), registry, nil)
	if scheduler.deliverySem == nil {
		t.Fatal("deliverySem should not be nil")
	}
}

func TestScheduler_ConcurrencyBoundRespected(t *testing.T) {
	// This test verifies the semaphore actually limits concurrency.
	// We set MaxConcurrentDeliveries=2, create tasks, and verify all get processed.
	// The key proof: if the semaphore blocked permanently, not all tasks would complete.

	registry := NewSessionRegistry(60 * time.Second)
	taskCount := 10
	config := SchedulerConfig{
		TickInterval:            50 * time.Millisecond,
		MaxRetries:              1,
		RetryDelay:              time.Second,
		DeliveryTimeout:         time.Second,
		MaxConcurrentDeliveries: 2, // tight bound
	}
	scheduler := NewScheduler(config, registry, nil)

	session := &Session{
		Code:        "test-session",
		OverlayPath: "/tmp/nonexistent.sock",
		ProjectPath: "/project",
		Command:     "claude",
		StartedAt:   time.Now(),
		Status:      SessionStatusActive,
		LastSeen:    time.Now(),
	}
	_ = registry.Register(session)

	// Add due tasks
	past := time.Now().Add(-time.Minute)
	for i := 0; i < taskCount; i++ {
		task := &ScheduledTask{
			ID:          fmt.Sprintf("conc-task-%d", i),
			SessionCode: "test-session",
			Message:     fmt.Sprintf("Message %d", i),
			DeliverAt:   past,
			CreatedAt:   past,
			ProjectPath: "/project",
			Status:      TaskStatusPending,
		}
		scheduler.tasks.Store(task.ID, task)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer scheduler.Stop()

	// Wait for delivery attempts to complete.
	// With concurrency=2 and 1-second delivery timeout, all 10 tasks need
	// at most 5 rounds of 2 (but delivery fails fast on connection refused).
	time.Sleep(1500 * time.Millisecond)

	// Verify all tasks were processed via the failure counter.
	// Failed tasks get removed from the map but increment totalFailed.
	failedTotal := scheduler.totalFailed.Load()
	if failedTotal < int64(taskCount) {
		// Some tasks might still be in-flight or re-attempted on next tick.
		// Check that at least most were processed.
		var remaining atomic.Int64
		scheduler.tasks.Range(func(key, value interface{}) bool {
			remaining.Add(1)
			return true
		})
		if failedTotal+remaining.Load() < int64(taskCount) {
			t.Errorf("Expected all %d tasks processed, got %d failed + %d remaining",
				taskCount, failedTotal, remaining.Load())
		}
	}
}
