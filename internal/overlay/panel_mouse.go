package overlay

import (
	"strconv"
	"strings"

	"github.com/standardbeagle/vt10x"
)

// Mouse in the panel view. While the panels are open agnt asks the terminal
// for click reports (mode 1000, SGR encoding 1006); closing them puts back
// whatever mouse mode the agent had set, read from the filter's model of the
// host terminal. Clicks are matched against the regions the last panel frame
// drew (PanelHit) and run the same handlers the keys do.

// panelMouseOn turns on click and wheel reports, SGR encoded. Mode 1000
// replaces any motion mode the agent set, so a TUI's any-motion tracking
// does not flood the panels while they are open.
const panelMouseOn = "\x1b[?1000h\x1b[?1006h"

// mouseRestoreSequence returns the bytes that undo panelMouseOn and put the
// terminal back in the mouse modes the agent had: tracking off and SGR off,
// then the agent's own tracking mode and encoding back on.
func mouseRestoreSequence(mode vt10x.ModeFlag) string {
	var b strings.Builder
	b.WriteString("\x1b[?1000l\x1b[?1006l")
	switch {
	case mode&vt10x.ModeMouseX10 != 0:
		b.WriteString("\x1b[?9h")
	case mode&vt10x.ModeMouseButton != 0:
		b.WriteString("\x1b[?1000h")
	case mode&vt10x.ModeMouseMotion != 0:
		b.WriteString("\x1b[?1002h")
	case mode&vt10x.ModeMouseMany != 0:
		b.WriteString("\x1b[?1003h")
	}
	if mode&vt10x.ModeMouseSgr != 0 {
		b.WriteString("\x1b[?1006h")
	}
	return b.String()
}

// mouseEvent is one SGR mouse report: ESC [ < button ; col ; row (M|m).
type mouseEvent struct {
	button  int // low two bits plus the wheel bit; modifier and motion bits removed
	motion  bool
	release bool
	row     int // 1-based
	col     int // 1-based
}

const (
	mouseLeft      = 0
	mouseWheelUp   = 64
	mouseWheelDown = 65

	mouseModifierBits = 4 | 8 | 16 // shift, meta, ctrl
	mouseMotionBit    = 32
)

// parseSGRMouse reads an SGR mouse report as the escape reader returns it.
func parseSGRMouse(seq string) (mouseEvent, bool) {
	body, ok := strings.CutPrefix(seq, "\x1b[<")
	if !ok || len(body) < 6 {
		return mouseEvent{}, false
	}
	final := body[len(body)-1]
	if final != 'M' && final != 'm' {
		return mouseEvent{}, false
	}
	fields := strings.Split(body[:len(body)-1], ";")
	if len(fields) != 3 {
		return mouseEvent{}, false
	}
	var n [3]int
	for i, f := range fields {
		v, err := strconv.Atoi(f)
		if err != nil || v < 0 || v > 1<<16 {
			return mouseEvent{}, false
		}
		n[i] = v
	}
	if n[1] < 1 || n[2] < 1 {
		return mouseEvent{}, false
	}
	return mouseEvent{
		button:  n[0] &^ (mouseModifierBits | mouseMotionBit),
		motion:  n[0]&mouseMotionBit != 0,
		release: final == 'm',
		col:     n[1],
		row:     n[2],
	}, true
}

// panelHitKind says what a clickable region of the panel view does.
type panelHitKind int

const (
	hitKey      panelHitKind = iota // press Key
	hitTab                          // switch to panel Index
	hitCloseTab                     // close finished panel Index
	hitScript                       // select overview script Index; again opens it
)

// PanelHit is one clickable region of the last panel frame: columns
// [Col, End) on Row, all 1-based.
type PanelHit struct {
	Row, Col, End int
	Kind          panelHitKind
	Index         int
	Key           string
}

// addHit records a clickable region. Caller holds r.mu, inside a panel draw.
func (r *Renderer) addHit(h PanelHit) {
	if h.End > h.Col {
		r.panelHits = append(r.panelHits, h)
	}
}

// PanelHitAt returns the clickable region under row, col in the last panel
// frame. A later region wins, so one drawn on top of another takes the click.
func (r *Renderer) PanelHitAt(row, col int) (PanelHit, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.panelHits) - 1; i >= 0; i-- {
		h := r.panelHits[i]
		if h.Row == row && col >= h.Col && col < h.End {
			return h, true
		}
	}
	return PanelHit{}, false
}

// wheelLines is how far one wheel notch scrolls a text panel.
const wheelLines = 3

// handlePanelMouse acts on one mouse report. Only presses act; releases and
// motion are dropped without touching any state (they arrive between the two
// presses of Ctrl+C twice, for one). Must be called with overlay.mu held.
func (r *InputRouter) handlePanelMouse(ev mouseEvent) {
	if ev.release || ev.motion {
		return
	}
	o := r.overlay
	switch ev.button {
	case mouseWheelUp, mouseWheelDown:
		key, n := "Up", wheelLines
		if ev.button == mouseWheelDown {
			key = "Down"
		}
		// One notch, one step through a list; three lines through text.
		if o.commandInput || r.isOverviewWithScripts() {
			n = 1
		}
		for range n {
			r.handleMenuKeyLocked(key)
		}
		return
	case mouseLeft:
	default:
		return
	}

	hit, ok := o.renderer.PanelHitAt(ev.row, ev.col)
	if !ok {
		return
	}
	r.ctrlCExitArmed = false
	switch hit.Kind {
	case hitKey:
		r.handleMenuKeyLocked(hit.Key)
	case hitTab:
		r.focusPanel(hit.Index)
	case hitCloseTab:
		r.focusPanel(hit.Index)
		r.closeCurrentPanel()
	case hitScript:
		if hit.Index == o.overviewSelectedIdx {
			r.overviewEnter()
			return
		}
		o.overviewSelectedIdx = hit.Index
		o.draw()
	}
}

// focusPanel switches the panel view to panel i, closing the palette a
// click left open. Must be called with overlay.mu held.
func (r *InputRouter) focusPanel(i int) {
	o := r.overlay
	if i < 0 || i >= len(o.panelItems) {
		return
	}
	o.commandInput = false
	o.commandBuffer = ""
	if i == o.panelIndex && o.panelMode {
		o.draw()
		return
	}
	r.handlePanelNav(i - o.panelIndex)
}
