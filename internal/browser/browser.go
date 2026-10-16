package browser

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
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

	// UserDataDir is the Chrome profile directory. Empty (the default) gives
	// every launch its own fresh temporary profile, removed when the browser
	// exits. A caller that sets it owns the directory: it is used as given
	// and never removed.
	UserDataDir string
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

	// profileDir is the temporary profile this launch created and must
	// remove; empty when the caller supplied Config.UserDataDir. Written by
	// Start before the process exists, read only by the goroutine that reaps
	// the process.
	profileDir string

	// afterSpawn, when set, runs in Start after the process exists and before
	// Running is published. Nil in production; tests use it to land a Stop in
	// exactly that window.
	afterSpawn func()

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
			b.setError(fmt.Errorf("chrome not found: install Chrome or specify binary_path"))
			close(b.done)
			return b.Error()
		}
	}

	// Give this launch its own profile directory. Chrome is single-instance
	// per profile: started against a profile another Chrome already has open
	// (the user's everyday browser, on the default profile), the new process
	// hands its URL to that instance and exits at once — so the launch
	// "succeeds", every flag passed here (proxy, extensions off) is ignored,
	// and there is no process left to manage or stop.
	if err := b.prepareProfileDir(); err != nil {
		b.setState(StateFailed)
		b.setError(err)
		close(b.done)
		return b.Error()
	}

	// Build Chrome arguments
	args := b.buildArgs()

	ctx, cancel := context.WithCancel(ctx)
	b.cancel = cancel

	b.cmd = exec.CommandContext(ctx, binaryPath, args...)

	if err := b.cmd.Start(); err != nil {
		cancel()
		b.removeProfileDir()
		b.setState(StateFailed)
		b.setError(fmt.Errorf("failed to start Chrome: %w", err))
		close(b.done)
		return b.Error()
	}

	b.pid.Store(int32(b.cmd.Process.Pid))
	now := time.Now()
	b.startedAt.Store(&now)

	if b.afterSpawn != nil {
		b.afterSpawn()
	}

	// Publish Running with a CAS, not a store: Stop may have claimed
	// Starting→Stopping while the process was being spawned. The CAS pair
	// also gives Stop happens-before visibility of cancel and cmd written
	// above, so a Stop that sees Running never observes them nil. (An
	// unconditional store here used to overwrite Stopping, leaving a browser
	// that Stop had "stopped" running for good.)
	if !b.compareAndSwapState(StateStarting, StateRunning) {
		// Stop won the race during startup — Start owns teardown on this
		// path, because Stop never touches cancel or cmd from Starting.
		cancel()
		_ = b.cmd.Process.Kill()
		_ = b.cmd.Wait()
		b.removeProfileDir()
		b.setState(StateStopped)
		close(b.done)
		return fmt.Errorf("browser stopped during startup")
	}

	// Monitor process in goroutine
	go func() {
		defer close(b.done)
		err := b.cmd.Wait()
		b.removeProfileDir()
		if err != nil && ctx.Err() == nil {
			// Process exited with error (not cancelled)
			b.setError(fmt.Errorf("chrome exited: %w", err))
			b.setState(StateFailed)
		} else {
			b.setState(StateStopped)
		}
	}()

	return nil
}

// prepareProfileDir creates the temporary profile for this launch unless the
// caller supplied one.
func (b *Browser) prepareProfileDir() error {
	if b.config.UserDataDir != "" {
		return nil
	}
	dir, err := os.MkdirTemp("", "agnt-browser-profile-")
	if err != nil {
		return fmt.Errorf("failed to create a browser profile directory: %w", err)
	}
	b.profileDir = dir
	return nil
}

// userDataDir is the profile directory passed to Chrome: the caller's, or the
// temporary one prepareProfileDir created. Empty before Start.
func (b *Browser) userDataDir() string {
	if b.config.UserDataDir != "" {
		return b.config.UserDataDir
	}
	return b.profileDir
}

// profileRemoveAttempts and profileRemoveDelay bound the cleanup retry.
const (
	profileRemoveAttempts = 20
	profileRemoveDelay    = 100 * time.Millisecond
)

// removeProfileDir deletes the temporary profile once the browser process is
// gone. Chrome's helper processes can outlive the main process by a moment
// and still hold files in the profile (always, on Windows, where an open file
// cannot be deleted), so removal is retried briefly. A directory that still
// cannot be removed is reported on the browser's error rather than dropped:
// it is a leaked directory someone has to know about.
func (b *Browser) removeProfileDir() {
	dir := b.profileDir
	if dir == "" {
		return
	}
	var err error
	for i := 0; i < profileRemoveAttempts; i++ {
		if err = os.RemoveAll(dir); err == nil {
			return
		}
		time.Sleep(profileRemoveDelay)
	}
	if b.Error() == nil {
		b.setError(fmt.Errorf("browser exited but its temporary profile %s could not be removed: %w", dir, err))
	}
}

// getuid is os.Getuid (-1 on Windows), replaceable in tests.
var getuid = os.Getuid

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
		// Prevent OS credential/keyring popups that freeze headless sessions
		"--password-store=basic",
	}

	if runtime.GOOS == "darwin" {
		args = append(args, "--use-mock-keychain")
	}

	// Chrome refuses to start as root without --no-sandbox ("Running as root
	// without --no-sandbox is not supported") and exits at once, after Start
	// has already reported the browser running. Root is the normal user in a
	// Linux container. chromedp's allocator adds the same flag for root.
	if getuid() == 0 {
		args = append(args, "--no-sandbox")
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

	if dir := b.userDataDir(); dir != "" {
		args = append(args, fmt.Sprintf("--user-data-dir=%s", dir))
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

	if b.compareAndSwapState(StateRunning, StateStopping) {
		// The Running CAS pairs with Start's Starting→Running CAS, so cancel
		// and cmd are guaranteed visible (and non-nil) here.
		//
		// Cancelling is the kill: the command was built with
		// exec.CommandContext, whose watcher kills the process as soon as
		// the context is done. A second, explicit Kill here raced that
		// watcher and the reaper, and reported "failed to kill browser" for
		// a browser that was in fact already dead.
		b.cancel()
	} else if b.compareAndSwapState(StateStarting, StateStopping) {
		// Start is still spawning the process and owns cancel and cmd; it
		// will observe Stopping, tear the process down, and close done
		// itself. Touching those fields from here would be a data race.
	} else {
		return nil // Already stopping or stopped
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
