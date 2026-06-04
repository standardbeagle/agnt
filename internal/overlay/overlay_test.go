package overlay

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDraw_AllVisibleScreensIncludeStatusBar verifies the global status bar is
// drawn on every visible screen through the central draw() dispatch: the bare
// indicator and the panel view. The panel keybind footer is moved one row up so
// the protected bottom row is reserved for the status bar.
func TestDraw_AllVisibleScreensIncludeStatusBar(t *testing.T) {
	const w, h = 80, 24

	t.Run("indicator", func(t *testing.T) {
		var buf bytes.Buffer
		o := New(nil, w, h, DefaultConfig()) // ShowIndicator defaults true -> StateIndicator
		o.renderer = NewRenderer(&buf, w, h)
		o.statusMu.Lock()
		o.status = Status{DaemonConnected: ConnectionConnected}
		o.statusMu.Unlock()
		o.Redraw()

		out := buf.String()
		assert.Contains(t, out, "Ctrl+→", "indicator screen must show the status bar")
		assert.Contains(t, out, fmt.Sprintf("\x1b[%d;1H", h), "status bar targets the bottom row")
	})

	t.Run("panel", func(t *testing.T) {
		var buf bytes.Buffer
		o := New(nil, w, h, DefaultConfig())
		o.renderer = NewRenderer(&buf, w, h)
		o.panelMode = true
		o.panelItems = []PanelItem{{Type: "overview", Label: "overview"}}
		o.panelIndex = 0
		o.state.Store(int32(StateMenu))
		o.statusMu.Lock()
		o.status = Status{DaemonConnected: ConnectionConnected}
		o.statusMu.Unlock()
		o.Redraw()

		out := buf.String()
		assert.Contains(t, out, "Ctrl+→", "panel screen must show the status bar")
		assert.Contains(t, out, "Esc Exit", "panel keybind footer must remain")
		assert.Contains(t, out, fmt.Sprintf("\x1b[%d;1H", h), "status bar targets the bottom row")
		assert.Contains(t, out, fmt.Sprintf("\x1b[%d;1H", h-1), "panel footer sits one row above the status bar")
	})

	t.Run("hidden draws nothing", func(t *testing.T) {
		var buf bytes.Buffer
		cfg := DefaultConfig()
		cfg.ShowIndicator = false
		o := New(nil, w, h, cfg) // StateHidden
		o.renderer = NewRenderer(&buf, w, h)
		o.Redraw()
		assert.Empty(t, buf.String(), "hidden overlay must not draw the status bar")
	})
}

func TestNewOverlay(t *testing.T) {
	cfg := DefaultConfig()
	o := New(nil, 80, 24, cfg)

	if o == nil {
		t.Fatal("expected overlay, got nil")
	}

	if o.width != 80 {
		t.Errorf("expected width 80, got %d", o.width)
	}

	if o.height != 24 {
		t.Errorf("expected height 24, got %d", o.height)
	}

	// Default config should show indicator
	if !o.ShowIndicator() {
		t.Error("expected indicator to be shown by default")
	}

	if o.State() != StateIndicator {
		t.Errorf("expected StateIndicator, got %v", o.State())
	}
}

func TestOverlayStates(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ShowIndicator = false
	o := New(nil, 80, 24, cfg)

	// Initial state should be hidden
	if o.State() != StateHidden {
		t.Errorf("expected StateHidden, got %v", o.State())
	}

	if o.IsActive() {
		t.Error("overlay should not be active when hidden")
	}

	// Show should switch to menu
	o.Show()
	if o.State() != StateMenu {
		t.Errorf("expected StateMenu after Show, got %v", o.State())
	}

	if !o.IsActive() {
		t.Error("overlay should be active when menu is shown")
	}

	// Hide should switch back to hidden
	o.Hide()
	if o.State() != StateHidden {
		t.Errorf("expected StateHidden after Hide, got %v", o.State())
	}
}

func TestOverlayToggleIndicator(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ShowIndicator = true
	o := New(nil, 80, 24, cfg)

	if !o.ShowIndicator() {
		t.Error("expected indicator to be shown initially")
	}

	o.ToggleIndicator()
	if o.ShowIndicator() {
		t.Error("expected indicator to be hidden after toggle")
	}

	o.ToggleIndicator()
	if !o.ShowIndicator() {
		t.Error("expected indicator to be shown after second toggle")
	}
}

