// Package overlay provides a terminal overlay system for agnt.
// It renders a status indicator bar and popup menus using ANSI escape sequences,
// while passing through input/output to the underlying PTY.
package overlay

import (
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// PtyReadWriter is an interface for interacting with a PTY.
// This allows both Unix PTY (*os.File) and Windows ConPTY to be used.
type PtyReadWriter interface {
	io.Reader
	io.Writer
}

// State represents the overlay display state.
type State int

const (
	StateHidden        State = iota // No overlay visible, full PTY passthrough
	StateIndicator                  // Status bar visible at bottom
	StateMenu                       // Panel browser active
	StateProcessViewer              // Process output viewer (temporary, while key held)
)

// ConnectionStatus represents daemon connection state.
type ConnectionStatus int

const (
	ConnectionUnknown ConnectionStatus = iota
	ConnectionConnected
	ConnectionDisconnected
	ConnectionError
)

// ProcessInfo holds information about a running process.
type ProcessInfo struct {
	ID            string
	Command       string
	State         string
	Runtime       time.Duration
	LastOutput    string   // Last line of output (trimmed)
	URLs          []string // URLs parsed from recent output
	LinkedProxyID string   // ID of proxy targeting this process (if any)
	HasAlerts     bool     // Whether process has recent alerts/errors
}

// ProxyInfo holds information about a running proxy.
type ProxyInfo struct {
	ID              string
	TargetURL       string
	ListenAddr      string
	HasErrors       bool
	ErrorCount      int
	TunnelURL       string
	TunnelRunning   bool
	LinkedProcessID string // ID of process this proxy targets (if any)
	TailscaleURL    string // Tailscale DNS URL if available (e.g., http://machine.tailnet.ts.net:port)

	// State is the runtime state reported by the daemon. Values:
	//   "running"                   — forwarding requests normally
	//   "waiting_for_dependencies"  — bound, but holding the gate until
	//                                 every `wait-for` script reports ready
	//   "stopped"                   — not running
	// When empty, renderers fall back to the legacy "running" vs
	// "error" distinction.
	State string

	// WaitingOn holds the sorted list of pending `wait-for` script
	// names. Populated only when State is "waiting_for_dependencies".
	WaitingOn []string

	// Uptime is the daemon-formatted proxy uptime (e.g. "14m"); empty if not running.
	Uptime string
	// TotalRequests is the cumulative request count the proxy has forwarded.
	TotalRequests int64
}

// ErrorInfo holds recent error information.
type ErrorInfo struct {
	Source    string // "proxy", "process", "daemon"
	Message   string
	Timestamp time.Time
}

// BrowserSession holds information about a connected browser.
type BrowserSession struct {
	ProxyID      string
	SessionID    string
	URL          string
	Interactions int
	Mutations    int
	LastActivity time.Time
}

// ScriptInfo holds information about a managed script from the ScriptRegistry.
// Scripts persist across process lifetimes, so their state is always available.
type ScriptInfo struct {
	Name       string // Script name (e.g., "dev")
	ProcessID  string // Current process ID (empty if idle)
	State      string // "idle", "starting", "running", "failed", "stopped", "restarting"
	Command    string
	StartCount int64
	FailCount  int64
	LastError  string
	HasAlerts  bool // Whether process has recent alerts/errors
}

// StartupLogEntry holds a startup log entry for display.
type StartupLogEntry struct {
	ScriptName string
	Level      string // "info", "warning", "error"
	EventType  string
	Message    string
	Timestamp  time.Time
}

// NoticeInfo is one surfaced silent-failure for the overview notice banner: a
// config-declared resource (proxy, script, port) that failed to start and has
// not since recovered. Mirror of the daemon's Notice (JSON-bridged).
type NoticeInfo struct {
	ID          string // "<domain>:<process_id>", stable per resource+domain
	Domain      string // "proxy" | "script" | "port"
	Severity    string // "error" | "warning"
	Resource    string // short name, e.g. "dev"
	Summary     string // one-line headline
	Detail      string // full message
	Remediation string // actionable hint, may be empty
	EventType   string // originating event_type
}

// PortInfo is one listening TCP port and its owner for the overview ports
// section. Status is "managed", "unmanaged", or "conflict".
type PortInfo struct {
	Port    int
	PID     int
	Name    string
	Windows bool
	Status  string
	System  bool // OS/infra daemon — hidden from overview unless showAllPorts
}

// OrphanInfo is one orphaned process group (leader dead, members alive).
type OrphanInfo struct {
	PGID  int
	Count int
}

// Status holds the current system status for display.
type Status struct {
	DaemonConnected ConnectionStatus
	DaemonPingMs    int64
	Processes       []ProcessInfo
	Scripts         []ScriptInfo
	Proxies         []ProxyInfo
	Ports           []PortInfo
	Orphans         []OrphanInfo
	BrowserSessions []BrowserSession
	RecentErrors    []ErrorInfo
	StartupLog      []StartupLogEntry
	Notices         []NoticeInfo
	LastUpdate      time.Time
}

// PanelItem represents a navigable panel in the horizontal panel view.
// Process panels persist until explicitly closed by the user.
type PanelItem struct {
	Type         string // "overview", "process", "proxy"
	ID           string // process or proxy ID
	Label        string // short display label for tab bar
	Content      string // cached output content (accumulates across restarts)
	ScrollOffset int    // lines scrolled up from bottom (0 = pinned to bottom)
	ProcessState string // last known process state ("running", "failed", "stopped", "")
	lineCount    int    // cached line count, updated by SetContent
}

// IsDone returns true if the process has stopped and the panel can be closed.
func (p *PanelItem) IsDone() bool {
	return p.ProcessState == "stopped" || p.ProcessState == "failed"
}

// ContentLines returns the cached number of lines in the panel's content.
func (p *PanelItem) ContentLines() int {
	return p.lineCount
}

// SetContent replaces the panel content and updates the cached line count.
func (p *PanelItem) SetContent(content string) {
	p.Content = content
	p.lineCount = countLines(content)
}

// AppendContent appends text to the panel content with a separator and caps at maxLines.
func (p *PanelItem) AppendContent(text, separator string, maxLines int) {
	if p.Content == "" {
		p.SetContent(text)
		return
	}
	combined := p.Content + separator + text
	// Cap content size
	lines := strings.Split(combined, "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	p.SetContent(strings.Join(lines, "\n"))
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := 1
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			n++
		}
	}
	return n
}

