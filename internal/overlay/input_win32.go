package overlay

// The win32-input-mode decoder. Windows Terminal, talking to a native Windows
// console, reports each key as CSI Vk;Sc;Uc;Kd;Cs;Rc _ instead of VT bytes.
// This turns those reports back into the bytes a POSIX child expects. Only
// Windows runs it (InputRouter.decodeWin32); under Linux and WSL the terminal
// already sends VT bytes.

import (
	"io"
	"iter"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/standardbeagle/agnt/internal/debug"
)

// DebugWin32Input enables logging of win32-input-mode parsing
var DebugWin32Input = false

// win32ParseState holds state during win32 input parsing.
type win32ParseState struct {
	result     []byte
	foundWin32 bool
	dec        *win32Decoder
}

// win32Decoder is the win32-input-mode state that outlives one read. A
// character outside the BMP arrives as two key events, a UTF-16 high then low
// surrogate, and a read boundary can fall between them.
type win32Decoder struct {
	highSurrogate rune // pending high half; 0 when none
}

// debugLog logs a message if DebugWin32Input is enabled.
func debugLog(format string, args ...interface{}) {
	if DebugWin32Input {
		// Never os.Stderr here: this runs inside a live PTY session.
		debug.Log("win32", format, args...)
	}
}

// parseWin32InputModeInternal parses Windows Terminal win32-input-mode sequences.
// Format: CSI Vk ; Sc ; Uc ; Kd ; Cs ; Rc _
// Where Uc is the unicode character value we want.
// Also filters out Focus In/Out sequences (CSI I and CSI O) that Windows Terminal sends.
// Returns parsed bytes and any incomplete sequence at the end that should be
// prepended to the next buffer read.
func parseWin32InputModeInternal(data []byte) ([]byte, []byte) {
	var d win32Decoder
	return d.parse(data)
}

// parse is parseWin32InputModeInternal with decoder state carried between
// calls, which is how the scanner uses it.
func (d *win32Decoder) parse(data []byte) ([]byte, []byte) {
	if DebugWin32Input && len(data) > 0 {
		dump := data
		if len(dump) > 80 {
			dump = dump[:80]
		}
		debugLog("RAW INPUT (%d bytes): %q", len(data), dump)
	}

	state := &win32ParseState{dec: d}
	i := 0

	for i < len(data) {
		if data[i] == 0x1b {
			newIndex, remainder := state.handleEscByte(data, i)
			if remainder != nil {
				return state.result, remainder
			}
			if newIndex != i {
				i = newIndex
				continue
			}
		}

		// Regular byte - pass through
		debugLog("passthrough byte %d (0x%02x) '%c'", data[i], data[i], printableChar(data[i]))
		state.result = append(state.result, data[i])
		i++
	}

	if DebugWin32Input && state.foundWin32 {
		debugLog("input %d bytes -> output %d bytes", len(data), len(state.result))
	}
	return state.result, nil
}

// handleEscByte processes an ESC byte and returns the new index and any remainder.
// If remainder is non-nil, parsing should stop and return it.
// If newIndex != i, the caller should continue from newIndex.
func (s *win32ParseState) handleEscByte(data []byte, i int) (newIndex int, remainder []byte) {
	debugLogEscPosition(data, i)

	// ESC at end of buffer - save as remainder
	if i+1 >= len(data) {
		if DebugWin32Input && s.foundWin32 {
			debugLog("input %d bytes -> output %d bytes, remainder %d bytes", len(data), len(s.result), len(data)-i)
		}
		return i, data[i:]
	}

	// Check for CSI sequence (ESC [)
	if data[i+1] == '[' {
		return s.handleCSISequence(data, i)
	}

	return i, nil
}

// debugLogEscPosition logs ESC byte position information.
func debugLogEscPosition(data []byte, i int) {
	if !DebugWin32Input {
		return
	}
	if i+1 < len(data) {
		debugLog("ESC at i=%d, next byte=%d (0x%02x '%c')", i, data[i+1], data[i+1], printableChar(data[i+1]))
	} else {
		debugLog("ESC at i=%d, no next byte (end of buffer) - saving as remainder", i)
	}
}

