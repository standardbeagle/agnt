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

// DaemonClient provides a shared, reusable client connection to the daemon.
// Implemented by daemon.Conn via a thin adapter (cmd/agnt/pty_common.go).
// Uses non-chaining methods to avoid Go interface compatibility issues with
// concrete return types.
type DaemonClient interface {
	SocketPath() string
	EnsureConnected() error
	IsConnected() bool
	Close() error
	Disconnect() error
	Ping() error

	// RequestJSON sends a request with a JSON payload and optional additional arguments.
	// Args are verb sub-command and identifiers (e.g., "LIST", processID).
	RequestJSON(verb string, payload interface{}, args ...string) (map[string]interface{}, error)

	// RequestString sends a request with arguments and returns the response as a string.
	// All args are passed as protocol arguments (sub-verb, ID, key=value pairs, etc).
	RequestString(verb string, args ...string) (string, error)

	// RequestOK sends a request and returns nil on success.
	RequestOK(verb string, payload interface{}, args ...string) error
}

// BashRunner is an interface for running bash commands via the daemon.
type BashRunner interface {
	RunBashCommand(command string) (processID string, err error)
}

// ProcessOutputFetcher is an interface for fetching process output from the daemon.
type ProcessOutputFetcher interface {
	// GetProcessOutput fetches the last N lines of output for a process.
	GetProcessOutput(processID string, tailLines int) (string, error)
	// GetScriptOutput fetches the last N lines of output for a script by name.
	GetScriptOutput(scriptName string, tailLines int) (string, error)
}

// DaemonConnector is an interface for connecting to and managing the daemon.
type DaemonConnector interface {
	// Connect attempts to connect to the daemon, auto-starting it if needed.
	// Returns nil on success, or an error describing why the connection failed.
	Connect() error
	// IsConnected returns true if currently connected to the daemon.
	IsConnected() bool
}

// ScriptController is an interface for stopping, starting, and restarting scripts via the daemon.
type ScriptController interface {
	// StopScript stops a script by name.
	StopScript(name string) error
	// RestartScript restarts a script by name.
	RestartScript(name string) error
	// StartScript starts a stopped script by name.
	StartScript(name string) error
	// RunCommand runs an ad-hoc shell command as a background process.
	RunCommand(command string) error
}

// StatusSummarizer is an interface for summarizing system status.
type StatusSummarizer interface {
	// Summarize aggregates all system data and generates a summary.
	Summarize(ctx context.Context) (*SummaryResult, error)
	// IsAvailable returns true if the AI channel is available.
	IsAvailable() bool
}

// panelRefreshInterval is the polling period for live-refreshing process panels.
const panelRefreshInterval = 750 * time.Millisecond

// InputRouter routes input between the PTY and the overlay.
type InputRouter struct {
	ptmx             PtyReadWriter
	overlay          *Overlay
	hotkey           byte
	input            io.Reader // defaults to os.Stdin
	running          atomic.Bool
	done             chan struct{}
	escReader        *EscapeSequenceReader
	bashRunner       BashRunner
	outputFetcher    ProcessOutputFetcher
	daemonConnector  DaemonConnector
	scriptController ScriptController
	statusFetcher    *StatusFetcher
	summarizer       StatusSummarizer

	// Process viewer state
	viewerActive         bool
	viewerUsingAltScreen bool

	// Panel refresh ticker for live-tailing process output
	panelRefreshStop chan struct{} // closed to stop the refresh goroutine

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

// SetScriptController sets the script controller for stopping/restarting scripts.
func (r *InputRouter) SetScriptController(ctrl ScriptController) {
	r.scriptController = ctrl
}

// SetBashRunner sets the bash runner for executing bash commands via the daemon.
func (r *InputRouter) SetBashRunner(runner BashRunner) {
	r.bashRunner = runner
}

// SetOutputFetcher sets the output fetcher for viewing process output.
// Also wires up the before-unfreeze callback so the active process panel
// is refreshed from the daemon when the overlay closes.
func (r *InputRouter) SetOutputFetcher(fetcher ProcessOutputFetcher) {
	r.outputFetcher = fetcher
	r.overlay.SetBeforeUnfreezeCallback(func(panelID string) {
		if panelID == "" || r.outputFetcher == nil {
			return
		}
		r.eagerRefreshPanel(panelID)
	})
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

	// Start a goroutine to read from the input source using the win32-input-mode
	// iterator. The iterator handles buffer boundaries and escape sequence parsing.
	inputSrc := r.input
	if inputSrc == nil {
		inputSrc = os.Stdin
	}
	go func() {
		for b := range ScanWin32Input(inputSrc) {
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
				// Pass through to PTY - batch with any pending bytes to keep
				// multi-byte escape sequences (e.g. arrow keys \x1b[A) intact.
				buf := []byte{b}

				// If this looks like the start of an escape sequence, give the
				// producer goroutine a moment to push the rest of the sequence
				// into the channel before we drain.
				if b == 0x1b {
					time.Sleep(1 * time.Millisecond)
				}

				drain := true
				for drain {
					select {
					case nb := <-inputCh:
						if nb == r.hotkey {
							r.ptmx.Write(buf)
							buf = nil
							r.overlay.Toggle()
							drain = false
						} else {
							buf = append(buf, nb)
						}
					default:
						drain = false
					}
				}
				if len(buf) > 0 {
					// Intercept Ctrl+Arrow for direct panel browsing
					if delta := panelShortcutDelta(buf); delta != 0 {
						r.enterPanelBrowse(delta)
						continue
					}
					r.ptmx.Write(buf)
				}
			}
		}
	}
}

