package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/standardbeagle/agnt/internal/overlay"
)

// LineEditor provides line editing for raw mode terminal input.
// It handles cursor movement, history, and echoes through the overlay writer chain.
//
// The buffer holds runes and the cursor is a rune index: input arrives one
// byte at a time, so multi-byte UTF-8 is reassembled in utf8Pending before it
// reaches the buffer. Cursor motion on screen is measured in terminal cells
// (CJK and emoji occupy two).
type LineEditor struct {
	mu      sync.Mutex
	buf     []rune   // Current line buffer
	cursor  int      // Cursor position within buf (rune index)
	hist    []string // Command history
	histIdx int      // Current history index (-1 = current input)
	saved   string   // Saved current input when browsing history

	// utf8Pending holds the leading bytes of a multi-byte rune whose
	// continuation bytes have not arrived yet (they may straddle reads).
	utf8Pending []byte

	prompt string    // Prompt string (e.g., "> ")
	out    io.Writer // Output writer (overlay chain)
	active bool      // Whether editor accepts input

	lines chan string   // Submitted lines
	eof   chan struct{} // EOF signal (Ctrl+D)

	// onInterrupt is called when Ctrl+C arrives while the editor is inactive
	// (a turn is streaming). The terminal is in raw mode, so Ctrl+C is the
	// byte 0x03 and never a SIGINT — without this hook a running turn could
	// not be stopped from the keyboard at all.
	onInterrupt func()

	// ask is non-nil while Ask owns the editor: the next submitted line (or a
	// Ctrl+C / Ctrl+D cancel) is delivered there instead of to lines.
	ask chan askAnswer

	escReader *overlay.EscapeSequenceReader
}

// askAnswer is one reply to LineEditor.Ask.
type askAnswer struct {
	line string
	ok   bool
}

// NewLineEditor creates a line editor that echoes through the given writer.
func NewLineEditor(out io.Writer, prompt string) *LineEditor {
	return &LineEditor{
		prompt:    prompt,
		out:       out,
		active:    true,
		lines:     make(chan string, 4),
		eof:       make(chan struct{}),
		histIdx:   -1,
		escReader: overlay.NewEscapeSequenceReader(),
	}
}

// Lines returns the channel that receives submitted lines.
func (le *LineEditor) Lines() <-chan string {
	return le.lines
}

// EOF returns the channel that signals Ctrl+D.
func (le *LineEditor) EOF() <-chan struct{} {
	return le.eof
}

// SetActive enables or disables the editor.
// When inactive, Feed() is a no-op (used during streaming).
func (le *LineEditor) SetActive(active bool) {
	le.mu.Lock()
	defer le.mu.Unlock()
	le.active = active
}

// IsActive returns whether the editor is currently accepting input.
func (le *LineEditor) IsActive() bool {
	le.mu.Lock()
	defer le.mu.Unlock()
	return le.active
}

// ShowPrompt writes the prompt to the output writer.
func (le *LineEditor) ShowPrompt() {
	le.mu.Lock()
	defer le.mu.Unlock()
	fmt.Fprint(le.out, le.prompt)
}

// Redraw re-renders the prompt and current buffer.
// Called after resize or menu close to restore the input line.
func (le *LineEditor) Redraw() {
	le.mu.Lock()
	defer le.mu.Unlock()
	le.redraw()
}

func (le *LineEditor) redraw() {
	// Clear line, write prompt + buffer, position cursor
	fmt.Fprint(le.out, "\r\033[K")
	fmt.Fprint(le.out, le.prompt)
	io.WriteString(le.out, string(le.buf))
	// Move cursor to correct position if not at end
	if back := cellWidth(le.buf[le.cursor:]); back > 0 {
		fmt.Fprintf(le.out, "\033[%dD", back)
	}
}

// cellWidth returns the number of terminal cells rs occupies.
func cellWidth(rs []rune) int {
	n := 0
	for _, r := range rs {
		n += overlay.RuneCells(r)
	}
	return n
}

// SetInterruptHandler installs the function called when Ctrl+C arrives while
// the editor is inactive (i.e. while a turn is running). Pass nil to clear.
func (le *LineEditor) SetInterruptHandler(fn func()) {
	le.mu.Lock()
	defer le.mu.Unlock()
	le.onInterrupt = fn
}

