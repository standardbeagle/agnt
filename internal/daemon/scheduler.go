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

	"golang.org/x/sync/semaphore"
)

// taskStatus represents the numeric state of a scheduled task for atomic operations.
type taskStatus uint32

const (
	taskStatusPending    taskStatus = 0
	taskStatusDelivering taskStatus = 1
	taskStatusDelivered  taskStatus = 2
	taskStatusFailed     taskStatus = 3
	taskStatusCancelled  taskStatus = 4
)

// TaskStatus represents the string form of a task status for JSON serialization.
type TaskStatus string

const (
	// TaskStatusPending indicates the task is waiting to be delivered.
	TaskStatusPending TaskStatus = "pending"
	// TaskStatusDelivering indicates the task is currently being delivered.
	TaskStatusDelivering TaskStatus = "delivering"
	// TaskStatusDelivered indicates the task was successfully delivered.
	TaskStatusDelivered TaskStatus = "delivered"
	// TaskStatusFailed indicates the task failed after max retries.
	TaskStatusFailed TaskStatus = "failed"
	// TaskStatusCancelled indicates the task was cancelled.
	TaskStatusCancelled TaskStatus = "cancelled"
)

// taskStatusToString maps numeric status to string form.
func taskStatusToString(s taskStatus) TaskStatus {
	switch s {
	case taskStatusPending:
		return TaskStatusPending
	case taskStatusDelivering:
		return TaskStatusDelivering
	case taskStatusDelivered:
		return TaskStatusDelivered
	case taskStatusFailed:
		return TaskStatusFailed
	case taskStatusCancelled:
		return TaskStatusCancelled
	default:
		return TaskStatusPending
	}
}

// taskStatusFromString maps string status to numeric form.
func taskStatusFromString(s TaskStatus) taskStatus {
	switch s {
	case TaskStatusPending:
		return taskStatusPending
	case TaskStatusDelivering:
		return taskStatusDelivering
	case TaskStatusDelivered:
		return taskStatusDelivered
	case TaskStatusFailed:
		return taskStatusFailed
	case TaskStatusCancelled:
		return taskStatusCancelled
	default:
		return taskStatusPending
	}
}

// ScheduledTask represents a message scheduled for future delivery.
// All mutable fields use atomic operations for lock-free concurrent access.
type ScheduledTask struct {
	ID          string    `json:"id"`           // Unique task ID (e.g., "task-abc123")
	SessionCode string    `json:"session_code"` // Target session
	Message     string    `json:"message"`      // Message to deliver
	DeliverAt   time.Time `json:"deliver_at"`   // Scheduled delivery time
	CreatedAt   time.Time `json:"created_at"`   // When task was created
	ProjectPath string    `json:"project_path"` // For project-scoped filtering

	// Atomic mutable state (not directly serialized, use accessors)
	status    atomic.Uint32
	attempts  atomic.Int32
	lastError atomic.Pointer[string]
}

// Status returns the current task status as a string.
func (t *ScheduledTask) Status() TaskStatus {
	return taskStatusToString(taskStatus(t.status.Load()))
}

// SetStatus sets the task status.
func (t *ScheduledTask) SetStatus(s TaskStatus) {
	t.status.Store(uint32(taskStatusFromString(s)))
}

// CompareAndSwapStatus atomically transitions from old to new status.
// Returns true if the swap succeeded.
func (t *ScheduledTask) CompareAndSwapStatus(old, new taskStatus) bool {
	return t.status.CompareAndSwap(uint32(old), uint32(new))
}

// Attempts returns the current attempt count.
func (t *ScheduledTask) Attempts() int {
	return int(t.attempts.Load())
}

// IncrementAttempts atomically increments and returns the new attempt count.
func (t *ScheduledTask) IncrementAttempts() int {
	return int(t.attempts.Add(1))
}

// LastError returns the last error message.
func (t *ScheduledTask) LastError() string {
	if p := t.lastError.Load(); p != nil {
		return *p
	}
	return ""
}