// handleCSISequence processes a CSI sequence starting at position i.
func (s *win32ParseState) handleCSISequence(data []byte, i int) (newIndex int, remainder []byte) {
	debugLog("CSI detected at i=%d", i)

	// Check for Focus In/Out sequences - skip them
	if i+2 < len(data) && (data[i+2] == 'I' || data[i+2] == 'O') {
		debugLog("skipping focus %c sequence", data[i+2])
		return i + 3, nil
	}

	// Need at least one more byte after ESC[ to determine sequence type
	if i+2 >= len(data) {
		debugLog("ESC[ at end of buffer - saving as remainder")
		return i, data[i:]
	}

	// Look for win32-input-mode sequence ending with '_'
	end, hitInvalidChar := findWin32SequenceEnd(data, i+2)

	if end > 0 {
		s.foundWin32 = true
		s.parseWin32Sequence(data[i+2 : end])
		return end + 1, nil
	}

	// Incomplete sequence - save as remainder
	if !hitInvalidChar {
		debugLog("incomplete CSI sequence at end of buffer - saving as remainder")
		return i, data[i:]
	}

	// Not a win32-input-mode sequence - fall through to pass through
	return i, nil
}

// findWin32SequenceEnd looks for the ending '_' of a win32 input sequence.
// Returns the index of '_' (or -1 if not found) and whether an invalid char was hit.
func findWin32SequenceEnd(data []byte, start int) (end int, hitInvalidChar bool) {
	for j := start; j < len(data); j++ {
		if data[j] == '_' {
			return j, false
		}
		// If we hit another ESC or non-sequence char, stop looking
		if data[j] == 0x1b || (data[j] < '0' || data[j] > '9') && data[j] != ';' {
			debugLog("search broke at j=%d byte=%d (0x%02x) - not a win32-input-mode sequence", j, data[j], data[j])
			return -1, true
		}
	}
	return -1, false
}

// Win32 Virtual Key codes for non-character keys that need ANSI translation.
const (
	vkBack   = 0x08
	vkTab    = 0x09
	vkReturn = 0x0D
	vkEscape = 0x1B
	vkEnd    = 0x23
	vkHome   = 0x24
	vkLeft   = 0x25
	vkUp     = 0x26
	vkRight  = 0x27
	vkDown   = 0x28
	vkInsert = 0x2D
	vkDelete = 0x2E
	vkF1     = 0x70
	vkF2     = 0x71
	vkF3     = 0x72
	vkF4     = 0x73
	vkF5     = 0x74
	vkF6     = 0x75
	vkF7     = 0x76
	vkF8     = 0x77
	vkF9     = 0x78
	vkF10    = 0x79
	vkF11    = 0x7A
	vkF12    = 0x7B
)

// Win32 control key state flags (Cs field).
const (
	csShift    = 0x10
	csCtrl     = 0x08 // LEFT_CTRL_PRESSED or RIGHT_CTRL_PRESSED
	csAlt      = 0x03 // LEFT_ALT_PRESSED or RIGHT_ALT_PRESSED
	csCtrlMask = 0x0C // Either ctrl key
	csAltMask  = 0x03 // Either alt key
)

// vkToANSI maps a virtual key code + control state to ANSI escape bytes.
// Returns nil if the key doesn't map to an ANSI sequence.
func vkToANSI(vk, cs int) []byte {
	// Calculate xterm modifier parameter: 1 + (shift?1:0) + (alt?2:0) + (ctrl?4:0)
	mod := 1
	if cs&csShift != 0 {
		mod += 1
	}
	if cs&csAltMask != 0 {
		mod += 2
	}
	if cs&csCtrlMask != 0 {
		mod += 4
	}

	// Arrow keys: ESC [ 1 ; mod A/B/C/D (or ESC [ A/B/C/D without modifiers)
	if vk >= vkLeft && vk <= vkDown {
		suffix := []byte{'D', 'A', 'C', 'B'}[vk-vkLeft] // Left=D Up=A Right=C Down=B
		if mod > 1 {
			return []byte{0x1b, '[', '1', ';', byte('0' + mod), suffix}
		}
		return []byte{0x1b, '[', suffix}
	}

	// Home/End: ESC [ 1 ; mod H/F (or ESC [ H/F)
	if vk == vkHome || vk == vkEnd {
		suffix := byte('H')
		if vk == vkEnd {
			suffix = 'F'
		}
		if mod > 1 {
			return []byte{0x1b, '[', '1', ';', byte('0' + mod), suffix}
		}
		return []byte{0x1b, '[', suffix}
	}

	// Insert/Delete: ESC [ 2~ / ESC [ 3~ (with modifiers: ESC [ 2 ; mod ~)
	if vk == vkInsert || vk == vkDelete {
		code := byte('2')
		if vk == vkDelete {
			code = '3'
		}
		if mod > 1 {
			return []byte{0x1b, '[', code, ';', byte('0' + mod), '~'}
		}
		return []byte{0x1b, '[', code, '~'}
	}

	// F1-F4: ESC O P/Q/R/S (or ESC [ 1 ; mod P/Q/R/S with modifiers)
	if vk >= vkF1 && vk <= vkF4 {
		suffix := byte('P' + byte(vk-vkF1))
		if mod > 1 {
			return []byte{0x1b, '[', '1', ';', byte('0' + mod), suffix}
		}
		return []byte{0x1b, 'O', suffix}
	}

	// F5-F12: ESC [ code ~ (with modifiers: ESC [ code ; mod ~)
	if vk >= vkF5 && vk <= vkF12 {
		codes := []string{"15", "17", "18", "19", "20", "21", "23", "24"}
		code := codes[vk-vkF5]
		if mod > 1 {
			seq := []byte{0x1b, '['}
			seq = append(seq, code...)
			seq = append(seq, ';', byte('0'+mod), '~')
			return seq
		}
		seq := []byte{0x1b, '['}
		seq = append(seq, code...)
		seq = append(seq, '~')
		return seq
	}

	return nil
}