// Ask shows prompt and returns the next line the user submits, temporarily
// taking the editor over from the normal Lines() flow (and re-activating it if
// a turn had it disabled). It is how a mid-turn question — an ACP permission
// request — reaches the user in raw mode, where the input router is the only
// reader of stdin and Enter is '\r', so a direct os.Stdin line read can never
// complete. ok is false when the user cancels (Ctrl+C / Ctrl+D) or ctx ends.
func (le *LineEditor) Ask(ctx context.Context, prompt string) (line string, ok bool) {
	ch := make(chan askAnswer, 1)

	le.mu.Lock()
	if le.ask != nil {
		// One question at a time; a second concurrent Ask cannot be shown.
		le.mu.Unlock()
		return "", false
	}
	prevPrompt, prevActive := le.prompt, le.active
	prevBuf, prevCursor := le.buf, le.cursor
	le.prompt, le.active, le.ask = prompt, true, ch
	le.buf, le.cursor = nil, 0
	le.redraw()
	le.mu.Unlock()

	var ans askAnswer
	select {
	case ans = <-ch:
	case <-ctx.Done():
	}

	le.mu.Lock()
	le.prompt, le.active, le.ask = prevPrompt, prevActive, nil
	le.buf, le.cursor = prevBuf, prevCursor
	le.mu.Unlock()
	return ans.line, ans.ok
}

// Feed processes a raw byte from the input router.
// Escape sequences should be pre-parsed into key names via FeedKey.
func (le *LineEditor) Feed(b byte) {
	le.mu.Lock()
	if !le.active {
		interrupt := le.onInterrupt
		le.mu.Unlock()
		// Ctrl+C during a turn: the editor ignores everything else while
		// inactive, but this one byte is the only keyboard interrupt there is.
		if b == 0x03 && interrupt != nil {
			interrupt()
		}
		return
	}

	// Reassemble multi-byte UTF-8. A lead/continuation byte never starts an
	// escape sequence, so it bypasses the escape reader unless one is already
	// mid-sequence (X10 mouse reports carry arbitrary bytes). An ASCII byte
	// cannot continue a multi-byte rune: it discards a broken prefix and is
	// then handled as itself below.
	if len(le.utf8Pending) > 0 && b < 0x80 {
		le.utf8Pending = le.utf8Pending[:0]
	}
	if !le.escReader.IsPending() && b >= 0x80 {
		le.feedUTF8(b)
		le.mu.Unlock()
		return
	}

	// Use escape sequence reader to handle arrow keys
	key, complete := le.escReader.Feed(b)
	if !complete {
		le.mu.Unlock()
		return
	}
	le.mu.Unlock()

	le.FeedKey(key)
}

// feedUTF8 accumulates one non-ASCII byte of a multi-byte rune and inserts the
// rune once it is complete. An invalid sequence is discarded whole — to the
// end of the unit, never handed on as fresh input. Caller holds le.mu.
func (le *LineEditor) feedUTF8(b byte) {
	if len(le.utf8Pending) > 0 && utf8.RuneStart(b) {
		// A new lead byte where a continuation was due: the old prefix is
		// broken, start over with this one.
		le.utf8Pending = le.utf8Pending[:0]
	}
	le.utf8Pending = append(le.utf8Pending, b)
	if !utf8.FullRune(le.utf8Pending) {
		if len(le.utf8Pending) >= utf8.UTFMax {
			le.utf8Pending = le.utf8Pending[:0]
		}
		return
	}
	r, size := utf8.DecodeRune(le.utf8Pending)
	le.utf8Pending = le.utf8Pending[:0]
	if r == utf8.RuneError && size <= 1 {
		return // stray continuation byte or invalid lead
	}
	// C1 controls (U+0080–U+009F) are not printable.
	if r >= 0xa0 {
		le.insertRune(r)
	}
}

// FeedEscapeTimeout should be called when escape sequence times out.
func (le *LineEditor) FeedEscapeTimeout() {
	le.mu.Lock()
	key, had := le.escReader.Timeout()
	le.mu.Unlock()
	if had {
		le.FeedKey(key)
	}
}