func TestOverlayStatus(t *testing.T) {
	cfg := DefaultConfig()
	o := New(nil, 80, 24, cfg)

	status := Status{
		DaemonConnected: ConnectionConnected,
		DaemonPingMs:    5,
		Processes: []ProcessInfo{
			{ID: "test-1", State: "running"},
			{ID: "test-2", State: "stopped"},
		},
		Proxies: []ProxyInfo{
			{ID: "proxy-1", HasErrors: false},
			{ID: "proxy-2", HasErrors: true, ErrorCount: 3},
		},
	}

	o.UpdateStatus(status)

	got := o.GetStatus()
	if got.DaemonConnected != ConnectionConnected {
		t.Errorf("expected ConnectionConnected, got %v", got.DaemonConnected)
	}

	if len(got.Processes) != 2 {
		t.Errorf("expected 2 processes, got %d", len(got.Processes))
	}

	if len(got.Proxies) != 2 {
		t.Errorf("expected 2 proxies, got %d", len(got.Proxies))
	}
}

func TestOverlaySetSize(t *testing.T) {
	cfg := DefaultConfig()
	o := New(nil, 80, 24, cfg)

	o.SetSize(120, 40)

	if o.width != 120 {
		t.Errorf("expected width 120, got %d", o.width)
	}

	if o.height != 40 {
		t.Errorf("expected height 40, got %d", o.height)
	}
}

