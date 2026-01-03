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
	previewBuffer   bytes.Buffer // Accumulates output for line extraction
	previewLines    []string     // Recent complete lines
	previewMaxLines int          // Max lines to keep
	previewDebounce time.Duration
	previewLastSent time.Time
	previewPending  atomic.Bool // Whether a debounced send is pending
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
}

// DefaultActivityMonitorConfig returns the default configuration.
func DefaultActivityMonitorConfig() ActivityMonitorConfig {
	return ActivityMonitorConfig{
		IdleTimeout:     2 * time.Second,
		MinActiveBytes:  10,
		PreviewMaxLines: 5,
		PreviewDebounce: 200 * time.Millisecond,
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

	am := &ActivityMonitor{
		writer:          w,
		idleTimeout:     cfg.IdleTimeout,
		onStateChange:   cfg.OnStateChange,
		onOutputPreview: cfg.OnOutputPreview,
		minActiveBytes:  cfg.MinActiveBytes,
		previewMaxLines: cfg.PreviewMaxLines,
		previewDebounce: cfg.PreviewDebounce,
		stopCh:          make(chan struct{}),
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
func (am *ActivityMonitor) captureForPreview(p []byte) {
	var hasLines bool

	am.previewMu.Lock()
	// Append to buffer
	am.previewBuffer.Write(p)

	// Extract complete lines
	for {
		line, err := am.previewBuffer.ReadString('\n')
		if err != nil {
			// No complete line yet, put partial line back
			if len(line) > 0 {
				am.previewBuffer.WriteString(line)
			}
			break
		}

		// Clean up the line (remove ANSI, trim, limit length)
		cleanLine := am.cleanLine(line)
		if cleanLine != "" {
			am.previewLines = append(am.previewLines, cleanLine)
			hasLines = true
			// Keep only the last N lines
			if len(am.previewLines) > am.previewMaxLines {
				am.previewLines = am.previewLines[len(am.previewLines)-am.previewMaxLines:]
			}
		}
	}

	// Limit buffer size to prevent memory issues
	if am.previewBuffer.Len() > 4096 {
		am.previewBuffer.Reset()
	}
	am.previewMu.Unlock()

	// Schedule debounced broadcast (outside lock to avoid deadlock)
	if hasLines {
		am.scheduleBroadcast()
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
