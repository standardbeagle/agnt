package overlay

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ActivityState represents the current activity state.
type ActivityState int

const (
	ActivityIdle ActivityState = iota
	ActivityActive
)

// ActivityMonitor wraps an io.Writer to monitor output activity.
// It detects when data is being written (active) and when writing stops (idle).
// It can also broadcast output previews (recent lines) to connected browsers.
// It handles animated output (like Ink spinners) by detecting carriage returns
// and debouncing rapid updates to prevent scroll spam.
type ActivityMonitor struct {
	writer          io.Writer
	idleTimeout     time.Duration
	onStateChange   func(ActivityState)
	onOutputPreview func(lines []string) // Called with recent output lines
	state           atomic.Int32         // 0 = idle, 1 = active
	lastActivity    atomic.Int64         // Unix nano timestamp of last write
	stopCh          chan struct{}
	wg              sync.WaitGroup
	minActiveBytes  int // Minimum bytes to trigger active state
	activityCounter atomic.Int64

	// Output preview state
	previewMu       sync.Mutex
	previewLines    []string // Recent complete lines
	previewMaxLines int      // Max lines to keep
	previewDebounce time.Duration
	previewLastSent time.Time
	previewPending  atomic.Bool // Whether a debounced send is pending

	// Animation-aware line buffering
	currentLine         bytes.Buffer  // Current line being built
	isAnimating         bool          // True if we've seen \r without \n (line is being updated in place)
	animationLastUpdate time.Time     // Last time the animating line was updated
	animationDebounce   time.Duration // How long to wait before flushing animating line
	animationPending    atomic.Bool   // Whether an animation flush is pending

	// Done message
	showDoneMessage bool
	doneMessage     string
}

// ActivityMonitorConfig configures the activity monitor.
type ActivityMonitorConfig struct {
	// IdleTimeout is how long to wait with no output before transitioning to idle.
	// Default: 2 seconds
	IdleTimeout time.Duration

	// OnStateChange is called when activity state changes.
	OnStateChange func(ActivityState)

	// MinActiveBytes is the minimum bytes written to trigger active state.
	// This prevents brief flickers of activity for small outputs.
	// Default: 10
	MinActiveBytes int

	// OnOutputPreview is called with recent output lines for browser display.
	// Lines are debounced to avoid overwhelming the browser.
	OnOutputPreview func(lines []string)

	// PreviewMaxLines is the maximum number of lines to keep for preview.
	// Default: 5
	PreviewMaxLines int

	// PreviewDebounce is the minimum time between output preview broadcasts.
	// Default: 200ms
	PreviewDebounce time.Duration

	// AnimationDebounce is how long to wait after the last update to an animating
	// line before flushing it. This prevents rapid updates (like Ink spinners)
	// from appearing as scrolling lines. Default: 100ms
	AnimationDebounce time.Duration

	// ShowDoneMessage adds a persistent "Done" message when activity goes idle.
	// Default: true
	ShowDoneMessage bool

	// DoneMessage is the message to show when activity goes idle.
	// Default: "✓ Done"
	DoneMessage string
}

// DefaultActivityMonitorConfig returns the default configuration.
func DefaultActivityMonitorConfig() ActivityMonitorConfig {
	return ActivityMonitorConfig{
		IdleTimeout:       2 * time.Second,
		MinActiveBytes:    10,
		PreviewMaxLines:   5,
		PreviewDebounce:   200 * time.Millisecond,
		AnimationDebounce: 100 * time.Millisecond,
		ShowDoneMessage:   true,
		DoneMessage:       "✓ Done",
	}
}

// NewActivityMonitor creates a new activity monitor wrapping the given writer.
func NewActivityMonitor(w io.Writer, cfg ActivityMonitorConfig) *ActivityMonitor {
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = 2 * time.Second
	}
	if cfg.MinActiveBytes == 0 {
		cfg.MinActiveBytes = 10
	}
	if cfg.PreviewMaxLines == 0 {
		cfg.PreviewMaxLines = 5
	}
	if cfg.PreviewDebounce == 0 {
		cfg.PreviewDebounce = 200 * time.Millisecond
	}
	if cfg.AnimationDebounce == 0 {
		cfg.AnimationDebounce = 100 * time.Millisecond
	}
	if cfg.DoneMessage == "" {
		cfg.DoneMessage = "✓ Done"
	}

	am := &ActivityMonitor{
		writer:            w,
		idleTimeout:       cfg.IdleTimeout,
		onStateChange:     cfg.OnStateChange,
		onOutputPreview:   cfg.OnOutputPreview,
		minActiveBytes:    cfg.MinActiveBytes,
		previewMaxLines:   cfg.PreviewMaxLines,
		previewDebounce:   cfg.PreviewDebounce,
		animationDebounce: cfg.AnimationDebounce,
		showDoneMessage:   cfg.ShowDoneMessage,
		doneMessage:       cfg.DoneMessage,
		stopCh:            make(chan struct{}),
	}

	// Start the idle check goroutine
	am.wg.Add(1)
	go am.checkIdle()

	return am
}