// Stop stops the input router.
func (r *InputRouter) Stop() {
	r.overlay.mu.Lock()
	r.stopPanelRefresh()
	r.overlay.mu.Unlock()
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
		r.stopPanelRefresh()
		r.overlay.hideMenu()
		return
	}

	switch key {
	case "Escape":
		if r.overlay.panelMode {
			r.exitPanelMode()
			return
		}
		r.overlay.hideMenu()
		return

	case "Ctrl+Right", "\t": // Ctrl+Right or Tab: next panel
		r.handlePanelNav(1)
		return

	case "Ctrl+Left", "Shift+Tab": // Ctrl+Left or Shift+Tab: previous panel
		r.handlePanelNav(-1)
		return

	case "\r", "\n": // Enter
		if r.overlay.panelMode {
			if r.isOverviewWithScripts() {
				r.overviewEnter()
				return
			}
			return // No-op in other panel views
		}
		if r.overlay.selectedIndex >= 0 && r.overlay.selectedIndex < len(menu.Items) {
			item := menu.Items[r.overlay.selectedIndex]
			r.executeMenuItem(item)
		}
		return

	case "Up", "k": // Up arrow or vim style
		if r.overlay.panelMode {
			if r.isOverviewWithScripts() {
				if r.overlay.overviewSelectedIdx > 0 {
					r.overlay.overviewSelectedIdx--
					r.overlay.draw()
				}
				return
			}
			// Scroll up in panel content
			if r.overlay.panelIndex >= 0 && r.overlay.panelIndex < len(r.overlay.panelItems) {
				panel := &r.overlay.panelItems[r.overlay.panelIndex]
				if panel.ScrollOffset < panel.ContentLines()-1 {
					panel.ScrollOffset++
					r.overlay.draw()
				}
			}
			return
		}
		if r.overlay.selectedIndex > 0 {
			r.overlay.selectedIndex--
			r.overlay.draw()
		}
		return

	case "Down", "j": // Down arrow or vim style
		if r.overlay.panelMode {
			if r.isOverviewWithScripts() {
				r.overlay.statusMu.RLock()
				scriptCount := len(r.overlay.status.Scripts)
				r.overlay.statusMu.RUnlock()
				if r.overlay.overviewSelectedIdx < scriptCount-1 {
					r.overlay.overviewSelectedIdx++
					r.overlay.draw()
				}
				return
			}
			// Scroll down in panel content (toward bottom)
			if r.overlay.panelIndex >= 0 && r.overlay.panelIndex < len(r.overlay.panelItems) {
				panel := &r.overlay.panelItems[r.overlay.panelIndex]
				if panel.ScrollOffset > 0 {
					panel.ScrollOffset--
					r.overlay.draw()
				}
			}
			return
		}
		if r.overlay.selectedIndex < len(menu.Items)-1 {
			r.overlay.selectedIndex++
			r.overlay.draw()
		}
		return

	case "Right": // Plain right arrow enters panel mode on first panel
		if !r.overlay.panelMode && len(r.overlay.panelItems) > 1 {
			r.handlePanelNav(1)
			return
		}

	case "Left": // Plain left arrow in panel mode goes back
		if r.overlay.panelMode {
			r.handlePanelNav(-1)
			return
		}

	case "q": // Quick close
		if r.overlay.panelMode {
			r.exitPanelMode()
			return
		}
		r.overlay.hideMenu()
		return

	case "x": // Close current panel (only if process has stopped)
		if r.overlay.panelMode {
			r.closeCurrentPanel()
			return
		}
	}

	// Check for 1-9 to view process output
	// In panel mode: jump to the Nth process panel
	// In menu mode: open the legacy process viewer
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
		processNum := int(key[0] - '0')

		// Try to jump to the process panel directly
		if len(r.overlay.panelItems) > 0 {
			procIdx := 0
			for i, p := range r.overlay.panelItems {
				if p.Type == "process" {
					procIdx++
					if procIdx == processNum {
						// Navigate directly to this panel
						r.overlay.panelIndex = i
						r.overlay.panelMode = true

						panel := &r.overlay.panelItems[i]
						if panel.Type == "process" && r.outputFetcher != nil {
							r.fetchScriptOutput(panel)
							r.startPanelRefresh(panel.ID)
						}

						r.overlay.renderer.ClearScreen()
						r.overlay.draw()
						return
					}
				}
			}
		}

		// Fallback: legacy process viewer
		r.overlay.hideMenu()
		r.overlay.mu.Unlock()
		r.showProcessViewer(processNum)
		r.overlay.mu.Lock()
		return
	}

	// Overview panel: intercept 's' (stop) and 'r' (restart) for selected script
	// Command input mode: all keys go to the command buffer
	if r.overlay.commandInput {
		r.handleCommandInput(key)
		return
	}

	if r.isOverviewWithScripts() && len(key) == 1 {
		switch key[0] {
		case 's', 'S':
			r.overviewStopScript()
			return
		case 'r', 'R':
			r.overviewRestartScript()
			return
		case 'a', 'A':
			r.overviewStartScript()
			return
		case ':', '/':
			r.overlay.commandInput = true
			r.overlay.commandBuffer = ""
			r.overlay.draw()
			return
		}
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

// exitPanelMode closes panel mode entirely.
// Must be called with overlay.mu held.
func (r *InputRouter) exitPanelMode() {
	r.stopPanelRefresh()
	r.overlay.hideMenu()
}

// isOverviewWithScripts returns true if the overlay is in panel mode on the
// overview panel and there are scripts to select.
// Must be called with overlay.mu held.
func (r *InputRouter) isOverviewWithScripts() bool {
	if !r.overlay.panelMode || r.overlay.panelIndex != 0 {
		return false
	}
	if len(r.overlay.panelItems) == 0 || r.overlay.panelItems[0].Type != "overview" {
		return false
	}
	r.overlay.statusMu.RLock()
	n := len(r.overlay.status.Scripts)
	r.overlay.statusMu.RUnlock()
	return n > 0
}

// selectedScript returns the ScriptInfo at the current overview selection index,
// or nil if out of range.
// Must be called with overlay.mu held.
func (r *InputRouter) selectedScript() *ScriptInfo {
	r.overlay.statusMu.RLock()
	defer r.overlay.statusMu.RUnlock()
	idx := r.overlay.overviewSelectedIdx
	if idx < 0 || idx >= len(r.overlay.status.Scripts) {
		return nil
	}
	s := r.overlay.status.Scripts[idx]
	return &s
}

// overviewEnter navigates to the panel for the selected script.
// Must be called with overlay.mu held.
func (r *InputRouter) overviewEnter() {
	script := r.selectedScript()
	if script == nil {
		return
	}
	// Find the panel matching this script name
	for i, p := range r.overlay.panelItems {
		if p.Type == "process" && p.ID == script.Name {
			r.overlay.panelIndex = i
			panel := &r.overlay.panelItems[i]
			if r.outputFetcher != nil {
				r.fetchScriptOutput(panel)
				r.startPanelRefresh(panel.ID)
			}
			r.overlay.renderer.ClearScreen()
			r.overlay.draw()
			return
		}
	}
}

// overviewStopScript stops the selected script via the daemon.
// Must be called with overlay.mu held.
func (r *InputRouter) overviewStopScript() {
	if r.scriptController == nil {
		return
	}
	script := r.selectedScript()
	if script == nil {
		return
	}
	name := script.Name
	r.overlay.mu.Unlock()
	_ = r.scriptController.StopScript(name)
	r.overlay.mu.Lock()
	r.overlay.draw()
}

// overviewRestartScript restarts the selected script via the daemon.
// Must be called with overlay.mu held.
func (r *InputRouter) overviewRestartScript() {
	if r.scriptController == nil {
		return
	}
	script := r.selectedScript()
	if script == nil {
		return
	}
	name := script.Name
	r.overlay.mu.Unlock()
	_ = r.scriptController.RestartScript(name)
	r.overlay.mu.Lock()
	r.overlay.draw()
}

// overviewStartScript starts the selected stopped script via the daemon.
func (r *InputRouter) overviewStartScript() {
	if r.scriptController == nil {
		return
	}
	script := r.selectedScript()
	if script == nil {
		return
	}
	name := script.Name
	r.overlay.mu.Unlock()
	_ = r.scriptController.StartScript(name)
	r.overlay.mu.Lock()
	r.overlay.draw()
}

// handleCommandInput processes keystrokes while the command input is active.
func (r *InputRouter) handleCommandInput(key string) {
	switch {
	case key == "Escape" || (len(key) == 1 && key[0] == 27):
		r.overlay.commandInput = false
		r.overlay.commandBuffer = ""
		r.overlay.draw()
	case key == "Enter" || (len(key) == 1 && key[0] == 13):
		cmd := r.overlay.commandBuffer
		r.overlay.commandInput = false
		r.overlay.commandBuffer = ""
		if cmd != "" && r.scriptController != nil {
			r.overlay.mu.Unlock()
			_ = r.scriptController.RunCommand(cmd)
			r.overlay.mu.Lock()
		}
		r.overlay.draw()
	case key == "Backspace" || (len(key) == 1 && key[0] == 127):
		if len(r.overlay.commandBuffer) > 0 {
			r.overlay.commandBuffer = r.overlay.commandBuffer[:len(r.overlay.commandBuffer)-1]
			r.overlay.draw()
		}
	case len(key) == 1 && key[0] >= 32 && key[0] < 127: // Printable ASCII
		r.overlay.commandBuffer += key
		r.overlay.draw()
	}
}

// fetchScriptOutput fetches script output and replaces the panel's content buffer.
// Script output includes restart markers from the ScriptRegistry, so no diffing is needed.
// Must be called with overlay.mu held (temporarily releases during I/O).
const maxPanelLines = 2000

func (r *InputRouter) fetchScriptOutput(panel *PanelItem) {
	if r.outputFetcher == nil {
		return
	}
	r.overlay.mu.Unlock()
	output, err := r.outputFetcher.GetScriptOutput(panel.ID, maxPanelLines)
	r.overlay.mu.Lock()

	if err != nil || output == "" {
		return
	}
	output = strings.TrimRight(output, "\n")
	panel.SetContent(output)
}

// startPanelRefresh starts a background goroutine that periodically polls
// script output for the given process panel and redraws on change.
// Must be called with overlay.mu held.
func (r *InputRouter) startPanelRefresh(panelID string) {
	r.stopPanelRefresh()
	if r.outputFetcher == nil {
		return
	}
	stop := make(chan struct{})
	r.panelRefreshStop = stop

	go r.runPanelRefresh(stop, panelID)
}

// stopPanelRefresh stops any running panel refresh goroutine.
// Safe to call when no refresh is active.
func (r *InputRouter) stopPanelRefresh() {
	if r.panelRefreshStop != nil {
		close(r.panelRefreshStop)
		r.panelRefreshStop = nil
	}
}

// runPanelRefresh is the background loop that polls script output.
func (r *InputRouter) runPanelRefresh(stop <-chan struct{}, panelID string) {
	ticker := time.NewTicker(panelRefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			r.refreshPanelContent(panelID)
		}
	}
}

