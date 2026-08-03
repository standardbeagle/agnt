package overlay

import (
	"context"
	"fmt"
	"io"
	"iter"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
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
	// KillPort kills whatever process is listening on the given TCP port.
	KillPort(port int) error
	// CleanOrphans reaps orphaned process groups (leader dead, members alive).
	CleanOrphans() error
	// RestartProxy restarts a reverse proxy by ID.
	RestartProxy(id string) error
	// StopProxy stops a reverse proxy by ID.
	StopProxy(id string) error
	// StopTunnel stops a tunnel by ID.
	StopTunnel(id string) error
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
	input            io.Reader // defaults to os.Stdin
	running          atomic.Bool
	done             chan struct{}
	stopOnce         sync.Once
	escReader        *EscapeSequenceReader
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

	// Forwarding pause: stop pushing errors/notifications to the agent when the
	// flood overwhelms it. forwardingToggle applies the state across both
	// delivery paths (in-process PTY + daemon push); forwardPaused is the local
	// display state. Set via SetForwardingToggle.
	forwardingToggle func(paused bool)
	forwardPaused    bool

	// modalInput, when set, diverts raw input bytes to the referenced channel
	// instead of the normal PTY/overlay handling. It exists so a one-off
	// startup prompt (e.g. the port-conflict Y/n question) can read a
	// keystroke without spawning a second os.Stdin reader that would race the
	// router's producer goroutine for the answer byte. Only one modal capture
	// is active at a time.
	modalInput atomic.Pointer[chan byte]
}

// IsRunning reports whether the router's Run loop is active.
func (r *InputRouter) IsRunning() bool { return r.running.Load() }

// BeginModalCapture diverts subsequent input bytes to the returned channel
// until EndModalCapture is called. This lets a startup prompt own stdin input
// while the router remains the single reader of os.Stdin.
func (r *InputRouter) BeginModalCapture() <-chan byte {
	ch := make(chan byte, 16)
	r.modalInput.Store(&ch)
	return ch
}

// EndModalCapture restores normal input handling after a modal capture.
func (r *InputRouter) EndModalCapture() {
	r.modalInput.Store(nil)
}

// NewInputRouter creates a new InputRouter.
func NewInputRouter(ptmx PtyReadWriter, overlay *Overlay) *InputRouter {
	return &InputRouter{
		ptmx:      ptmx,
		overlay:   overlay,
		done:      make(chan struct{}),
		escReader: NewEscapeSequenceReader(),
	}
}

