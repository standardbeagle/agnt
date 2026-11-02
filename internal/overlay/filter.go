package overlay

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/standardbeagle/vt10x"
)

// FilterConfig configures the PTY output filter behavior.
type FilterConfig struct {
	// ProtectBottomRows is the number of rows to protect at the bottom.
	// Scroll region will be set to exclude these rows.
	ProtectBottomRows int

	// RedrawInterval is how often to check if indicator needs redrawing.
	// Set to 0 to disable periodic redraw.
	RedrawInterval time.Duration

	// OnRedraw is called when the indicator should be redrawn.
	OnRedraw func()
}

// ProtectedWriter filters PTY output to protect the status bar area.
// It intercepts ANSI sequences that could affect the protected region.
type ProtectedWriter struct {
	out    io.Writer
	mu     sync.Mutex
	config FilterConfig

	height int

	// Parser state
	state    parseState
	params   []int  // CSI parameters
	paramBuf []byte // Current parameter being parsed
	intermed []byte // Intermediate bytes
	oscBuf   []byte // OSC string accumulator
	escBuf   []byte // Buffer for incomplete escape sequences

	// filtered collects one Write's output; reused so the hot path does not
	// allocate a buffer per PTY read.
	filtered bytes.Buffer

	// hostModel is a terminal emulator fed exactly the bytes this writer
	// sends to the host terminal: the filtered child output and the scroll
	// region / cursor sequences the writer emits on its own. Its cursor is
	// therefore the host terminal's cursor, which every overlay draw restores
	// with an absolute CUP (cursorpark.go). The child redraws with relative
	// moves from wherever that CUP put it, so the model must be exact — wide
	// cells, grapheme clusters, pending wrap, margins, save slots — and a real
	// emulator is. See cursortrack_test.go.
	//
	// Guarded by mu: every Write, Resize and read of it happens with mu held.
	// The emulator's replies to DSR/DA queries go to io.Discard; the host
	// terminal answers the child itself.
	hostModel vt10x.Terminal

	// Published copy of the model's cursor for lock-free readers
	// (ChildCursor). Updated only after the bytes that moved the cursor have
	// been written, so a reader never sees a position the host terminal has
	// not reached yet.
	cursorRow atomic.Int32
	cursorCol atomic.Int32

	// Alt screen, published from the model with the cursor.
	inAltScreen atomic.Bool

	// The child's mouse tracking mode and encoding (vt10x mouse mode bits),
	// published from the model with the cursor, so the panel view can put
	// them back after borrowing the mouse.
	mouseModes atomic.Uint32

	// DECTCEM (mode 25), published from the model with the cursor. The
	// overlay reads this so a status-bar draw restores the child's own
	// cursor visibility instead of forcing the cursor visible on top of a
	// TUI that deliberately hid it.
	cursorVisible atomic.Bool

	// A UTF-8 sequence cut off by the end of a PTY read is held here until
	// its last byte arrives, so a character never reaches the terminal in
	// halves with an overlay draw between them (see HoldChildCursor).
	utf8Buf  [4]byte
	utf8Len  int // bytes accumulated so far
	utf8Need int // total bytes needed for current char

	// Redraw state
	redrawNeeded atomic.Bool
	stopRedraw   chan struct{}
	redrawWg     sync.WaitGroup
}

type parseState int

const (
	stateGround      parseState = iota
	stateEscape                 // Saw ESC
	stateCSI                    // Saw ESC [
	stateCSIParam               // In CSI parameters
	stateCSIIntermed            // In CSI intermediate bytes
	stateOSC                    // Saw ESC ]
	stateOSCString              // In OSC string
	stateDCS                    // Device Control String
	stateSOS                    // Start of String
	statePM                     // Privacy Message
	stateAPC                    // Application Program Command
)

// NewProtectedWriter creates a new PTY output filter.
func NewProtectedWriter(out io.Writer, width, height int, config FilterConfig) *ProtectedWriter {
	pw := &ProtectedWriter{
		out:        out,
		config:     config,
		height:     height,
		state:      stateGround,
		params:     make([]int, 0, 16),
		paramBuf:   make([]byte, 0, 16),
		intermed:   make([]byte, 0, 4),
		oscBuf:     make([]byte, 0, 256),
		escBuf:     make([]byte, 0, 32),
		hostModel:  vt10x.New(vt10x.WithWriter(io.Discard), vt10x.WithSize(max(width, 1), max(height, 1))),
		stopRedraw: make(chan struct{}),
	}
	pw.publishCursor()

	// Start periodic redraw if configured
	if config.RedrawInterval > 0 && config.OnRedraw != nil {
		pw.redrawWg.Add(1)
		go pw.redrawLoop()
	}

	return pw
}

