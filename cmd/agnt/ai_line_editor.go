package main

import (
	"fmt"
	"io"
	"sync"

	"github.com/standardbeagle/agnt/internal/overlay"
)

// LineEditor provides line editing for raw mode terminal input.
// It handles cursor movement, history, and echoes through the overlay writer chain.
type LineEditor struct {
	mu      sync.Mutex
	buf     []byte   // Current line buffer
	cursor  int      // Cursor position within buf
	hist    []string // Command history
	histIdx int      // Current history index (-1 = current input)
	saved   string   // Saved current input when browsing history

	prompt string    // Prompt string (e.g., "> ")
	out    io.Writer // Output writer (overlay chain)
	active bool      // Whether editor accepts input

	lines chan string   // Submitted lines
	eof   chan struct{} // EOF signal (Ctrl+D)

	escReader *overlay.EscapeSequenceReader
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
	le.out.Write(le.buf)
	// Move cursor to correct position if not at end
	if le.cursor < len(le.buf) {
		back := len(le.buf) - le.cursor
		fmt.Fprintf(le.out, "\033[%dD", back)
	}
}

// Feed processes a raw byte from the input router.
// Escape sequences should be pre-parsed into key names via FeedKey.
func (le *LineEditor) Feed(b byte) {
	le.mu.Lock()
	if !le.active {
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
		le.addHistory(line)
		le.buf = le.buf[:0]
		le.cursor = 0
		le.histIdx = -1
		fmt.Fprint(le.out, "\r\n")
		le.mu.Unlock()
		le.lines <- line
		le.mu.Lock()

	case 0x03: // Ctrl+C
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
		if b >= 0x20 && b < 0x7f {
			le.insertChar(b)
		}
	}
}

func (le *LineEditor) insertChar(b byte) {
	if le.cursor == len(le.buf) {
		// Append at end — simple echo
		le.buf = append(le.buf, b)
		le.cursor++
		le.out.Write([]byte{b})
	} else {
		// Insert in middle — splice and redraw remainder
		le.buf = append(le.buf, 0)
		copy(le.buf[le.cursor+1:], le.buf[le.cursor:])
		le.buf[le.cursor] = b
		le.cursor++
		// Write from cursor to end, then move cursor back
		le.out.Write(le.buf[le.cursor-1:])
		back := len(le.buf) - le.cursor
		if back > 0 {
			fmt.Fprintf(le.out, "\033[%dD", back)
		}
	}
}

func (le *LineEditor) backspace() {
	if le.cursor == 0 {
		return
	}
	if le.cursor == len(le.buf) {
		// Delete last char
		le.buf = le.buf[:len(le.buf)-1]
		le.cursor--
		fmt.Fprint(le.out, "\b \b")
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
		fmt.Fprint(le.out, "\033[D")
	}
}

func (le *LineEditor) cursorRight() {
	if le.cursor < len(le.buf) {
		le.cursor++
		fmt.Fprint(le.out, "\033[C")
	}
}

func (le *LineEditor) cursorHome() {
	if le.cursor > 0 {
		fmt.Fprintf(le.out, "\033[%dD", le.cursor)
		le.cursor = 0
	}
}

func (le *LineEditor) cursorEnd() {
	if le.cursor < len(le.buf) {
		fmt.Fprintf(le.out, "\033[%dC", len(le.buf)-le.cursor)
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
	le.buf = []byte(s)
	le.cursor = len(le.buf)
	le.redraw()
}
