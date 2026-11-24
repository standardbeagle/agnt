package chromedp

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/standardbeagle/agnt/internal/browserexit"
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

	// Owner identifies who started the session (the daemon puts the agnt
	// session code here). Opaque to this package; carried so an unexpected
	// end can be reported to the owner.
	Owner string
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
	// Exit is set when the session's Chrome went away without a requested
	// stop.
	Exit *browserexit.Record `json:"exit,omitempty"`
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

	// startCancel cancels the startup navigation while Start is still running
	// it. Stop reads it from another goroutine (a session in Starting has no
	// other handle Stop may touch), hence the atomic pointer. Nil outside the
	// navigation.
	startCancel atomic.Pointer[context.CancelFunc]

	// launch and navigate are the two browser-touching steps of Start. They
	// default to the chromedp calls and are replaceable in tests so the
	// startup timeout and the Stop-during-Starting handoff can be exercised
	// without spawning Chrome.
	launch   func(ctx context.Context) error
	navigate func(ctx context.Context, url string) error

	// parentCtx is the context Start was given. Its end (daemon shutdown)
	// ends the session as a requested stop, not a crash.
	parentCtx context.Context
	// stopRequested is set by Stop before it tears the browser down, so the
	// monitor can tell a requested stop from Chrome going away on its own.
	stopRequested atomic.Bool
	// proc is the Chrome process chromedp launched (captured through
	// ModifyCmdFunc); its ProcessState is read only after the allocator has
	// reaped it.
	proc atomic.Pointer[exec.Cmd]
	// output keeps the end of Chrome's stdout and stderr.
	output *browserexit.Tail
	// exit is how the session ended when nobody asked it to; nil otherwise.
	exit atomic.Pointer[browserexit.Record]
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
		output: browserexit.NewTail(browserexit.DefaultTailBytes),
		launch: func(ctx context.Context) error {
			return chromedp.Run(ctx)
		},
		navigate: func(ctx context.Context, url string) error {
			return chromedp.Run(ctx, chromedp.Navigate(url))
		},
	}
}

// Start initializes the chromedp session with the allocator configured
// to route through the agnt proxy.
func (s *AutomationSession) Start(ctx context.Context) error {
	if !s.compareAndSwapState(StateIdle, StateStarting) {
		return fmt.Errorf("session already started")
	}
	s.parentCtx = ctx

	// Parse window size
	width, height := 1920, 1080
	if s.config.WindowSize != "" {
		if _, err := fmt.Sscanf(s.config.WindowSize, "%d,%d", &width, &height); err != nil {
			// Use defaults if parsing fails
			width, height = 1920, 1080
		}
	}

	// Build allocator options
	opts := s.buildAllocatorOpts(width, height)

	// Create allocator context
	s.allocCtx, s.allocCancel = chromedp.NewExecAllocator(ctx, opts...)

	// Create browser context with timeout
	s.taskCtx, s.taskCancel = chromedp.NewContext(s.allocCtx,
		chromedp.WithLogf(func(format string, args ...interface{}) {
			// Suppress chromedp logs for cleaner output
			// Could be made configurable in the future
		}),
	)

	// Start the browser by running a minimal task. This step runs on taskCtx
	// itself, never a derived deadline context: chromedp ties the browser
	// process to the context of the Run that allocates it, so a timeout
	// context here would kill the browser the moment it was released. The
	// launch is bounded by chromedp's own websocket-URL read timeout.
	if err := s.launch(s.taskCtx); err != nil {
		s.setState(StateFailed)
		s.setError(fmt.Errorf("failed to start chromedp session: %w", err))
		s.cleanup()
		close(s.done)
		return s.Error()
	}

	// Navigate to initial URL if specified. Bounded by the session's action
	// timeout and cancellable by Stop: navigation waits for the page's load
	// event, and a page that never finishes loading would otherwise hold
	// Start — and the session's slot, in a state Stop could not end — forever.
	if s.config.URL != "" && s.State() == StateStarting {
		navCtx, navCancel := context.WithTimeout(s.taskCtx, s.config.Timeout)
		s.startCancel.Store(&navCancel)
		// Stop may have claimed Starting→Stopping before the cancel was
		// published; it found nothing to cancel, so honour it here.
		if s.State() != StateStarting {
			navCancel()
		}
		err := s.navigate(navCtx, s.config.URL)
		timedOut := navCtx.Err() == context.DeadlineExceeded
		s.startCancel.Store(nil)
		navCancel()

		if err != nil && s.State() == StateStarting {
			s.setState(StateFailed)
			if timedOut {
				s.setError(fmt.Errorf("failed to navigate to %s: page did not finish loading within %s: %w",
					s.config.URL, s.config.Timeout, err))
			} else {
				s.setError(fmt.Errorf("failed to navigate to %s: %w", s.config.URL, err))
			}
			s.cleanup()
			close(s.done)
			return s.Error()
		}
		// err != nil with the state no longer Starting means Stop cancelled
		// the navigation; the CAS below takes the stopped-during-startup path.
	}

	// Publish Running with a CAS, not a store: Stop may have claimed
	// Starting→Stopping while the browser was launching. The CAS pair also
	// gives Stop happens-before visibility of the cancel fields written
	// above, so Stop's cleanup never observes them nil.
	if !s.compareAndSwapState(StateStarting, StateRunning) {
		// Stop won the race during startup — we own cleanup on this path
		// because Stop never touches cancel fields from Starting.
		s.cleanup()
		s.setState(StateStopped)
		close(s.done)
		return fmt.Errorf("session stopped during startup")
	}

	// Record start time
	now := time.Now()
	s.startedAt.Store(&now)

	go s.monitor()

	return nil
}