// parseWin32Sequence parses the win32 input sequence content and appends result.
func (s *win32ParseState) parseWin32Sequence(seqData []byte) {
	seq := string(seqData)
	parts := splitSemicolon(seq)
	if len(parts) < 4 {
		return
	}

	// Format: Vk ; Sc ; Uc ; Kd ; Cs ; Rc
	vk := parseInt(parts[0])
	uc := parseInt(parts[2])
	kd := parseInt(parts[3])

	// Only process key-down events (kd=1)
	if kd != 1 {
		debugLog("seq=%s -> skipped (kd=%d, key up)", seq, kd)
		return
	}

	cs := 0
	if len(parts) >= 5 {
		cs = parseInt(parts[4])
	}

	// A unicode character is emitted as the character (see appendChar).
	if uc > 0 {
		s.appendChar(vk, cs, rune(uc))
		debugLog("seq=%s -> char %d (0x%02x)", seq, uc, uc)
		return
	}

	// No unicode char (uc=0) — check for virtual keys that need ANSI translation
	if ansi := vkToANSI(vk, cs); ansi != nil {
		s.result = append(s.result, ansi...)
		debugLog("seq=%s -> vk=%d cs=%d ansi=%q", seq, vk, cs, ansi)
		return
	}

	debugLog("seq=%s -> skipped (uc=0, vk=%d, no mapping)", seq, vk)
}

// appendChar appends the character a key-down event carries. Uc is a UTF-16
// code unit, so it is encoded as UTF-8 for the child — truncating it to a
// byte turned "é" into an invalid byte and "中" (0x4E2D) into "-" — and the
// two halves of a surrogate pair are joined into one rune. Alt without Ctrl
// sends ESC first, as xterm and conhost do; Ctrl together with Alt is AltGr,
// whose character is sent as is.
func (s *win32ParseState) appendChar(vk, cs int, r rune) {
	switch {
	case r >= 0xD800 && r <= 0xDBFF: // high surrogate: wait for the low half
		s.dec.highSurrogate = r
		return
	case r >= 0xDC00 && r <= 0xDFFF: // low surrogate
		hi := s.dec.highSurrogate
		s.dec.highSurrogate = 0
		if hi == 0 {
			return // stray low half: nothing to pair it with
		}
		r = utf16.DecodeRune(hi, r)
	default:
		s.dec.highSurrogate = 0 // an unpaired high half is dropped
	}

	// Windows Terminal sends \b for Backspace, but POSIX terminals and
	// readline expect \x7f (DEL).
	if vk == vkBack && r == 0x08 {
		r = 0x7f
	}
	// Shift+Tab arrives as Uc=9 with the Shift flag; POSIX apps expect the
	// back-tab sequence, not a plain Tab.
	if vk == vkTab && r == '\t' && cs&csShift != 0 {
		s.result = append(s.result, 0x1b, '[', 'Z')
		return
	}
	if cs&csAltMask != 0 && cs&csCtrlMask == 0 {
		s.result = append(s.result, 0x1b)
	}
	s.result = utf8.AppendRune(s.result, r)
}

// printableChar returns the character if printable, otherwise '.'
func printableChar(b byte) byte {
	if b >= 32 && b < 127 {
		return b
	}
	return '.'
}

// splitSemicolon splits a string by semicolons without allocating a slice.
func splitSemicolon(s string) []string {
	var parts []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ';' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return parts
}

