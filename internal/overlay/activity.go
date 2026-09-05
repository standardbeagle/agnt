package overlay

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/standardbeagle/vt10x"
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
	writer            io.Writer
	idleTimeout       time.Duration
	idleCheckInterval time.Duration
	onStateChange     func(ActivityState)
	onOutputPreview   func(lines []string, throbber string) // Called with recent output lines + in-flight animated line
	state             atomic.Int32                          // 0 = idle, 1 = active
	lastActivity      atomic.Int64                          // Unix nano timestamp of last write
	stopCh            chan struct{}
	wg                sync.WaitGroup
	minActiveBytes    int // Minimum bytes to trigger active state
	activityCounter   atomic.Int64

	// Output preview state. The preview is a snapshot of the child's actual
	// terminal screen, produced by feeding raw output into a vt10x emulator.
	// This tracks cursor movement, in-place repaints, and line wrapping the way
	// a real terminal does, so a TUI (Claude Code, Kimi, any Ink app) that
	// repaints a multi-line region no longer streams a fresh copy of the block
	// on every keystroke — the earlier line-list heuristic stripped the cursor
	// escapes and committed each repaint as new scrolling lines.
	previewMu       sync.Mutex
	screen          vt10x.Terminal // Virtual screen; source of truth for the preview snapshot
	previewMaxLines int            // Max screen-tail rows to send
	previewDebounce time.Duration
	previewLastSent time.Time
	previewPending  atomic.Bool // Whether a debounced send is pending
	doneShown       bool        // Append the done marker to the snapshot while idle (guarded by previewMu)

	// Line assembly for the per-line alert tap (onOutputLine). Independent of
	// the screen snapshot: the tap wants logical, LF-terminated lines, not a
	// wrapped screen grid. currentLine buffers the in-flight line; the CR/LF
	// bookkeeping keeps spinner redraws from being emitted as committed lines.
	currentLine bytes.Buffer // Current line being built
	pendingCR   bool         // True after a \r whose role (CRLF terminator vs in-place redraw) is not yet known
	isAnimating bool         // True if we've seen \r without \n (line is being updated in place)

	// Done message
	showDoneMessage bool
	doneMessage     string

	// Per-line callback for alert scanning
	onOutputLine func(string)

	// One-shot callback fired the first time the monitor transitions to active.
	// Used by StartupSplash to clear itself when child output begins.
	onFirstActivity    func()
	firstActivityFired atomic.Bool
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

	// OnOutputPreview is called with recent output lines for browser display,
	// plus the current in-flight animated line (spinner/throbber text redrawn
	// in place via \r), which is delivered separately so consumers can update
	// it in place instead of appending each frame as a new line.
	// Broadcasts are debounced to avoid overwhelming the browser.
	OnOutputPreview func(lines []string, throbber string)

	// PreviewMaxLines is the maximum number of lines to keep for preview.
	// Default: 8
	PreviewMaxLines int

	// PreviewDebounce is the minimum time between output preview broadcasts.
	// Default: 200ms
	PreviewDebounce time.Duration

	// ShowDoneMessage adds a persistent "Done" message when activity goes idle.
	// Default: true
	ShowDoneMessage bool

	// DoneMessage is the message to show when activity goes idle.
	// Default: "✓ Done"
	DoneMessage string

	// OnOutputLine is called with each complete, cleaned line of output.
	// Used by AlertScanner to detect error/warning patterns.
	OnOutputLine func(string)

	// OnFirstActivity is called once when the monitor first transitions to active state.
	// Used by StartupSplash to clear splash text when child output begins.
	OnFirstActivity func()

	// IdleCheckInterval is how often to check for idle state transitions.
	// Zero means use the default (500ms).
	IdleCheckInterval time.Duration

	// InitialCols/InitialRows size the virtual screen the preview snapshot is
	// read from. They should match the child PTY's dimensions; Resize keeps
	// them current on SIGWINCH. Zero falls back to 80x24.
	InitialCols int
	InitialRows int
}