// Overlay manages the terminal overlay display.
type Overlay struct {
	// Terminal state
	ptmx   PtyReadWriter
	width  int
	height int

	// Display state
	state   atomic.Int32 // State enum
	showBar atomic.Bool  // Whether indicator bar is visible

	// Status data
	status   Status
	statusMu sync.RWMutex

	// Panel navigation (niri-style horizontal panels)
	panelIndex int         // Current panel index (0 = overview)
	panelItems []PanelItem // Available panels built from status
	panelMode  bool        // Whether in panel view (vs indicator)

	// Overview panel: interactive script selection
	overviewSelectedIdx int // Selected script row in overview (0-based)

	// Command input mode (bottom of overview)
	commandInput       bool   // Whether command input is active
	commandBuffer      string // Current command text being typed
	commandSelectedIdx int    // Highlighted row in the filtered command palette

	// Ports section: hide system/infra ports by default (toggle-ports flips it)
	showAllPorts bool

	// Overview notice banner: session-only dismissals of silent-failure notices
	notices noticeDismissals

	// Overview global actions (summarize / reconnect)
	summarizing      atomic.Bool  // AI summarize in flight (drives spinner + re-entrancy guard)
	connecting       atomic.Bool  // daemon reconnect in flight (spinner + re-entrancy guard)
	spinnerTick      atomic.Int64 // bumped by the action spinner ticker to advance frames
	summarizeEnabled bool         // whether a summarizer is configured (gates the 'm' action; under mu)
	summaryText      string       // latest AI summary (source for the summary panel; under mu)
	summaryErr       string       // last summarize error, shown on the actions line (under mu)
	connectErr       string       // last reconnect error, shown on the connection line (under mu)

	// Rendering
	renderer *Renderer

	// Output gating for freeze/unfreeze
	gate *OutputGate

	// Callbacks
	onAction         func(Action) error
	onFreeze         func()               // Called when screen should freeze (stop PTY output)
	onUnfreeze       func()               // Called when screen should unfreeze (send SIGWINCH)
	onBeforeUnfreeze func(panelID string) // Called before gate unfreeze with the active process panel ID
	childInAltScreen func() bool

	// Whether alt screen is currently used for menu/viewer (per open/close cycle)
	usingAltScreen bool

	// Redraw pausing (prevents indicator writes during streaming)
	redrawPaused atomic.Bool

	// Transition spinner: signals the spinner goroutine to stop
	transitionStop chan struct{}

	// Mutex for state changes
	mu sync.Mutex
}

