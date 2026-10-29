package vt10x

import (
	"fmt"
)

// maxCSIParam caps a parsed numeric parameter, as xterm does, so a hostile or
// corrupted stream cannot overflow the arithmetic done with it.
const maxCSIParam = 65535

// CSI (Control Sequence Introducer)
// ESC+[
//
// A sequence is prefix? params? inter* final. The prefix is one of < = > ?
// (0x3C-0x3F) and is only valid first. The intermediates are 0x20-0x2F and
// must follow the parameters. The final byte is 0x40-0x7E. Two sequences that
// differ in prefix or intermediate are different sequences even when they
// share a final byte (CSI u is DECRC, CSI > 1 u pushes the kitty keyboard
// flags), so dispatch keys on all three.
type csiEscape struct {
	buf     []byte
	args    []int
	mode    byte // final byte
	prefix  byte // 0, or one of < = > ?
	inter   byte // first intermediate byte, or 0
	priv    bool // prefix == '?'
	invalid bool // malformed; the whole sequence is ignored
}

func (c *csiEscape) reset() {
	c.buf = c.buf[:0]
	c.args = c.args[:0]
	c.mode = 0
	c.prefix = 0
	c.inter = 0
	c.priv = false
	c.invalid = false
}

func (c *csiEscape) put(b byte) bool {
	c.buf = append(c.buf, b)
	final := b >= 0x40 && b <= 0x7E
	if final || len(c.buf) >= 256 {
		c.parse()
		if !final {
			// Ran out of room without a final byte: never dispatch on a
			// parameter byte that merely happened to be last.
			c.invalid = true
		}
		return true
	}
	return false
}

func (c *csiEscape) parse() {
	c.mode = c.buf[len(c.buf)-1]
	c.args = c.args[:0]
	c.prefix, c.inter, c.priv, c.invalid = 0, 0, false, false
	b := c.buf[:len(c.buf)-1]
	if len(b) == 0 {
		return
	}
	if b[0] >= '<' && b[0] <= '?' {
		c.prefix = b[0]
		c.priv = b[0] == '?'
		b = b[1:]
	}
	// Scan the arguments in place. The previous string(c.buf) + strings.Split
	// allocated one string for the buffer and one more per argument, on every
	// CSI sequence -- and a direct-colour SGR carries five. On a TUI repaint
	// that was the bulk of the parser's garbage.
	n, has, listOpen := 0, false, true
	for _, ch := range b {
		switch {
		case ch >= '0' && ch <= '9':
			if c.inter != 0 {
				c.invalid = true // parameter after an intermediate
				return
			}
			if listOpen {
				n = min(n*10+int(ch-'0'), maxCSIParam)
				has = true
			}
		case ch == ';':
			if c.inter != 0 {
				c.invalid = true
				return
			}
			if listOpen {
				c.args = append(c.args, n)
				n, has = 0, false
			}
		case ch == ':':
			// Sub-parameters are not interpreted: the list ends here, as it
			// always has, but the sequence is still well formed.
			if c.inter != 0 {
				c.invalid = true
				return
			}
			if listOpen && has {
				c.args = append(c.args, n)
			}
			listOpen = false
		case ch >= 0x20 && ch <= 0x2F:
			if c.inter == 0 {
				c.inter = ch
				if listOpen && has {
					c.args = append(c.args, n)
				}
				listOpen = false
			} else {
				c.invalid = true // more than one intermediate is not a sequence we know
				return
			}
		default:
			// A prefix byte after the parameters started, or a byte that has
			// no business inside a CSI.
			c.invalid = true
			return
		}
	}
	if listOpen && has {
		c.args = append(c.args, n)
	}
}

func (c *csiEscape) arg(i, def int) int {
	if i >= len(c.args) || i < 0 {
		return def
	}
	return c.args[i]
}

// maxarg takes the maximum of arg(i, def) and def
func (c *csiEscape) maxarg(i, def int) int {
	return max(c.arg(i, def), def)
}

func (t *State) handleCSI() {
	c := &t.csi
	switch {
	case c.invalid:
		t.logf("malformed CSI sequence %q\n", c.buf)
	case c.prefix == 0 && c.inter == 0:
		t.handleCSIPlain()
	case c.prefix == '?' && c.inter == 0:
		t.handleCSIPrivate()
	default:
		// Kitty keyboard (> < = ? u), modifyOtherKeys (> m), DA2/DA3 (> = c),
		// XTVERSION (> q), DECSCUSR (SP q), DECSTR (! p), DECRQM ($ p) and
		// friends. None of them changes the screen this emulator models, and
		// none of them is the unprefixed sequence with the same final byte.
		t.logf("ignored CSI sequence %q\n", c.buf)
	}
}

// handleCSIPrivate handles CSI ? Ps final.
func (t *State) handleCSIPrivate() {
	c := &t.csi
	switch c.mode {
	case 'h': // DECSET
		t.setMode(true, true, c.args)
	case 'l': // DECRST
		t.setMode(true, false, c.args)
	case 'J', 'K': // DECSED, DECSEL: selective erase. Nothing is ever
		// protected here (DECSCA is not modelled), so they erase like ED/EL.
		t.handleCSIPlain()
	default:
		// ? u kitty query, ? n DEC DSR, ? $ p DECRQM (intermediate, not here)...
		t.logf("ignored private CSI sequence %q\n", c.buf)
	}
}

