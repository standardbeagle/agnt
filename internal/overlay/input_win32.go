package overlay

// The win32-input-mode decoder. Windows Terminal, talking to a native Windows
// console, reports each key as CSI Vk;Sc;Uc;Kd;Cs;Rc _ instead of VT bytes.
// This turns those reports back into the bytes a POSIX child expects. Only
// Windows runs it (InputRouter.decodeWin32); under Linux and WSL the terminal
// already sends VT bytes.
//
// Everything that is not a key report passes through, one whole escape
// sequence at a time: a read boundary inside a sequence holds it until the
// rest arrives, so the child never gets a fragment in one write and the tail
// in the next. Every hold is bounded twice, by length (win32CSIMaxLen,
// stringMaxLen) and by time (holdBudget). A unit that blows a bound is dropped
// to its end, never released as a fragment, and each drop is logged.

import (
	"bytes"
	"io"
	"iter"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/standardbeagle/agnt/internal/debug"
)

// win32CSIMaxLen bounds a held CSI, introducer included. The longest valid key
// report is ESC [ + six fields + five ';' + '_': 6 fields of at most 10 digits
// (Cs is a DWORD) is 67 bytes, rounded up.
const win32CSIMaxLen = 72

// win32RepeatMax caps Rc, the repeat count of one report. A held key repeats
// by sending more reports; a huge Rc can only be corruption.
const win32RepeatMax = 64

// x10MousePayload is the byte count after ESC [ M in an X10 mouse report.
const x10MousePayload = 3

// Discard modes: what the decoder is dropping across reads after a unit
// overran a bound or went stale.
const (
	discardNone   = iota
	discardCSI    // to the next CSI final byte
	discardString // to the next BEL or ESC \
)

// win32Decoder is the decoder state that outlives one read.
type win32Decoder struct {
	// highSurrogate is a pending UTF-16 high half; 0 when none. A character
	// outside the BMP arrives as two key reports and a read boundary can fall
	// between them.
	highSurrogate rune

	// discard is the discardNone/CSI/String mode; discarded counts the bytes
	// dropped in it, so an unterminated stream gives up at stringMaxLen.
	discard   int
	discarded int
	// stringESC records an ESC seen in discardString mode whose '\' may come
	// in the next read.
	stringESC bool
}

// parseWin32InputModeInternal decodes one buffer with a fresh decoder.
// It returns the decoded bytes and any incomplete sequence at the end, to be
// prepended to the next read.
func parseWin32InputModeInternal(data []byte) ([]byte, []byte) {
	var d win32Decoder
	return d.parse(data)
}

// parse decodes data with state carried between calls, which is how the
// scanner uses it. The returned remainder is always a prefix of one escape
// sequence, never longer than its bound.
func (d *win32Decoder) parse(data []byte) (out, remainder []byte) {
	i := 0
	for i < len(data) {
		if d.discard != discardNone {
			i = d.skipDiscarded(data, i)
			continue
		}
		if data[i] != 0x1b {
			out = append(out, data[i])
			i++
			continue
		}
		n, emit, hold := d.sequenceAt(data, i)
		if hold {
			return out, data[i:]
		}
		out = append(out, emit...)
		i += n
	}
	return out, nil
}

// skipDiscarded drops bytes from i in the current discard mode and returns
// where decoding resumes.
func (d *win32Decoder) skipDiscarded(data []byte, i int) int {
	for ; i < len(data); i++ {
		b := data[i]
		d.discarded++
		switch d.discard {
		case discardCSI:
			if b >= 0x40 && b <= 0x7e {
				d.endDiscard()
				return i + 1
			}
		case discardString:
			if b == 0x07 || (d.stringESC && b == '\\') {
				d.endDiscard()
				return i + 1
			}
			d.stringESC = b == 0x1b
		}
		if d.discarded > stringMaxLen {
			debug.Log("win32", "discard gave up after %d bytes with no terminator", d.discarded)
			d.endDiscard()
			return i + 1
		}
	}
	return i
}

func (d *win32Decoder) endDiscard() {
	d.discard, d.discarded, d.stringESC = discardNone, 0, false
}

// startDiscard drops the unit in progress through its end.
func (d *win32Decoder) startDiscard(mode int, why string, head []byte) {
	debug.Log("win32", "dropping %s sequence %q: %s", discardName(mode), truncateForLog(head), why)
	d.discard, d.discarded, d.stringESC = mode, len(head), false
}