// Config holds overlay configuration.
type Config struct {
	// Whether to show indicator bar by default
	ShowIndicator bool

	// Status refresh interval
	StatusRefreshInterval time.Duration

	// Version string to display in dashboard
	Version string

	// Action callback
	OnAction func(Action) error

	// Freeze/unfreeze callbacks for screen management
	OnFreeze   func() // Called when menu opens (freeze PTY output)
	OnUnfreeze func() // Called when menu closes (send SIGWINCH to redraw)
}

// DefaultConfig returns the default overlay configuration.
func DefaultConfig() Config {
	return Config{
		ShowIndicator:         true,
		StatusRefreshInterval: 2 * time.Second,
	}
}

// New creates a new Overlay.
func New(ptmx PtyReadWriter, width, height int, cfg Config) *Overlay {
	renderer := NewRenderer(os.Stdout, width, height)
	if cfg.Version != "" {
		renderer.SetVersion(cfg.Version)
	}

	o := &Overlay{
		ptmx:       ptmx,
		width:      width,
		height:     height,
		renderer:   renderer,
		onAction:   cfg.OnAction,
		onFreeze:   cfg.OnFreeze,
		onUnfreeze: cfg.OnUnfreeze,
	}

	if cfg.ShowIndicator {
		o.showBar.Store(true)
		o.state.Store(int32(StateIndicator))
	} else {
		o.state.Store(int32(StateHidden))
	}

	return o
}

// SetGate sets the output gate for freeze/unfreeze control.
func (o *Overlay) SetGate(gate *OutputGate) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.gate = gate

	// Route renderer writes through the gate's mutex so overlay draws
	// and PTY output never interleave on stdout. Critical on Windows
	// ConPTY where scroll regions aren't reliably enforced.
	o.renderer.SetOutput(gateDirectWriter{gate})
}

// gateDirectWriter is an io.Writer that routes through OutputGate.WriteDirect.
type gateDirectWriter struct {
	gate *OutputGate
}

func (w gateDirectWriter) Write(p []byte) (int, error) {
	return w.gate.WriteDirect(p)
}

// SetBeforeUnfreezeCallback sets a callback that fires before the gate unfreezes
// when closing the overlay. The callback receives the ID of the active process
// panel (empty string if no process panel was active). This allows the panel's
// cached content to be refreshed from the daemon before the overlay fully closes.
func (o *Overlay) SetBeforeUnfreezeCallback(fn func(panelID string)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.onBeforeUnfreeze = fn
}

// SetAltScreenChecker sets a callback that reports whether the child process
// is currently in the alternate screen buffer. Used to decide whether the
// overlay can safely use alt screen for menus (only when child is on main screen).
func (o *Overlay) SetAltScreenChecker(fn func() bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.childInAltScreen = fn
}

// isChildInAltScreen returns true if the child is in the alt screen buffer.
func (o *Overlay) isChildInAltScreen() bool {
	if o.childInAltScreen != nil {
		return o.childInAltScreen()
	}
	return false
}

// State returns the current overlay state.
func (o *Overlay) State() State {
	return State(o.state.Load())
}

// IsActive returns true if the overlay is capturing input.
func (o *Overlay) IsActive() bool {
	state := o.State()
	return state == StateMenu
}

// ShowIndicator returns whether the indicator bar is visible.
func (o *Overlay) ShowIndicator() bool {
	return o.showBar.Load()
}

