// Package daemon provides the background daemon for persistent state management.
package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// TaskStatus represents the current state of a scheduled task.
type TaskStatus string

const (
	// TaskStatusPending indicates the task is waiting to be delivered.
	TaskStatusPending TaskStatus = "pending"
	// TaskStatusDelivered indicates the task was successfully delivered.
	TaskStatusDelivered TaskStatus = "delivered"
	// TaskStatusFailed indicates the task failed after max retries.
	TaskStatusFailed TaskStatus = "failed"
	// TaskStatusCancelled indicates the task was cancelled.
	TaskStatusCancelled TaskStatus = "cancelled"
)

// ScheduledTask represents a message scheduled for future delivery.
type ScheduledTask struct {
	ID          string     `json:"id"`                   // Unique task ID (e.g., "task-abc123")
	SessionCode string     `json:"session_code"`         // Target session
	Message     string     `json:"message"`              // Message to deliver
	DeliverAt   time.Time  `json:"deliver_at"`           // Scheduled delivery time
	CreatedAt   time.Time  `json:"created_at"`           // When task was created
	ProjectPath string     `json:"project_path"`         // For project-scoped filtering
	Status      TaskStatus `json:"status"`               // Current status
	Attempts    int        `json:"attempts"`             // Delivery attempts
	LastError   string     `json:"last_error,omitempty"` // Last delivery error
}

// ToJSON returns the task as a JSON-serializable map.
func (t *ScheduledTask) ToJSON() map[string]interface{} {
	return map[string]interface{}{
		"id":           t.ID,
		"session_code": t.SessionCode,
		"message":      t.Message,
		"deliver_at":   t.DeliverAt.Format(time.RFC3339),
		"created_at":   t.CreatedAt.Format(time.RFC3339),
		"project_path": t.ProjectPath,
		"status":       string(t.Status),
		"attempts":     t.Attempts,
		"last_error":   t.LastError,
	}
}

// SchedulerConfig configures the scheduler.
type SchedulerConfig struct {
	// TickInterval is how often the scheduler checks for due tasks.
	TickInterval time.Duration
	// MaxRetries is the maximum number of delivery attempts.
	MaxRetries int
	// RetryDelay is the base delay between retries (exponential backoff).
	RetryDelay time.Duration
	// DeliveryTimeout is the timeout for each delivery attempt.
	DeliveryTimeout time.Duration
}

// DefaultSchedulerConfig returns sensible defaults.
func DefaultSchedulerConfig() SchedulerConfig {
	return SchedulerConfig{
		TickInterval:    1 * time.Second,
		MaxRetries:      3,
		RetryDelay:      5 * time.Second,
		DeliveryTimeout: 5 * time.Second,
	}
}

// Scheduler manages scheduled message delivery.
type Scheduler struct {
	config   SchedulerConfig
	registry *SessionRegistry
	stateMgr *SchedulerStateManager

	// Task storage (sync.Map for lock-free access)
	tasks sync.Map // map[string]*ScheduledTask

	// Lifecycle management
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	mu      sync.Mutex
	started bool

	// Statistics (atomics)
	totalScheduled atomic.Int64
	totalDelivered atomic.Int64
	totalFailed    atomic.Int64
	totalCancelled atomic.Int64

	// Task ID counter
	nextTaskID atomic.Int64
}

// NewScheduler creates a new scheduler.
func NewScheduler(config SchedulerConfig, registry *SessionRegistry, stateMgr *SchedulerStateManager) *Scheduler {
	if config.TickInterval == 0 {
		config = DefaultSchedulerConfig()
	}
	return &Scheduler{
		config:   config,
		registry: registry,
		stateMgr: stateMgr,
	}
}

// Start begins the scheduler's tick loop.
func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.started {
		return fmt.Errorf("scheduler already started")
	}

	s.ctx, s.cancel = context.WithCancel(ctx)
	s.started = true

	// Load persisted tasks from all project directories
	if s.stateMgr != nil {
		tasks := s.stateMgr.LoadAllTasks()
		for _, task := range tasks {
			if task.Status == TaskStatusPending {
				s.tasks.Store(task.ID, task)
			}
		}
	}

	s.wg.Add(1)
	go s.run()

	return nil
}