func TestRendererEstimateVisibleLength(t *testing.T) {
	r := NewRenderer(&bytes.Buffer{}, 80, 24)

	tests := []struct {
		input    string
		expected int
	}{
		{"hello", 5},
		{"hello world", 11},
		{FgRed + "hello" + Reset, 5},
		{Bold + FgGreen + "test" + Reset, 4},
		{"", 0},
		{FgBlue + FgRed + "", 0},
	}

	for _, tt := range tests {
		got := r.estimateVisibleLength(tt.input)
		if got != tt.expected {
			t.Errorf("estimateVisibleLength(%q) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

func TestRendererPadRight(t *testing.T) {
	r := NewRenderer(&bytes.Buffer{}, 80, 24)

	result := r.padRight("hello", 10)
	if len(result) != 10 {
		t.Errorf("expected length 10, got %d", len(result))
	}

	if !strings.HasPrefix(result, "hello") {
		t.Error("expected string to start with 'hello'")
	}
}

func TestRendererPadCenter(t *testing.T) {
	r := NewRenderer(&bytes.Buffer{}, 80, 24)

	result := r.padCenter("hi", 10)
	expected := "    hi    "
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestMainMenu(t *testing.T) {
	menu := MainMenu()

	if menu.Title != "agnt" {
		t.Errorf("expected title 'agnt', got %q", menu.Title)
	}

	if len(menu.Items) == 0 {
		t.Error("expected menu items, got none")
	}

	// Check that some expected items exist
	foundBash := false
	foundClose := false
	for _, item := range menu.Items {
		if item.Action == ActionBashCommand {
			foundBash = true
		}
		if item.Action == ActionClose {
			foundClose = true
		}
	}

	if !foundBash {
		t.Error("expected ActionBashCommand in menu")
	}

	if !foundClose {
		t.Error("expected ActionClose in menu")
	}
}

func TestEscapeSequenceReader(t *testing.T) {
	r := NewEscapeSequenceReader()

	// Test regular character
	key, complete := r.Feed('a')
	if !complete || key != "a" {
		t.Errorf("expected 'a' complete, got %q complete=%v", key, complete)
	}

	// Test escape sequence for Up arrow
	r.Reset()
	_, complete = r.Feed(0x1b) // ESC
	if complete {
		t.Error("expected incomplete after ESC")
	}

	_, complete = r.Feed('[')
	if complete {
		t.Error("expected incomplete after [")
	}

	key, complete = r.Feed('A')
	if !complete || key != "Up" {
		t.Errorf("expected 'Up' complete, got %q complete=%v", key, complete)
	}

	// Test Down arrow
	r.Reset()
	r.Feed(0x1b)
	r.Feed('[')
	key, complete = r.Feed('B')
	if !complete || key != "Down" {
		t.Errorf("expected 'Down' complete, got %q complete=%v", key, complete)
	}
}

func TestEscapeSequenceReaderAllArrowKeys(t *testing.T) {
	tests := []struct {
		sequence []byte
		expected string
	}{
		{[]byte{0x1b, '[', 'A'}, "Up"},
		{[]byte{0x1b, '[', 'B'}, "Down"},
		{[]byte{0x1b, '[', 'C'}, "Right"},
		{[]byte{0x1b, '[', 'D'}, "Left"},
		{[]byte{0x1b, '[', 'H'}, "Home"},
		{[]byte{0x1b, '[', 'F'}, "End"},
		{[]byte{0x1b, '[', '3', '~'}, "Delete"},
	}

	for _, tt := range tests {
		r := NewEscapeSequenceReader()
		var key string
		var complete bool

		for _, b := range tt.sequence {
			key, complete = r.Feed(b)
		}

		if !complete {
			t.Errorf("sequence %v: expected complete, got incomplete", tt.sequence)
		}
		if key != tt.expected {
			t.Errorf("sequence %v: expected %q, got %q", tt.sequence, tt.expected, key)
		}
	}
}

func TestEscapeSequenceReaderTimeout(t *testing.T) {
	r := NewEscapeSequenceReader()

	// Feed just ESC
	_, complete := r.Feed(0x1b)
	if complete {
		t.Error("expected incomplete after ESC")
	}

	// Check IsPending
	if !r.IsPending() {
		t.Error("expected IsPending() to be true after ESC")
	}

	// Timeout should return Escape
	key, hadPending := r.Timeout()
	if !hadPending {
		t.Error("expected hadPending to be true")
	}
	if key != "Escape" {
		t.Errorf("expected 'Escape', got %q", key)
	}

	// After timeout, should not be pending
	if r.IsPending() {
		t.Error("expected IsPending() to be false after Timeout()")
	}
}

func TestEscapeSequenceReaderNonCSI(t *testing.T) {
	r := NewEscapeSequenceReader()

	// Feed ESC followed by a non-[ character (not a CSI sequence)
	_, complete := r.Feed(0x1b)
	if complete {
		t.Error("expected incomplete after ESC")
	}

	// Feed 'a' instead of '[' - this is "Escape+a"
	key, complete := r.Feed('a')
	if !complete {
		t.Error("expected complete after non-CSI byte")
	}
	if key != "Escape+a" {
		t.Errorf("expected 'Escape+a', got %q", key)
	}
}

func TestEscapeSequenceReaderIsPending(t *testing.T) {
	r := NewEscapeSequenceReader()

	// Initially not pending
	if r.IsPending() {
		t.Error("expected not pending initially")
	}

	// After regular char, not pending
	r.Feed('x')
	if r.IsPending() {
		t.Error("expected not pending after regular char")
	}

	// After ESC, pending
	r.Feed(0x1b)
	if !r.IsPending() {
		t.Error("expected pending after ESC")
	}

	// After '[', still pending
	r.Feed('[')
	if !r.IsPending() {
		t.Error("expected pending after [")
	}

	// After 'A' (complete sequence), not pending
	r.Feed('A')
	if r.IsPending() {
		t.Error("expected not pending after complete sequence")
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if !cfg.ShowIndicator {
		t.Error("expected ShowIndicator to be true by default")
	}

	if cfg.StatusRefreshInterval <= 0 {
		t.Error("expected positive StatusRefreshInterval")
	}
}

func TestProcessStateIconHealthy(t *testing.T) {
	// Running without alerts = green circle
	icon, color := processStateIcon("running", false, 0)
	assert.Equal(t, "\u25cf", icon)
	assert.Equal(t, FgGreen, color)
}

func TestProcessStateIconWithAlerts(t *testing.T) {
	// Running with alerts = yellow square
	icon, color := processStateIcon("running", true, 0)
	assert.Equal(t, "\u25a0", icon)
	assert.Equal(t, FgYellow, color)
}

func TestProcessStateIconFailed(t *testing.T) {
	// Failed ignores hasAlerts (already an error state)
	icon, color := processStateIcon("failed", false, 0)
	assert.Equal(t, "\u2717", icon)
	assert.Equal(t, FgRed, color)

	icon2, color2 := processStateIcon("failed", true, 0)
	assert.Equal(t, icon, icon2)
	assert.Equal(t, color, color2)
}

func TestProcessStateIconStopped(t *testing.T) {
	icon, color := processStateIcon("stopped", false, 0)
	assert.Equal(t, "\u2717", icon)
	assert.Equal(t, FgYellow, color)
}

func TestProcessStateIconStarting(t *testing.T) {
	icon, color := processStateIcon("starting", false, 0)
	assert.Equal(t, "\u25cc", icon)
	assert.Equal(t, FgCyan, color)
}

func TestProcessStateIconIdle(t *testing.T) {
	icon, color := processStateIcon("idle", false, 0)
	assert.Equal(t, "\u25cb", icon)
	assert.Equal(t, FgBrightBlack, color)
}

func TestProcessStateIconUnknown(t *testing.T) {
	icon, color := processStateIcon("something-else", false, 0)
	assert.Equal(t, "\u25cb", icon)
	assert.Equal(t, FgBrightBlack, color)
}

func TestTransitionSpinnerStartsOnHideMenu(t *testing.T) {
	var buf bytes.Buffer
	cfg := DefaultConfig()
	cfg.ShowIndicator = true
	o := New(nil, 80, 24, cfg)
	gate := NewOutputGate(&buf)
	o.SetGate(gate)

	// Open the overlay to enter menu state
	o.Show()
	assert.Equal(t, StateMenu, o.State())

	// Close the overlay (triggers transition spinner)
	o.Hide()
	assert.Equal(t, StateIndicator, o.State())

	// Spinner should be running
	o.mu.Lock()
	assert.NotNil(t, o.transitionStop, "spinner should be running after hideMenu")
	o.mu.Unlock()

	// Stop it to clean up
	o.stopTransitionSpinner()
}

func TestTransitionSpinnerStopsOnUpdateStatus(t *testing.T) {
	var buf bytes.Buffer
	cfg := DefaultConfig()
	cfg.ShowIndicator = true
	o := New(nil, 80, 24, cfg)
	gate := NewOutputGate(&buf)
	o.SetGate(gate)

	// Open and close to start the spinner
	o.Show()
	o.Hide()

	o.mu.Lock()
	assert.NotNil(t, o.transitionStop, "spinner should be running")
	o.mu.Unlock()

	// UpdateStatus should stop the spinner
	o.UpdateStatus(Status{DaemonConnected: ConnectionConnected})

	o.mu.Lock()
	assert.Nil(t, o.transitionStop, "spinner should be stopped after UpdateStatus")
	o.mu.Unlock()
}

func TestTransitionSpinnerStopsOnShowMenu(t *testing.T) {
	var buf bytes.Buffer
	cfg := DefaultConfig()
	cfg.ShowIndicator = true
	o := New(nil, 80, 24, cfg)
	gate := NewOutputGate(&buf)
	o.SetGate(gate)

	// Start spinner
	o.Show()
	o.Hide()

	o.mu.Lock()
	assert.NotNil(t, o.transitionStop, "spinner should be running")
	o.mu.Unlock()

	// Re-opening should stop the spinner
	o.Show()

	o.mu.Lock()
	assert.Nil(t, o.transitionStop, "spinner should be stopped after Show")
	o.mu.Unlock()

	o.Hide()
}

func TestTransitionSpinnerNotStartedWhenBarHidden(t *testing.T) {
	var buf bytes.Buffer
	cfg := DefaultConfig()
	cfg.ShowIndicator = false
	o := New(nil, 80, 24, cfg)
	gate := NewOutputGate(&buf)
	o.SetGate(gate)

	o.Show()
	o.Hide()

	o.mu.Lock()
	assert.Nil(t, o.transitionStop, "spinner should not start when bar is hidden")
	o.mu.Unlock()
}

func TestTransitionSpinnerDoubleStopIsSafe(t *testing.T) {
	cfg := DefaultConfig()
	o := New(nil, 80, 24, cfg)

	// Stopping when nothing is running should not panic
	o.stopTransitionSpinner()
	o.stopTransitionSpinner()
}