// SetSize updates the terminal dimensions.
func (o *Overlay) SetSize(width, height int) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.width = width
	o.height = height
	o.renderer.SetSize(width, height)

	// Redraw if visible
	if o.showBar.Load() {
		o.draw()
	}
}

// UpdateStatus updates the displayed status.
func (o *Overlay) UpdateStatus(status Status) {
	o.statusMu.Lock()
	o.status = status
	o.status.LastUpdate = time.Now()
	o.statusMu.Unlock()

	// Redraw indicator if visible and not paused
	if o.showBar.Load() && o.State() == StateIndicator && !o.redrawPaused.Load() {
		o.mu.Lock()
		o.stopTransitionSpinnerLocked()
		o.draw()
		o.mu.Unlock()
	}
}

// SetRedrawPaused pauses or resumes indicator redraws.
// Status updates are still stored — just not rendered until unpaused.
func (o *Overlay) SetRedrawPaused(paused bool) {
	o.redrawPaused.Store(paused)
}

// GetStatus returns the current status.
func (o *Overlay) GetStatus() Status {
	o.statusMu.RLock()
	defer o.statusMu.RUnlock()
	return o.status
}

// showPanelDirect prepares the overlay to enter panel mode directly from the
// indicator state. The caller should follow up with handlePanelNav to navigate
// to the target panel.
// Must be called with mu held.
func (o *Overlay) showPanelDirect() {
	o.stopTransitionSpinnerLocked()

	// Freeze PTY output so it doesn't corrupt the panel view.
	if o.gate != nil {
		o.gate.Freeze()
	}

	// Use alt screen when child is on main screen (terminal restores content on exit).
	// When child is in alt screen (fullscreen app), draw directly — SIGWINCH will
	// trigger the child to redraw when the panel view closes.
	o.usingAltScreen = !o.isChildInAltScreen()
	if o.usingAltScreen {
		o.renderer.EnterAltScreen()
	}

	o.state.Store(int32(StateMenu))
	o.panelMode = false
	o.panelIndex = 0
	o.buildPanelItems()
}

// buildPanelItems merges current status into the panel list.
// Existing panels are preserved (content, scroll); new scripts/proxies are added.
// Panel IDs for scripts use the script name (not the process ID).
func (o *Overlay) buildPanelItems() {
	o.statusMu.RLock()
	status := o.status
	o.statusMu.RUnlock()

	// Ensure overview exists at index 0
	if len(o.panelItems) == 0 || o.panelItems[0].Type != "overview" {
		o.panelItems = append([]PanelItem{{Type: "overview", Label: "overview"}}, o.panelItems...)
	}

	// Ensure the summary panel sits at index 1 once a summary exists. Inserted
	// before the existing-panel index is built so later panels index correctly.
	if o.summaryText != "" {
		sumIdx := -1
		for i, p := range o.panelItems {
			if p.Type == "summary" {
				sumIdx = i
				break
			}
		}
		if sumIdx == -1 {
			o.panelItems = append(o.panelItems, PanelItem{})
			copy(o.panelItems[2:], o.panelItems[1:])
			o.panelItems[1] = PanelItem{Type: "summary", ID: "__summary", Label: "summary"}
			sumIdx = 1
		}
		o.panelItems[sumIdx].SetContent(o.summaryText)
	}

	// Index existing panels
	existing := make(map[string]int, len(o.panelItems))
	for i, p := range o.panelItems {
		if p.ID != "" {
			existing[p.ID] = i
		}
	}

	// Build script state lookup
	activeScripts := make(map[string]string, len(status.Scripts))
	for _, s := range status.Scripts {
		activeScripts[s.Name] = s.State
	}

	// Update existing script panels and add new ones
	for _, s := range status.Scripts {
		if idx, ok := existing[s.Name]; ok {
			o.panelItems[idx].ProcessState = s.State
		} else {
			label := s.Name
			if len(label) > 12 {
				label = label[:12]
			}
			o.panelItems = append(o.panelItems, PanelItem{
				Type: "process", ID: s.Name, Label: label, ProcessState: s.State,
			})
		}
	}

	// Mark panels whose script is gone as stopped
	for i := range o.panelItems {
		p := &o.panelItems[i]
		if p.Type == "process" && activeScripts[p.ID] == "" {
			if p.ProcessState == "" || p.ProcessState == "running" {
				p.ProcessState = "stopped"
			}
		}
	}

	// Add new proxies
	for _, p := range status.Proxies {
		if _, ok := existing[p.ID]; !ok {
			label := stripProjectPrefix(p.ID)
			if len(label) > 12 {
				label = label[:12]
			}
			o.panelItems = append(o.panelItems, PanelItem{
				Type: "proxy", ID: p.ID, Label: label,
			})
		}
	}

	// Ensure log panel exists as the last panel and update its content
	logIdx := -1
	for i, p := range o.panelItems {
		if p.Type == "log" {
			logIdx = i
			break
		}
	}
	if logIdx == -1 {
		o.panelItems = append(o.panelItems, PanelItem{Type: "log", ID: "__log", Label: "log"})
		logIdx = len(o.panelItems) - 1
	}
	o.panelItems[logIdx].SetContent(formatStartupLog(status.StartupLog))

	// Clamp overview selection to script count
	if n := len(status.Scripts); n > 0 {
		if o.overviewSelectedIdx >= n {
			o.overviewSelectedIdx = n - 1
		}
	} else {
		o.overviewSelectedIdx = 0
	}
}