// SetLastError sets the last error message.
func (t *ScheduledTask) SetLastError(err string) {
	t.lastError.Store(&err)
}

// scheduledTaskJSON is the JSON wire format for ScheduledTask.
type scheduledTaskJSON struct {
	ID          string     `json:"id"`
	SessionCode string     `json:"session_code"`
	Message     string     `json:"message"`
	DeliverAt   time.Time  `json:"deliver_at"`
	CreatedAt   time.Time  `json:"created_at"`
	ProjectPath string     `json:"project_path"`
	Status      TaskStatus `json:"status"`
	Attempts    int        `json:"attempts"`
	LastError   string     `json:"last_error,omitempty"`
}

// MarshalJSON implements json.Marshaler for atomic-safe serialization.
func (t *ScheduledTask) MarshalJSON() ([]byte, error) {
	return json.Marshal(scheduledTaskJSON{
		ID:          t.ID,
		SessionCode: t.SessionCode,
		Message:     t.Message,
		DeliverAt:   t.DeliverAt,
		CreatedAt:   t.CreatedAt,
		ProjectPath: t.ProjectPath,
		Status:      t.Status(),
		Attempts:    t.Attempts(),
		LastError:   t.LastError(),
	})
}

// UnmarshalJSON implements json.Unmarshaler for atomic-safe deserialization.
func (t *ScheduledTask) UnmarshalJSON(data []byte) error {
	var raw scheduledTaskJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	t.ID = raw.ID
	t.SessionCode = raw.SessionCode
	t.Message = raw.Message
	t.DeliverAt = raw.DeliverAt
	t.CreatedAt = raw.CreatedAt
	t.ProjectPath = raw.ProjectPath
	t.SetStatus(raw.Status)
	t.attempts.Store(int32(raw.Attempts))
	if raw.LastError != "" {
		t.SetLastError(raw.LastError)
	}
	return nil
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
		"status":       string(t.Status()),
		"attempts":     t.Attempts(),
		"last_error":   t.LastError(),
	}
}

// NewScheduledTask creates a ScheduledTask with the given initial status.
func NewScheduledTask(id, sessionCode, message, projectPath string, deliverAt, createdAt time.Time, status TaskStatus) *ScheduledTask {
	t := &ScheduledTask{
		ID:          id,
		SessionCode: sessionCode,
		Message:     message,
		DeliverAt:   deliverAt,
		CreatedAt:   createdAt,
		ProjectPath: projectPath,
	}
	t.SetStatus(status)
	return t
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
	// MaxConcurrentDeliveries limits simultaneous delivery goroutines.
	// Default: 10
	MaxConcurrentDeliveries int64
}

// DefaultSchedulerConfig returns sensible defaults.
func DefaultSchedulerConfig() SchedulerConfig {
	return SchedulerConfig{
		TickInterval:            1 * time.Second,
		MaxRetries:              3,
		RetryDelay:              5 * time.Second,
		DeliveryTimeout:         5 * time.Second,
		MaxConcurrentDeliveries: 10,
	}
}