// EscapePending returns true if the escape reader has a pending sequence.
func (le *LineEditor) EscapePending() bool {
	le.mu.Lock()
	defer le.mu.Unlock()
	return le.escReader.IsPending()
}

// FeedKey processes a parsed key (e.g., "a", "Up", "Home").
func (le *LineEditor) FeedKey(key string) {
	le.mu.Lock()
	defer le.mu.Unlock()

	if !le.active {
		return
	}

	switch key {
	case "Up":
		le.historyUp()
	case "Down":
		le.historyDown()
	case "Left":
		le.cursorLeft()
	case "Right":
		le.cursorRight()
	case "Home":
		le.cursorHome()
	case "End":
		le.cursorEnd()
	case "Delete":
		le.deleteChar()
	default:
		if len(key) == 1 {
			le.feedByte(key[0])
		}
		// Ignore multi-char keys we don't handle (Escape+X, etc.)
	}
}

func (le *LineEditor) feedByte(b byte) {
	switch b {
	case '\r', '\n': // Enter
		line := string(le.buf)
		le.buf = le.buf[:0]
		le.cursor = 0
		le.histIdx = -1
		fmt.Fprint(le.out, "\r\n")
		if le.ask != nil {
			// An Ask reply: not history, not a REPL line.
			le.answerAsk(askAnswer{line: line, ok: true})
			return
		}
		le.addHistory(line)
		le.mu.Unlock()
		le.lines <- line
		le.mu.Lock()

	case 0x03: // Ctrl+C
		if le.ask != nil {
			fmt.Fprint(le.out, "^C\r\n")
			le.answerAsk(askAnswer{})
			return
		}
		if len(le.buf) > 0 {
			// Clear current line
			le.buf = le.buf[:0]
			le.cursor = 0
			fmt.Fprint(le.out, "^C\r\n")
			le.redraw()
		} else {
			// Empty line Ctrl+C — print ^C and show new prompt
			fmt.Fprint(le.out, "^C\r\n")
			le.redraw()
		}

	case 0x04: // Ctrl+D
		if le.ask != nil && len(le.buf) == 0 {
			fmt.Fprint(le.out, "\r\n")
			le.answerAsk(askAnswer{})
			return
		}
		if len(le.buf) == 0 {
			le.mu.Unlock()
			select {
			case le.eof <- struct{}{}:
			default:
			}
			le.mu.Lock()
		}
		// Ctrl+D with content: delete char under cursor (like Delete)
		if le.cursor < len(le.buf) {
			le.deleteChar()
		}

	case 0x7f, 0x08: // Backspace
		le.backspace()

	case 0x01: // Ctrl+A — Home
		le.cursorHome()

	case 0x05: // Ctrl+E — End
		le.cursorEnd()

	case 0x15: // Ctrl+U — Clear line
		le.buf = le.buf[:0]
		le.cursor = 0
		le.redraw()

	case 0x17: // Ctrl+W — Delete word
		le.deleteWord()

	case 0x0b: // Ctrl+K — Kill to end of line
		le.buf = le.buf[:le.cursor]
		le.redraw()

	default:
		// Printable ASCII. Non-ASCII runes arrive through feedUTF8.
		if b >= 0x20 && b < 0x7f {
			le.insertRune(rune(b))
		}
	}
}

// answerAsk delivers an Ask reply. The channel is buffered and Ask clears
// le.ask before another reply can be produced, so this never blocks. Caller
// holds le.mu.
func (le *LineEditor) answerAsk(a askAnswer) {
	select {
	case le.ask <- a:
	default:
	}
	// Stop accepting keys until Ask restores the previous state, so a second
	// Enter cannot be read as another answer.
	le.active = false
}

