package overlay

import (
	"fmt"
	"io"
	"strings"
	"sync"
)

// ScreenRegion represents a rectangular region of the terminal screen.
type ScreenRegion struct {
	Row    int // 1-indexed top row
	Col    int // 1-indexed left column
	Width  int
	Height int
}

// ScreenBuffer stores saved screen content for restoration.
type ScreenBuffer struct {
	Region  ScreenRegion
	Content [][]rune   // [row][col] - stored content
	Styles  [][]string // [row][col] - ANSI style codes (if we track them)
}

// ScreenManager handles saving and restoring screen regions.
// It uses ANSI escape sequences to query and restore terminal content.
type ScreenManager struct {
	out       io.Writer
	width     int
	height    int
	mu        sync.Mutex
	savedRegs map[string]*ScreenBuffer // named saved regions
}

// NewScreenManager creates a new ScreenManager.
func NewScreenManager(out io.Writer, width, height int) *ScreenManager {
	return &ScreenManager{
		out:       out,
		width:     width,
		height:    height,
		savedRegs: make(map[string]*ScreenBuffer),
	}
}

// SetSize updates the terminal dimensions.
func (sm *ScreenManager) SetSize(width, height int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.width = width
	sm.height = height
}

// write outputs a string (caller must hold lock).
func (sm *ScreenManager) write(s string) {
	io.WriteString(sm.out, s)
}

// moveTo moves cursor to row, col (1-indexed).
func (sm *ScreenManager) moveTo(row, col int) {
	sm.write(fmt.Sprintf("\x1b[%d;%dH", row, col))
}

// SaveRegion saves the screen content in a region.
// Since we can't actually read terminal content, we use the alternate screen buffer
// approach - save cursor, draw overlay, restore when done.
//
// For terminals that don't support reading content, we'll use a different strategy:
// We'll track what we've drawn and clear it properly.
func (sm *ScreenManager) SaveRegion(name string, region ScreenRegion) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Store the region dimensions - we can't actually read the content
	// but we'll remember where we drew so we can clear it
	sm.savedRegs[name] = &ScreenBuffer{
		Region: region,
	}
}

// RestoreRegion restores a previously saved region by overwriting with spaces.
// This visually clears the region. The underlying application will redraw
// when it receives SIGWINCH.
func (sm *ScreenManager) RestoreRegion(name string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	buf, ok := sm.savedRegs[name]
	if !ok {
		return
	}

	// Overwrite the region with spaces using default attributes.
	// We use spaces instead of ECH because ECH leaves "blank" cells
	// that some terminal emulators don't properly refresh on SIGWINCH.
	sm.write(CursorSave + CursorHide + Reset)
	spaces := strings.Repeat(" ", buf.Region.Width)
	for row := buf.Region.Row; row < buf.Region.Row+buf.Region.Height; row++ {
		if row > 0 && row <= sm.height {
			sm.moveTo(row, buf.Region.Col)
			sm.write(spaces)
		}
	}
	sm.write(CursorRestore + CursorShow)

	delete(sm.savedRegs, name)
}

// ClearRegion clears a rectangular region without saving.
func (sm *ScreenManager) ClearRegion(region ScreenRegion) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.write(CursorSave + CursorHide)
	for row := region.Row; row < region.Row+region.Height; row++ {
		if row > 0 && row <= sm.height {
			sm.moveTo(row, region.Col)
			sm.write(fmt.Sprintf("\x1b[%dX", region.Width))
		}
	}
	sm.write(CursorRestore + CursorShow)
}

// ForceRedraw sends a signal to force the underlying application to redraw.
// This works by simulating a resize event (SIGWINCH).
func (sm *ScreenManager) ForceRedraw() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Method 1: Clear and redraw approach
	// We can't send SIGWINCH from here, but we can request a redraw
	// by outputting a window size report request
	sm.write("\x1b[18t") // Request window size in characters
}

// DrawOverlay draws content at a specific region and tracks it for later clearing.
// Returns the region for use with RestoreRegion.
func (sm *ScreenManager) DrawOverlay(name string, row, col, width, height int, drawFunc func(row, col, width, height int)) ScreenRegion {
	region := ScreenRegion{
		Row:    row,
		Col:    col,
		Width:  width,
		Height: height,
	}

	sm.SaveRegion(name, region)
	drawFunc(row, col, width, height)

	return region
}

// OverlayStack manages a stack of overlays for proper z-order handling.
type OverlayStack struct {
	sm    *ScreenManager
	stack []string // overlay names in z-order (bottom to top)
	mu    sync.Mutex
}

// NewOverlayStack creates a new overlay stack.
func NewOverlayStack(sm *ScreenManager) *OverlayStack {
	return &OverlayStack{
		sm:    sm,
		stack: make([]string, 0),
	}
}

// Push adds an overlay to the stack.
func (os *OverlayStack) Push(name string, region ScreenRegion) {
	os.mu.Lock()
	defer os.mu.Unlock()

	os.sm.SaveRegion(name, region)
	os.stack = append(os.stack, name)
}

// Pop removes and restores the top overlay.
func (os *OverlayStack) Pop() {
	os.mu.Lock()
	defer os.mu.Unlock()

	if len(os.stack) == 0 {
		return
	}

	name := os.stack[len(os.stack)-1]
	os.stack = os.stack[:len(os.stack)-1]
	os.sm.RestoreRegion(name)
}

// PopAll removes and restores all overlays.
func (os *OverlayStack) PopAll() {
	os.mu.Lock()
	defer os.mu.Unlock()

	// Restore in reverse order (top to bottom)
	for i := len(os.stack) - 1; i >= 0; i-- {
		os.sm.RestoreRegion(os.stack[i])
	}
	os.stack = os.stack[:0]
}

// Clear removes all overlays from the stack without clearing screen regions.
// Use this when relying on another mechanism (like SIGWINCH) to redraw.
func (os *OverlayStack) Clear() {
	os.mu.Lock()
	defer os.mu.Unlock()

	// Just clear the saved regions without restoring (no screen clearing)
	os.sm.mu.Lock()
	for _, name := range os.stack {
		delete(os.sm.savedRegs, name)
	}
	os.sm.mu.Unlock()

	os.stack = os.stack[:0]
}

// Depth returns the number of overlays on the stack.
func (os *OverlayStack) Depth() int {
	os.mu.Lock()
	defer os.mu.Unlock()
	return len(os.stack)
}
