// Package overlay provides a terminal overlay system for agnt.
// It renders a status indicator bar and popup menus using ANSI escape sequences,
// while passing through input/output to the underlying PTY.
package overlay

import (
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// State represents the overlay display state.
type State int

const (
	StateHidden    State = iota // No overlay visible, full PTY passthrough
	StateIndicator              // Status bar visible at bottom
	StateMenu                   // Popup menu active
	StateInput                  // Text input mode
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
	ID      string
	Command string
	State   string
	Runtime time.Duration
}

// ProxyInfo holds information about a running proxy.
type ProxyInfo struct {
	ID         string
	TargetURL  string
	ListenAddr string
	HasErrors  bool
	ErrorCount int
}

// ErrorInfo holds recent error information.
type ErrorInfo struct {
	Source    string // "proxy", "process", "daemon"
	Message   string
	Timestamp time.Time
}

// Status holds the current system status for display.
type Status struct {
	DaemonConnected ConnectionStatus
	DaemonPingMs    int64
	Processes       []ProcessInfo
	Proxies         []ProxyInfo
	RecentErrors    []ErrorInfo
	LastUpdate      time.Time
}

// Overlay manages the terminal overlay display.
type Overlay struct {
	// Terminal state
	ptmx   *os.File
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

	// Rendering
	renderer *Renderer

	// Output gating for freeze/unfreeze
	gate *OutputGate

	// Callbacks
	onAction   func(Action) error
	onFreeze   func() // Called when screen should freeze (stop PTY output)
	onUnfreeze func() // Called when screen should unfreeze (send SIGWINCH)

	// Mutex for state changes
	mu sync.Mutex
}

// Config holds overlay configuration.
type Config struct {
	// Hotkey to toggle overlay (default: Ctrl+O = 0x0f)
	Hotkey byte

	// Whether to show indicator bar by default
	ShowIndicator bool

	// Status refresh interval
	StatusRefreshInterval time.Duration

	// Action callback
	OnAction func(Action) error

	// Freeze/unfreeze callbacks for screen management
	OnFreeze   func() // Called when menu opens (freeze PTY output)
	OnUnfreeze func() // Called when menu closes (send SIGWINCH to redraw)
}

// DefaultConfig returns the default overlay configuration.
func DefaultConfig() Config {
	return Config{
		Hotkey:                0x0f, // Ctrl+O
		ShowIndicator:         true,
		StatusRefreshInterval: 2 * time.Second,
	}
}

// New creates a new Overlay.
func New(ptmx *os.File, width, height int, cfg Config) *Overlay {
	o := &Overlay{
		ptmx:       ptmx,
		width:      width,
		height:     height,
		renderer:   NewRenderer(os.Stdout, width, height),
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

	// Redraw indicator if visible
	if o.showBar.Load() && o.State() == StateIndicator {
		o.mu.Lock()
		o.draw()
		o.mu.Unlock()
	}
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
	// Freeze PTY output so it doesn't corrupt the menu
	if o.gate != nil {
		o.gate.Freeze()
	}

	// Switch to alternate screen buffer - preserves main screen content
	o.renderer.EnterAltScreen()

	o.state.Store(int32(StateMenu))
	o.menuStack = []Menu{MainMenu()}
	o.selectedIndex = 0
	o.draw()
}

func (o *Overlay) hideMenu() {
	o.menuStack = nil
	o.inputBuffer = ""

	if o.showBar.Load() {
		o.state.Store(int32(StateIndicator))
	} else {
		o.state.Store(int32(StateHidden))
	}

	// Exit alternate screen - automatically restores main screen content
	o.renderer.ExitAltScreen()

	// Unfreeze PTY output so it resumes flowing
	if o.gate != nil {
		o.gate.Unfreeze()
	}

	// Redraw indicator bar (it's in the reserved row, not affected by alt screen)
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
		if len(o.menuStack) > 0 {
			o.renderer.DrawIndicator(status)
			o.renderer.DrawMenu(o.menuStack[len(o.menuStack)-1], o.selectedIndex)
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
