// Package overlay provides a terminal overlay system for agnt.
// It renders a status indicator bar and popup menus using ANSI escape sequences,
// while passing through input/output to the underlying PTY.
package overlay

import (
	"io"
	"os"
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
	StateMenu                       // Popup menu active
	StateInput                      // Text input mode
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

// Status holds the current system status for display.
type Status struct {
	DaemonConnected ConnectionStatus
	DaemonPingMs    int64
	Processes       []ProcessInfo
	Proxies         []ProxyInfo
	BrowserSessions []BrowserSession
	RecentErrors    []ErrorInfo
	LastUpdate      time.Time
}

// PanelItem represents a navigable panel in the horizontal panel view.
type PanelItem struct {
	Type    string // "overview", "process", "proxy"
	ID      string // process or proxy ID
	Label   string // short display label for tab bar
	Content string // cached content for display
}

// Overlay manages the terminal overlay display.
type Overlay struct {
	// Terminal state
	ptmx   PtyReadWriter
	width  int
	height int

	// Display state
	state       atomic.Int32 // State enum
	showBar     atomic.Bool  // Whether indicator bar is visible
	menuStack   []Menu
	inputBuffer string
	inputPrompt string
	inputAction func(string) error

	// Status data
	status   Status
	statusMu sync.RWMutex

	// Menu selection
	selectedIndex int

	// Panel navigation (niri-style horizontal panels)
	panelIndex       int         // Current panel index (0 = overview)
	panelItems       []PanelItem // Available panels built from status
	panelMode        bool        // Whether in panel view (vs overview/menu)
	directPanelEntry bool        // Entered panel mode directly (Ctrl+Arrow from indicator)

	// Rendering
	renderer *Renderer

	// Output gating for freeze/unfreeze
	gate *OutputGate

	// Callbacks
	onAction         func(Action) error
	onFreeze         func() // Called when screen should freeze (stop PTY output)
	onUnfreeze       func() // Called when screen should unfreeze (send SIGWINCH)
	childInAltScreen func() bool

	// Whether alt screen is currently used for menu/viewer (per open/close cycle)
	usingAltScreen bool

	// Redraw pausing (prevents indicator writes during streaming)
	redrawPaused atomic.Bool

	// Mutex for state changes
	mu sync.Mutex
}

// Config holds overlay configuration.
type Config struct {
	// Hotkey to toggle overlay (default: Ctrl+L = 0x0C)
	Hotkey byte

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
		Hotkey:                0x19, // Ctrl+Y
		ShowIndicator:         true,
		StatusRefreshInterval: 2 * time.Second,
	}
}

// New creates a new Overlay.
func New(ptmx PtyReadWriter, width, height int, cfg Config) *Overlay {
	renderer := NewRenderer(os.Stdout, width, height)
	if cfg.Hotkey != 0 {
		renderer.SetHotkey(cfg.Hotkey)
	}
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
	return state == StateMenu || state == StateInput
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

// Toggle toggles the overlay menu visibility.
func (o *Overlay) Toggle() {
	o.mu.Lock()
	defer o.mu.Unlock()

	state := o.State()
	switch state {
	case StateHidden, StateIndicator:
		o.showMenu()
	case StateMenu, StateInput:
		o.hideMenu()
	}
}

// Show shows the overlay menu.
func (o *Overlay) Show() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.showMenu()
}

// Hide hides the overlay.
func (o *Overlay) Hide() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.hideMenu()
}

// ToggleIndicator toggles the indicator bar visibility.
func (o *Overlay) ToggleIndicator() {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.showBar.Load() {
		o.showBar.Store(false)
		o.state.Store(int32(StateHidden))
		o.renderer.ClearIndicator()
	} else {
		o.showBar.Store(true)
		o.state.Store(int32(StateIndicator))
		o.draw()
	}
}

func (o *Overlay) showMenu() {
	o.activateOverlay(false)
	o.draw()
}