// refreshPanelContent fetches the latest output for a process panel and
// redraws if the content changed. Respects ScrollOffset (only auto-scrolls
// when the user is pinned to the bottom).
func (r *InputRouter) refreshPanelContent(panelID string) {
	output, err := r.outputFetcher.GetScriptOutput(panelID, maxPanelLines)
	if err != nil {
		return
	}
	output = strings.TrimRight(output, "\n")

	r.overlay.mu.Lock()
	defer r.overlay.mu.Unlock()

	// Verify panel is still the active process panel
	if !r.overlay.panelMode || r.overlay.panelIndex >= len(r.overlay.panelItems) {
		return
	}
	panel := &r.overlay.panelItems[r.overlay.panelIndex]
	if panel.Type != "process" || panel.ID != panelID {
		return
	}

	// Check if the process state changed (e.g., failed -> running after restart)
	stateChanged := false
	r.overlay.statusMu.RLock()
	for _, s := range r.overlay.status.Scripts {
		if s.Name == panelID && s.State != panel.ProcessState {
			panel.ProcessState = s.State
			stateChanged = true
			break
		}
	}
	r.overlay.statusMu.RUnlock()

	// Skip redraw when content and state are both unchanged
	if panel.Content == output && !stateChanged {
		return
	}

	wasAtBottom := panel.ScrollOffset == 0
	if panel.Content != output {
		panel.SetContent(output)
		if wasAtBottom {
			panel.ScrollOffset = 0
		}
	}

	// Full redraw when state changed (header needs update) or no diff cache
	if stateChanged || !r.overlay.renderer.RefreshPanelContent(*panel) {
		r.overlay.draw()
	}
}