// formatStartupLog formats startup log entries into readable text for the log panel.
// Each line is prefixed with HH:MM:SS so users can correlate events with timing.
// Entries with a zero Timestamp render with blank time padding so columns stay aligned.
func formatStartupLog(entries []StartupLogEntry) string {
	if len(entries) == 0 {
		return "no startup log entries"
	}
	var b strings.Builder
	for _, e := range entries {
		icon := "  "
		switch e.Level {
		case "error":
			icon = "✗ "
		case "warning":
			icon = "⚠ "
		case "info":
			icon = "✓ "
		}
		ts := "        "
		if !e.Timestamp.IsZero() {
			ts = e.Timestamp.Format("15:04:05")
		}
		line := ts + " " + icon + "[" + e.EventType + "]"
		if e.ScriptName != "" {
			line += " " + e.ScriptName + ":"
		}
		line += " " + e.Message
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func (o *Overlay) hideMenu() {
	// Capture the active process panel ID before resetting state.
	// This lets the before-unfreeze callback refresh the panel's cached
	// content from the daemon so it's up-to-date for the next open.
	var activePanelID string
	if o.panelMode && o.panelIndex < len(o.panelItems) {
		if p := o.panelItems[o.panelIndex]; p.Type == "process" {
			activePanelID = p.ID
		}
	}

	o.panelMode = false
	o.panelIndex = 0
	// panelItems are intentionally NOT cleared — they persist across open/close

	if o.showBar.Load() {
		o.state.Store(int32(StateIndicator))
	} else {
		o.state.Store(int32(StateHidden))
	}

	if o.usingAltScreen {
		// Exit alt screen — terminal restores the previous main screen content.
		o.renderer.ExitAltScreen()
	} else {
		// Child is a fullscreen TUI — clear the visible area so menu artifacts
		// are gone before the gate unfreezes and SIGWINCH triggers the child
		// to redraw. Use ED 2 + cursor home (preserves scroll region).
		o.renderer.ClearVisible()
	}

	// Refresh the active panel's content from the daemon before unfreezing.
	// This captures any output that arrived while the overlay was open,
	// ensuring the panel history is complete for the next open.
	if activePanelID != "" && o.onBeforeUnfreeze != nil {
		cb := o.onBeforeUnfreeze
		o.mu.Unlock()
		cb(activePanelID)
		o.mu.Lock()
	}

	// Start a transition spinner on the status bar before unfreezing.
	// This gives visual feedback while the child process redraws.
	if o.showBar.Load() {
		o.startTransitionSpinner()
	}

	// Unfreeze PTY output so it resumes flowing.
	// The gate's onUnfreeze callback sends SIGWINCH and re-enforces scroll region.
	if o.gate != nil {
		o.gate.Unfreeze()
	}
}

// spinnerFrames are ASCII spinner characters compatible with all terminals.
var spinnerFrames = [4]string{"|", "/", "-", "\\"}

// startTransitionSpinner launches a goroutine that cycles a spinner character
// on the status bar. It runs until stopTransitionSpinner is called or
// UpdateStatus fires (indicating real content has arrived).
// Must be called with mu held.
func (o *Overlay) startTransitionSpinner() {
	o.stopTransitionSpinnerLocked()

	stop := make(chan struct{})
	o.transitionStop = stop

	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()

		i := 0
		o.renderer.DrawStatusBarMessage(spinnerFrames[i] + " loading...")
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				i = (i + 1) % len(spinnerFrames)
				o.renderer.DrawStatusBarMessage(spinnerFrames[i] + " loading...")
			}
		}
	}()
}