// Stop stops the scheduler.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return
	}
	s.cancel()
	s.mu.Unlock()

	s.wg.Wait()

	s.mu.Lock()
	s.started = false
	s.mu.Unlock()
}

// run is the main scheduler loop.
func (s *Scheduler) run() {
	defer s.wg.Done()

	ticker := time.NewTicker(s.config.TickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.checkDueTasks()
		}
	}
}

// checkDueTasks checks for and delivers due tasks.
func (s *Scheduler) checkDueTasks() {
	now := time.Now()
	s.tasks.Range(func(key, value interface{}) bool {
		task := value.(*ScheduledTask)
		if task.Status == TaskStatusPending && task.DeliverAt.Before(now) {
			// Attempt delivery in a goroutine
			go s.deliverTask(task)
		}
		return true
	})
}

// deliverTask attempts to deliver a scheduled task.
func (s *Scheduler) deliverTask(task *ScheduledTask) {
	// Get the session
	session, ok := s.registry.Get(task.SessionCode)
	if !ok {
		task.Attempts++
		task.LastError = fmt.Sprintf("session %q not found", task.SessionCode)
		if task.Attempts >= s.config.MaxRetries {
			task.Status = TaskStatusFailed
			s.totalFailed.Add(1)
			s.removeTaskFromStorage(task)
		}
		s.persistTask(task)
		return
	}

	if session.GetStatus() != SessionStatusActive {
		task.Attempts++
		task.LastError = "session not active"
		if task.Attempts >= s.config.MaxRetries {
			task.Status = TaskStatusFailed
			s.totalFailed.Add(1)
			s.removeTaskFromStorage(task)
		}
		s.persistTask(task)
		return
	}

	// Create HTTP client for overlay socket
	client := s.createOverlayClient(session.OverlayPath)

	// Prepare the message payload
	payload := map[string]interface{}{
		"text":    task.Message,
		"enter":   true,
		"instant": true,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		task.Attempts++
		task.LastError = fmt.Sprintf("failed to marshal payload: %v", err)
		if task.Attempts >= s.config.MaxRetries {
			task.Status = TaskStatusFailed
			s.totalFailed.Add(1)
			s.removeTaskFromStorage(task)
		}
		s.persistTask(task)
		return
	}

	// Send to overlay /type endpoint
	ctx, cancel := context.WithTimeout(s.ctx, s.config.DeliveryTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", "http://localhost/type", bytes.NewReader(data))
	if err != nil {
		task.Attempts++
		task.LastError = fmt.Sprintf("failed to create request: %v", err)
		if task.Attempts >= s.config.MaxRetries {
			task.Status = TaskStatusFailed
			s.totalFailed.Add(1)
			s.removeTaskFromStorage(task)
		}
		s.persistTask(task)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		task.Attempts++
		task.LastError = fmt.Sprintf("delivery failed: %v", err)
		if task.Attempts >= s.config.MaxRetries {
			task.Status = TaskStatusFailed
			s.totalFailed.Add(1)
			s.removeTaskFromStorage(task)
		}
		s.persistTask(task)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		task.Attempts++
		task.LastError = fmt.Sprintf("overlay returned status %d", resp.StatusCode)
		if task.Attempts >= s.config.MaxRetries {
			task.Status = TaskStatusFailed
			s.totalFailed.Add(1)
			s.removeTaskFromStorage(task)
		}
		s.persistTask(task)
		return
	}

	// Success!
	task.Status = TaskStatusDelivered
	s.totalDelivered.Add(1)
	s.removeTaskFromStorage(task)
}

// createOverlayClient creates an HTTP client that connects via Unix socket.
func (s *Scheduler) createOverlayClient(socketPath string) *http.Client {
	return &http.Client{
		Timeout: s.config.DeliveryTimeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}
}

// persistTask saves the task state to persistent storage.
func (s *Scheduler) persistTask(task *ScheduledTask) {
	if s.stateMgr != nil {
		s.stateMgr.SaveTask(task)
	}
}

// removeTaskFromStorage removes a completed/failed/cancelled task from storage.
func (s *Scheduler) removeTaskFromStorage(task *ScheduledTask) {
	s.tasks.Delete(task.ID)
	if s.stateMgr != nil {
		s.stateMgr.RemoveTask(task.ID, task.ProjectPath)
	}
}

// Schedule adds a new task to the scheduler.
func (s *Scheduler) Schedule(sessionCode string, duration time.Duration, message string, projectPath string) (*ScheduledTask, error) {
	if sessionCode == "" {
		return nil, fmt.Errorf("session code is required")
	}
	if message == "" {
		return nil, fmt.Errorf("message is required")
	}
	if duration <= 0 {
		return nil, fmt.Errorf("duration must be positive")
	}

	// Verify session exists
	if _, ok := s.registry.Get(sessionCode); !ok {
		return nil, fmt.Errorf("session %q not found", sessionCode)
	}

	taskID := fmt.Sprintf("task-%d", s.nextTaskID.Add(1))
	now := time.Now()

	task := &ScheduledTask{
		ID:          taskID,
		SessionCode: sessionCode,
		Message:     message,
		DeliverAt:   now.Add(duration),
		CreatedAt:   now,
		ProjectPath: projectPath,
		Status:      TaskStatusPending,
		Attempts:    0,
	}

	s.tasks.Store(task.ID, task)
	s.totalScheduled.Add(1)
	s.persistTask(task)

	return task, nil
}

// Cancel cancels a scheduled task.
func (s *Scheduler) Cancel(taskID string) error {
	val, ok := s.tasks.Load(taskID)
	if !ok {
		return fmt.Errorf("task %q not found", taskID)
	}

	task := val.(*ScheduledTask)
	if task.Status != TaskStatusPending {
		return fmt.Errorf("task %q is not pending (status: %s)", taskID, task.Status)
	}

	task.Status = TaskStatusCancelled
	s.totalCancelled.Add(1)
	s.removeTaskFromStorage(task)

	return nil
}

// GetTask retrieves a task by ID.
func (s *Scheduler) GetTask(taskID string) (*ScheduledTask, bool) {
	val, ok := s.tasks.Load(taskID)
	if !ok {
		return nil, false
	}
	return val.(*ScheduledTask), true
}

// ListTasks returns all tasks, optionally filtered by project path.
func (s *Scheduler) ListTasks(projectPath string, global bool) []*ScheduledTask {
	var result []*ScheduledTask
	s.tasks.Range(func(key, value interface{}) bool {
		task := value.(*ScheduledTask)
		if global || projectPath == "" || task.ProjectPath == projectPath {
			result = append(result, task)
		}
		return true
	})
	return result
}

// ListPendingTasks returns only pending tasks.
func (s *Scheduler) ListPendingTasks(projectPath string, global bool) []*ScheduledTask {
	var result []*ScheduledTask
	s.tasks.Range(func(key, value interface{}) bool {
		task := value.(*ScheduledTask)
		if task.Status == TaskStatusPending {
			if global || projectPath == "" || task.ProjectPath == projectPath {
				result = append(result, task)
			}
		}
		return true
	})
	return result
}

// SchedulerInfo contains statistics about the scheduler.
type SchedulerInfo struct {
	TotalScheduled int64 `json:"total_scheduled"`
	TotalDelivered int64 `json:"total_delivered"`
	TotalFailed    int64 `json:"total_failed"`
	TotalCancelled int64 `json:"total_cancelled"`
	PendingCount   int64 `json:"pending_count"`
}

// Info returns statistics about the scheduler.
func (s *Scheduler) Info() SchedulerInfo {
	// Count pending tasks
	var pendingCount int64
	s.tasks.Range(func(key, value interface{}) bool {
		task := value.(*ScheduledTask)
		if task.Status == TaskStatusPending {
			pendingCount++
		}
		return true
	})

	return SchedulerInfo{
		TotalScheduled: s.totalScheduled.Load(),
		TotalDelivered: s.totalDelivered.Load(),
		TotalFailed:    s.totalFailed.Load(),
		TotalCancelled: s.totalCancelled.Load(),
		PendingCount:   pendingCount,
	}
}