// Write implements io.Writer and tracks activity.
func (am *ActivityMonitor) Write(p []byte) (n int, err error) {
	n, err = am.writer.Write(p)
	if n > 0 {
		am.lastActivity.Store(time.Now().UnixNano())
		am.activityCounter.Add(int64(n))

		// Check if we should transition to active
		if am.state.Load() == 0 {
			// Only trigger active if we've accumulated enough bytes
			if am.activityCounter.Load() >= int64(am.minActiveBytes) {
				am.setState(ActivityActive)
			}
		}

		// Capture output for preview broadcasting
		if am.onOutputPreview != nil {
			am.captureForPreview(p[:n])
		}
	}
	return n, err
}

// captureForPreview accumulates output and extracts complete lines for preview.
// It handles animated output by detecting carriage returns (\r) and debouncing
// rapid updates to the same line before forwarding.
func (am *ActivityMonitor) captureForPreview(p []byte) {
	var hasNewLine bool
	var hasAnimationUpdate bool
	var shouldScheduleAnimationFlush bool

	am.previewMu.Lock()

	for _, b := range p {
		switch b {
		case '\r':
			// Carriage return: line is being updated in place (animation)
			// Reset line content but mark as animating
			am.currentLine.Reset()
			am.isAnimating = true
			am.animationLastUpdate = time.Now()
			hasAnimationUpdate = true

		case '\n':
			// Newline: commit the current line
			if am.currentLine.Len() > 0 || am.isAnimating {
				cleanLine := am.cleanLine(am.currentLine.String())
				if cleanLine != "" {
					am.previewLines = append(am.previewLines, cleanLine)
					hasNewLine = true
					// Keep only the last N lines
					if len(am.previewLines) > am.previewMaxLines {
						am.previewLines = am.previewLines[len(am.previewLines)-am.previewMaxLines:]
					}
				}
			}
			am.currentLine.Reset()
			am.isAnimating = false

		default:
			// Regular character: append to current line
			am.currentLine.WriteByte(b)
			if am.isAnimating {
				am.animationLastUpdate = time.Now()
				hasAnimationUpdate = true
			}
		}
	}

	// Limit current line buffer size to prevent memory issues
	if am.currentLine.Len() > 4096 {
		am.currentLine.Reset()
		am.isAnimating = false
	}

	// Capture isAnimating state while holding lock to avoid race
	shouldScheduleAnimationFlush = hasAnimationUpdate && am.isAnimating

	am.previewMu.Unlock()

	// Schedule broadcasts (outside lock to avoid deadlock)
	if hasNewLine {
		am.scheduleBroadcast()
	}
	if shouldScheduleAnimationFlush {
		am.scheduleAnimationFlush()
	}
}

// cleanLine removes ANSI escape codes and cleans up a line for display.
func (am *ActivityMonitor) cleanLine(line string) string {
	// Remove trailing newline/carriage return
	line = strings.TrimRight(line, "\r\n")

	// Remove ANSI escape sequences (simple pattern)
	result := make([]byte, 0, len(line))
	inEscape := false
	for i := 0; i < len(line); i++ {
		if line[i] == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			// End of escape sequence at letter
			if (line[i] >= 'A' && line[i] <= 'Z') || (line[i] >= 'a' && line[i] <= 'z') {
				inEscape = false
			}
			continue
		}
		// Skip other control characters
		if line[i] < 32 && line[i] != '\t' {
			continue
		}
		result = append(result, line[i])
	}

	cleaned := strings.TrimSpace(string(result))

	// Limit line length
	if len(cleaned) > 120 {
		cleaned = cleaned[:117] + "..."
	}

	return cleaned
}

// scheduleBroadcast schedules a debounced broadcast of preview lines.
func (am *ActivityMonitor) scheduleBroadcast() {
	// If already pending, don't schedule another
	if am.previewPending.Load() {
		return
	}

	// Check if we've waited long enough since last send
	if time.Since(am.previewLastSent) >= am.previewDebounce {
		// Can send immediately
		am.sendPreview()
		return
	}

	// Schedule delayed send
	if am.previewPending.CompareAndSwap(false, true) {
		go func() {
			select {
			case <-am.stopCh:
				return
			case <-time.After(am.previewDebounce - time.Since(am.previewLastSent)):
				am.previewPending.Store(false)
				am.previewMu.Lock()
				am.sendPreviewLocked()
				am.previewMu.Unlock()
			}
		}()
	}
}