// eagerRefreshPanel fetches the latest output for a process panel by ID.
// Unlike refreshPanelContent, this does not check panelMode since it runs
// during the hide transition when panelMode has already been cleared.
// Called from the before-unfreeze callback without overlay.mu held.
func (r *InputRouter) eagerRefreshPanel(panelID string) {
	output, err := r.outputFetcher.GetScriptOutput(panelID, maxPanelLines)
	if err != nil {
		return
	}
	output = strings.TrimRight(output, "\n")

	r.overlay.mu.Lock()
	defer r.overlay.mu.Unlock()

	for i := range r.overlay.panelItems {
		p := &r.overlay.panelItems[i]
		if p.Type == "process" && p.ID == panelID {
			p.SetContent(output)
			return
		}
	}
}

// closeCurrentPanel removes the current panel if the process has stopped.
// Must be called with overlay.mu held.
func (r *InputRouter) closeCurrentPanel() {
	if !r.overlay.panelMode || r.overlay.panelIndex >= len(r.overlay.panelItems) {
		return
	}
	panel := r.overlay.panelItems[r.overlay.panelIndex]
	if !panel.IsDone() {
		return // Can't close running process panels
	}
	r.stopPanelRefresh()
	// Remove the panel
	r.overlay.panelItems = append(
		r.overlay.panelItems[:r.overlay.panelIndex],
		r.overlay.panelItems[r.overlay.panelIndex+1:]...,
	)
	// Adjust index
	if r.overlay.panelIndex >= len(r.overlay.panelItems) {
		r.overlay.panelIndex = len(r.overlay.panelItems) - 1
	}
	if r.overlay.panelIndex < 0 {
		r.overlay.panelIndex = 0
	}
	if len(r.overlay.panelItems) == 0 {
		r.exitPanelMode()
		return
	}
	r.overlay.renderer.ClearScreen()
	r.overlay.draw()
}

