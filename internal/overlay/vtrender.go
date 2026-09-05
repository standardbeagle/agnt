package overlay

import (
	"bytes"
	"strconv"

	"github.com/standardbeagle/vt10x"
)

// writeInt appends a small non-negative int without strconv.Itoa's string
// allocation — this runs several times per attribute change and per row.
func writeInt(buf *bytes.Buffer, n int) {
	var scratch [8]byte
	buf.Write(strconv.AppendInt(scratch[:0], int64(n), 10))
}

// Rendering a vt10x screen back onto a real terminal.
//
// The virtual screen is fed every byte the child writes, so it is a complete
// model of what the child believes is on screen — including while the overlay
// has the gate frozen and the child's bytes are going nowhere. That makes it
// the one source able to restore the child's screen without asking the child
// to repaint (which is a race the child sometimes loses; see jiggleRepaint).
//
// Two shapes come off the same walk: an ANSI repaint for the local terminal,
// and a cell snapshot for a remote viewer.
//
// Colour fidelity caveat, inherent to vt10x's model: Color packs 24-bit RGB as
// r<<16|g<<8|b in the same uint32 space as the xterm-256 indices, so a value
// below 256 is ambiguous — xterm index 5 and rgb(0,0,5) are indistinguishable
// after parsing. We resolve <256 as an index, which is correct for every
// palette user and wrong only for near-black RGB. Values >=256 are RGB.

// vt10x attribute bits (mirrored from state.go, which keeps them unexported).
const (
	vtAttrReverse   = 1 << 0
	vtAttrUnderline = 1 << 1
	vtAttrBold      = 1 << 2
	vtAttrItalic    = 1 << 4
	vtAttrBlink     = 1 << 5
)

// cellStyle is the part of a Glyph that costs an SGR change.
type cellStyle struct {
	fg, bg vt10x.Color
	mode   int16
}

func styleOf(g vt10x.Glyph) cellStyle {
	return cellStyle{fg: g.FG, bg: g.BG, mode: g.Mode &^ (1 << 6)} // drop attrWrap
}

// writeSGR emits the shortest sequence that moves the terminal from `from` to
// `to`. Emitting a full reset per cell would cost ~20 bytes on every one of
// the cols*rows cells; runs of identical style are the overwhelming majority,
// so the cost is per *change*, not per cell.
func writeSGR(buf *bytes.Buffer, from, to cellStyle, first bool) {
	if !first && from == to {
		return
	}
	buf.WriteString("\x1b[0")
	if to.mode&vtAttrBold != 0 {
		buf.WriteString(";1")
	}
	if to.mode&vtAttrItalic != 0 {
		buf.WriteString(";3")
	}
	if to.mode&vtAttrUnderline != 0 {
		buf.WriteString(";4")
	}
	if to.mode&vtAttrBlink != 0 {
		buf.WriteString(";5")
	}
	if to.mode&vtAttrReverse != 0 {
		buf.WriteString(";7")
	}
	writeColor(buf, to.fg, true)
	writeColor(buf, to.bg, false)
	buf.WriteByte('m')
}

func writeColor(buf *bytes.Buffer, c vt10x.Color, fg bool) {
	base := 40
	if fg {
		base = 30
	}
	switch {
	case c == vt10x.DefaultFG || c == vt10x.DefaultBG || c == vt10x.DefaultCursor:
		return // SGR 0 already restored the default
	case c.IsRGB():
		r, g, b := c.RGB()
		buf.WriteByte(';')
		writeInt(buf, base+8)
		buf.WriteString(";2;")
		writeInt(buf, int(r))
		buf.WriteByte(';')
		writeInt(buf, int(g))
		buf.WriteByte(';')
		writeInt(buf, int(b))
	case c < 8:
		buf.WriteByte(';')
		writeInt(buf, base+int(c))
	case c < 16:
		buf.WriteByte(';')
		writeInt(buf, base+60+int(c)-8)
	case c < 256:
		buf.WriteByte(';')
		writeInt(buf, base+8)
		buf.WriteString(";5;")
		writeInt(buf, int(c))
	default:
		return // not a colour this emulator can name
	}
}

// isBlank reports whether a cell would render identically to an erased cell,
// so a run of them at end-of-line can be replaced by a single EL.
func isBlank(g vt10x.Glyph) bool {
	if g.Char != ' ' && g.Char != 0 {
		return false
	}
	if g.Mode&(vtAttrReverse|vtAttrUnderline) != 0 {
		return false
	}
	return g.BG == vt10x.DefaultBG
}

// RenderScreenANSI renders the whole virtual screen as an absolute repaint.
// rows beyond maxRows are skipped so the caller can reserve the status bar.
func RenderScreenANSI(v vt10x.View, maxRows int) []byte {
	v.Lock()
	defer v.Unlock()

	cols, rows := v.Size()
	if cols <= 0 || rows <= 0 {
		return nil
	}
	if maxRows > 0 && rows > maxRows {
		rows = maxRows
	}

	var buf bytes.Buffer
	buf.Grow(cols*rows + 512)
	buf.WriteString(CursorHide)

	var cur cellStyle
	first := true
	row := make([]vt10x.Glyph, 0, cols)
	for y := 0; y < rows; y++ {
		row = v.Row(y, row[:0])
		if len(row) > cols {
			row = row[:cols]
		}
		// Trailing blanks are the bulk of a typical TUI row; erase instead of
		// painting them.
		last := len(row) - 1
		for last >= 0 && isBlank(row[last]) {
			last--
		}

		// EL erases using the *current* SGR background, so reset first or the
		// previous row's colour paints this row's trailing region. The reset
		// also invalidates the running style, hence first = true.
		buf.WriteString(Reset)
		buf.WriteString("\x1b[")
		writeInt(&buf, y+1)
		buf.WriteString(";1H\x1b[K")
		first = true
		if last < 0 {
			continue
		}
		for x := 0; x <= last; x++ {
			g := row[x]
			st := styleOf(g)
			writeSGR(&buf, cur, st, first)
			cur, first = st, false
			ch := g.Char
			if ch == 0 {
				ch = ' '
			}
			buf.WriteRune(ch)
		}
	}

	buf.WriteString(Reset)
	return buf.Bytes()
}