// handleCSIPlain handles CSI Ps final: no prefix, no intermediate.
func (t *State) handleCSIPlain() {
	c := &t.csi
	switch c.mode {
	default:
		goto unknown
	case '@': // ICH - insert <n> blank char
		t.insertBlanks(c.maxarg(0, 1))
	case 'A': // CUU - cursor <n> up
		t.moveVertical(-c.maxarg(0, 1))
	case 'B', 'e': // CUD, VPR - cursor <n> down
		t.moveVertical(c.maxarg(0, 1))
	case 'c': // DA - device attributes
		if c.arg(0, 0) == 0 {
			// TODO: write vt102 id
		}
	case 'C', 'a': // CUF, HPR - cursor <n> forward
		t.moveTo(t.cur.X+c.maxarg(0, 1), t.cur.Y)
	case 'D': // CUB - cursor <n> backward
		t.moveTo(t.cur.X-c.maxarg(0, 1), t.cur.Y)
	case 'E': // CNL - cursor <n> down and first col
		t.moveVertical(c.maxarg(0, 1))
		t.moveTo(0, t.cur.Y)
	case 'F': // CPL - cursor <n> up and first col
		t.moveVertical(-c.maxarg(0, 1))
		t.moveTo(0, t.cur.Y)
	case 'g': // TBC - tabulation clear
		switch c.arg(0, 0) {
		// clear current tab stop
		case 0:
			t.tabs[t.cur.X] = false
		// clear all tabs
		case 3:
			for i := range t.tabs {
				t.tabs[i] = false
			}
		default:
			goto unknown
		}
	case 'G', '`': // CHA, HPA - Move to <col>
		t.moveTo(c.arg(0, 1)-1, t.cur.Y)
	case 'H', 'f': // CUP, HVP - move to <row> <col>
		t.moveAbsTo(c.arg(1, 1)-1, c.arg(0, 1)-1)
	case 'I': // CHT - cursor forward tabulation <n> tab stops
		n := c.maxarg(0, 1)
		for i := 0; i < n; i++ {
			t.putTab(true)
		}
	case 'J': // ED - clear screen
		// TODO: sel.ob.x = -1
		switch c.arg(0, 0) {
		case 0: // below
			t.clear(t.cur.X, t.cur.Y, t.cols-1, t.cur.Y)
			if t.cur.Y < t.rows-1 {
				t.clear(0, t.cur.Y+1, t.cols-1, t.rows-1)
			}
		case 1: // above
			if t.cur.Y > 0 {
				t.clear(0, 0, t.cols-1, t.cur.Y-1)
			}
			t.clear(0, t.cur.Y, t.cur.X, t.cur.Y)
		case 2: // all
			t.clear(0, 0, t.cols-1, t.rows-1)
		default:
			goto unknown
		}
	case 'K': // EL - clear line
		switch c.arg(0, 0) {
		case 0: // right
			t.clear(t.cur.X, t.cur.Y, t.cols-1, t.cur.Y)
		case 1: // left
			t.clear(0, t.cur.Y, t.cur.X, t.cur.Y)
		case 2: // all
			t.clear(0, t.cur.Y, t.cols-1, t.cur.Y)
		}
	case 'S': // SU - scroll <n> lines up
		t.scrollUp(t.top, c.maxarg(0, 1))
	case 'T': // SD - scroll <n> lines down
		t.scrollDown(t.top, c.maxarg(0, 1))
	case 'L': // IL - insert <n> blank lines
		t.insertBlankLines(c.maxarg(0, 1))
	case 'l': // RM - reset mode
		t.setMode(false, false, c.args)
	case 'M': // DL - delete <n> lines
		t.deleteLines(c.maxarg(0, 1))
	case 'X': // ECH - erase <n> chars
		t.clear(t.cur.X, t.cur.Y, t.cur.X+c.maxarg(0, 1)-1, t.cur.Y)
	case 'P': // DCH - delete <n> chars
		t.deleteChars(c.maxarg(0, 1))
	case 'Z': // CBT - cursor backward tabulation <n> tab stops
		n := c.maxarg(0, 1)
		for i := 0; i < n; i++ {
			t.putTab(false)
		}
	case 'd': // VPA - move to <row>
		t.moveAbsTo(t.cur.X, c.arg(0, 1)-1)
	case 'h': // SM - set terminal mode
		t.setMode(false, true, c.args)
	case 'm': // SGR - terminal attribute (color)
		t.setAttr(c.args)
	case 'n':
		switch c.arg(0, 0) {
		case 5: // DSR - device status report
			t.w.Write([]byte("\033[0n"))
		case 6: // CPR - cursor position report
			t.w.Write([]byte(fmt.Sprintf("\033[%d;%dR", t.cur.Y+1, t.cur.X+1)))
		}
	case 'r': // DECSTBM - set scrolling region
		t.setScroll(c.arg(0, 1)-1, c.arg(1, t.rows)-1)
		t.moveAbsTo(0, 0)
	case 's': // DECSC - save cursor position (ANSI.SYS)
		t.saveCursor()
	case 'u': // DECRC - restore cursor position (ANSI.SYS)
		t.restoreCursor()
	}
	return
unknown: // TODO: get rid of this goto
	if t.DebugLogger != nil {
		t.logf("unknown CSI sequence '%c'\n", c.mode)
	}
	// TODO: c.dump()
}