// SetSize updates the terminal dimensions.
func (pw *ProtectedWriter) SetSize(width, height int) {
	pw.mu.Lock()
	defer pw.mu.Unlock()
	pw.height = height
	pw.hostModel.Resize(max(width, 1), max(height, 1))

	// After resize, enforce scroll region
	pw.enforceScrollRegion()
	pw.publishCursor()
}

// Stop stops the periodic redraw goroutine.
func (pw *ProtectedWriter) Stop() {
	close(pw.stopRedraw)
	pw.redrawWg.Wait()
}

// RequestRedraw marks that a redraw is needed.
func (pw *ProtectedWriter) RequestRedraw() {
	pw.redrawNeeded.Store(true)
}

// redrawLoop periodically redraws the indicator if needed.
func (pw *ProtectedWriter) redrawLoop() {
	defer pw.redrawWg.Done()

	ticker := time.NewTicker(pw.config.RedrawInterval)
	defer ticker.Stop()

	for {
		select {
		case <-pw.stopRedraw:
			return
		case <-ticker.C:
			if pw.redrawNeeded.Swap(false) && pw.config.OnRedraw != nil {
				pw.config.OnRedraw()
			}
		}
	}
}

// Write filters the PTY output and writes to the underlying writer.
func (pw *ProtectedWriter) Write(p []byte) (n int, err error) {
	pw.mu.Lock()
	defer pw.mu.Unlock()

	out := &pw.filtered
	out.Reset()
	for i := 0; i < len(p); {
		if pw.state != stateGround || pw.utf8Len > 0 || p[i] == 0x1b {
			pw.processStateByte(out, p[i])
			i++
			continue
		}
		// Ground state, no rune pending: text up to the next ESC. If it is
		// valid UTF-8, every byte of it would pass through the per-byte path
		// unchanged, so copy it whole. Otherwise (a malformed byte, or a rune
		// split across reads) take the per-byte path for this run.
		end := len(p)
		if j := bytes.IndexByte(p[i:], 0x1b); j >= 0 {
			end = i + j
		}
		if utf8.Valid(p[i:end]) {
			out.Write(p[i:end])
			i = end
			continue
		}
		for ; i < end; i++ {
			pw.processStateByte(out, p[i])
		}
	}

	if out.Len() > 0 {
		err = pw.sendToHost(out.Bytes())
	}
	pw.publishCursor()

	return len(p), err
}

// sendToHost writes b to the host terminal and feeds the same bytes to the
// host model. Caller holds mu.
func (pw *ProtectedWriter) sendToHost(b []byte) error {
	_, err := pw.out.Write(b)
	_, _ = pw.hostModel.Write(b) // never fails: the emulator only parses
	return err
}

// processStateByte dispatches byte processing based on current parse state.
func (pw *ProtectedWriter) processStateByte(out *bytes.Buffer, b byte) {
	switch pw.state {
	case stateGround:
		pw.handleStateGround(out, b)
	case stateEscape:
		pw.handleStateEscape(out, b)
	case stateCSI:
		pw.handleStateCSI(out, b)
	case stateCSIParam:
		pw.handleStateCSIParam(out, b)
	case stateCSIIntermed:
		pw.handleStateCSIIntermed(out, b)
	case stateOSC:
		pw.handleStateOSC(out, b)
	case stateOSCString:
		pw.handleStateOSCString(out, b)
	case stateDCS, stateSOS, statePM, stateAPC:
		pw.handleStateStringTerminated(out, b)
	}
}

// handleStateGround processes bytes in ground state.
func (pw *ProtectedWriter) handleStateGround(out *bytes.Buffer, b byte) {
	if pw.utf8Len > 0 && (b < 0x80 || b >= 0xC0) {
		pw.flushPartialUTF8(out) // sequence cut short by a non-continuation byte
	}
	switch {
	case b == 0x1b: // ESC
		pw.state = stateEscape
		pw.escBuf = pw.escBuf[:0]
		pw.escBuf = append(pw.escBuf, b)

	case b >= 0xC0: // UTF-8 lead byte: hold until the character is complete
		pw.utf8Buf[0] = b
		pw.utf8Len = 1
		switch {
		case b < 0xE0:
			pw.utf8Need = 2
		case b < 0xF0:
			pw.utf8Need = 3
		default:
			pw.utf8Need = 4
		}

	case b >= 0x80 && pw.utf8Len > 0: // UTF-8 continuation byte
		pw.utf8Buf[pw.utf8Len] = b
		pw.utf8Len++
		if pw.utf8Len >= pw.utf8Need {
			if seq := pw.utf8Buf[:pw.utf8Len]; utf8.Valid(seq) {
				out.Write(seq)
			} else {
				out.WriteString(replacementChar)
			}
			pw.utf8Len = 0
			pw.utf8Need = 0
		}

	case b >= 0x80: // continuation byte with no lead
		out.WriteString(replacementChar)

	default: // ASCII text and C0 controls pass through
		out.WriteByte(b)
	}
}

