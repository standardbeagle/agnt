package overlay

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Default splash messages shown while the child process is starting.
// Content rotates every splashInterval. Keep lines short for readability.
var defaultSplashMessages = []string{
	"Waiting for output...",
	"Tip: Press Ctrl+Y to open the overlay menu",
	"Tip: agnt auto-starts scripts and proxies from .agnt.kdl",
	"Tip: Use get_errors to see all errors across processes and proxies",
	"Tip: Use proxy exec to run JavaScript in the browser",
	"Tip: Use responsive_audit to check mobile layout issues",
}

const (
	splashInterval = 2500 * time.Millisecond // Time between message rotation
	splashTimeout  = 30 * time.Second        // Max time splash is displayed
)

// StartupSplash displays rotating tip text in the terminal while the child
// process is starting up (after PTY creation, before first output).
// It writes above the protected status bar row and clears itself when
// the ActivityMonitor detects first output or the timeout expires.
type StartupSplash struct {
	out    io.Writer
	width  int
	height int // Full terminal height (status bar occupies the last row)

	// messages is the slice of rotating content strings.
	messages []string

	// interval overrides the message rotation interval when non-zero.
	interval time.Duration

	// state tracks whether the splash is active (0=inactive, 1=active).
	active atomic.Int32

	// mu protects stopCh for concurrent Start/Stop access.
	mu     sync.Mutex
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewStartupSplash creates a new splash display. The caller must call Start
// to begin the rotation, and the splash will self-clean via the OnFirstActivity
// callback or the timeout.
func NewStartupSplash(out io.Writer, width, height int) *StartupSplash {
	msgs := make([]string, len(defaultSplashMessages))
	copy(msgs, defaultSplashMessages)
	return &StartupSplash{
		out:      out,
		width:    width,
		height:   height,
		messages: msgs,
		stopCh:   make(chan struct{}),
	}
}

// SetMessages overrides the default splash messages.
func (s *StartupSplash) SetMessages(msgs []string) {
	if len(msgs) > 0 {
		s.messages = msgs
	}
}

// WithInterval overrides the message rotation interval. Used in tests.
func (s *StartupSplash) WithInterval(d time.Duration) *StartupSplash {
	s.interval = d
	return s
}

// Start begins the splash display. The splash renders immediately and
// rotates content on a timer. It auto-expires after splashTimeout.
// Call Stop to clean up the goroutine.
func (s *StartupSplash) Start() {
	s.mu.Lock()
	if !s.active.CompareAndSwap(0, 1) {
		s.mu.Unlock()
		return // Already started
	}
	// Create a fresh stopCh for this run cycle
	s.stopCh = make(chan struct{})
	s.wg.Add(1)
	s.mu.Unlock()

	go s.run()
}

// Stop terminates the splash, clears the display, and waits for the
// goroutine to finish. Safe to call multiple times.
func (s *StartupSplash) Stop() {
	s.mu.Lock()
	if s.active.Swap(0) == 0 {
		s.mu.Unlock()
		return // Already stopped
	}
	ch := s.stopCh
	s.mu.Unlock()

	close(ch)
	s.wg.Wait()
	s.clear()
}

// OnFirstActivity returns a callback suitable for use with
// ActivityMonitorConfig.OnFirstActivity. When called, it stops the splash.
func (s *StartupSplash) OnFirstActivity() func() {
	return func() {
		s.Stop()
	}
}

// run is the main splash loop. It rotates messages and auto-expires
// after splashTimeout.
func (s *StartupSplash) run() {
	defer s.wg.Done()

	s.mu.Lock()
	ch := s.stopCh
	s.mu.Unlock()

	timeout := time.NewTimer(splashTimeout)
	defer timeout.Stop()

	interval := s.interval
	if interval == 0 {
		interval = splashInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Show first message immediately
	idx := 0
	s.render(s.messages[idx])

	for {
		select {
		case <-ch:
			return
		case <-timeout.C:
			// Auto-expire: deactivate without clearing (let child output overwrite)
			s.active.Store(0)
			return
		case <-ticker.C:
			idx = (idx + 1) % len(s.messages)
			if s.active.Load() == 1 {
				s.render(s.messages[idx])
			}
		}
	}
}

// render writes a single splash line above the protected status bar area.
// It positions the cursor 2 rows from the bottom (1 row gap above the
// status bar), writes the message, and returns the cursor to its saved
// position.
func (s *StartupSplash) render(msg string) {
	// Truncate message to fit within terminal width
	maxLen := s.width - 4 // Leave room for decoration
	if maxLen < 10 {
		maxLen = 10
	}
	if len(msg) > maxLen {
		msg = msg[:maxLen-1] + "..."
	}

	// Build the display line: dim color
	line := fmt.Sprintf("%s%s%s", Dim, msg, Reset)

	// Position cursor 2 rows from bottom (above the 1-row status bar).
	// Save/restore cursor so we don't disturb the child's cursor position.
	targetRow := s.height - 1 // One row above the status bar
	if targetRow < 1 {
		targetRow = 1
	}

	var sb strings.Builder
	sb.WriteString(CursorSave)
	sb.WriteString(CursorHide)
	sb.WriteString(fmt.Sprintf(CursorToFormat, targetRow, 2))
	sb.WriteString(ClearLine)
	sb.WriteString(line)
	sb.WriteString(CursorRestore)
	sb.WriteString(CursorShow)

	s.out.Write([]byte(sb.String()))
}

// clear removes the splash text by clearing the row it occupied.
func (s *StartupSplash) clear() {
	targetRow := s.height - 1
	if targetRow < 1 {
		targetRow = 1
	}

	var sb strings.Builder
	sb.WriteString(CursorSave)
	sb.WriteString(CursorHide)
	sb.WriteString(fmt.Sprintf(CursorToFormat, targetRow, 1))
	sb.WriteString(ClearLine)
	sb.WriteString(CursorRestore)
	sb.WriteString(CursorShow)

	s.out.Write([]byte(sb.String()))
}