// DefaultActivityMonitorConfig returns the default configuration.
func DefaultActivityMonitorConfig() ActivityMonitorConfig {
	return ActivityMonitorConfig{
		IdleTimeout:     2 * time.Second,
		MinActiveBytes:  10,
		PreviewMaxLines: 8,
		PreviewDebounce: 200 * time.Millisecond,
		ShowDoneMessage: true,
		DoneMessage:     "✓ Done",
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
		cfg.PreviewMaxLines = 8
	}
	if cfg.PreviewDebounce == 0 {
		cfg.PreviewDebounce = 200 * time.Millisecond
	}
	if cfg.DoneMessage == "" {
		cfg.DoneMessage = "✓ Done"
	}

	idleCheckInterval := cfg.IdleCheckInterval
	if idleCheckInterval == 0 {
		idleCheckInterval = 500 * time.Millisecond
	}

	cols, rows := cfg.InitialCols, cfg.InitialRows
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}

	am := &ActivityMonitor{
		writer:            w,
		idleTimeout:       cfg.IdleTimeout,
		idleCheckInterval: idleCheckInterval,
		onStateChange:     cfg.OnStateChange,
		onOutputPreview:   cfg.OnOutputPreview,
		minActiveBytes:    cfg.MinActiveBytes,
		screen:            vt10x.New(vt10x.WithSize(cols, rows)),
		previewMaxLines:   cfg.PreviewMaxLines,
		previewDebounce:   cfg.PreviewDebounce,
		showDoneMessage:   cfg.ShowDoneMessage,
		doneMessage:       cfg.DoneMessage,
		onOutputLine:      cfg.OnOutputLine,
		onFirstActivity:   cfg.OnFirstActivity,
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

		// Split output into complete lines. This feeds both the preview broadcast
		// and the per-line tap; gating it on onOutputPreview alone made a
		// monitor configured with only OnOutputLine silently deliver nothing.
		if am.onOutputPreview != nil || am.onOutputLine != nil {
			am.captureForPreview(p[:n])
		}
	}
	return n, err
}

// captureForPreview accumulates output and extracts complete lines for preview.
// It handles animated output by detecting carriage returns (\r) and debouncing
// rapid updates to the same line before forwarding.
func (am *ActivityMonitor) captureForPreview(p []byte) {
	// Feed the virtual terminal first. It parses every escape sequence —
	// cursor moves, erases, wrapping — so the preview reflects the child's
	// actual on-screen layout rather than a naive line list. Write locks the
	// screen's own mutex internally.
	_, _ = am.screen.Write(p)

	// The per-line alert tap (onOutputLine) wants logical, LF-terminated lines,
	// not the wrapped screen grid, so it keeps its own line assembly. Complete
	// lines are collected under the lock and delivered after it, in order:
	// handing each to its own goroutine would race the stream and mis-attribute
	// a signal line to the wrong preceding line.
	var completedLines []string

	am.previewMu.Lock()
	for _, b := range p {
		switch b {
		case '\r':
			// A carriage return is ambiguous until the next byte arrives: it
			// either terminates a CRLF line (ONLCR) or rewinds the cursor for
			// an in-place redraw (a spinner). Defer the decision; the flag
			// survives across Write calls because the PTY may split a chunk
			// between the CR and the LF.
			am.pendingCR = true

		case '\n':
			am.pendingCR = false
			if am.currentLine.Len() > 0 || am.isAnimating {
				cleanLine := am.cleanLine(am.currentLine.String())
				if cleanLine != "" && am.onOutputLine != nil {
					completedLines = append(completedLines, cleanLine)
				}
			}
			am.currentLine.Reset()
			am.isAnimating = false

		default:
			// Content after a CR and no LF: the CR was a redraw, so the pending
			// line is overwritten rather than committed to the tap.
			if am.pendingCR {
				am.pendingCR = false
				am.currentLine.Reset()
				am.isAnimating = true
			}
			am.currentLine.WriteByte(b)
		}
	}

	// Limit current line buffer size to prevent memory issues
	if am.currentLine.Len() > 4096 {
		am.currentLine.Reset()
		am.isAnimating = false
		am.pendingCR = false
	}
	am.previewMu.Unlock()

	// Deliver complete lines in order, outside the lock. onOutputLine must be
	// fast and must not block: it runs on the PTY write path, like onStateChange.
	for _, line := range completedLines {
		am.onOutputLine(line)
	}

	// Any output changed the screen; refresh the preview (debounced).
	if len(p) > 0 {
		am.scheduleBroadcast()
	}
}

// isNoiseLine reports whether a cleaned line carries no readable content —
// box-drawing borders, rules, and other pure-punctuation separators that
// only add scroll noise to the floating preview.
func isNoiseLine(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
	}
	return true
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

	// Strip enclosing box-drawing borders (`│ text │`) so boxed CLI output
	// reads as plain text in the preview.
	cleaned = strings.TrimSpace(strings.Trim(cleaned, "│┃║"))

	// Limit line length to 120 (117 + "...") rune-safe, so a multibyte glyph
	// is never split at the cut.
	cleaned = TruncateRunes(cleaned, 117, "...")

	return cleaned
}