func discardName(mode int) string {
	if mode == discardString {
		return "string"
	}
	return "CSI"
}

func truncateForLog(b []byte) []byte {
	if len(b) > 40 {
		return b[:40]
	}
	return b
}

// sequenceAt classifies the escape sequence at data[i] (an ESC). It returns
// how many bytes it spans and what to emit for it, or hold when the sequence
// is still incomplete at the end of data and must wait for the next read.
func (d *win32Decoder) sequenceAt(data []byte, i int) (n int, emit []byte, hold bool) {
	if i+1 >= len(data) {
		return 0, nil, true // ESC alone: the scanner's timer decides
	}
	switch next := data[i+1]; {
	case next == '[':
		return d.csiAt(data, i)
	case next == 'O': // SS3: one more byte
		if i+2 >= len(data) {
			return 0, nil, true
		}
		if data[i+2] < 0x20 || data[i+2] == 0x7f {
			// Not an SS3 final: ESC O was Alt+Shift+O, and the control
			// byte after it starts something new.
			return 1, data[i : i+1], false
		}
		return 3, data[i : i+3], false
	case stringSequenceIntroducers[next]:
		return d.stringAt(data, i)
	default: // ESC + byte, e.g. Alt+key in VT form: the ESC alone
		return 1, data[i : i+1], false
	}
}

// csiAt handles ESC [ at data[i]. A CSI is parameter bytes 0x30-0x3f, then
// intermediate bytes 0x20-0x2f, then one final byte 0x40-0x7e.
func (d *win32Decoder) csiAt(data []byte, i int) (int, []byte, bool) {
	j := i + 2
	for j < len(data) && data[j] >= 0x30 && data[j] <= 0x3f {
		j++
	}
	paramsEnd := j
	for j < len(data) && data[j] >= 0x20 && data[j] <= 0x2f {
		j++
	}
	if j-i > win32CSIMaxLen {
		d.startDiscard(discardCSI, "longer than win32CSIMaxLen", data[i:j])
		return j - i, nil, false
	}
	if j >= len(data) {
		return 0, nil, true
	}
	final := data[j]
	if final < 0x40 || final > 0x7e {
		// A control byte or ESC inside the sequence aborts it. Pass the ESC
		// through and decode what follows as fresh input.
		debug.Log("win32", "malformed CSI %q aborted by 0x%02x", truncateForLog(data[i:j]), final)
		return 1, data[i : i+1], false
	}
	end := j + 1
	params := data[i+2 : paramsEnd]
	plain := paramsEnd == j // no intermediates

	switch {
	case final == 'M' && len(params) == 0 && plain:
		// X10 mouse: three raw payload bytes follow.
		if end+x10MousePayload > len(data) {
			return 0, nil, true
		}
		end += x10MousePayload
		return end - i, data[i:end], false
	case (final == 'I' || final == 'O') && len(params) == 0 && plain:
		// Focus in/out. Under ConPTY the console host consumes these; the
		// child never asked for them through us.
		return end - i, nil, false
	case final == '_' && plain && isWin32Params(params):
		return end - i, d.decodeKeyReport(params), false
	default:
		return end - i, data[i:end], false
	}
}

// isWin32Params reports whether params is only digits and ';', the shape of a
// key report. A private prefix such as '<' or '?' makes it something else.
func isWin32Params(params []byte) bool {
	for _, b := range params {
		if (b < '0' || b > '9') && b != ';' {
			return false
		}
	}
	return true
}

// stringAt handles a string sequence (OSC, DCS, SOS, PM, APC) at data[i]. It
// ends at BEL or ESC \.
func (d *win32Decoder) stringAt(data []byte, i int) (int, []byte, bool) {
	for j := i + 2; j < len(data); j++ {
		if j-i > stringMaxLen {
			d.startDiscard(discardString, "longer than stringMaxLen", data[i:j])
			d.stringESC = data[j-1] == 0x1b
			return j - i, nil, false
		}
		switch {
		case data[j] == 0x07:
			return j + 1 - i, data[i : j+1], false
		case data[j] == 0x1b && j+1 < len(data) && data[j+1] == '\\':
			return j + 2 - i, data[i : j+2], false
		}
	}
	if len(data)-i > stringMaxLen {
		d.startDiscard(discardString, "longer than stringMaxLen", data[i:])
		d.stringESC = data[len(data)-1] == 0x1b
		return len(data) - i, nil, false
	}
	return 0, nil, true
}