// handlePanelNav handles Ctrl+Left/Right panel navigation.
// Must be called with overlay.mu held.
func (r *InputRouter) handlePanelNav(delta int) {
	if len(r.overlay.panelItems) == 0 {
		r.overlay.buildPanelItems()
	}
	if len(r.overlay.panelItems) <= 1 {
		return
	}

	wasInPanelMode := r.overlay.panelMode

	newIndex := r.overlay.panelIndex + delta
	if newIndex < 0 {
		if r.overlay.panelMode {
			r.exitPanelMode()
		}
		return
	}
	if newIndex >= len(r.overlay.panelItems) {
		return
	}

	r.overlay.panelIndex = newIndex
	r.overlay.panelMode = true

	// Fetch content for the focused panel and manage refresh ticker
	panel := &r.overlay.panelItems[newIndex]
	if panel.Type == "process" && r.outputFetcher != nil {
		r.fetchScriptOutput(panel)
		r.startPanelRefresh(panel.ID)
	} else {
		r.stopPanelRefresh()
	}

	if !wasInPanelMode {
		// Clear the dashboard before drawing panel view
		r.overlay.renderer.ClearScreen()
	}
	r.overlay.draw()
}

// panelShortcutDelta checks if a collected input buffer matches a panel
// navigation shortcut (Ctrl+Right or Ctrl+Left). Returns +1, -1, or 0.
func panelShortcutDelta(buf []byte) int {
	s := string(buf)
	switch s {
	case "\x1b[1;5C": // Ctrl+Right
		return 1
	case "\x1b[1;5D": // Ctrl+Left
		return -1
	}
	return 0
}

