package overlay

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
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

	// Terminal state tracking
	width        int
	height       int
	protectedRow int // First protected row (1-indexed)

	// Parser state
	state    parseState
	params   []int  // CSI parameters
	paramBuf []byte // Current parameter being parsed
	intermed []byte // Intermediate bytes
	oscBuf   []byte // OSC string accumulator
	escBuf   []byte // Buffer for incomplete escape sequences

	// Cursor tracking (1-indexed, 0 means unknown)
	cursorRow atomic.Int32
	cursorCol atomic.Int32

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
		out:          out,
		config:       config,
		width:        width,
		height:       height,
		protectedRow: height - config.ProtectBottomRows + 1,
		state:        stateGround,
		params:       make([]int, 0, 16),
		paramBuf:     make([]byte, 0, 16),
		intermed:     make([]byte, 0, 4),
		oscBuf:       make([]byte, 0, 256),
		escBuf:       make([]byte, 0, 32),
		stopRedraw:   make(chan struct{}),
	}

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
	pw.width = width
	pw.height = height
	pw.protectedRow = height - pw.config.ProtectBottomRows + 1

	// After resize, enforce scroll region
	pw.enforceScrollRegion()
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

	var out bytes.Buffer
	out.Grow(len(p) + 64) // Pre-allocate with some extra for modifications

	for i := 0; i < len(p); i++ {
		b := p[i]
		pw.processStateByte(&out, b)
	}

	// Write any remaining ground state data
	if out.Len() > 0 {
		_, err = pw.out.Write(out.Bytes())
	}

	return len(p), err
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
	if b == 0x1b { // ESC
		pw.state = stateEscape
		pw.escBuf = pw.escBuf[:0]
		pw.escBuf = append(pw.escBuf, b)
	} else {
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
	case '7', '8', 'M', 'D', 'E': // DECSC, DECRC, RI, IND, NEL
		out.Write(pw.escBuf)
		pw.state = stateGround
	case 'c': // RIS - full reset
		// Allow reset but re-enforce scroll region after
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
	case b == '?' || b == '>' || b == '!' || b == '=':
		// Private mode introducer
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
	} else {
		pw.oscBuf = append(pw.oscBuf, 0x1b, b)
		pw.state = stateOSC
	}
}

// handleStateStringTerminated processes bytes in DCS, SOS, PM, APC states.
func (pw *ProtectedWriter) handleStateStringTerminated(out *bytes.Buffer, b byte) {
	pw.escBuf = append(pw.escBuf, b)
	// Look for ST (ESC \) or single-byte terminator
	if b == 0x1b {
		// Might be start of ST
	} else if b == '\\' && len(pw.escBuf) > 1 && pw.escBuf[len(pw.escBuf)-2] == 0x1b {
		out.Write(pw.escBuf)
		pw.state = stateGround
	} else if b == 0x9c { // Single-byte ST
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

// handleCSI processes a complete CSI sequence.
func (pw *ProtectedWriter) handleCSI(out *bytes.Buffer, final byte) {
	// Check for private mode sequences
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
			// Write modified scroll region
			fmt.Fprintf(out, "\x1b[%d;%dr", top, bottom)
			return
		}

	case 'H', 'f': // CUP/HVP - Cursor Position
		row := 1
		col := 1
		if len(pw.params) >= 1 && pw.params[0] > 0 {
			row = pw.params[0]
		}
		if len(pw.params) >= 2 && pw.params[1] > 0 {
			col = pw.params[1]
		}
		// Clamp row to avoid protected region
		if row >= pw.protectedRow {
			row = pw.protectedRow - 1
			if row < 1 {
				row = 1
			}
			pw.redrawNeeded.Store(true) // Something tried to enter protected area
		}
		pw.cursorRow.Store(int32(row))
		pw.cursorCol.Store(int32(col))
		fmt.Fprintf(out, "\x1b[%d;%d%c", row, col, final)
		return

	case 'A': // CUU - Cursor Up
		n := 1
		if len(pw.params) >= 1 && pw.params[0] > 0 {
			n = pw.params[0]
		}
		row := int(pw.cursorRow.Load()) - n
		if row < 1 {
			row = 1
		}
		pw.cursorRow.Store(int32(row))
		// Pass through

	case 'B': // CUD - Cursor Down
		n := 1
		if len(pw.params) >= 1 && pw.params[0] > 0 {
			n = pw.params[0]
		}
		row := int(pw.cursorRow.Load()) + n
		// Clamp to protected region
		if row >= pw.protectedRow {
			row = pw.protectedRow - 1
			if row < 1 {
				row = 1
			}
			pw.redrawNeeded.Store(true)
			fmt.Fprintf(out, "\x1b[%d;%dH", row, pw.cursorCol.Load())
			return
		}
		pw.cursorRow.Store(int32(row))
		// Pass through

	case 'd': // VPA - Vertical Position Absolute
		row := 1
		if len(pw.params) >= 1 && pw.params[0] > 0 {
			row = pw.params[0]
		}
		// Clamp to protected region
		if row >= pw.protectedRow {
			row = pw.protectedRow - 1
			if row < 1 {
				row = 1
			}
			pw.redrawNeeded.Store(true)
		}
		pw.cursorRow.Store(int32(row))
		fmt.Fprintf(out, "\x1b[%dd", row)
		return

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

	case 'h', 'l': // SM/RM - Set/Reset Mode
		if isPrivate && len(pw.params) > 0 {
			switch pw.params[0] {
			case 1049, 47, 1047: // Alternate screen buffer sequences
				// Block alt screen - don't let child escape our protected main screen
				// This keeps our scroll region enforcement active
				return
			}
		}
		// Pass through
	}

	// Default: pass through unmodified
	out.Write(pw.escBuf)
}

// enforceScrollRegion writes the scroll region sequence to protect bottom rows.
func (pw *ProtectedWriter) enforceScrollRegion() {
	bottom := pw.height - pw.config.ProtectBottomRows
	if bottom < 1 {
		bottom = 1
	}
	fmt.Fprintf(pw.out, "\x1b[1;%dr", bottom)
}

// EnforceScrollRegion can be called externally to re-apply scroll region protection.
func (pw *ProtectedWriter) EnforceScrollRegion() {
	pw.mu.Lock()
	defer pw.mu.Unlock()
	pw.enforceScrollRegion()
}