// dropStale is called by the scanner when a held sequence outlived its time
// budget. A lone ESC is the Esc key and is released. Anything longer is the
// head of a unit whose tail may still arrive, so the head is dropped and the
// decoder drops the tail too, instead of typing both into the child.
func (d *win32Decoder) dropStale(pending []byte) []byte {
	if len(pending) <= 1 {
		return pending
	}
	switch next := pending[1]; {
	case next == '[':
		d.startDiscard(discardCSI, "went stale", pending)
	case stringSequenceIntroducers[next]:
		d.startDiscard(discardString, "went stale", pending)
		d.stringESC = pending[len(pending)-1] == 0x1b
	default:
		// ESC O with its last byte missing: Alt+Shift+O in VT form looks the
		// same, so it is released.
		return pending
	}
	return nil
}

// Win32 Virtual Key codes for non-character keys that need ANSI translation.
const (
	vkBack   = 0x08
	vkTab    = 0x09
	vkReturn = 0x0D
	vkMenu   = 0x12 // Alt
	vkEscape = 0x1B
	vkSpace  = 0x20
	vkPrior  = 0x21 // PageUp
	vkNext   = 0x22 // PageDown
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

	// Insert/Delete/PageUp/PageDown: ESC [ code ~ (with modifiers: ESC [ code ; mod ~)
	if code, ok := tildeKeyCodes[vk]; ok {
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
		seq := []byte{0x1b, '['}
		seq = append(seq, codes[vk-vkF5]...)
		if mod > 1 {
			seq = append(seq, ';', byte('0'+mod))
		}
		return append(seq, '~')
	}

	// Ctrl+Space carries no character; a VT terminal sends NUL.
	if vk == vkSpace && cs&csCtrlMask != 0 {
		return []byte{0}
	}

	return nil
}

// tildeKeyCodes are the keys a VT terminal sends as ESC [ code ~.
var tildeKeyCodes = map[int]byte{vkInsert: '2', vkDelete: '3', vkPrior: '5', vkNext: '6'}

// win32Report is one parsed key report.
type win32Report struct {
	vk, sc, uc, kd, cs, rc int
}

// Field bounds: Vk, Sc, Uc and Rc are WORDs, Kd a BOOL, Cs a DWORD.
var win32FieldMax = [6]uint64{0xFFFF, 0xFFFF, 0xFFFF, 1, 0xFFFFFFFF, 0xFFFF}

// parseWin32Report parses Vk;Sc;Uc;Kd;Cs;Rc. Missing trailing fields take the
// spec's defaults (0, and Rc 1); empty fields are 0. A field out of range or
// a seventh field makes the report malformed.
func parseWin32Report(params []byte) (win32Report, bool) {
	var f [6]uint64
	f[5] = 1
	field, digits := 0, 0
	var v uint64
	commit := func() bool {
		if field >= len(f) || v > win32FieldMax[field] {
			return false
		}
		if digits > 0 {
			f[field] = v
		}
		return true
	}
	for _, b := range params {
		if b == ';' {
			if !commit() {
				return win32Report{}, false
			}
			field, digits, v = field+1, 0, 0
			continue
		}
		digits++
		if digits > 10 {
			return win32Report{}, false
		}
		v = v*10 + uint64(b-'0')
	}
	if !commit() {
		return win32Report{}, false
	}
	return win32Report{int(f[0]), int(f[1]), int(f[2]), int(f[3]), int(f[4]), int(f[5])}, true
}

// decodeKeyReport turns one key report's parameters into the bytes to send.
func (d *win32Decoder) decodeKeyReport(params []byte) []byte {
	rep, ok := parseWin32Report(params)
	if !ok {
		debug.Log("win32", "dropping malformed key report %q", truncateForLog(params))
		return nil
	}
	if rep.kd != 1 {
		// Key-ups type nothing, except the Alt release that ends an
		// Alt+numpad composition: it carries the composed character. Alt
		// is not a modifier of that character.
		if rep.vk == vkMenu && rep.uc > 0 {
			return d.charBytes(nil, rep.vk, rep.cs&^csAltMask, rune(rep.uc))
		}
		return nil
	}
	n := max(rep.rc, 1)
	if n > win32RepeatMax {
		debug.Log("win32", "capping repeat count %d at %d", n, win32RepeatMax)
		n = win32RepeatMax
	}

	var one []byte
	switch {
	case rep.uc > 0:
		one = d.charBytes(nil, rep.vk, rep.cs, rune(rep.uc))
	default:
		one = vkToANSI(rep.vk, rep.cs)
	}
	if len(one) == 0 {
		return nil
	}
	out := make([]byte, 0, len(one)*n)
	for range n {
		out = append(out, one...)
	}
	return out
}

