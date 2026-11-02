package vt10x

import (
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

func isControlCode(c rune) bool {
	return c < 0x20 || c == 0177
}

func (t *State) parse(c rune) {
	if t.DebugLogger != nil {
		t.logf("%q", string(c))
	}
	if isControlCode(c) {
		if t.handleControlCodes(c) || t.cur.Attr.Mode&attrGfx == 0 {
			return
		}
	}
	// TODO: update selection; see st.c:2450
	t.printRune(c)
}

// printRune puts one rune on the screen. Most runes are a whole one-column
// character (printable ASCII, box drawing, Latin, CJK) and go straight to a new
// cell. The rest -- combining marks, joiners, variation selectors, emoji,
// regional indicators -- may belong to the cluster in the previous cell, which
// only the grapheme segmenter can say, so they take the slower path.
func (t *State) printRune(c rune) {
	if t.prepend {
		// The previous cluster ends in a Prepend character, which absorbs
		// whatever comes next, ASCII included.
		t.printClusterRune(c)
		return
	}
	w := 1
	if c > 0x7F {
		if w = fastWidth(c); w == 0 {
			t.printClusterRune(c)
			return
		}
	}
	t.putCell(c, "", w)
}

// printClusterRune handles a rune that may extend the newest cluster. If it
// does, it is appended to that cell (widening it when the cluster grows to two
// columns); otherwise it starts a new cell of the width the segmenter reports,
// or is dropped when it has no width and nothing to attach to.
func (t *State) printClusterRune(c rune) {
	if t.joinOK {
		cell := &t.lines[t.lastY][t.lastX]
		buf := utf8.AppendRune(t.clusterBuf[:0], cell.Char)
		buf = append(buf, cell.Extra...)
		buf = utf8.AppendRune(buf, c)
		t.clusterBuf = buf
		if first, _, w, _ := uniseg.FirstGraphemeCluster(buf, -1); len(first) == len(buf) {
			t.joinRune(cell, c, w)
			return
		}
	}
	buf := utf8.AppendRune(t.clusterBuf[:0], c)
	t.clusterBuf = buf
	_, _, w, _ := uniseg.FirstGraphemeCluster(buf, -1)
	if w == 0 && c >= 0x1F3FB && c <= 0x1F3FF {
		w = 2 // a skin-tone modifier with no base is drawn as its own emoji
	}
	if w == 0 {
		return // a lone mark or joiner: nothing to attach it to
	}
	if w > 2 {
		w = 2
	}
	t.putCell(c, "", w)
	t.prepend = t.endsInPrepend(c)
}

// endsInPrepend reports whether a cluster whose last rune is r swallows the
// next rune. Prepend characters start at U+0600, which keeps the common
// case off the segmenter.
func (t *State) endsInPrepend(r rune) bool {
	if r < 0x600 {
		return false
	}
	buf := utf8.AppendRune(t.clusterBuf[:0], r)
	buf = append(buf, 'a')
	t.clusterBuf = buf
	first, _, _, _ := uniseg.FirstGraphemeCluster(buf, -1)
	return len(first) == len(buf)
}

// joinRune appends c to the cluster in cell and applies the cluster's new
// width. A cluster only ever grows: a mark that would narrow it (VS15) is kept
// in the text and the cell keeps its columns.
func (t *State) joinRune(cell *Glyph, c rune, width int) {
	cell.Extra += string(c)
	t.changed |= ChangedScreen
	t.dirty[t.lastY] = true
	t.prepend = t.endsInPrepend(c)
	if width < 2 || cell.Mode&attrWide != 0 || t.lastX+1 >= t.cols {
		return
	}
	// One column grew to two: a regional-indicator pair, or VS16 on a text
	// symbol. Take the next cell, which is where the cursor is, if there is one.
	y, x := t.lastY, t.lastX
	line := t.lines[y]
	if line[x+1].Mode&(attrWide|attrWideCont) != 0 {
		t.blankWidePair(y, x+1)
	}
	cell.Mode |= attrWide
	cont := *cell
	cont.Char, cont.Extra = 0, ""
	cont.Mode = cont.Mode&^(attrWide|attrWrap) | attrWideCont
	line[x+1] = cont
	if x+2 < t.cols {
		t.moveTo(x+2, y)
	} else {
		t.cur.X = t.cols - 1
		if t.mode&ModeWrap != 0 {
			t.cur.State |= cursorWrapNext
		}
	}
}

// putCell writes one cluster of the given width (1 or 2) at the cursor and
// advances it. A wide cluster that does not fit in the last column wraps to the
// next line first, or, with autowrap off, is dropped and leaves the column
// blank, as xterm does.
func (t *State) putCell(c rune, extra string, w int) {
	if t.mode&ModeWrap != 0 && t.cur.State&cursorWrapNext != 0 {
		t.lines[t.cur.Y][t.cur.X].Mode |= attrWrap
		t.newline(true)
	}
	if w == 2 && t.cur.X == t.cols-1 {
		if t.cols < 2 {
			w = 1
		} else {
			t.blankCell(t.cur.Y, t.cur.X)
			if t.mode&ModeWrap == 0 {
				t.joinOK, t.prepend = false, false
				return
			}
			t.lines[t.cur.Y][t.cur.X].Mode |= attrWrap
			t.newline(true)
		}
	}

	if t.mode&ModeInsert != 0 && t.cur.X+1 < t.cols {
		// TODO: move shiz, look at st.c:2458
		if t.DebugLogger != nil {
			t.logln("insert mode not implemented")
		}
	}

	x, y := t.cur.X, t.cur.Y
	if w == 2 {
		if t.lines[y][x+1].Mode&(attrWide|attrWideCont) != 0 {
			t.blankWidePair(y, x+1)
		}
		t.setChar(c, &t.cur.Attr, x, y)
		head := &t.lines[y][x]
		head.Mode |= attrWide
		cont := *head
		cont.Char = 0
		cont.Mode = cont.Mode&^attrWide | attrWideCont
		t.lines[y][x+1] = cont
	} else {
		t.setChar(c, &t.cur.Attr, x, y)
	}
	t.joinOK, t.lastX, t.lastY, t.prepend = true, x, y, false

	if x+w < t.cols {
		// The row is already valid, so skip moveTo's clamping; setChar marked
		// the screen changed.
		t.cur.X = x + w
		t.cur.State &^= cursorWrapNext
	} else {
		// At the right margin. With autowrap on the cursor waits there and the
		// next character wraps; with it off there is nothing to wait for, so
		// turning autowrap on later does not wrap a character written before
		// (xterm and tmux agree).
		t.cur.X = t.cols - 1
		if t.mode&ModeWrap != 0 {
			t.cur.State |= cursorWrapNext
		} else {
			t.cur.State &^= cursorWrapNext // left pending before autowrap was turned off
		}
	}
}

func (t *State) parseEsc(c rune) {
	if t.handleControlCodes(c) {
		return
	}
	next := psText
	if t.DebugLogger != nil {
		t.logf("%q", string(c))
	}
	switch c {
	case '[':
		next = psCSI
	case '#':
		next = psEscTest
	case 'P', // DCS - Device Control String
		'_', // APC - Application Program Command
		'^', // PM - Privacy Message
		']', // OSC - Operating System Command
		'k': // old title set compatibility
		t.str.reset()
		t.str.typ = c
		next = psStr
	case '(': // set primary charset G0
		next = psAltCharset
	case ')', // set secondary charset G1 (ignored)
		'*', // set tertiary charset G2 (ignored)
		'+': // set quaternary charset G3 (ignored)
	case 'D': // IND - linefeed
		if t.cur.Y == t.bottom {
			t.scrollUp(t.top, 1)
		} else {
			t.moveTo(t.cur.X, t.cur.Y+1)
		}
	case 'E': // NEL - next line
		t.newline(true)
	case 'H': // HTS - horizontal tab stop
		t.tabs[t.cur.X] = true
	case 'M': // RI - reverse index
		if t.cur.Y == t.top {
			t.scrollDown(t.top, 1)
		} else {
			t.moveTo(t.cur.X, t.cur.Y-1)
		}
	case 'Z': // DECID - identify terminal
		// TODO: write to our writer our id
	case 'c': // RIS - reset to initial state
		t.reset()
	case '=': // DECPAM - application keypad
		t.mode |= ModeAppKeypad
	case '>': // DECPNM - normal keypad
		t.mode &^= ModeAppKeypad
	case '7': // DECSC - save cursor
		t.saveCursor()
	case '8': // DECRC - restore cursor
		t.restoreCursor()
	case '\\': // ST - stop
	default:
		if t.DebugLogger != nil {
			t.logf("unknown ESC sequence '%c'\n", c)
		}
	}
	t.state = next
}

func (t *State) parseEscCSI(c rune) {
	if t.handleControlCodes(c) {
		return
	}
	if t.DebugLogger != nil {
		t.logf("%q", string(c))
	}
	if c > 0x7F {
		// Not a CSI byte. Truncating it to a byte would make U+0140 look like
		// the final byte '@'; abandon the sequence and print the character.
		t.state = psText
		t.parse(c)
		return
	}
	if t.csi.put(byte(c)) {
		t.state = psText
		t.handleCSI()
	}
}

func (t *State) parseEscStr(c rune) {
	if t.DebugLogger != nil {
		t.logf("%q", string(c))
	}
	switch c {
	case '\033':
		t.state = psStrEnd
	case '\a': // backwards compatiblity to xterm
		t.state = psText
		t.handleSTR()
	default:
		t.str.put(c)
	}
}

func (t *State) parseEscStrEnd(c rune) {
	if t.handleControlCodes(c) {
		return
	}
	if t.DebugLogger != nil {
		t.logf("%q", string(c))
	}
	t.state = psText
	if c == '\\' {
		t.handleSTR()
	}
}

func (t *State) parseEscAltCharset(c rune) {
	if t.handleControlCodes(c) {
		return
	}
	if t.DebugLogger != nil {
		t.logf("%q", string(c))
	}
	switch c {
	case '0': // line drawing set
		t.cur.Attr.Mode |= attrGfx
	case 'B': // USASCII
		t.cur.Attr.Mode &^= attrGfx
	case 'A', // UK (ignored)
		'<', // multinational (ignored)
		'5', // Finnish (ignored)
		'C', // Finnish (ignored)
		'K': // German (ignored)
	default:
		if t.DebugLogger != nil {
			t.logf("unknown alt. charset '%c'\n", c)
		}
	}
	t.state = psText
}

func (t *State) parseEscTest(c rune) {
	if t.handleControlCodes(c) {
		return
	}
	// DEC screen alignment test
	if c == '8' {
		for y := 0; y < t.rows; y++ {
			for x := 0; x < t.cols; x++ {
				t.setChar('E', &t.cur.Attr, x, y)
			}
		}
	}
	t.state = psText
}

func (t *State) handleControlCodes(c rune) bool {
	if !isControlCode(c) {
		return false
	}
	switch c {
	// HT
	case '\t':
		t.putTab(true)
	// BS
	case '\b':
		t.moveTo(t.cur.X-1, t.cur.Y)
	// CR
	case '\r':
		t.moveTo(0, t.cur.Y)
	// LF, VT, LF
	case '\f', '\v', '\n':
		// go to first col if mode is set
		t.newline(t.mode&ModeCRLF != 0)
	// BEL
	case '\a':
		// TODO: emit sound
		// TODO: window alert if not focused
	// ESC
	case 033:
		t.csi.reset()
		t.state = psEsc
	// SO, SI
	case 016, 017:
		// different charsets not supported. apps should use the correct
		// alt charset escapes, probably for line drawing
	// SUB, CAN
	case 032, 030:
		t.csi.reset()
	// ignore ENQ, NUL, XON, XOFF, DEL
	case 005, 000, 021, 023, 0177:
	default:
		return false
	}
	return true
}
