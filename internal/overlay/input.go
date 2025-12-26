package overlay

import (
	"context"
	"fmt"
	"io"
	"iter"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// BashRunner is an interface for running bash commands via the daemon.
type BashRunner interface {
	RunBashCommand(command string) (processID string, err error)
}

// ProcessOutputFetcher is an interface for fetching process output from the daemon.
type ProcessOutputFetcher interface {
	// GetProcessOutput fetches the last N lines of output for a process.
	GetProcessOutput(processID string, tailLines int) (string, error)
}

// DaemonConnector is an interface for connecting to and managing the daemon.
type DaemonConnector interface {
	// Connect attempts to connect to the daemon, auto-starting it if needed.
	// Returns nil on success, or an error describing why the connection failed.
	Connect() error
	// IsConnected returns true if currently connected to the daemon.
	IsConnected() bool
}

// StatusSummarizer is an interface for summarizing system status.
type StatusSummarizer interface {
	// Summarize aggregates all system data and generates a summary.
	Summarize(ctx context.Context) (*SummaryResult, error)
	// IsAvailable returns true if the AI channel is available.
	IsAvailable() bool
}

// InputRouter routes input between the PTY and the overlay.
type InputRouter struct {
	ptmx            PtyReadWriter
	overlay         *Overlay
	hotkey          byte
	running         atomic.Bool
	done            chan struct{}
	escReader       *EscapeSequenceReader
	bashRunner      BashRunner
	outputFetcher   ProcessOutputFetcher
	daemonConnector DaemonConnector
	statusFetcher   *StatusFetcher
	summarizer      StatusSummarizer

	// Process viewer state
	viewerActive bool

	// Last error from daemon connection attempt
	lastDaemonError string
}

// NewInputRouter creates a new InputRouter.
func NewInputRouter(ptmx PtyReadWriter, overlay *Overlay, hotkey byte) *InputRouter {
	return &InputRouter{
		ptmx:      ptmx,
		overlay:   overlay,
		hotkey:    hotkey,
		done:      make(chan struct{}),
		escReader: NewEscapeSequenceReader(),
	}
}

// SetBashRunner sets the bash runner for executing bash commands via the daemon.
func (r *InputRouter) SetBashRunner(runner BashRunner) {
	r.bashRunner = runner
}

// SetOutputFetcher sets the output fetcher for viewing process output.
func (r *InputRouter) SetOutputFetcher(fetcher ProcessOutputFetcher) {
	r.outputFetcher = fetcher
}

// SetDaemonConnector sets the daemon connector for connecting to the daemon.
func (r *InputRouter) SetDaemonConnector(connector DaemonConnector) {
	r.daemonConnector = connector
}

// SetStatusFetcher sets the status fetcher for refreshing after connection.
func (r *InputRouter) SetStatusFetcher(fetcher *StatusFetcher) {
	r.statusFetcher = fetcher
}

// SetSummarizer sets the summarizer for generating AI summaries.
func (r *InputRouter) SetSummarizer(summarizer StatusSummarizer) {
	r.summarizer = summarizer
}

// GetLastDaemonError returns the last error from daemon connection attempt.
func (r *InputRouter) GetLastDaemonError() string {
	return r.lastDaemonError
}

// Run starts routing input from stdin to either the overlay or PTY.
// This blocks until stdin is closed or Stop is called.
func (r *InputRouter) Run() error {
	r.running.Store(true)
	defer r.running.Store(false)

	inputCh := make(chan byte, 16)
	errCh := make(chan error, 1)

	// Start a goroutine to read from stdin using the win32-input-mode iterator.
	// The iterator handles buffer boundaries and escape sequence parsing internally.
	go func() {
		for b := range ScanWin32Input(os.Stdin) {
			inputCh <- b
		}
		errCh <- io.EOF
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

			// If process viewer is active, any key closes it
			if r.viewerActive {
				r.closeProcessViewer()
				continue
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

	// Check for 1-9 to view process output (only in main menu)
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
		processNum := int(key[0] - '0')
		r.overlay.hideMenu()
		r.overlay.mu.Unlock()
		r.showProcessViewer(processNum)
		r.overlay.mu.Lock()
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
		// Clear the parent menu before showing submenu
		r.overlay.renderer.ClearCurrentMenu()
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
			if r.bashRunner != nil {
				// Run the command via the daemon (tracked and logged)
				_, err := r.bashRunner.RunBashCommand(cmd)
				if err != nil {
					return err
				}
			} else {
				// Fallback: Type the command into the PTY
				io.WriteString(r.ptmx, cmd+"\n")
			}
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
		// Clear current menu before showing process list
		r.overlay.renderer.ClearCurrentMenu()
		status := r.overlay.GetStatus()
		menu := ProcessListMenu(status.Processes)
		r.overlay.menuStack = append(r.overlay.menuStack, menu)
		r.overlay.selectedIndex = 0
		r.overlay.draw()

	case ActionShowProxies:
		// Clear current menu before showing proxy list
		r.overlay.renderer.ClearCurrentMenu()
		status := r.overlay.GetStatus()
		menu := ProxyListMenu(status.Proxies)
		r.overlay.menuStack = append(r.overlay.menuStack, menu)
		r.overlay.selectedIndex = 0
		r.overlay.draw()

	case ActionConnectDaemon:
		r.lastDaemonError = ""
		if r.daemonConnector != nil {
			// Release lock during potentially slow operation
			r.overlay.mu.Unlock()
			err := r.daemonConnector.Connect()
			r.overlay.mu.Lock()

			if err != nil {
				r.lastDaemonError = err.Error()
				// Show error menu
				errorMenu := ErrorMenu("Connection Failed", err.Error())
				r.overlay.menuStack = append(r.overlay.menuStack, errorMenu)
				r.overlay.selectedIndex = 0
				r.overlay.draw()
			} else {
				// Connection successful - refresh status and switch to main menu
				r.overlay.mu.Unlock()
				if r.statusFetcher != nil {
					r.statusFetcher.Refresh()
				}
				r.overlay.mu.Lock()
				r.overlay.menuStack = []Menu{MainMenu()}
				r.overlay.selectedIndex = 0
				r.overlay.draw()
			}
		}

	case ActionSummarize:
		r.overlay.hideMenu()
		if r.summarizer == nil {
			// No summarizer configured - show error
			r.overlay.mu.Unlock()
			io.WriteString(r.ptmx, "\r\n[agnt] No AI summarizer configured\r\n")
			r.overlay.mu.Lock()
			return
		}
		if !r.summarizer.IsAvailable() {
			// AI agent not available
			r.overlay.mu.Unlock()
			io.WriteString(r.ptmx, "\r\n[agnt] AI agent not available in PATH\r\n")
			r.overlay.mu.Lock()
			return
		}

		// Release lock during AI call (can take time)
		r.overlay.mu.Unlock()

		// Start spinner in status bar
		spinnerDone := make(chan struct{})
		go func() {
			frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
			i := 0
			// Initial message on status bar
			r.overlay.DrawStatusBarMessage(fmt.Sprintf("%s Summarizing system status...", frames[0]))
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-spinnerDone:
					return
				case <-ticker.C:
					i = (i + 1) % len(frames)
					// Update spinner on status bar (in place)
					r.overlay.DrawStatusBarMessage(fmt.Sprintf("%s Summarizing system status...", frames[i]))
				}
			}
		}()

		// Call summarizer with 2 minute timeout
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		result, err := r.summarizer.Summarize(ctx)
		cancel()

		// Stop spinner and restore status bar
		close(spinnerDone)
		// Small delay to ensure spinner cleanup completes
		time.Sleep(50 * time.Millisecond)
		// Restore the normal status bar indicator
		r.overlay.RedrawIndicator()

		if err != nil {
			io.WriteString(r.ptmx, "\r\n[agnt] Summary failed: "+err.Error()+"\r\n")
		} else {
			// Inject summary into PTY
			io.WriteString(r.ptmx, "\r\n--- Status Summary ---\r\n")
			io.WriteString(r.ptmx, result.Summary)
			io.WriteString(r.ptmx, "\r\n--- End Summary ---\r\n")
		}
		r.overlay.mu.Lock()

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

// DebugWin32Input enables logging of win32-input-mode parsing
var DebugWin32Input = false

// parseWin32InputModeInternal parses Windows Terminal win32-input-mode sequences.
// Format: CSI Vk ; Sc ; Uc ; Kd ; Cs ; Rc _
// Where Uc is the unicode character value we want.
// Also filters out Focus In/Out sequences (CSI I and CSI O) that Windows Terminal sends.
// Returns parsed bytes and any incomplete sequence at the end that should be
// prepended to the next buffer read.
func parseWin32InputModeInternal(data []byte) ([]byte, []byte) {
	var result []byte
	i := 0
	foundWin32 := false

	if DebugWin32Input && len(data) > 0 {
		// Dump first 80 bytes of raw input
		dump := data
		if len(dump) > 80 {
			dump = dump[:80]
		}
		fmt.Fprintf(os.Stderr, "[win32] RAW INPUT (%d bytes): %q\r\n", len(data), dump)
	}

	for i < len(data) {
		// Check for ESC byte
		if data[i] == 0x1b {
			// We have an ESC - check what follows
			if DebugWin32Input {
				if i+1 < len(data) {
					fmt.Fprintf(os.Stderr, "[win32] ESC at i=%d, next byte=%d (0x%02x '%c')\r\n", i, data[i+1], data[i+1], printableChar(data[i+1]))
				} else {
					fmt.Fprintf(os.Stderr, "[win32] ESC at i=%d, no next byte (end of buffer) - saving as remainder\r\n", i)
				}
			}

			// If ESC is at the end of buffer, save it as remainder for next read
			if i+1 >= len(data) {
				if DebugWin32Input && foundWin32 {
					fmt.Fprintf(os.Stderr, "[win32] input %d bytes -> output %d bytes, remainder %d bytes\r\n", len(data), len(result), len(data)-i)
				}
				return result, data[i:]
			}

			// Check for CSI sequence (ESC [)
			if data[i+1] == '[' {
				if DebugWin32Input {
					fmt.Fprintf(os.Stderr, "[win32] CSI detected at i=%d\r\n", i)
				}

				// Check for Focus In (ESC [ I) or Focus Out (ESC [ O) - skip these
				if i+2 < len(data) && (data[i+2] == 'I' || data[i+2] == 'O') {
					if DebugWin32Input {
						fmt.Fprintf(os.Stderr, "[win32] skipping focus %c sequence\r\n", data[i+2])
					}
					i += 3
					continue
				}

				// Need at least one more byte after ESC[ to determine sequence type
				if i+2 >= len(data) {
					if DebugWin32Input {
						fmt.Fprintf(os.Stderr, "[win32] ESC[ at end of buffer - saving as remainder\r\n")
					}
					return result, data[i:]
				}

				// Look for win32-input-mode sequence ending with '_'
				end := -1
				hitInvalidChar := false
				for j := i + 2; j < len(data); j++ {
					if data[j] == '_' {
						end = j
						break
					}
					// If we hit another ESC or non-sequence char, stop looking
					if data[j] == 0x1b || (data[j] < '0' || data[j] > '9') && data[j] != ';' {
						if DebugWin32Input {
							fmt.Fprintf(os.Stderr, "[win32] search broke at j=%d byte=%d (0x%02x) - not a win32-input-mode sequence\r\n", j, data[j], data[j])
						}
						hitInvalidChar = true
						break
					}
				}

				if end > 0 {
					foundWin32 = true
					// Parse the sequence: Vk;Sc;Uc;Kd;Cs;Rc
					seq := string(data[i+2 : end])
					parts := splitSemicolon(seq)
					if len(parts) >= 4 {
						// Uc (unicode char) is the 3rd field (index 2)
						// Kd (key down) is the 4th field (index 3)
						uc := parseInt(parts[2])
						kd := parseInt(parts[3])
						// Only emit on key down (kd=1)
						if uc > 0 && kd == 1 {
							result = append(result, byte(uc))
							if DebugWin32Input {
								fmt.Fprintf(os.Stderr, "[win32] seq=%s -> byte %d (0x%02x)\r\n", seq, uc, uc)
							}
						} else if DebugWin32Input {
							// Log key-up events too
							fmt.Fprintf(os.Stderr, "[win32] seq=%s -> skipped (uc=%d, kd=%d)\r\n", seq, uc, kd)
						}
					}
					i = end + 1
					continue
				}

				// If we didn't find '_' and didn't hit an invalid char, the sequence
				// might be incomplete (split across buffer boundary)
				if !hitInvalidChar {
					if DebugWin32Input {
						fmt.Fprintf(os.Stderr, "[win32] incomplete CSI sequence at end of buffer - saving as remainder\r\n")
					}
					return result, data[i:]
				}
				// Otherwise it's not a win32-input-mode sequence, fall through to pass through
			}
		}

		// Regular byte - pass through
		if DebugWin32Input {
			fmt.Fprintf(os.Stderr, "[win32] passthrough byte %d (0x%02x) '%c'\r\n", data[i], data[i], printableChar(data[i]))
		}
		result = append(result, data[i])
		i++
	}
	if DebugWin32Input && foundWin32 {
		fmt.Fprintf(os.Stderr, "[win32] input %d bytes -> output %d bytes\r\n", len(data), len(result))
	}
	return result, nil
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
func ScanWin32Input(r io.Reader) iter.Seq[byte] {
	return func(yield func(byte) bool) {
		var pending []byte
		buf := make([]byte, 256)

		for {
			n, err := r.Read(buf)
			if n > 0 {
				// Combine pending bytes with new data
				var data []byte
				if len(pending) > 0 {
					data = make([]byte, len(pending)+n)
					copy(data, pending)
					copy(data[len(pending):], buf[:n])
				} else {
					data = buf[:n]
				}

				// Parse and yield bytes
				parsed, remainder := parseWin32InputModeInternal(data)
				pending = remainder

				for _, b := range parsed {
					if !yield(b) {
						return
					}
				}
			}

			if err != nil {
				// Yield any remaining pending bytes on EOF
				if err == io.EOF && len(pending) > 0 {
					for _, b := range pending {
						if !yield(b) {
							return
						}
					}
				}
				return
			}
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

// showProcessViewer shows the output of the Nth process on the alt screen.
func (r *InputRouter) showProcessViewer(n int) {
	if r.outputFetcher == nil {
		return
	}

	// Get the process list from overlay status
	status := r.overlay.GetStatus()
	if n < 1 || n > len(status.Processes) {
		return
	}

	proc := status.Processes[n-1]

	// Fetch the process output
	output, err := r.outputFetcher.GetProcessOutput(proc.ID, 100)
	if err != nil {
		output = "Error fetching output: " + err.Error()
	}

	// Enter alt screen and display output
	r.viewerActive = true
	r.overlay.renderer.EnterAltScreen()
	r.overlay.renderer.DrawProcessOutput(proc.ID, proc.Command, proc.State, output)
}

// closeProcessViewer closes the process viewer and returns to main screen.
func (r *InputRouter) closeProcessViewer() {
	if !r.viewerActive {
		return
	}
	r.viewerActive = false
	r.overlay.renderer.ExitAltScreen()
}