// charBytes appends the character a key report carries. Uc is a UTF-16 code
// unit, so it is encoded as UTF-8 for the child — truncating it to a byte
// turned "é" into an invalid byte and "中" (0x4E2D) into "-" — and the two
// halves of a surrogate pair are joined into one rune. Alt without Ctrl sends
// ESC first, as xterm and conhost do; Ctrl together with Alt is AltGr, whose
// character is sent as is.
func (d *win32Decoder) charBytes(out []byte, vk, cs int, r rune) []byte {
	switch {
	case r >= 0xD800 && r <= 0xDBFF: // high surrogate: wait for the low half
		if d.highSurrogate != 0 {
			debug.Log("win32", "dropping unpaired high surrogate %U", d.highSurrogate)
		}
		d.highSurrogate = r
		return out
	case r >= 0xDC00 && r <= 0xDFFF: // low surrogate
		hi := d.highSurrogate
		d.highSurrogate = 0
		if hi == 0 {
			debug.Log("win32", "dropping unpaired low surrogate %U", r)
			return out
		}
		r = utf16.DecodeRune(hi, r)
	default:
		if d.highSurrogate != 0 {
			debug.Log("win32", "dropping unpaired high surrogate %U", d.highSurrogate)
			d.highSurrogate = 0
		}
	}

	// Windows Terminal sends \b for Backspace, but POSIX terminals and
	// readline expect \x7f (DEL).
	if vk == vkBack && r == 0x08 {
		r = 0x7f
	}
	// Shift+Tab arrives as Uc=9 with the Shift flag; POSIX apps expect the
	// back-tab sequence, not a plain Tab.
	if vk == vkTab && r == '\t' && cs&csShift != 0 {
		return append(out, 0x1b, '[', 'Z')
	}
	if cs&csAltMask != 0 && cs&csCtrlMask == 0 {
		out = append(out, 0x1b)
	}
	return utf8.AppendRune(out, r)
}

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
// sequence, and a sequence split by a slow link resumes well inside the wider
// budget; nothing resumes after it.
const staleSequenceFactor = 20

// holdBudget returns how long a remainder may be held before it goes stale.
func holdBudget(pending []byte, flushAfter time.Duration) time.Duration {
	if len(pending) <= 1 {
		return flushAfter
	}
	return staleSequenceFactor * flushAfter
}

// ScanWin32Input returns an iterator over the decoded bytes of r, with the
// production hold budget.
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

// scanWin32Chunks reads r and yields each decoded burst whole. A burst holds
// only complete sequences — a sequence cut by a read boundary stays in the
// remainder until it completes or its hold budget runs out — so a consumer
// that writes chunk by chunk never splits a sequence across two writes.
//
// The budget runs from when the remainder began, not from the latest read:
// reads that extend a sequence without finishing it do not hold it longer.
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
		var pendingSince time.Time
		var dec win32Decoder
		timer := time.NewTimer(time.Hour)
		timer.Stop()
		defer timer.Stop()

		for {
			var timerC <-chan time.Time
			if len(pending) > 0 {
				timer.Reset(time.Until(pendingSince.Add(holdBudget(pending, flushAfter))))
				timerC = timer.C
			}

			select {
			case <-timerC:
				// The rest of the sequence never came.
				out := dec.dropStale(pending)
				pending = nil
				if !emit(out) {
					return
				}

			case rr := <-reads:
				timer.Stop()
				if len(rr.data) > 0 {
					data := rr.data
					if len(pending) > 0 {
						data = append(append([]byte{}, pending...), rr.data...)
					}
					parsed, remainder := dec.parse(data)
					// A remainder that extends the held one keeps its
					// start time; any other remainder is a new sequence.
					if len(remainder) > 0 && !bytes.HasPrefix(remainder, pending) || len(pending) == 0 {
						pendingSince = time.Now()
					}
					pending = remainder
					if !emit(parsed) {
						return
					}
				}
				if rr.err != nil {
					// Any read error ends the stream; what was held still
					// goes out rather than vanishing with it.
					if rr.err != io.EOF {
						debug.Log("win32", "input read ended: %v", rr.err)
					}
					emit(pending)
					return
				}
			}
		}
	}
}