// stopTransitionSpinner stops the transition spinner if running and redraws
// the normal indicator. Safe to call when no spinner is active.
func (o *Overlay) stopTransitionSpinner() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.stopTransitionSpinnerLocked()
}

// stopTransitionSpinnerLocked stops the transition spinner. Must be called with mu held.
func (o *Overlay) stopTransitionSpinnerLocked() {
	if o.transitionStop != nil {
		close(o.transitionStop)
		o.transitionStop = nil
	}
}

func (o *Overlay) draw() {
	state := o.State()
	if state == StateHidden {
		return
	}

	o.statusMu.RLock()
	status := o.status
	o.statusMu.RUnlock()

	// Apply session dismissals (and prune resolved ones) before rendering the
	// overview notice banner.
	status.Notices = o.notices.filter(status.Notices)

	// Mode-specific content occupies the region above the bottom status row.
	// Each screen leaves the bottom row free for the global status bar, which
	// is drawn once below.
	if state == StateMenu && o.panelMode && len(o.panelItems) > 0 {
		idx := o.panelIndex
		if idx >= len(o.panelItems) {
			idx = len(o.panelItems) - 1
		}
		o.renderer.DrawPanelView(o.panelItems, idx, status, o.overviewSelectedIdx, o.commandInput, o.commandBuffer, o.commandSelectedIdx, o.showAllPorts, o.overviewActionsLocked())
	}

	// The status bar is the single global element shared by every visible
	// screen. Drawing it last, in one place, guarantees consistent presence on
	// the protected bottom row across all states (indicator and panel views).
	o.renderer.DrawIndicator(status)
}

// Redraw forces a redraw of the overlay.
func (o *Overlay) Redraw() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.draw()
}

// OverviewActions carries the transient global-action state rendered on the
// overview panel's connection and actions lines.
type OverviewActions struct {
	SummarizeEnabled bool   // a summarizer is configured (show the 'm' action)
	Summarizing      bool   // summarize in flight (spinner on the actions line)
	SummaryErr       string // last summarize error
	Connecting       bool   // reconnect in flight (spinner on the connection line)
	ConnectErr       string // last reconnect error
	SpinnerFrame     int    // current spinner frame index
}

// overviewActionsLocked snapshots the overview action state. Must be called
// with mu held (draw path).
func (o *Overlay) overviewActionsLocked() OverviewActions {
	return OverviewActions{
		SummarizeEnabled: o.summarizeEnabled,
		Summarizing:      o.summarizing.Load(),
		SummaryErr:       o.summaryErr,
		Connecting:       o.connecting.Load(),
		ConnectErr:       o.connectErr,
		SpinnerFrame:     int(o.spinnerTick.Load()),
	}
}

// SetSummarizeEnabled records whether a summarizer is configured. Gates the
// overview 'm' action affordance.
func (o *Overlay) SetSummarizeEnabled(enabled bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.summarizeEnabled = enabled
}

// DrawStatusBarMessage draws a message on the status bar.
// This is used for transient messages like spinner updates.
// Note: The caller should call Redraw() or DrawIndicator() when done
// to restore the normal status bar.
func (o *Overlay) DrawStatusBarMessage(message string) {
	o.renderer.DrawStatusBarMessage(message)
}

// RedrawIndicator redraws just the status bar indicator with current status.
func (o *Overlay) RedrawIndicator() {
	o.statusMu.RLock()
	status := o.status
	o.statusMu.RUnlock()
	o.renderer.DrawIndicator(status)
}
