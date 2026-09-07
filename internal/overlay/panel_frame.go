package overlay

import (
	"bytes"
	"slices"

	"github.com/standardbeagle/vt10x"
)

// diffPanelFrame is the single panel presentation boundary. Panel drawing
// describes a complete view; only this method decides what reaches the terminal.
// The caller holds r.mu. The protected status row is owned by DrawIndicator.
func (r *Renderer) diffPanelFrame(frame *bytes.Buffer) *bytes.Buffer {
	if r.width <= 0 || r.height <= 1 {
		return frame
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
		return frame
	}
	var out bytes.Buffer
	park, parked := parkCursor()
	for y := 0; y < r.height-1; y++ {
		start := y * r.width
		row := cells[start : start+r.width]
		if slices.Equal(row, previous[start:start+r.width]) {
			continue
		}
		if out.Len() == 0 {
			out.WriteString(park + CursorHide)
		}
		out.WriteString("\x1b[")
		writeInt(&out, y+1)
		out.WriteString(";1H")
		var style cellStyle
		for x, glyph := range row {
			next := styleOf(glyph)
			writeSGR(&out, style, next, x == 0)
			style = next
			ch := glyph.Char
			if ch == 0 {
				ch = ' '
			}
			out.WriteRune(ch)
		}
	}
	if out.Len() > 0 {
		out.WriteString(Reset + parked.restore() + CursorShow)
	}
	return &out
}
