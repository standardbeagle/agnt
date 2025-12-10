package overlay

import (
	"io"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// InputRouter routes input between the PTY and the overlay.
type InputRouter struct {
	ptmx      *os.File
	overlay   *Overlay
	hotkey    byte
	running   atomic.Bool
	done      chan struct{}
	escReader *EscapeSequenceReader
}

// NewInputRouter creates a new InputRouter.
func NewInputRouter(ptmx *os.File, overlay *Overlay, hotkey byte) *InputRouter {
	return &InputRouter{
		ptmx:      ptmx,
		overlay:   overlay,
		hotkey:    hotkey,
		done:      make(chan struct{}),
		escReader: NewEscapeSequenceReader(),
	}
}

// Run starts routing input from stdin to either the overlay or PTY.
// This blocks until stdin is closed or Stop is called.
func (r *InputRouter) Run() error {
	r.running.Store(true)
	defer r.running.Store(false)

	buf := make([]byte, 1)
	inputCh := make(chan byte, 16)
	errCh := make(chan error, 1)

	// Start a goroutine to read from stdin
	go func() {
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				errCh <- err
				return
			}
			if n > 0 {
				inputCh <- buf[0]
			}
		}
	}()

	// Escape sequence timeout
	const escTimeout = 50 * time.Millisecond
	var escTimer *time.Timer

	for {
		select {
		case <-r.done:
			return nil

		case err := <-errCh:
			if err == io.EOF {
				return nil
			}
			return err

		case <-func() <-chan time.Time {
			if escTimer != nil {
				return escTimer.C
			}
			return nil
		}():
			// Escape sequence timeout - treat as plain Escape
			escTimer = nil
			if key, hadPending := r.escReader.Timeout(); hadPending {
				r.handleMenuKey(key)
			}

		case b := <-inputCh:
			// Cancel any pending escape timer
			if escTimer != nil {
				escTimer.Stop()
				escTimer = nil
			}

			if r.overlay.IsActive() {
				// Overlay is capturing input
				r.handleOverlayInput(b)

				// If we're now waiting for more escape sequence bytes, start a timer
				if r.escReader.IsPending() {
					escTimer = time.NewTimer(escTimeout)
				}
			} else if b == r.hotkey {
				// Hotkey pressed - toggle overlay
				r.overlay.Toggle()
			} else {
				// Pass through to PTY
				r.ptmx.Write([]byte{b})
			}
		}
	}
}

// Stop stops the input router.
func (r *InputRouter) Stop() {
	if r.running.Load() {
		close(r.done)
	}
}

// handleOverlayInput processes input when overlay is active.
func (r *InputRouter) handleOverlayInput(b byte) {
	state := r.overlay.State()

	switch state {
	case StateMenu:
		// Use escape sequence reader to handle arrow keys properly
		key, complete := r.escReader.Feed(b)
		if complete && key != "" {
			r.handleMenuKey(key)
		}
	case StateInput:
		r.handleTextInput(b)
	}
}

// handleMenuKey handles parsed key input in menu mode.
func (r *InputRouter) handleMenuKey(key string) {
	r.overlay.mu.Lock()
	defer r.overlay.mu.Unlock()

	if len(r.overlay.menuStack) == 0 {
		return
	}

	menu := r.overlay.menuStack[len(r.overlay.menuStack)-1]

	// Handle "Escape+X" keys (when Escape is followed quickly by another key)
	if strings.HasPrefix(key, "Escape+") {
		// Just treat as Escape - close the menu
		r.overlay.hideMenu()
		return
	}

	switch key {
	case "Escape":
		r.overlay.hideMenu()
		return

	case "\r", "\n": // Enter
		if r.overlay.selectedIndex >= 0 && r.overlay.selectedIndex < len(menu.Items) {
			item := menu.Items[r.overlay.selectedIndex]
			r.executeMenuItem(item)
		}
		return

	case "Up", "k": // Up arrow or vim style
		if r.overlay.selectedIndex > 0 {
			r.overlay.selectedIndex--
			r.overlay.draw()
		}
		return

	case "Down", "j": // Down arrow or vim style
		if r.overlay.selectedIndex < len(menu.Items)-1 {
			r.overlay.selectedIndex++
			r.overlay.draw()
		}
		return

	case "q": // Quick close
		r.overlay.hideMenu()
		return
	}

	// Check for shortcut keys (single character keys)
	if len(key) == 1 {
		b := key[0]
		for i, item := range menu.Items {
			if item.Shortcut != 0 && (byte(item.Shortcut) == b || byte(item.Shortcut)|0x20 == b|0x20) {
				r.overlay.selectedIndex = i
				r.executeMenuItem(item)
				return
			}
		}
	}
}