// enterPanelBrowse opens the overlay directly into panel mode, bypassing the
// menu overview. Called when Ctrl+Arrow is pressed from the indicator state.
func (r *InputRouter) enterPanelBrowse(delta int) {
	r.overlay.mu.Lock()
	defer r.overlay.mu.Unlock()

	r.overlay.showPanelDirect()

	// Start at overview (index 0)
	r.overlay.panelMode = true
	r.overlay.panelIndex = 0
	r.overlay.renderer.ClearScreen()
	r.overlay.draw()
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

// win32ParseState holds state during win32 input parsing.
type win32ParseState struct {
	result     []byte
	foundWin32 bool
}

// debugLog logs a message if DebugWin32Input is enabled.
func debugLog(format string, args ...interface{}) {
	if DebugWin32Input {
		fmt.Fprintf(os.Stderr, "[win32] "+format+"\r\n", args...)
	}
}

// parseWin32InputModeInternal parses Windows Terminal win32-input-mode sequences.
// Format: CSI Vk ; Sc ; Uc ; Kd ; Cs ; Rc _
// Where Uc is the unicode character value we want.
// Also filters out Focus In/Out sequences (CSI I and CSI O) that Windows Terminal sends.
// Returns parsed bytes and any incomplete sequence at the end that should be
// prepended to the next buffer read.
func parseWin32InputModeInternal(data []byte) ([]byte, []byte) {
	if DebugWin32Input && len(data) > 0 {
		dump := data
		if len(dump) > 80 {
			dump = dump[:80]
		}
		debugLog("RAW INPUT (%d bytes): %q", len(data), dump)
	}

	state := &win32ParseState{}
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

	// If there's a unicode character, emit it directly
	if uc > 0 {
		s.result = append(s.result, byte(uc))
		debugLog("seq=%s -> byte %d (0x%02x)", seq, uc, uc)
		return
	}

	// No unicode char (uc=0) — check for virtual keys that need ANSI translation
	cs := 0
	if len(parts) >= 5 {
		cs = parseInt(parts[4])
	}
	if ansi := vkToANSI(vk, cs); ansi != nil {
		s.result = append(s.result, ansi...)
		debugLog("seq=%s -> vk=%d cs=%d ansi=%q", seq, vk, cs, ansi)
		return
	}

	debugLog("seq=%s -> skipped (uc=0, vk=%d, no mapping)", seq, vk)
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
	// Ctrl+Arrow sequences (xterm-style)
	case "\x1b[1;5C":
		r.state = 0
		return "Ctrl+Right", true
	case "\x1b[1;5D":
		r.state = 0
		return "Ctrl+Left", true
	case "\x1b[1;5A":
		r.state = 0
		return "Ctrl+Up", true
	case "\x1b[1;5B":
		r.state = 0
		return "Ctrl+Down", true
	// Shift+Tab (backtab)
	case "\x1b[Z":
		r.state = 0
		return "Shift+Tab", true
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

// showProcessViewer shows the output of the Nth script as a full-screen overlay.
func (r *InputRouter) showProcessViewer(n int) {
	if r.outputFetcher == nil {
		return
	}

	// Get the script list from overlay status
	status := r.overlay.GetStatus()
	if n < 1 || n > len(status.Scripts) {
		return
	}

	script := status.Scripts[n-1]

	// Fetch the script output
	output, err := r.outputFetcher.GetScriptOutput(script.Name, 100)
	if err != nil {
		output = "Error fetching output: " + err.Error()
	}

	r.viewerActive = true

	// Freeze gate to prevent PTY output from corrupting the viewer
	if r.overlay.gate != nil {
		r.overlay.gate.Freeze()
	}

	// Use alt screen when child is on main screen (restores content on close).
	// When child is in alt screen (fullscreen app), draw directly.
	r.viewerUsingAltScreen = !r.overlay.isChildInAltScreen()
	if r.viewerUsingAltScreen {
		r.overlay.renderer.EnterAltScreen()
	}

	r.overlay.renderer.DrawProcessOutput(script.Name, script.Command, script.State, output)
}

// closeProcessViewer closes the process viewer.
func (r *InputRouter) closeProcessViewer() {
	if !r.viewerActive {
		return
	}
	r.viewerActive = false

	if r.viewerUsingAltScreen {
		r.overlay.renderer.ExitAltScreen()
	} else {
		r.overlay.renderer.ClearVisible()
	}

	// Unfreeze gate — callback sends SIGWINCH and re-enforces scroll region.
	if r.overlay.gate != nil {
		r.overlay.gate.Unfreeze()
	}

	// Redraw indicator bar
	if r.overlay.showBar.Load() {
		r.overlay.statusMu.RLock()
		status := r.overlay.status
		r.overlay.statusMu.RUnlock()
		r.overlay.renderer.DrawIndicator(status)
	}
}
