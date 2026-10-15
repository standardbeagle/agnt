package overlay

import (
	"bytes"
	"slices"

	"github.com/standardbeagle/vt10x"
)

// diffPanelFrame is the single panel presentation boundary. Panel drawing
// describes a complete view; only this method decides what reaches the terminal.
// The caller holds r.mu. The protected status row is owned by DrawIndicator.
// overChild reports a row diff, which is drawn over whatever the terminal
// shows and must go out through flushPanelDiff; otherwise the result is a
// complete frame for flushBuffer.
func (r *Renderer) diffPanelFrame(frame *bytes.Buffer) (out *bytes.Buffer, overChild bool) {
	if r.width <= 0 || r.height <= 1 {
		return frame, false
	}
	terminal := vt10x.New(vt10x.WithSize(r.width, r.height))
	_, _ = terminal.Write(frame.Bytes()) // vt10x consumes an in-memory ANSI stream.
	cells := make([]vt10x.Glyph, 0, r.width*(r.height-1))
	for y := 0; y < r.height-1; y++ {
		cells = terminal.Row(y, cells)
	}
	previous := r.panelFrame
	r.panelFrame = cells
	if len(previous) != len(cells) {
		return frame, false
	}
	var diff bytes.Buffer
	for y := 0; y < r.height-1; y++ {
		start := y * r.width
		row := cells[start : start+r.width]
		if slices.Equal(row, previous[start:start+r.width]) {
			continue
		}
		if diff.Len() == 0 {
			diff.WriteString(CursorHide)
		}
		diff.WriteString("\x1b[")
		writeInt(&diff, y+1)
		diff.WriteString(";1H")
		var style cellStyle
		for x, glyph := range row {
			next := styleOf(glyph)
			writeSGR(&diff, style, next, x == 0)
			style = next
			ch := glyph.Char
			if ch == 0 {
				ch = ' '
			}
			diff.WriteRune(ch)
		}
	}
	if diff.Len() > 0 {
		diff.WriteString(Reset)
	}
	return &diff, true
}

// flushPanelDiff writes a row diff from diffPanelFrame like flushOverChild,
// then shows the cursor the panel view always leaves visible. Called WITHOUT
// r.mu. An empty diff writes nothing.
func (r *Renderer) flushPanelDiff(diff *bytes.Buffer) {
	if diff.Len() == 0 {
		return
	}
	drawOverChild(func(park string, parked parkedCursor) {
		_, _ = r.out.Write([]byte(park + diff.String() + parked.restore() + CursorShow))
	})
}