// scheduleAnimationFlush schedules a debounced flush of the current animating line.
// This waits until the line has been quiet (no updates) for animationDebounce duration,
// then flushes the final state as a preview line.
func (am *ActivityMonitor) scheduleAnimationFlush() {
	// If already pending, don't schedule another - the existing one will check for updates
	if am.animationPending.Load() {
		return
	}

	if am.animationPending.CompareAndSwap(false, true) {
		go func() {
			defer am.animationPending.Store(false)

			for {
				select {
				case <-am.stopCh:
					return
				case <-time.After(am.animationDebounce):
					am.previewMu.Lock()

					// Check if we're still animating and if enough time has passed
					if !am.isAnimating {
						am.previewMu.Unlock()
						return
					}

					timeSinceUpdate := time.Since(am.animationLastUpdate)
					if timeSinceUpdate < am.animationDebounce {
						// More updates came in, wait longer
						am.previewMu.Unlock()
						continue
					}

					// Line has been quiet long enough - flush it
					if am.currentLine.Len() > 0 {
						cleanLine := am.cleanLine(am.currentLine.String())
						if cleanLine != "" {
							am.previewLines = append(am.previewLines, cleanLine)
							// Keep only the last N lines
							if len(am.previewLines) > am.previewMaxLines {
								am.previewLines = am.previewLines[len(am.previewLines)-am.previewMaxLines:]
							}
						}
						am.currentLine.Reset()
					}
					am.isAnimating = false
					am.previewMu.Unlock()

					// Broadcast the update
					am.scheduleBroadcast()
					return
				}
			}
		}()
	}
}

// sendPreview sends the current preview lines to the callback.
func (am *ActivityMonitor) sendPreview() {
	am.previewMu.Lock()
	am.sendPreviewLocked()
	am.previewMu.Unlock()
}

// sendPreviewLocked sends preview (must hold previewMu).
func (am *ActivityMonitor) sendPreviewLocked() {
	if len(am.previewLines) == 0 {
		return
	}

	// Copy lines to send
	lines := make([]string, len(am.previewLines))
	copy(lines, am.previewLines)

	am.previewLastSent = time.Now()

	// Call callback outside lock to prevent deadlock
	go am.onOutputPreview(lines)
}

// setState changes the activity state and notifies the callback.
func (am *ActivityMonitor) setState(newState ActivityState) {
	oldState := ActivityState(am.state.Swap(int32(newState)))
	if oldState != newState {
		if newState == ActivityIdle {
			am.activityCounter.Store(0) // Reset counter on idle

			// Add done message to preview when transitioning to idle
			if am.showDoneMessage && am.onOutputPreview != nil {
				am.previewMu.Lock()
				// Flush any remaining animating line first
				if am.currentLine.Len() > 0 {
					cleanLine := am.cleanLine(am.currentLine.String())
					if cleanLine != "" {
						am.previewLines = append(am.previewLines, cleanLine)
					}
					am.currentLine.Reset()
					am.isAnimating = false
				}
				// Add the done message
				am.previewLines = append(am.previewLines, am.doneMessage)
				// Keep only the last N lines
				if len(am.previewLines) > am.previewMaxLines {
					am.previewLines = am.previewLines[len(am.previewLines)-am.previewMaxLines:]
				}
				am.previewMu.Unlock()
				am.scheduleBroadcast()
			}
		}
		if am.onStateChange != nil {
			am.onStateChange(newState)
		}
	}
}

// checkIdle periodically checks if the output has gone idle.
func (am *ActivityMonitor) checkIdle() {
	defer am.wg.Done()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-am.stopCh:
			return
		case <-ticker.C:
			if am.state.Load() == 1 { // Currently active
				lastActivity := time.Unix(0, am.lastActivity.Load())
				if time.Since(lastActivity) > am.idleTimeout {
					am.setState(ActivityIdle)
				}
			}
		}
	}
}

// State returns the current activity state.
func (am *ActivityMonitor) State() ActivityState {
	return ActivityState(am.state.Load())
}

// IsActive returns true if currently active.
func (am *ActivityMonitor) IsActive() bool {
	return am.state.Load() == 1
}

// Stop stops the activity monitor.
func (am *ActivityMonitor) Stop() {
	select {
	case <-am.stopCh:
		// Already stopped
	default:
		close(am.stopCh)
	}
	am.wg.Wait()
}
