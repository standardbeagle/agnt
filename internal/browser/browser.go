package browser

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// State represents the browser state.
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

// Config holds browser configuration.
type Config struct {
	ID         string // Browser instance ID
	URL        string // Initial URL to open
	Headless   bool   // Run in headless mode (default: true)
	BinaryPath string // Path to Chrome binary (auto-detected if empty)
	ProxyURL   string // Proxy server URL (optional)
	Path       string // Project path for session scoping
	WindowSize string // Window size (default: "1920,1080")
}

// Browser represents a running browser instance.
type Browser struct {
	config Config
	state  atomic.Uint32
	pid    atomic.Int32
	cmd    *exec.Cmd
	cancel context.CancelFunc
	done   chan struct{}
	err    error
	errMu  sync.RWMutex

	startedAt atomic.Pointer[time.Time]
}

// Info contains information about a browser instance.
type Info struct {
	ID         string `json:"id"`
	State      string `json:"state"`
	PID        int    `json:"pid,omitempty"`
	URL        string `json:"url,omitempty"`
	Headless   bool   `json:"headless"`
	BinaryPath string `json:"binary_path,omitempty"`
	ProxyURL   string `json:"proxy_url,omitempty"`
	Path       string `json:"path,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	Error      string `json:"error,omitempty"`
}

// New creates a new browser instance with the given configuration.
func New(config Config) *Browser {
	if config.WindowSize == "" {
		config.WindowSize = "1920,1080"
	}
	return &Browser{
		config: config,
		done:   make(chan struct{}),
	}
}

// Start starts the browser.
func (b *Browser) Start(ctx context.Context) error {
	if !b.compareAndSwapState(StateIdle, StateStarting) {
		return fmt.Errorf("browser already started")
	}

	// Find Chrome binary
	binaryPath := b.config.BinaryPath
	if binaryPath == "" {
		discoverer := DefaultDiscoverer()
		binaryPath = discoverer.Find()
		if binaryPath == "" {
			b.setState(StateFailed)
			b.setError(fmt.Errorf("Chrome not found: install Chrome or specify binary_path"))
			close(b.done)
			return b.Error()
		}
	}

	// Build Chrome arguments
	args := b.buildArgs()

	ctx, cancel := context.WithCancel(ctx)
	b.cancel = cancel

	b.cmd = exec.CommandContext(ctx, binaryPath, args...)

	if err := b.cmd.Start(); err != nil {
		b.setState(StateFailed)
		b.setError(fmt.Errorf("failed to start Chrome: %w", err))
		close(b.done)
		return b.Error()
	}

	b.pid.Store(int32(b.cmd.Process.Pid))
	now := time.Now()
	b.startedAt.Store(&now)
	b.setState(StateRunning)

	// Monitor process in goroutine
	go func() {
		defer close(b.done)
		err := b.cmd.Wait()
		if err != nil && ctx.Err() == nil {
			// Process exited with error (not cancelled)
			b.setError(fmt.Errorf("Chrome exited: %w", err))
			b.setState(StateFailed)
		} else {
			b.setState(StateStopped)
		}
	}()

	return nil
}

// buildArgs constructs the Chrome command-line arguments.
func (b *Browser) buildArgs() []string {
	args := []string{
		"--disable-background-networking",
		"--disable-default-apps",
		"--disable-extensions",
		"--disable-sync",
		"--disable-dev-shm-usage", // Docker/container support
		"--no-first-run",
		"--no-default-browser-check",
		fmt.Sprintf("--window-size=%s", b.config.WindowSize),
	}

	if b.config.Headless {
		args = append(args,
			"--headless=new", // Chrome 109+ headless mode
			"--disable-gpu",
		)
	}

	if b.config.ProxyURL != "" {
		args = append(args, fmt.Sprintf("--proxy-server=%s", b.config.ProxyURL))
	}

	// Add URL as last argument
	if b.config.URL != "" {
		args = append(args, b.config.URL)
	}

	return args
}

// Stop stops the browser.
func (b *Browser) Stop(ctx context.Context) error {
	state := b.State()
	if state == StateStopped || state == StateFailed || state == StateIdle {
		return nil // Already stopped
	}

	if !b.compareAndSwapState(StateRunning, StateStopping) {
		// Try from starting state
		if !b.compareAndSwapState(StateStarting, StateStopping) {
			return nil // Already stopping or stopped
		}
	}

	if b.cancel != nil {
		b.cancel()
	}

	if b.cmd != nil && b.cmd.Process != nil {
		if err := b.cmd.Process.Kill(); err != nil {
			return fmt.Errorf("failed to kill browser: %w", err)
		}
	}

	// Wait for done or context timeout
	select {
	case <-b.done:
	case <-ctx.Done():
		return ctx.Err()
	}

	b.setState(StateStopped)
	return nil
}

// State returns the current browser state.
func (b *Browser) State() State {
	return State(b.state.Load())
}

// PID returns the process ID if running.
func (b *Browser) PID() int {
	return int(b.pid.Load())
}

// ID returns the browser ID.
func (b *Browser) ID() string {
	return b.config.ID
}

// Path returns the project path for this browser.
func (b *Browser) Path() string {
	return b.config.Path
}

// Error returns the last error if any.
func (b *Browser) Error() error {
	b.errMu.RLock()
	defer b.errMu.RUnlock()
	return b.err
}

// Done returns a channel that's closed when the browser exits.
func (b *Browser) Done() <-chan struct{} {
	return b.done
}

// Info returns information about the browser.
func (b *Browser) Info() Info {
	info := Info{
		ID:         b.config.ID,
		State:      b.State().String(),
		PID:        b.PID(),
		URL:        b.config.URL,
		Headless:   b.config.Headless,
		BinaryPath: b.config.BinaryPath,
		ProxyURL:   b.config.ProxyURL,
		Path:       b.config.Path,
	}

	if startedAt := b.startedAt.Load(); startedAt != nil {
		info.StartedAt = startedAt.Format(time.RFC3339)
	}

	b.errMu.RLock()
	if b.err != nil {
		info.Error = b.err.Error()
	}
	b.errMu.RUnlock()

	return info
}

func (b *Browser) setState(s State) {
	b.state.Store(uint32(s))
}

func (b *Browser) compareAndSwapState(old, new State) bool {
	return b.state.CompareAndSwap(uint32(old), uint32(new))
}

func (b *Browser) setError(err error) {
	b.errMu.Lock()
	b.err = err
	b.errMu.Unlock()
}