// SetScriptController sets the script controller for stopping/restarting scripts.
func (r *InputRouter) SetScriptController(ctrl ScriptController) {
	r.scriptController = ctrl
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

// SetForwardingToggle wires the callback that pauses/resumes agent-inbound push
// (errors + notifications) across the in-process and daemon delivery paths.
func (r *InputRouter) SetForwardingToggle(fn func(paused bool)) {
	r.forwardingToggle = fn
}

// setForwarding applies a forwarding pause state: invokes the toggle, updates
// the persistent indicator, and shows a transient confirmation. Caller need not
// hold overlay.mu — this acquires nothing that conflicts (the toggle does its
// own IPC; the overlay setters lock internally).
func (r *InputRouter) setForwarding(paused bool) {
	r.forwardPaused = paused
	if r.forwardingToggle != nil {
		r.forwardingToggle(paused)
	}
	r.overlay.SetForwardPaused(paused)
	if paused {
		r.overlay.DrawStatusBarMessage("🔇 agent forwarding paused — errors still pullable via get_incidents")
	} else {
		r.overlay.DrawStatusBarMessage("🔊 agent forwarding resumed")
	}
}

// SetSummarizer sets the summarizer for generating AI summaries and enables the
// overview 'm' action affordance.
func (r *InputRouter) SetSummarizer(summarizer StatusSummarizer) {
	r.summarizer = summarizer
	r.overlay.SetSummarizeEnabled(summarizer != nil)
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

	// Hoisted out of the select: a closure in the case expression would
	// allocate on every loop iteration.
	var escTimerC <-chan time.Time

	for {
		if escTimer != nil {
			escTimerC = escTimer.C
		} else {
			escTimerC = nil
		}
		select {
		case <-r.done:
			return nil

		case err := <-errCh:
			if err == io.EOF {
				return nil
			}
			return err

		case <-escTimerC:
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

			// Modal capture: a startup prompt owns input. Divert the byte to
			// it and skip normal handling so the prompt and the router never
			// both consume from os.Stdin.
			if mc := r.modalInput.Load(); mc != nil {
				select {
				case *mc <- b:
				default:
				}
				continue
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
						buf = append(buf, nb)
					default:
						drain = false
					}
				}
				if len(buf) > 0 {
					// Intercept Ctrl+Up/Down to pause/resume agent forwarding,
					// even mid-passthrough, so an overwhelmed agent can be muted
					// instantly without opening the overlay.
					if paused, ok := forwardingHotkey(buf); ok {
						r.setForwarding(paused)
						continue
					}
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

// Stop stops the input router. Idempotent: pipelineRuntime.Stop is documented
// safe to call multiple times, and a bare close(r.done) would panic on the
// second call.
func (r *InputRouter) Stop() {
	r.overlay.mu.Lock()
	r.stopPanelRefresh()
	r.overlay.mu.Unlock()
	r.stopOnce.Do(func() {
		close(r.done)
	})
}

// handleOverlayInput processes input when overlay is active.
func (r *InputRouter) handleOverlayInput(b byte) {
	state := r.overlay.State()

	if state == StateMenu {
		// Use escape sequence reader to handle arrow keys properly
		key, complete := r.escReader.Feed(b)
		if complete && key != "" {
			r.handleMenuKey(key)
		}
	}
}

// handleMenuKey handles parsed key input in panel-browser mode.
func (r *InputRouter) handleMenuKey(key string) {
	r.overlay.mu.Lock()
	defer r.overlay.mu.Unlock()

	// Command palette captures ALL keys while active. This must run before the
	// global menu switch below: otherwise Enter/Up/Down/q/x/1-9 are stolen by
	// panel navigation (the original bug — Enter selected a script instead of
	// running the typed command).
	if r.overlay.commandInput {
		r.handleCommandInput(key)
		return
	}

	// Handle "Escape+X" keys (when Escape is followed quickly by another key)
	if strings.HasPrefix(key, "Escape+") {
		// Treat as Escape - close the panel view
		r.stopPanelRefresh()
		r.overlay.hideMenu()
		return
	}

	switch key {
	case "Escape":
		r.exitPanelMode()
		return

	case "Ctrl+Right", "\t": // Ctrl+Right or Tab: next panel
		r.handlePanelNav(1)
		return

	case "Ctrl+Left", "Shift+Tab": // Ctrl+Left or Shift+Tab: previous panel
		r.handlePanelNav(-1)
		return

	case "\r", "\n": // Enter
		if r.isOverviewWithScripts() {
			r.overviewEnter()
		}
		return

	case "Up", "k": // Up arrow or vim style
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

	case "Down", "j": // Down arrow or vim style
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

	case "Left": // Plain left arrow goes back a panel
		r.handlePanelNav(-1)
		return

	case "q": // Quick close
		r.exitPanelMode()
		return

	case "x": // Close current panel (only if process has stopped)
		r.closeCurrentPanel()
		return
	}

	// 1-9: jump to the Nth process panel
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
		processNum := int(key[0] - '0')
		procIdx := 0
		for i, p := range r.overlay.panelItems {
			if p.Type == "process" {
				procIdx++
				if procIdx == processNum {
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
		return
	}

	// Overview panel global actions (work with or without scripts)
	if r.isOverviewPanel() && len(key) == 1 {
		switch key[0] {
		case 'm', 'M':
			r.startSummarize()
			return
		case 'c', 'C':
			r.startReconnect()
			return
		case ':', '/':
			r.overlay.commandInput = true
			r.overlay.commandBuffer = ""
			r.overlay.commandSelectedIdx = 0
			r.overlay.draw()
			return
		}
	}

	// Overview panel script actions (require a selected script)
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
	if err := r.scriptController.StopScript(name); err != nil {
		debug.Log("overlay", "StopScript %q failed: %v", name, err)
	}
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
	if err := r.scriptController.RestartScript(name); err != nil {
		debug.Log("overlay", "RestartScript %q failed: %v", name, err)
	}
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
	if err := r.scriptController.StartScript(name); err != nil {
		debug.Log("overlay", "StartScript %q failed: %v", name, err)
	}
	r.overlay.mu.Lock()
	r.overlay.draw()
}

// isOverviewPanel reports whether the overview panel is focused. Unlike
// isOverviewWithScripts it does not require any scripts, so global actions
// (summarize / reconnect) work on an empty project.
// Must be called with overlay.mu held.
func (r *InputRouter) isOverviewPanel() bool {
	if !r.overlay.panelMode || r.overlay.panelIndex != 0 {
		return false
	}
	return len(r.overlay.panelItems) > 0 && r.overlay.panelItems[0].Type == "overview"
}

// startSummarize kicks off an async AI status summarize. The result is shown in
// a dedicated scrollable summary panel (not injected into the PTY).
// Must be called with overlay.mu held.
func (r *InputRouter) startSummarize() {
	if r.summarizer == nil || r.overlay.summarizing.Load() {
		return
	}
	if !r.summarizer.IsAvailable() {
		r.overlay.summaryErr = "AI agent not available in PATH"
		r.overlay.draw()
		return
	}
	r.overlay.summaryErr = ""
	r.overlay.summarizing.Store(true)
	r.overlay.draw()
	r.startActionSpinner(&r.overlay.summarizing)
	go r.runSummarize()
}

// runSummarize performs the summarize call off the overlay lock and publishes
// the result to the summary panel.
func (r *InputRouter) runSummarize() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	result, err := r.summarizer.Summarize(ctx)
	cancel()

	r.overlay.mu.Lock()
	defer r.overlay.mu.Unlock()
	r.overlay.summarizing.Store(false)

	if err != nil {
		r.overlay.summaryErr = err.Error()
		r.overlay.draw()
		return
	}
	if result == nil || result.Summary == "" {
		r.overlay.summaryErr = "summarizer returned no content"
		r.overlay.draw()
		return
	}

	r.overlay.summaryText = result.Summary
	r.overlay.buildPanelItems()
	for i, p := range r.overlay.panelItems {
		if p.Type == "summary" {
			r.overlay.panelIndex = i
			r.overlay.panelMode = true
			break
		}
	}
	r.overlay.renderer.ClearScreen()
	r.overlay.draw()
}

// startReconnect kicks off an async daemon reconnect. No-op when already
// connected or a reconnect is in flight.
// Must be called with overlay.mu held.
func (r *InputRouter) startReconnect() {
	if r.daemonConnector == nil || r.overlay.connecting.Load() {
		return
	}
	r.overlay.statusMu.RLock()
	connected := r.overlay.status.DaemonConnected == ConnectionConnected
	r.overlay.statusMu.RUnlock()
	if connected {
		return
	}
	r.overlay.connectErr = ""
	r.overlay.connecting.Store(true)
	r.overlay.draw()
	r.startActionSpinner(&r.overlay.connecting)
	go r.runReconnect()
}

// runReconnect performs the connect off the overlay lock and refreshes status
// on success.
func (r *InputRouter) runReconnect() {
	err := r.daemonConnector.Connect()

	r.overlay.mu.Lock()
	r.overlay.connecting.Store(false)
	if err != nil {
		r.overlay.connectErr = err.Error()
		r.overlay.draw()
		r.overlay.mu.Unlock()
		return
	}
	r.overlay.mu.Unlock()

	// Refresh daemon-backed status so the connection line and panels update.
	if r.statusFetcher != nil {
		r.statusFetcher.Refresh()
	}
	r.overlay.Redraw()
}

// startActionSpinner advances the overview spinner frame and redraws while the
// given action flag is set. Exits when the flag clears.
func (r *InputRouter) startActionSpinner(active *atomic.Bool) {
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for active.Load() {
			<-ticker.C
			if !active.Load() {
				return
			}
			r.overlay.spinnerTick.Add(1)
			r.overlay.Redraw()
		}
	}()
}

// handleCommandInput processes keystrokes while the command palette is active.
// Typing filters the command list; Up/Down move the highlight; Enter runs the
// highlighted command with whatever argument follows it in the buffer; Tab
// completes the highlighted command name. Must be called with overlay.mu held.
func (r *InputRouter) handleCommandInput(key string) {
	o := r.overlay
	switch {
	case key == "Escape" || (len(key) == 1 && key[0] == 27):
		o.commandInput = false
		o.commandBuffer = ""
		o.commandSelectedIdx = 0
		o.draw()

	case key == "Up":
		if o.commandSelectedIdx > 0 {
			o.commandSelectedIdx--
			o.draw()
		}

	case key == "Down":
		matches, _, _ := filterPaletteCommands(o.commandBuffer)
		if o.commandSelectedIdx < len(matches)-1 {
			o.commandSelectedIdx++
			o.draw()
		}

	case key == "\t": // Tab completes the highlighted command name
		matches, _, _ := filterPaletteCommands(o.commandBuffer)
		if o.commandSelectedIdx < len(matches) {
			o.commandBuffer = matches[o.commandSelectedIdx].Name + " "
			o.commandSelectedIdx = 0
			o.draw()
		}

	case key == "Enter" || (len(key) == 1 && (key[0] == 13 || key[0] == 10)):
		matches, _, args := filterPaletteCommands(o.commandBuffer)
		var chosen *PaletteCommand
		if o.commandSelectedIdx >= 0 && o.commandSelectedIdx < len(matches) {
			c := matches[o.commandSelectedIdx]
			chosen = &c
		}
		o.commandInput = false
		o.commandBuffer = ""
		o.commandSelectedIdx = 0
		if chosen != nil {
			r.dispatchPaletteCommand(*chosen, args)
		}
		o.draw()

	case key == "Backspace" || (len(key) == 1 && key[0] == 127):
		if len(o.commandBuffer) > 0 {
			o.commandBuffer = o.commandBuffer[:len(o.commandBuffer)-1]
			o.commandSelectedIdx = 0
			o.draw()
		}

	case len(key) == 1 && key[0] >= 32 && key[0] < 127: // Printable ASCII
		o.commandBuffer += key
		o.commandSelectedIdx = 0
		o.draw()
	}
}

// dispatchPaletteCommand executes the chosen palette command. Must be called
// with overlay.mu held; it releases the lock only around daemon controller
// calls (which do network I/O) and re-acquires it before returning, matching
// the locking discipline of the surrounding menu handler.
func (r *InputRouter) dispatchPaletteCommand(c PaletteCommand, args string) {
	// Summarize/reconnect mutate overlay state directly and expect the lock
	// to be held, so handle them without releasing it.
	switch c.Name {
	case "summarize":
		r.startSummarize()
		return
	case "reconnect":
		r.startReconnect()
		return
	case "toggle-ports":
		r.overlay.showAllPorts = !r.overlay.showAllPorts
		return
	case "mute":
		r.setForwarding(true)
		return
	case "unmute":
		r.setForwarding(false)
		return
	case "dismiss":
		if n, err := strconv.Atoi(strings.TrimSpace(args)); err == nil {
			r.overlay.DismissNoticeByIndex(n)
		}
		return
	case "dismiss-all":
		r.overlay.DismissAllNotices()
		return
	}

	if r.scriptController == nil {
		return
	}

	r.overlay.mu.Unlock()
	// These are explicit user-invoked palette commands; a daemon-side failure
	// must not vanish. Surface it to the debug log (the only PTY-safe sink).
	switch c.Name {
	case "start":
		if args != "" {
			if err := r.scriptController.StartScript(args); err != nil {
				debug.Log("overlay", "palette start %q failed: %v", args, err)
			}
		}
	case "stop":
		if args != "" {
			if err := r.scriptController.StopScript(args); err != nil {
				debug.Log("overlay", "palette stop %q failed: %v", args, err)
			}
		}
	case "restart":
		if args != "" {
			if err := r.scriptController.RestartScript(args); err != nil {
				debug.Log("overlay", "palette restart %q failed: %v", args, err)
			}
		}
	case "kill-port":
		if p, err := strconv.Atoi(strings.TrimSpace(args)); err == nil && p > 0 {
			if err := r.scriptController.KillPort(p); err != nil {
				debug.Log("overlay", "palette kill-port %d failed: %v", p, err)
			}
		}
	case "kill-orphans":
		if err := r.scriptController.CleanOrphans(); err != nil {
			debug.Log("overlay", "palette kill-orphans failed: %v", err)
		}
	case "restart-proxy":
		if args != "" {
			if err := r.scriptController.RestartProxy(args); err != nil {
				debug.Log("overlay", "palette restart-proxy %q failed: %v", args, err)
			}
		}
	case "stop-proxy":
		if args != "" {
			if err := r.scriptController.StopProxy(args); err != nil {
				debug.Log("overlay", "palette stop-proxy %q failed: %v", args, err)
			}
		}
	case "stop-tunnel":
		if args != "" {
			if err := r.scriptController.StopTunnel(args); err != nil {
				debug.Log("overlay", "palette stop-tunnel %q failed: %v", args, err)
			}
		}
	case "run":
		if args != "" {
			if err := r.scriptController.RunCommand(args); err != nil {
				debug.Log("overlay", "palette run %q failed: %v", args, err)
			}
		}
	}
	if r.statusFetcher != nil {
		r.statusFetcher.Refresh()
	}
	r.overlay.mu.Lock()
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

	if err != nil {
		// Best-effort panel fill; keep whatever content the panel already has.
		debug.Log("overlay", "fetchScriptOutput %q failed: %v", panel.ID, err)
		return
	}
	if output == "" {
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
		// Best-effort: this runs on a refresh ticker; keep the prior content on
		// a transient fetch failure. No log here to avoid per-tick spam when the
		// daemon is briefly unreachable.
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

	// Auto-scroll only when the user is pinned to the bottom. When the user has
	// scrolled up (ScrollOffset > 0), keep the same lines in view as new output
	// appends at the bottom: ScrollOffset is measured from the bottom, so the
	// render window (endLine = len(lines) - ScrollOffset) would otherwise slide
	// downward by the number of appended lines, yanking the view toward the
	// bottom. Push the offset up by that delta to anchor the visible region.
	//
	// Exact below the maxPanelLines (2000) cap; once the tail window saturates
	// the appended-line count reads as 0 and the view can still drift — full
	// anchoring there would need stable per-line identity, out of scope here.
	wasAtBottom := panel.ScrollOffset == 0
	if panel.Content != output {
		prevLines := panel.ContentLines()
		panel.SetContent(output)
		if wasAtBottom {
			panel.ScrollOffset = 0
		} else if added := panel.ContentLines() - prevLines; added > 0 {
			panel.ScrollOffset += added
			if maxOff := panel.ContentLines() - 1; maxOff >= 0 && panel.ScrollOffset > maxOff {
				panel.ScrollOffset = maxOff
			}
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
		// Best-effort eager fill during the hide transition; keep prior content.
		debug.Log("overlay", "eagerRefreshPanel %q failed: %v", panelID, err)
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

// forwardingHotkey maps Ctrl+Up → pause and Ctrl+Down → resume agent forwarding.
// Returns (paused, true) on a match, (false, false) otherwise. These sit in the
// same Ctrl+Arrow family the overlay already owns (panelShortcutDelta), so they
// don't collide with the wrapped AI tool, which sees plain Up/Down for history.
func forwardingHotkey(buf []byte) (paused bool, ok bool) {
	switch string(buf) {
	case "\x1b[1;5A": // Ctrl+Up
		return true, true
	case "\x1b[1;5B": // Ctrl+Down
		return false, true
	}
	return false, false
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

	// If there's a unicode character, emit it directly.
	// Special case: VK_BACK (0x08) with uc=0x08 (\b). Windows Terminal sends
	// \b for Backspace in win32-input-mode, but POSIX terminals and readline
	// expect \x7f (DEL) for Backspace. Translate here so the child process
	// receives the standard Backspace sequence regardless of the host terminal's
	// win32-input-mode encoding.
	if uc > 0 {
		b := byte(uc)
		if vk == vkBack && b == 0x08 {
			b = 0x7f
			debugLog("seq=%s -> VK_BACK translated \\x08 -> \\x7f", seq)
		}
		s.result = append(s.result, b)
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