// monitor waits for the session's task context to end and records why.
// chromedp cancels that context itself when its connection to Chrome drops,
// so a Chrome that crashes or quits ends it exactly as Stop does. The two are
// told apart by stopRequested (and the parent context, which ends on daemon
// shutdown): anything else is Chrome going away on its own, recorded with its
// exit status and output tail, and the session is left Failed.
func (s *AutomationSession) monitor() {
	defer close(s.done)
	<-s.taskCtx.Done()
	if s.stopRequested.Load() || (s.parentCtx != nil && s.parentCtx.Err() != nil) {
		if s.State() == StateRunning {
			s.setState(StateStopped)
		}
		return
	}
	// Ends the process if it is somehow still alive, and waits for the
	// allocator to reap it, after which its exit status is readable.
	s.cleanup()
	rec := s.exitRecord()
	s.exit.Store(&rec)
	s.setError(fmt.Errorf("chrome %s", rec.Describe()))
	if rec.Crashed {
		s.setState(StateFailed)
	} else {
		s.setState(StateStopped)
	}
}

// exitRecord describes how the session's Chrome ended. Called after the
// allocator has reaped the process.
func (s *AutomationSession) exitRecord() browserexit.Record {
	tail := s.output.String()
	cmd := s.proc.Load()
	if cmd == nil {
		return browserexit.Record{At: time.Now(), ExitCode: -1, Crashed: true,
			Status: "connection to Chrome lost (process status unknown)", StderrTail: tail}
	}
	return browserexit.FromProcessState(cmd.ProcessState, nil, time.Now(), tail)
}

// adoptProcess is the ModifyCmdFunc chromedp calls with the Chrome command
// before starting it. Setting a ModifyCmdFunc replaces chromedp's own
// platform options, so they are applied here (childProcessOptions).
func (s *AutomationSession) adoptProcess(cmd *exec.Cmd) {
	childProcessOptions(cmd)
	s.proc.Store(cmd)
}

// Stop gracefully stops the automation session.
func (s *AutomationSession) Stop(ctx context.Context) error {
	state := s.State()
	if state == StateStopped || state == StateFailed || state == StateIdle {
		return nil // Already stopped
	}

	// Recorded before the browser is torn down, so the monitor reads its
	// end as a requested stop.
	s.stopRequested.Store(true)
	if s.compareAndSwapState(StateRunning, StateStopping) {
		// The Running CAS pairs with Start's Starting→Running CAS, so the
		// cancel fields are guaranteed visible here.
		s.cleanup()
	} else if s.compareAndSwapState(StateStarting, StateStopping) {
		// Start is still launching the browser and owns its cancel fields;
		// it will observe Stopping, clean up, and close done itself. The one
		// thing Stop does reach for is the startup navigation's cancel, so a
		// page that never finishes loading cannot make the session
		// unstoppable.
		if cancel := s.startCancel.Load(); cancel != nil {
			(*cancel)()
		}
	} else {
		return nil // Already stopping or stopped
	}

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

// Owner returns SessionConfig.Owner.
func (s *AutomationSession) Owner() string {
	return s.config.Owner
}

// Exit returns how the session's Chrome ended when nobody asked it to, or nil.
func (s *AutomationSession) Exit() *browserexit.Record {
	return s.exit.Load()
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

	if rec := s.exit.Load(); rec != nil {
		info.State = rec.StateName()
		info.Exit = rec
	}

	return info
}

// Config returns the session configuration.
func (s *AutomationSession) Config() SessionConfig {
	return s.config
}

// buildAllocatorOpts constructs the ExecAllocator options for a session.
// Extracted for testability.
func (s *AutomationSession) buildAllocatorOpts(width, height int) []chromedp.ExecAllocatorOption {
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

		// Prevent OS credential/keyring popups that freeze headless sessions
		chromedp.Flag("password-store", "basic"),

		// Capture the process and the end of its output, so a Chrome that
		// dies on its own is reported with its exit status (monitor).
		chromedp.ModifyCmdFunc(s.adoptProcess),
		chromedp.CombinedOutput(s.output),
	}

	if runtime.GOOS == "darwin" {
		opts = append(opts, chromedp.Flag("use-mock-keychain", true))
	}

	if s.config.Headless {
		opts = append(opts, chromedp.Headless)
	}

	if s.config.ProxyURL != "" {
		opts = append(opts, chromedp.ProxyServer(s.config.ProxyURL))
	}

	return opts
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