// handleStateEscape processes bytes after ESC.
func (pw *ProtectedWriter) handleStateEscape(out *bytes.Buffer, b byte) {
	pw.escBuf = append(pw.escBuf, b)
	switch b {
	case '[': // CSI
		pw.state = stateCSI
		pw.params = pw.params[:0]
		pw.paramBuf = pw.paramBuf[:0]
		pw.intermed = pw.intermed[:0]
	case ']': // OSC
		pw.state = stateOSC
		pw.oscBuf = pw.oscBuf[:0]
	case 'P': // DCS
		pw.state = stateDCS
	case 'X': // SOS
		pw.state = stateSOS
	case '^': // PM
		pw.state = statePM
	case '_': // APC
		pw.state = stateAPC
	case 'c': // RIS - full reset: the host drops our scroll region
		out.Write(pw.escBuf)
		pw.redrawNeeded.Store(true)
		pw.state = stateGround
	default:
		pw.handleEscapeDefault(out, b)
	}
}

// handleEscapeDefault processes unknown escape sequences.
func (pw *ProtectedWriter) handleEscapeDefault(out *bytes.Buffer, b byte) {
	if b >= 0x20 && b <= 0x2f {
		// Intermediate byte, stay in escape
	} else if b >= 0x30 && b <= 0x7e {
		// Final byte
		out.Write(pw.escBuf)
		pw.state = stateGround
	} else {
		// Invalid, output what we have
		out.Write(pw.escBuf)
		pw.state = stateGround
	}
}

// handleStateCSI processes bytes in CSI state.
func (pw *ProtectedWriter) handleStateCSI(out *bytes.Buffer, b byte) {
	pw.escBuf = append(pw.escBuf, b)
	switch {
	case b >= '0' && b <= '9':
		pw.paramBuf = append(pw.paramBuf, b)
		pw.state = stateCSIParam
	case b == ';':
		pw.params = append(pw.params, 0) // Default parameter
		pw.state = stateCSIParam
	case b == '?' || b == '>' || b == '<' || b == '!' || b == '=':
		// Private mode introducer. '<' included: CSI < u is the kitty
		// keyboard pop, and dropping out of the parser here printed its
		// final 'u' as a glyph in the cursor model.
		pw.intermed = append(pw.intermed, b)
	case b >= 0x20 && b <= 0x2f:
		pw.intermed = append(pw.intermed, b)
		pw.state = stateCSIIntermed
	case b >= 0x40 && b <= 0x7e:
		// Final byte with no parameters
		pw.handleCSI(out, b)
		pw.state = stateGround
	default:
		// Invalid
		out.Write(pw.escBuf)
		pw.state = stateGround
	}
}

// handleStateCSIParam processes bytes in CSI parameter state.
func (pw *ProtectedWriter) handleStateCSIParam(out *bytes.Buffer, b byte) {
	pw.escBuf = append(pw.escBuf, b)
	switch {
	case b >= '0' && b <= '9':
		pw.paramBuf = append(pw.paramBuf, b)
	case b == ';':
		pw.finishParam()
		pw.paramBuf = pw.paramBuf[:0]
	case b == ':':
		// Sub-parameter separator (used in SGR)
		pw.paramBuf = append(pw.paramBuf, b)
	case b >= 0x20 && b <= 0x2f:
		pw.finishParam()
		pw.intermed = append(pw.intermed, b)
		pw.state = stateCSIIntermed
	case b >= 0x40 && b <= 0x7e:
		pw.finishParam()
		pw.handleCSI(out, b)
		pw.state = stateGround
	default:
		// Invalid
		out.Write(pw.escBuf)
		pw.state = stateGround
	}
}

// handleStateCSIIntermed processes bytes in CSI intermediate state.
func (pw *ProtectedWriter) handleStateCSIIntermed(out *bytes.Buffer, b byte) {
	pw.escBuf = append(pw.escBuf, b)
	switch {
	case b >= 0x20 && b <= 0x2f:
		pw.intermed = append(pw.intermed, b)
	case b >= 0x40 && b <= 0x7e:
		pw.handleCSI(out, b)
		pw.state = stateGround
	default:
		// Invalid
		out.Write(pw.escBuf)
		pw.state = stateGround
	}
}