// showPanelDirect prepares the overlay to enter panel mode directly from the
// indicator state (bypassing the menu overview). The caller should follow up
// with handlePanelNav to navigate to the target panel.
// Must be called with mu held.
func (o *Overlay) showPanelDirect() {
	o.activateOverlay(true)
}

// activateOverlay is the shared setup for opening the overlay.
// When directPanel is true, the overlay enters panel mode directly (no draw);
// when false, the normal menu overview is shown.
// Must be called with mu held.
func (o *Overlay) activateOverlay(directPanel bool) {
	// Freeze PTY output so it doesn't corrupt the menu
	if o.gate != nil {
		o.gate.Freeze()
	}

	// Use alt screen when child is on main screen (terminal restores content on exit).
	// When child is in alt screen (fullscreen app), draw directly — SIGWINCH will
	// trigger the child to redraw when the menu closes.
	o.usingAltScreen = !o.isChildInAltScreen()
	if o.usingAltScreen {
		o.renderer.EnterAltScreen()
	}

	o.state.Store(int32(StateMenu))

	// Check daemon connection status and show appropriate menu
	o.statusMu.RLock()
	connected := o.status.DaemonConnected == ConnectionConnected
	o.statusMu.RUnlock()

	if connected {
		o.menuStack = []Menu{MainMenu()}
	} else {
		o.menuStack = []Menu{DisconnectedMenu()}
	}
	o.selectedIndex = 0
	o.directPanelEntry = directPanel
	o.panelMode = false
	o.panelIndex = 0
	o.buildPanelItems()
}

// buildPanelItems builds the panel list from current status.
// Panel 0 is always the overview/actions panel.
func (o *Overlay) buildPanelItems() {
	o.statusMu.RLock()
	status := o.status
	o.statusMu.RUnlock()

	items := []PanelItem{
		{Type: "overview", Label: "overview"},
	}

	for _, p := range status.Processes {
		label := p.ID
		if len(label) > 12 {
			label = label[:12]
		}
		items = append(items, PanelItem{
			Type:  "process",
			ID:    p.ID,
			Label: label,
		})
	}

	for _, p := range status.Proxies {
		label := p.ID
		if len(label) > 12 {
			label = label[:12]
		}
		items = append(items, PanelItem{
			Type:  "proxy",
			ID:    p.ID,
			Label: label,
		})
	}

	o.panelItems = items
}

func (o *Overlay) hideMenu() {
	o.menuStack = nil
	o.inputBuffer = ""
	o.panelMode = false
	o.panelIndex = 0
	o.panelItems = nil
	o.directPanelEntry = false

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

	// Unfreeze PTY output so it resumes flowing.
	// The gate's onUnfreeze callback sends SIGWINCH and re-enforces scroll region.
	if o.gate != nil {
		o.gate.Unfreeze()
	}

	// Redraw indicator bar
	if o.showBar.Load() {
		o.draw()
	}
}

func (o *Overlay) draw() {
	o.statusMu.RLock()
	status := o.status
	o.statusMu.RUnlock()

	switch o.State() {
	case StateIndicator:
		o.renderer.DrawIndicator(status)
	case StateMenu:
		if o.panelMode && len(o.panelItems) > 0 {
			// Panel view: full-screen panel with tab bar
			idx := o.panelIndex
			if idx >= len(o.panelItems) {
				idx = len(o.panelItems) - 1
			}
			o.renderer.DrawPanelView(o.panelItems, idx, status)
		} else if len(o.menuStack) > 0 {
			o.renderer.DrawIndicator(status)
			// Use DrawDashboard for the main menu (comprehensive view)
			if len(o.menuStack) == 1 {
				o.renderer.DrawDashboard(o.menuStack[0], o.selectedIndex, status)
			} else {
				o.renderer.DrawMenu(o.menuStack[len(o.menuStack)-1], o.selectedIndex)
			}
		}
	case StateInput:
		o.renderer.DrawIndicator(status)
		o.renderer.DrawInput(o.inputPrompt, o.inputBuffer)
	}
}

// Redraw forces a redraw of the overlay.
func (o *Overlay) Redraw() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.draw()
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