// parseInt parses a string to int, returning 0 on error.
func parseInt(s string) int {
	var n int
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}

// ScanWin32Input returns an iterator that reads from r and yields parsed bytes.
// It handles Windows Terminal win32-input-mode escape sequences, extracting the
// unicode character values and yielding them as individual bytes.
// Buffer boundaries are handled internally - incomplete sequences at the end of
// a read are held and combined with the next read.
// win32PendingFlush bounds how long a trailing ESC may sit in the scanner's
// remainder buffer. The parser holds a trailing ESC because a multi-byte
// sequence can straddle a read boundary — but a lone Esc keypress arrives as a
// one-byte read that looks exactly the same, and nothing follows it. Without a
// deadline that byte is held until EOF: Esc never reaches the overlay (whose
// own EscapeSequenceReader.Timeout disambiguation then can never run, since the
// byte never arrives) and never reaches the wrapped TUI on the passthrough
// path either. A real split sequence resumes in microseconds, so any wait this
// long means the rest is not coming.
const win32PendingFlush = 25 * time.Millisecond

// staleSequenceFactor scales the hold budget for a remainder that is already
// past its introducer. A bare ESC is held only briefly, because a lone Esc
// keypress looks exactly like one and must reach the layer above quickly. A
// remainder such as "\x1b[1;5" is different: it can only be the start of a
// sequence, and releasing it early hands the layer above a fragment -- which
// on the passthrough path means a bound key is typed into the child instead
// of acted on. A sequence split by a slow link resumes well inside the wider
// budget; nothing resumes after it.
const staleSequenceFactor = 20

// holdBudget returns how long a remainder may be held before it is released.
func holdBudget(pending []byte, flushAfter time.Duration) time.Duration {
	if len(pending) <= 1 {
		return flushAfter
	}
	return staleSequenceFactor * flushAfter
}

func ScanWin32Input(r io.Reader) iter.Seq[byte] {
	return scanWin32Input(r, win32PendingFlush)
}

// scanWin32Input is scanWin32Chunks one byte at a time.
func scanWin32Input(r io.Reader, flushAfter time.Duration) iter.Seq[byte] {
	return func(yield func(byte) bool) {
		for chunk := range scanWin32Chunks(r, flushAfter) {
			for _, b := range chunk {
				if !yield(b) {
					return
				}
			}
		}
	}
}

// scanWin32Chunks reads r and yields each parsed burst whole. A burst holds
// only complete sequences — a sequence cut by a read boundary stays in the
// remainder until it completes or its hold budget runs out — so a consumer
// that writes chunk by chunk never splits a sequence across two writes.
func scanWin32Chunks(r io.Reader, flushAfter time.Duration) iter.Seq[[]byte] {
	return func(yield func([]byte) bool) {
		type readResult struct {
			data []byte
			err  error
		}
		reads := make(chan readResult, 1)
		done := make(chan struct{})
		defer close(done)

		// Reads happen off the iterator goroutine so a held remainder can be
		// released on a timer while the next Read is still blocked.
		go func() {
			for {
				buf := make([]byte, 256)
				n, err := r.Read(buf)
				select {
				case reads <- readResult{buf[:n], err}:
				case <-done:
					return
				}
				if err != nil {
					return
				}
			}
		}()

		emit := func(bs []byte) bool {
			return len(bs) == 0 || yield(bs)
		}

		var pending []byte
		var dec win32Decoder
		var timer *time.Timer
		defer func() {
			if timer != nil {
				timer.Stop()
			}
		}()

		for {
			var timerC <-chan time.Time
			if len(pending) > 0 {
				if timer == nil {
					timer = time.NewTimer(holdBudget(pending, flushAfter))
				}
				timerC = timer.C
			}

			select {
			case <-timerC:
				// The rest of the sequence never came. Release the bytes so
				// the layer above can decide what they mean.
				timer = nil
				out := pending
				pending = nil
				if !emit(out) {
					return
				}

			case rr := <-reads:
				if timer != nil {
					timer.Stop()
					timer = nil
				}
				if len(rr.data) > 0 {
					data := rr.data
					if len(pending) > 0 {
						data = append(append([]byte{}, pending...), rr.data...)
					}
					parsed, remainder := dec.parse(data)
					pending = remainder
					if !emit(parsed) {
						return
					}
				}
				if rr.err != nil {
					if rr.err == io.EOF && len(pending) > 0 {
						emit(pending)
					}
					return
				}
			}
		}
	}
}