// handleStateOSC processes bytes in OSC state.
func (pw *ProtectedWriter) handleStateOSC(out *bytes.Buffer, b byte) {
	pw.escBuf = append(pw.escBuf, b)
	switch b {
	case 0x07: // BEL terminates OSC
		out.Write(pw.escBuf)
		pw.state = stateGround
	case 0x1b:
		pw.state = stateOSCString // Check for ST (ESC \)
	default:
		pw.oscBuf = append(pw.oscBuf, b)
	}
}

// handleStateOSCString processes bytes waiting for OSC string terminator.
func (pw *ProtectedWriter) handleStateOSCString(out *bytes.Buffer, b byte) {
	pw.escBuf = append(pw.escBuf, b)
	if b == '\\' { // ST (String Terminator)
		out.Write(pw.escBuf)
		pw.state = stateGround
		return
	}
	pw.endStringAtESC(out, b)
}

// endStringAtESC handles the byte after an ESC inside an OSC, DCS, SOS, PM or
// APC string when that byte is not the backslash of ST. A terminal ends the
// string at the ESC and reads the ESC as the start of the next sequence.
// Staying in the string instead held every later byte until some BEL or ST
// turned up: one stray "ESC ]" blanked the child's output, and grew escBuf,
// for as long as none did. escBuf ends with ESC b on entry.
func (pw *ProtectedWriter) endStringAtESC(out *bytes.Buffer, b byte) {
	out.Write(pw.escBuf[:len(pw.escBuf)-2])
	pw.escBuf = append(pw.escBuf[:0], 0x1b)
	pw.state = stateEscape
	pw.handleStateEscape(out, b)
}

// handleStateStringTerminated processes bytes in DCS, SOS, PM, APC states.
func (pw *ProtectedWriter) handleStateStringTerminated(out *bytes.Buffer, b byte) {
	pw.escBuf = append(pw.escBuf, b)
	// escBuf starts with the two-byte introducer, so an ESC inside the
	// payload is at index 2 or later.
	afterESC := len(pw.escBuf) > 3 && pw.escBuf[len(pw.escBuf)-2] == 0x1b
	switch {
	case afterESC && b == '\\': // ST
		out.Write(pw.escBuf)
		pw.state = stateGround
	case afterESC:
		pw.endStringAtESC(out, b)
	case b == 0x1b:
		// Might be start of ST
	case b == 0x9c: // Single-byte ST
		out.Write(pw.escBuf)
		pw.state = stateGround
	}
}

// finishParam adds the current parameter buffer to params.
func (pw *ProtectedWriter) finishParam() {
	if len(pw.paramBuf) > 0 {
		// Handle sub-parameters by just taking the first value
		paramStr := string(pw.paramBuf)
		if idx := bytes.IndexByte(pw.paramBuf, ':'); idx >= 0 {
			paramStr = string(pw.paramBuf[:idx])
		}
		if n, err := strconv.Atoi(paramStr); err == nil {
			pw.params = append(pw.params, n)
		} else {
			pw.params = append(pw.params, 0)
		}
	} else {
		pw.params = append(pw.params, 0)
	}
}

// handleCSI processes a complete CSI sequence. Only sequences the filter
// rewrites or reacts to are handled here; cursor motion passes through and is
// modelled by hostModel.
func (pw *ProtectedWriter) handleCSI(out *bytes.Buffer, final byte) {
	isPrivate := len(pw.intermed) > 0 && pw.intermed[0] == '?'

	switch final {
	case 'r': // DECSTBM - Set Top and Bottom Margins (scroll region)
		if !isPrivate {
			// Intercept scroll region reset and enforce our protected area
			top := 1
			bottom := pw.height - pw.config.ProtectBottomRows
			if len(pw.params) >= 1 && pw.params[0] > 0 {
				top = pw.params[0]
			}
			if len(pw.params) >= 2 && pw.params[1] > 0 {
				bottom = min(pw.params[1], pw.height-pw.config.ProtectBottomRows)
			}
			fmt.Fprintf(out, "\x1b[%d;%dr", top, bottom)
			return
		}

	case 'J': // ED - Erase in Display
		mode := 0
		if len(pw.params) >= 1 {
			mode = pw.params[0]
		}
		if mode == 2 || mode == 3 { // Clear entire screen
			// Allow but request redraw
			pw.redrawNeeded.Store(true)
		}
		// Pass through

	case 'c': // DA - Device Attributes response
		if isPrivate {
			// Suppress DA1 responses (CSI ? Ps c) — these are terminal-to-app
			// messages that should never appear in visible output. They can leak
			// when the PTY echoes input before the child disables echo mode.
			return
		}
		// Pass through (non-private CSI c is rarely used)
	}

	// Default: pass through unmodified
	out.Write(pw.escBuf)
}