// handleTextInput handles input in text input mode.
func (r *InputRouter) handleTextInput(b byte) {
	r.overlay.mu.Lock()
	defer r.overlay.mu.Unlock()

	switch b {
	case 0x1b: // Escape - cancel
		r.overlay.inputBuffer = ""
		r.overlay.hideMenu()
		return

	case 0x0d, 0x0a: // Enter - submit
		if r.overlay.inputAction != nil && r.overlay.inputBuffer != "" {
			action := r.overlay.inputAction
			value := r.overlay.inputBuffer
			r.overlay.inputBuffer = ""
			r.overlay.hideMenu()

			// Execute action outside of lock
			r.overlay.mu.Unlock()
			action(value)
			r.overlay.mu.Lock()
		}
		return

	case 0x7f, 0x08: // Backspace
		if len(r.overlay.inputBuffer) > 0 {
			r.overlay.inputBuffer = r.overlay.inputBuffer[:len(r.overlay.inputBuffer)-1]
			r.overlay.draw()
		}
		return

	case 0x15: // Ctrl+U - clear line
		r.overlay.inputBuffer = ""
		r.overlay.draw()
		return

	case 0x17: // Ctrl+W - delete word
		// Simple word deletion
		buf := r.overlay.inputBuffer
		for len(buf) > 0 && buf[len(buf)-1] == ' ' {
			buf = buf[:len(buf)-1]
		}
		for len(buf) > 0 && buf[len(buf)-1] != ' ' {
			buf = buf[:len(buf)-1]
		}
		r.overlay.inputBuffer = buf
		r.overlay.draw()
		return
	}

	// Regular character input
	if b >= 0x20 && b < 0x7f {
		r.overlay.inputBuffer += string(b)
		r.overlay.draw()
	}
}

// executeMenuItem executes the selected menu item.
func (r *InputRouter) executeMenuItem(item MenuItem) {
	// Handle sub-menu navigation
	if item.SubMenu != nil {
		r.overlay.menuStack = append(r.overlay.menuStack, *item.SubMenu)
		r.overlay.selectedIndex = 0
		r.overlay.draw()
		return
	}

	// Handle actions
	switch item.Action {
	case ActionClose:
		if len(r.overlay.menuStack) > 1 {
			// Pop sub-menu
			r.overlay.menuStack = r.overlay.menuStack[:len(r.overlay.menuStack)-1]
			r.overlay.selectedIndex = 0
			r.overlay.draw()
		} else {
			// Close overlay
			r.overlay.hideMenu()
		}

	case ActionBashCommand:
		r.overlay.state.Store(int32(StateInput))
		r.overlay.inputPrompt = "Bash Command"
		r.overlay.inputBuffer = ""
		r.overlay.inputAction = func(cmd string) error {
			// Type the command into the PTY
			r.ptmx.WriteString(cmd + "\n")
			return nil
		}
		r.overlay.draw()

	case ActionToggleIndicator:
		r.overlay.hideMenu()
		// Toggle is handled after hide
		r.overlay.mu.Unlock()
		r.overlay.ToggleIndicator()
		r.overlay.mu.Lock()

	case ActionRefreshStatus:
		// Trigger status refresh callback
		if r.overlay.onAction != nil {
			r.overlay.mu.Unlock()
			r.overlay.onAction(ActionRefreshStatus)
			r.overlay.mu.Lock()
		}
		r.overlay.draw()

	case ActionShowProcesses:
		status := r.overlay.GetStatus()
		menu := ProcessListMenu(status.Processes)
		r.overlay.menuStack = append(r.overlay.menuStack, menu)
		r.overlay.selectedIndex = 0
		r.overlay.draw()

	case ActionShowProxies:
		status := r.overlay.GetStatus()
		menu := ProxyListMenu(status.Proxies)
		r.overlay.menuStack = append(r.overlay.menuStack, menu)
		r.overlay.selectedIndex = 0
		r.overlay.draw()

	default:
		// Trigger action callback
		if r.overlay.onAction != nil {
			action := item.Action
			r.overlay.hideMenu()
			r.overlay.mu.Unlock()
			r.overlay.onAction(action)
			r.overlay.mu.Lock()
		}
	}
}

