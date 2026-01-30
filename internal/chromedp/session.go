package chromedp

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chromedp/chromedp"
)

// State represents the automation session state.
type State uint32

const (
	StateIdle State = iota
	StateStarting
	StateRunning
	StateStopping
	StateStopped
	StateFailed
)

func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateStarting:
		return "starting"
	case StateRunning:
		return "running"
	case StateStopping:
		return "stopping"
	case StateStopped:
		return "stopped"
	case StateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// SessionConfig holds automation session configuration.
type SessionConfig struct {
	ID         string        // Session instance ID
	URL        string        // Initial URL to navigate to
	Headless   bool          // Run in headless mode (default: true)
	ProxyURL   string        // Proxy server URL (required for agnt integration)
	Path       string        // Project path for session scoping
	WindowSize string        // Window size as "width,height" (default: "1920,1080")
	Timeout    time.Duration // Default action timeout (default: 30s)
}

// SessionInfo contains information about an automation session.
type SessionInfo struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	URL       string `json:"url,omitempty"`
	Headless  bool   `json:"headless"`
	ProxyURL  string `json:"proxy_url,omitempty"`
	Path      string `json:"path,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
	Error     string `json:"error,omitempty"`
}

// AutomationSession represents a chromedp-controlled browser session.
// It wraps chromedp contexts with atomic state management for safe
// concurrent access.
type AutomationSession struct {
	config SessionConfig
	state  atomic.Uint32

	// chromedp contexts
	allocCtx    context.Context
	allocCancel context.CancelFunc
	taskCtx     context.Context
	taskCancel  context.CancelFunc

	// Completion signaling
	done chan struct{}

	// Error handling
	err   error
	errMu sync.RWMutex

	// Timestamps
	startedAt atomic.Pointer[time.Time]
}

// NewSession creates a new automation session with the given configuration.
func NewSession(config SessionConfig) *AutomationSession {
	// Apply defaults
	if config.WindowSize == "" {
		config.WindowSize = "1920,1080"
	}
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}

	return &AutomationSession{
		config: config,
		done:   make(chan struct{}),
	}
}

// Start initializes the chromedp session with the allocator configured
// to route through the agnt proxy.
func (s *AutomationSession) Start(ctx context.Context) error {
	if !s.compareAndSwapState(StateIdle, StateStarting) {
		return fmt.Errorf("session already started")
	}

	// Parse window size
	width, height := 1920, 1080
	if s.config.WindowSize != "" {
		if _, err := fmt.Sscanf(s.config.WindowSize, "%d,%d", &width, &height); err != nil {
			// Use defaults if parsing fails
			width, height = 1920, 1080
		}
	}

	// Build allocator options
	opts := []chromedp.ExecAllocatorOption{
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.DisableGPU,
		chromedp.WindowSize(width, height),

		// Disable features that interfere with automation
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("disable-dev-shm-usage", true), // Docker/container support
	}

	// Configure headless mode
	if s.config.Headless {
		opts = append(opts, chromedp.Headless)
	}

	// Configure proxy - critical for agnt integration
	if s.config.ProxyURL != "" {
		opts = append(opts, chromedp.ProxyServer(s.config.ProxyURL))
	}

	// Create allocator context
	s.allocCtx, s.allocCancel = chromedp.NewExecAllocator(ctx, opts...)

	// Create browser context with timeout
	s.taskCtx, s.taskCancel = chromedp.NewContext(s.allocCtx,
		chromedp.WithLogf(func(format string, args ...interface{}) {
			// Suppress chromedp logs for cleaner output
			// Could be made configurable in the future
		}),
	)

	// Start the browser by running a minimal task
	if err := chromedp.Run(s.taskCtx); err != nil {
		s.setState(StateFailed)
		s.setError(fmt.Errorf("failed to start chromedp session: %w", err))
		s.cleanup()
		close(s.done)
		return s.Error()
	}

	// Navigate to initial URL if specified
	if s.config.URL != "" {
		if err := chromedp.Run(s.taskCtx, chromedp.Navigate(s.config.URL)); err != nil {
			s.setState(StateFailed)
			s.setError(fmt.Errorf("failed to navigate to %s: %w", s.config.URL, err))
			s.cleanup()
			close(s.done)
			return s.Error()
		}
	}

	// Record start time
	now := time.Now()
	s.startedAt.Store(&now)
	s.setState(StateRunning)

	// Monitor for context cancellation
	go func() {
		defer close(s.done)
		<-s.taskCtx.Done()
		if s.State() == StateRunning {
			// Context was cancelled externally
			s.setState(StateStopped)
		}
	}()

	return nil
}

// Stop gracefully stops the automation session.
func (s *AutomationSession) Stop(ctx context.Context) error {
	state := s.State()
	if state == StateStopped || state == StateFailed || state == StateIdle {
		return nil // Already stopped
	}

	if !s.compareAndSwapState(StateRunning, StateStopping) {
		// Try from starting state
		if !s.compareAndSwapState(StateStarting, StateStopping) {
			return nil // Already stopping or stopped
		}
	}

	s.cleanup()

	// Wait for done or context timeout
	select {
	case <-s.done:
	case <-ctx.Done():
		return ctx.Err()
	}

	s.setState(StateStopped)
	return nil
}

// cleanup releases chromedp resources.
func (s *AutomationSession) cleanup() {
	if s.taskCancel != nil {
		s.taskCancel()
	}
	if s.allocCancel != nil {
		s.allocCancel()
	}
}

// Run executes chromedp actions within the session.
// Returns error if session is not running.
func (s *AutomationSession) Run(actions ...chromedp.Action) error {
	if s.State() != StateRunning {
		return fmt.Errorf("session not running (state: %s)", s.State())
	}

	ctx, cancel := context.WithTimeout(s.taskCtx, s.config.Timeout)
	defer cancel()

	return chromedp.Run(ctx, actions...)
}

// RunWithTimeout executes chromedp actions with a custom timeout.
func (s *AutomationSession) RunWithTimeout(timeout time.Duration, actions ...chromedp.Action) error {
	if s.State() != StateRunning {
		return fmt.Errorf("session not running (state: %s)", s.State())
	}

	ctx, cancel := context.WithTimeout(s.taskCtx, timeout)
	defer cancel()

	return chromedp.Run(ctx, actions...)
}

// Context returns the chromedp task context for advanced usage.
// Returns nil if session is not running.
func (s *AutomationSession) Context() context.Context {
	if s.State() != StateRunning {
		return nil
	}
	return s.taskCtx
}

// State returns the current session state.
func (s *AutomationSession) State() State {
	return State(s.state.Load())
}

// ID returns the session ID.
func (s *AutomationSession) ID() string {
	return s.config.ID
}

// Path returns the project path for this session.
func (s *AutomationSession) Path() string {
	return s.config.Path
}

// Error returns the last error if any.
func (s *AutomationSession) Error() error {
	s.errMu.RLock()
	defer s.errMu.RUnlock()
	return s.err
}

// Done returns a channel that's closed when the session exits.
func (s *AutomationSession) Done() <-chan struct{} {
	return s.done
}

// Info returns information about the session.
func (s *AutomationSession) Info() SessionInfo {
	info := SessionInfo{
		ID:       s.config.ID,
		State:    s.State().String(),
		URL:      s.config.URL,
		Headless: s.config.Headless,
		ProxyURL: s.config.ProxyURL,
		Path:     s.config.Path,
	}

	if startedAt := s.startedAt.Load(); startedAt != nil {
		info.StartedAt = startedAt.Format(time.RFC3339)
	}

	s.errMu.RLock()
	if s.err != nil {
		info.Error = s.err.Error()
	}
	s.errMu.RUnlock()

	return info
}

// Config returns the session configuration.
func (s *AutomationSession) Config() SessionConfig {
	return s.config
}

func (s *AutomationSession) setState(state State) {
	s.state.Store(uint32(state))
}

// CompareAndSwapState atomically compares and swaps the session state.
// Returns true if the swap was successful.
func (s *AutomationSession) CompareAndSwapState(old, new State) bool {
	return s.compareAndSwapState(old, new)
}

func (s *AutomationSession) compareAndSwapState(old, new State) bool {
	return s.state.CompareAndSwap(uint32(old), uint32(new))
}

func (s *AutomationSession) setError(err error) {
	s.errMu.Lock()
	s.err = err
	s.errMu.Unlock()
}