func (le *LineEditor) insertRune(r rune) {
	if le.cursor == len(le.buf) {
		// Append at end — simple echo
		le.buf = append(le.buf, r)
		le.cursor++
		io.WriteString(le.out, string(r))
	} else {
		// Insert in middle — splice and redraw remainder
		le.buf = append(le.buf, 0)
		copy(le.buf[le.cursor+1:], le.buf[le.cursor:])
		le.buf[le.cursor] = r
		le.cursor++
		// Write from cursor to end, then move cursor back
		io.WriteString(le.out, string(le.buf[le.cursor-1:]))
		if back := cellWidth(le.buf[le.cursor:]); back > 0 {
			fmt.Fprintf(le.out, "\033[%dD", back)
		}
	}
}

func (le *LineEditor) backspace() {
	if le.cursor == 0 {
		return
	}
	if le.cursor == len(le.buf) {
		// Delete last char, erasing every cell it occupied
		w := overlay.RuneCells(le.buf[len(le.buf)-1])
		le.buf = le.buf[:len(le.buf)-1]
		le.cursor--
		io.WriteString(le.out, strings.Repeat("\b", w)+strings.Repeat(" ", w)+strings.Repeat("\b", w))
	} else {
		// Delete in middle
		copy(le.buf[le.cursor-1:], le.buf[le.cursor:])
		le.buf = le.buf[:len(le.buf)-1]
		le.cursor--
		le.redraw()
	}
}

func (le *LineEditor) deleteChar() {
	if le.cursor >= len(le.buf) {
		return
	}
	copy(le.buf[le.cursor:], le.buf[le.cursor+1:])
	le.buf = le.buf[:len(le.buf)-1]
	le.redraw()
}

func (le *LineEditor) deleteWord() {
	if le.cursor == 0 {
		return
	}
	// Skip trailing spaces
	end := le.cursor
	for le.cursor > 0 && le.buf[le.cursor-1] == ' ' {
		le.cursor--
	}
	// Skip word chars
	for le.cursor > 0 && le.buf[le.cursor-1] != ' ' {
		le.cursor--
	}
	copy(le.buf[le.cursor:], le.buf[end:])
	le.buf = le.buf[:len(le.buf)-(end-le.cursor)]
	le.redraw()
}

func (le *LineEditor) cursorLeft() {
	if le.cursor > 0 {
		le.cursor--
		fmt.Fprintf(le.out, "\033[%dD", overlay.RuneCells(le.buf[le.cursor]))
	}
}

func (le *LineEditor) cursorRight() {
	if le.cursor < len(le.buf) {
		fmt.Fprintf(le.out, "\033[%dC", overlay.RuneCells(le.buf[le.cursor]))
		le.cursor++
	}
}

func (le *LineEditor) cursorHome() {
	if le.cursor > 0 {
		fmt.Fprintf(le.out, "\033[%dD", cellWidth(le.buf[:le.cursor]))
		le.cursor = 0
	}
}

func (le *LineEditor) cursorEnd() {
	if le.cursor < len(le.buf) {
		fmt.Fprintf(le.out, "\033[%dC", cellWidth(le.buf[le.cursor:]))
		le.cursor = len(le.buf)
	}
}

func (le *LineEditor) addHistory(line string) {
	if line == "" {
		return
	}
	// Don't duplicate consecutive entries
	if len(le.hist) > 0 && le.hist[len(le.hist)-1] == line {
		return
	}
	le.hist = append(le.hist, line)
	// Cap history at 100
	if len(le.hist) > 100 {
		le.hist = le.hist[1:]
	}
}

func (le *LineEditor) historyUp() {
	if len(le.hist) == 0 {
		return
	}
	if le.histIdx == -1 {
		// Save current input
		le.saved = string(le.buf)
		le.histIdx = len(le.hist) - 1
	} else if le.histIdx > 0 {
		le.histIdx--
	} else {
		return
	}
	le.setBuffer(le.hist[le.histIdx])
}

func (le *LineEditor) historyDown() {
	if le.histIdx == -1 {
		return
	}
	if le.histIdx < len(le.hist)-1 {
		le.histIdx++
		le.setBuffer(le.hist[le.histIdx])
	} else {
		// Restore saved input
		le.histIdx = -1
		le.setBuffer(le.saved)
	}
}

func (le *LineEditor) setBuffer(s string) {
	le.buf = []rune(s)
	le.cursor = len(le.buf)
	le.redraw()
}