// Scheduler manages scheduled message delivery.
type Scheduler struct {
	config   SchedulerConfig
	registry *SessionRegistry
	stateMgr *SchedulerStateManager

	// Task storage (sync.Map for lock-free access)
	tasks sync.Map // map[string]*ScheduledTask

	// Concurrency control for delivery goroutines
	deliverySem *semaphore.Weighted

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
	if config.MaxConcurrentDeliveries <= 0 {
		config.MaxConcurrentDeliveries = 10
	}
	return &Scheduler{
		config:      config,
		registry:    registry,
		stateMgr:    stateMgr,
		deliverySem: semaphore.NewWeighted(config.MaxConcurrentDeliveries),
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
			if task.Status() == TaskStatusPending {
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
// Uses atomic CAS to claim tasks before spawning delivery goroutines,
// preventing duplicate delivery.
func (s *Scheduler) checkDueTasks() {
	now := time.Now()
	s.tasks.Range(func(key, value interface{}) bool {
		task := value.(*ScheduledTask)

		// Atomically claim: only Pending -> Delivering succeeds.
		// If another tick or Cancel already transitioned the status, CAS fails
		// and we skip this task. This eliminates the TOCTOU race.
		if !task.CompareAndSwapStatus(taskStatusPending, taskStatusDelivering) {
			return true
		}

		if !task.DeliverAt.Before(now) {
			// Not due yet, revert to pending
			task.status.Store(uint32(taskStatusPending))
			return true
		}

		// Acquire semaphore slot, respecting context cancellation.
		// If the context is cancelled or we can't acquire, revert and stop.
		if err := s.deliverySem.Acquire(s.ctx, 1); err != nil {
			task.status.Store(uint32(taskStatusPending))
			return false // context cancelled, stop iteration
		}

		go func() {
			defer s.deliverySem.Release(1)
			s.deliverTask(task)
		}()
		return true
	})
}

// deliverTask attempts to deliver a scheduled task.
// The task must already be in Delivering status (claimed by checkDueTasks).
func (s *Scheduler) deliverTask(task *ScheduledTask) {
	// Get the session
	session, ok := s.registry.Get(task.SessionCode)
	if !ok {
		s.handleDeliveryFailure(task, fmt.Sprintf("session %q not found", task.SessionCode))
		return
	}

	if session.GetStatus() != SessionStatusActive {
		s.handleDeliveryFailure(task, "session not active")
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
		s.handleDeliveryFailure(task, fmt.Sprintf("failed to marshal payload: %v", err))
		return
	}

	// Send to overlay /type endpoint
	ctx, cancel := context.WithTimeout(s.ctx, s.config.DeliveryTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", "http://localhost/type", bytes.NewReader(data))
	if err != nil {
		s.handleDeliveryFailure(task, fmt.Sprintf("failed to create request: %v", err))
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		s.handleDeliveryFailure(task, fmt.Sprintf("delivery failed: %v", err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		s.handleDeliveryFailure(task, fmt.Sprintf("overlay returned status %d", resp.StatusCode))
		return
	}

	// Success: Delivering -> Delivered
	task.status.Store(uint32(taskStatusDelivered))
	s.totalDelivered.Add(1)
	s.removeTaskFromStorage(task)
}

// handleDeliveryFailure processes a failed delivery attempt.
// If max retries reached, marks as failed; otherwise reverts to pending for retry.
func (s *Scheduler) handleDeliveryFailure(task *ScheduledTask, errMsg string) {
	attempts := task.IncrementAttempts()
	task.SetLastError(errMsg)

	if attempts >= s.config.MaxRetries {
		// Delivering -> Failed (terminal)
		task.status.Store(uint32(taskStatusFailed))
		s.totalFailed.Add(1)
		s.removeTaskFromStorage(task)
	} else {
		// Delivering -> Pending (retry on next tick)
		task.status.Store(uint32(taskStatusPending))
	}
	s.persistTask(task)
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

	task := NewScheduledTask(taskID, sessionCode, message, projectPath, now.Add(duration), now, TaskStatusPending)

	s.tasks.Store(task.ID, task)
	s.totalScheduled.Add(1)
	s.persistTask(task)

	return task, nil
}

// Cancel cancels a scheduled task.
// Uses atomic CAS to prevent races with concurrent delivery.
func (s *Scheduler) Cancel(taskID string) error {
	val, ok := s.tasks.Load(taskID)
	if !ok {
		return fmt.Errorf("task %q not found", taskID)
	}

	task := val.(*ScheduledTask)

	// Atomically transition Pending -> Cancelled.
	// If the task is already being delivered (Delivering), the CAS fails.
	if !task.CompareAndSwapStatus(taskStatusPending, taskStatusCancelled) {
		currentStatus := task.Status()
		return fmt.Errorf("task %q is not pending (status: %s)", taskID, currentStatus)
	}

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
		if task.Status() == TaskStatusPending {
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
		if task.Status() == TaskStatusPending {
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