// EscapeSequenceReader helps parse escape sequences from input.
type EscapeSequenceReader struct {
	buffer []byte
	state  int
}

// NewEscapeSequenceReader creates a new escape sequence reader.
func NewEscapeSequenceReader() *EscapeSequenceReader {
	return &EscapeSequenceReader{
		buffer: make([]byte, 0, 8),
	}
}

// Feed feeds a byte into the reader and returns any recognized key.
func (r *EscapeSequenceReader) Feed(b byte) (key string, complete bool) {
	if r.state == 0 {
		if b == 0x1b {
			r.state = 1
			r.buffer = append(r.buffer[:0], b)
			return "", false
		}
		return string(b), true
	}

	r.buffer = append(r.buffer, b)

	// Check for common sequences
	seq := string(r.buffer)
	switch seq {
	case "\x1b[A":
		r.state = 0
		return "Up", true
	case "\x1b[B":
		r.state = 0
		return "Down", true
	case "\x1b[C":
		r.state = 0
		return "Right", true
	case "\x1b[D":
		r.state = 0
		return "Left", true
	case "\x1b[H":
		r.state = 0
		return "Home", true
	case "\x1b[F":
		r.state = 0
		return "End", true
	case "\x1b[3~":
		r.state = 0
		return "Delete", true
	}

	// After \x1b, if next byte is not '[', it's not a CSI sequence
	// Treat as Escape + that character (return Escape, re-feed next byte)
	if len(r.buffer) == 2 && r.buffer[1] != '[' {
		r.state = 0
		// Return Escape, and the next byte will be processed on next Feed call
		// We need to handle this byte too, so return both
		nextByte := r.buffer[1]
		r.buffer = r.buffer[:0]
		// Return Escape; caller should handle Escape and then process nextByte
		// For simplicity, we'll return Escape and lose the next byte
		// Better: return multiple results or use a different approach
		return "Escape+" + string(nextByte), true
	}

	// If we have too many bytes, it's probably not a valid sequence
	if len(r.buffer) > 6 {
		r.state = 0
		return "Escape", true
	}

	return "", false
}

// Timeout should be called when no more input arrives after starting an escape sequence.
// This allows treating a lone Escape key press as "Escape".
func (r *EscapeSequenceReader) Timeout() (key string, hadPending bool) {
	if r.state != 0 {
		r.state = 0
		r.buffer = r.buffer[:0]
		return "Escape", true
	}
	return "", false
}

// Reset resets the reader state.
func (r *EscapeSequenceReader) Reset() {
	r.state = 0
	r.buffer = r.buffer[:0]
}

// IsPending returns true if we're in the middle of parsing an escape sequence.
func (r *EscapeSequenceReader) IsPending() bool {
	return r.state != 0
}