// replacementChar stands in for a malformed UTF-8 sequence. Terminals draw
// one for such bytes anyway, but not all the same way, and the host model
// skips them; sending U+FFFD makes the host and its model agree.
const replacementChar = "\uFFFD"

// publishCursor exposes the host model's cursor, visibility and screen to
// lock-free readers. Caller holds mu.
func (pw *ProtectedWriter) publishCursor() {
	c := pw.hostModel.Cursor()
	mode := pw.hostModel.Mode()
	pw.cursorRow.Store(int32(c.Y + 1))
	pw.cursorCol.Store(int32(c.X + 1))
	pw.cursorVisible.Store(mode&vt10x.ModeHide == 0)
	pw.inAltScreen.Store(mode&vt10x.ModeAltScreen != 0)
	pw.mouseModes.Store(uint32(mode & (vt10x.ModeMouseMask | vt10x.ModeMouseSgr)))
}

// ChildMouseRestore returns the bytes that put the host terminal back in the
// child's mouse modes after the panel view borrowed the mouse.
func (pw *ProtectedWriter) ChildMouseRestore() string {
	return mouseRestoreSequence(vt10x.ModeFlag(pw.mouseModes.Load()))
}

// enforceScrollRegion writes the scroll region sequence to protect bottom rows.
// DECSTBM homes the cursor, so the child's position has to be re-established
// afterwards. We do that with an absolute CUP from the host model rather than
// DECSC/DECRC: xterm backs DECSC and CSI s with the same single save slot, and
// the child TUI uses it for its own repaints — clobbering it makes the child
// restore to the wrong origin (see cursorpark.go).
func (pw *ProtectedWriter) enforceScrollRegion() {
	bottom := max(pw.height-pw.config.ProtectBottomRows, 1)
	c := pw.hostModel.Cursor()
	_ = pw.sendToHost(fmt.Appendf(nil, "\x1b[1;%dr\x1b[%d;%dH", bottom, c.Y+1, c.X+1))
}

// EnforceScrollRegion can be called externally to re-apply scroll region protection.
func (pw *ProtectedWriter) EnforceScrollRegion() {
	pw.mu.Lock()
	defer pw.mu.Unlock()
	pw.enforceScrollRegion()
	pw.publishCursor()
}

// flushPartialUTF8 replaces a UTF-8 sequence cut short by a non-continuation
// byte. The terminal shows it as one replacement glyph.
func (pw *ProtectedWriter) flushPartialUTF8(out *bytes.Buffer) {
	out.WriteString(replacementChar)
	pw.utf8Len = 0
	pw.utf8Need = 0
}

// ChildCursor reports the child's cursor position (1-indexed) and
// whether the child currently has the cursor visible. The overlay uses it to
// restore the child's cursor after drawing, without touching the terminal's
// single SCP/RCP save slot that the child TUI also uses.
func (pw *ProtectedWriter) ChildCursor() (row, col int, visible bool) {
	return int(pw.cursorRow.Load()), int(pw.cursorCol.Load()), pw.cursorVisible.Load()
}

// HoldChildCursor calls fn with the host model's cursor while holding the
// filter's lock. Write emits a chunk and feeds the model under that same lock,
// so while fn runs the position is exactly where the terminal's cursor is, and
// no child output can land between fn's writes. fn writes an overlay draw that
// ends by restoring that position (drawOverChild); it may write to the
// filter's output writer but must not call back into the filter (the lock is
// not reentrant).
//
// The draw itself is not fed to the host model: the model is read only for
// the cursor, and the draw ends on the cursor it started from. Its closing
// CUP is fed, because a CUP clears the terminal's pending-wrap flag and the
// model must lose it too.
//
// Known limit: that lost pending wrap means a child parked on the last column
// will overwrite that column instead of wrapping. Renderers that end
// full-width lines with CRLF (Ink, Bubble Tea) never rely on the pending wrap
// across frames.
func (pw *ProtectedWriter) HoldChildCursor(fn func(row, col int, visible bool)) {
	pw.mu.Lock()
	defer pw.mu.Unlock()
	row, col, visible := pw.ChildCursor() // published under mu: current
	fn(row, col, visible)
	_, _ = pw.hostModel.Write(fmt.Appendf(nil, CursorToFormat, row, col))
}

// InAltScreen returns whether the child process is currently in the alternate screen buffer.
func (pw *ProtectedWriter) InAltScreen() bool {
	return pw.inAltScreen.Load()
}