// screenTail reads the virtual screen and returns the last previewMaxLines rows
// that carry readable content, in top-to-bottom order. Blank and noise rows
// (borders, rules) are dropped so the floating preview stays compact. The screen
// already resolved every escape sequence into placed glyphs, so a repaint shows
// as an in-place change rather than a new copy of the block.
func (am *ActivityMonitor) screenTail() []string {
	am.screen.Lock()
	cols, rows := am.screen.Size()
	raw := make([]string, 0, rows)
	for y := 0; y < rows; y++ {
		var sb strings.Builder
		for x := 0; x < cols; x++ {
			g := am.screen.Cell(x, y)
			if g.Char == 0 {
				sb.WriteRune(' ')
			} else {
				sb.WriteRune(g.Char)
			}
		}
		raw = append(raw, sb.String())
	}
	am.screen.Unlock()

	out := make([]string, 0, len(raw))
	for _, row := range raw {
		cleaned := am.cleanLine(row)
		if cleaned == "" || isNoiseLine(cleaned) {
			continue
		}
		out = append(out, cleaned)
	}
	if len(out) > am.previewMaxLines {
		out = out[len(out)-am.previewMaxLines:]
	}
	return out
}

// RenderScreen returns the child's screen as an absolute ANSI repaint. The
// virtual screen is fed every byte the child writes — including while the
// overlay has the gate frozen and those bytes are reaching no terminal — so it
// can restore the child's display without asking the child to redraw.
func (am *ActivityMonitor) RenderScreen(maxRows int) []byte {
	return RenderScreenANSI(am.screen, maxRows)
}

// Resize updates the virtual screen dimensions. Call it whenever the child PTY
// is resized (SIGWINCH) so wrapping and the visible region stay correct.
func (am *ActivityMonitor) Resize(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	am.screen.Resize(cols, rows)
}

// scheduleBroadcast schedules a debounced broadcast of preview lines.
func (am *ActivityMonitor) scheduleBroadcast() {
	// A monitor may capture lines purely for the per-line tap, with no preview
	// consumer to broadcast to.
	if am.onOutputPreview == nil {
		return
	}
	// If already pending, don't schedule another
	if am.previewPending.Load() {
		return
	}

	// Read previewLastSent under lock to avoid racing with sendPreviewLocked.
	am.previewMu.Lock()
	elapsed := time.Since(am.previewLastSent)
	am.previewMu.Unlock()

	// Check if we've waited long enough since last send
	if elapsed >= am.previewDebounce {
		// Can send immediately
		am.sendPreview()
		return
	}

	// Schedule delayed send
	if am.previewPending.CompareAndSwap(false, true) {
		delay := am.previewDebounce - elapsed
		go func() {
			select {
			case <-am.stopCh:
				return
			case <-time.After(delay):
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

// sendPreviewLocked sends the current screen snapshot (must hold previewMu).
// The whole snapshot updates in place on every broadcast, so there is no longer
// a separate in-flight "throbber" line — the last row of the tail is the live
// line. The throbber argument is kept empty for wire compatibility.
func (am *ActivityMonitor) sendPreviewLocked() {
	lines := am.screenTail()
	if am.doneShown {
		// The done marker may be a bare glyph; append it past the noise filter
		// so idle always shows completion.
		lines = append(lines, am.doneMessage)
	}
	if len(lines) == 0 {
		return
	}

	am.previewLastSent = time.Now()

	// Call callback outside lock to prevent deadlock
	go am.onOutputPreview(lines, "")
}

// setState changes the activity state and notifies the callback.
func (am *ActivityMonitor) setState(newState ActivityState) {
	oldState := ActivityState(am.state.Swap(int32(newState)))
	if oldState != newState {
		if newState == ActivityIdle {
			am.activityCounter.Store(0) // Reset counter on idle

			// Mark the snapshot to carry the done marker while idle.
			if am.showDoneMessage && am.onOutputPreview != nil {
				am.previewMu.Lock()
				am.doneShown = true
				am.previewMu.Unlock()
				am.scheduleBroadcast()
			}
		}
		if newState == ActivityActive {
			// Fresh output supersedes the done marker.
			am.previewMu.Lock()
			am.doneShown = false
			am.previewMu.Unlock()
		}
		// Fire one-shot first-activity callback on transition to active
		if newState == ActivityActive && am.onFirstActivity != nil && am.firstActivityFired.CompareAndSwap(false, true) {
			go am.onFirstActivity()
		}
		if am.onStateChange != nil {
			am.onStateChange(newState)
		}
	}
}

// checkIdle periodically checks if the output has gone idle.
func (am *ActivityMonitor) checkIdle() {
	defer am.wg.Done()

	ticker := time.NewTicker(am.idleCheckInterval)
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
