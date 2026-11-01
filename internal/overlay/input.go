package overlay

import (
	"context"
	"fmt"
	"io"
	"iter"
	"math"
	"os"
	"runtime"
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

	// RequestData sends a request whose payload is raw bytes rather than
	// JSON (PROXY EXEC takes the script itself as its payload).
	RequestData(verb string, data []byte, args ...string) (map[string]interface{}, error)
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
	// StartTunnel opens a tunnel in front of a proxy, returning its public URL.
	StartTunnel(provider, proxyID string, localPort int) (string, error)
	// ProxyURL returns the current URL of the project's proxy declared as
	// configName in .agnt.kdl, scheme included, as the daemon reports it.
	ProxyURL(configName string) (string, error)
	// ReconcileConfig live-applies the project's .agnt.kdl.
	ReconcileConfig() error
	// ProjectPath is the directory holding the project's .agnt.kdl.
	ProjectPath() string
	// ProxyExec runs JavaScript in the browser page behind a proxy.
	ProxyExec(proxyID, code string) error
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

	// configDiscardArmed is set when Escape found unsaved config edits. The
	// next Escape discards them; anything else disarms it, so a confirmation
	// can never be satisfied by a keypress from minutes earlier.
	configDiscardArmed bool

	// ctrlCExitArmed is set by a Ctrl+C in panel view. A second Ctrl+C closes
	// the panels; any other key disarms it, as with configDiscardArmed.
	ctrlCExitArmed bool

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

	// pendingFlush overrides win32PendingFlush, the scanner's budget for
	// holding a sequence that straddles a read boundary. Tests shrink it so a
	// split-sequence case is decided by ordering rather than by a real wait.
	// Zero means the production value.
	pendingFlush time.Duration

	// decodeWin32 runs input through the win32-input-mode scanner. Only a
	// native Windows console sends those sequences: under Linux and WSL the
	// terminal's keys arrive as plain VT bytes. There the scanner had nothing
	// to decode, yet it held sequences on a timer and dropped the focus
	// reports the agent had asked for, so it is off outside Windows.
	decodeWin32 bool

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

// SetInput sets the reader Run consumes instead of os.Stdin. Call before Run.
//
// Run's producer goroutine blocks in Read and is not interrupted by Stop, so a
// router reading os.Stdin directly leaves a reader behind that keeps taking
// keystrokes after the router is gone. A caller that starts more than one
// router in a process (the first-run setup → relaunch flow) must pass a reader
// it can end — one whose Read returns once the router is stopped.
func (r *InputRouter) SetInput(in io.Reader) {
	r.input = in
}

// NewInputRouter creates a new InputRouter.
func NewInputRouter(ptmx PtyReadWriter, overlay *Overlay) *InputRouter {
	return &InputRouter{
		ptmx:        ptmx,
		overlay:     overlay,
		done:        make(chan struct{}),
		escReader:   NewEscapeSequenceReader(),
		decodeWin32: runtime.GOOS == "windows",
	}
}

// SetScriptController sets the script controller for stopping/restarting scripts.
func (r *InputRouter) SetScriptController(ctrl ScriptController) {
	r.scriptController = ctrl
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

// SetForwardingToggle wires the callback that pauses/resumes agent-inbound push
// (errors + notifications) across the in-process and daemon delivery paths.
func (r *InputRouter) SetForwardingToggle(fn func(paused bool)) {
	r.forwardingToggle = fn
}

// setForwarding applies a forwarding pause state: invokes the toggle, updates
// the persistent indicator, and shows a transient confirmation. Must be called
// without overlay.mu held: the toggle does IPC and Notify takes the lock to
// repaint.
func (r *InputRouter) setForwarding(paused bool) {
	r.forwardPaused = paused
	if r.forwardingToggle != nil {
		r.forwardingToggle(paused)
	}
	r.overlay.SetForwardPaused(paused)
	if paused {
		r.overlay.Notify(Notification{ID: "forwarding", Level: LevelInfo, Text: "🔇 agent forwarding paused — errors still pullable via get_incidents"})
	} else {
		r.overlay.Notify(Notification{ID: "forwarding", Level: LevelInfo, Text: "🔊 agent forwarding resumed"})
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

	// Input moves in the scanner's chunks, never byte by byte: a chunk holds
	// only whole sequences, and passthrough writes whole chunks. Bytes pushed
	// singly were drained by passthrough mid-burst, so a mouse report reached
	// the agent in two writes — and a report cut right after "\x1b[" makes
	// Claude Code drop the keypresses that follow it. Under any-motion mouse
	// tracking the reports never stop, so typing died while every key the
	// overlay binds itself still worked.
	inputCh := make(chan []byte, 16)
	errCh := make(chan error, 1)

	inputSrc := r.input
	if inputSrc == nil {
		inputSrc = os.Stdin
	}
	flush := r.pendingFlush
	if flush == 0 {
		flush = win32PendingFlush
	}
	chunks := readChunks(inputSrc)
	if r.decodeWin32 {
		chunks = scanWin32Chunks(inputSrc, flush)
	}
	go func() {
		for chunk := range chunks {
			// Once Stop has ended the loop below nobody drains inputCh, so a
			// blind send would block forever and pin the scanner with it.
			select {
			case inputCh <- chunk:
			case <-r.done:
				return
			}
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

		case chunk := <-inputCh:
			// Cancel any pending escape timer
			if escTimer != nil {
				escTimer.Stop()
				escTimer = nil
			}

			// Each byte is routed by the state at that byte, because a key
			// early in the chunk can change it (open or close the panel view).
			for i := 0; i < len(chunk); i++ {
				b := chunk[i]

				// Modal capture: a startup prompt owns input. Divert the byte
				// to it and skip normal handling so the prompt and the router
				// never both consume from os.Stdin.
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
					r.handleOverlayInput(b)
					continue
				}

				// The rest of the chunk belongs to the agent.
				r.passthrough(chunk[i:])
				break
			}

			// If the overlay is waiting for more escape sequence bytes, start a timer
			if r.overlay.IsActive() && r.escReader.IsPending() {
				wait := escTimeout
				if r.escReader.InPaste() {
					wait = pasteTimeout
				}
				escTimer = time.NewTimer(wait)
			}
		}
	}
}

// passthrough hands one chunk of the agent's input to the PTY in a single
// write, after the hotkeys the overlay keeps for itself on the agent's screen.
// A chunk ends on a sequence boundary, so the agent never sees half of one.
func (r *InputRouter) passthrough(buf []byte) {
	// Intercept Ctrl+Up/Down to pause/resume agent forwarding, even
	// mid-passthrough, so an overwhelmed agent can be muted instantly without
	// opening the overlay.
	if paused, ok := forwardingHotkey(buf); ok {
		r.setForwarding(paused)
		return
	}
	// Ctrl+Right opens the panel view. Ctrl+Left stays the agent's (word-left
	// in its input box): there is nothing to the left of the agent's screen.
	if isPanelOpenShortcut(buf) {
		r.enterPanelBrowse()
		return
	}
	// A failed write is reported by AgentTTY's error handler; the log
	// line is for the debug trace.
	if _, err := WriteTagged(r.ptmx, "keyboard", buf); err != nil {
		debug.Log("input", "passthrough: %d typed bytes not delivered to the agent: %v", len(buf), err)
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

	// A bracketed paste is text, never a run of key commands: read key by key
	// its 'q' closes the panel and its digits open scripts.
	if text, ok := strings.CutPrefix(key, pasteKeyPrefix); ok {
		r.handlePaste(text)
		return
	}

	// Ctrl+C twice closes the panels from anywhere, the palette and config
	// editor included: it is the exit people reach for when nothing else
	// they press seems to work. One press only arms it, because the agent's
	// own Ctrl+C means interrupt and a stray one must not lose the view.
	if key == "\x03" || key == "Ctrl+C" {
		if r.ctrlCExitArmed {
			r.overlay.commandInput = false
			r.overlay.commandBuffer = ""
			r.exitPanelMode()
			return
		}
		r.ctrlCExitArmed = true
		r.overlay.notifyLocked(Notification{Level: LevelInfo, Text: "Ctrl+C again to close the panels"})
		return
	}
	r.ctrlCExitArmed = false

	// Command palette captures ALL keys while active. This must run before the
	// global menu switch below: otherwise Enter/Up/Down/q/x/1-9 are stolen by
	// panel navigation (the original bug — Enter selected a script instead of
	// running the typed command).
	if r.overlay.commandInput {
		r.handleCommandInput(key)
		return
	}

	// The config editor captures keys the same way and for the same reason:
	// without this, typing a word containing 'q' or 'x' in the buffer would be
	// read as a panel-browser command and close the panel mid-edit.
	if r.isConfigPanel() && r.handleConfigKey(key) {
		return
	}

	// "Escape+X" is Escape followed quickly by another key (Alt+X); read it
	// as Escape.
	if key == "Escape" || strings.HasPrefix(key, "Escape+") {
		r.panelBack()
		return
	}

	switch key {
	case "Ctrl+Right", "Right", "\t": // next panel
		r.handlePanelNav(1)
		return

	case "Ctrl+Left", "Left", "Shift+Tab": // previous panel; from the overview, back to the agent
		r.handlePanelNav(-1)
		return

	case "\r", "\n": // Enter
		if r.isOverviewWithScripts() {
			r.overviewEnter()
		}
		return

	case "Up", "k":
		if r.isOverviewWithScripts() {
			r.moveOverviewSelection(-1)
			return
		}
		r.scrollPanel(1)
		return

	case "Down", "j":
		if r.isOverviewWithScripts() {
			r.moveOverviewSelection(1)
			return
		}
		r.scrollPanel(-1)
		return

	case "PageUp":
		r.scrollPanel(r.pageLines())
		return

	case "PageDown":
		r.scrollPanel(-r.pageLines())
		return

	case "Home", "g":
		if r.isOverviewWithScripts() {
			r.moveOverviewSelection(-math.MaxInt32)
			return
		}
		r.scrollPanel(math.MaxInt32)
		return

	case "End", "G":
		if r.isOverviewWithScripts() {
			r.moveOverviewSelection(math.MaxInt32)
			return
		}
		r.scrollPanel(-math.MaxInt32)
		return

	case "q": // close the panel view from anywhere
		r.exitPanelMode()
		return

	case "x": // Close current panel (only if process has stopped)
		r.closeCurrentPanel()
		return
	}

	// 1-9: open the Nth script, numbered as the overview lists them.
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
		n := int(key[0] - '1')
		r.overlay.statusMu.RLock()
		var name string
		if n < len(r.overlay.status.Scripts) {
			name = r.overlay.status.Scripts[n].Name
		}
		r.overlay.statusMu.RUnlock()
		if name != "" {
			r.overlay.overviewSelectedIdx = n
			r.openScriptPanel(name)
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

// handlePaste delivers a bracketed paste to the panel's text input the way
// typed text would arrive there. A panel without one ignores it, and says so.
// Must be called with overlay.mu held.
func (r *InputRouter) handlePaste(text string) {
	o := r.overlay
	text = strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(text)
	switch {
	case o.commandInput:
		// The palette is one line of printable ASCII, the same set typing
		// reaches it with; a pasted newline is not Enter.
		for i := 0; i < len(text); i++ {
			if c := text[i]; c >= 32 && c < 127 {
				o.commandBuffer += string(c)
			}
		}
		o.commandSelectedIdx = 0
		o.draw()
	case r.isConfigPanel() && o.configEditor != nil:
		r.configDiscardArmed = false
		o.configEditor.Paste(text)
		o.draw()
	default:
		o.notifyLocked(Notification{ID: "paste", Level: LevelInfo, Text: "paste ignored: this panel has no text input"})
	}
}

// exitPanelMode closes panel mode entirely.
// Must be called with overlay.mu held.
func (r *InputRouter) exitPanelMode() {
	r.ctrlCExitArmed = false
	r.stopPanelRefresh()
	r.overlay.hideMenu()
	debug.Log("input", "panel view closed (state=%v, gate frozen=%v)", r.overlay.State(), r.overlay.gate != nil && r.overlay.gate.IsFrozen())
}

// panelBack is Escape: from a panel opened off the overview it returns to the
// overview, and from the overview it closes the panel view. Escape closing
// everything from three panels deep left no way back to the overview short
// of tabbing through every panel in between.
// Must be called with overlay.mu held.
func (r *InputRouter) panelBack() {
	if !r.overlay.panelMode || r.overlay.panelIndex == 0 {
		r.exitPanelMode()
		return
	}
	r.stopPanelRefresh()
	r.overlay.panelIndex = 0
	r.overlay.draw()
}

// moveOverviewSelection moves the overview script highlight by delta, clamped
// to the script list. Must be called with overlay.mu held.
func (r *InputRouter) moveOverviewSelection(delta int) {
	r.overlay.statusMu.RLock()
	n := len(r.overlay.status.Scripts)
	r.overlay.statusMu.RUnlock()
	idx := max(0, min(n-1, r.overlay.overviewSelectedIdx+delta))
	if idx != r.overlay.overviewSelectedIdx {
		r.overlay.overviewSelectedIdx = idx
		r.overlay.draw()
	}
}

// scrollPanel scrolls the active panel's content. Positive delta moves toward
// older lines; ScrollOffset counts lines up from the bottom.
// Must be called with overlay.mu held.
func (r *InputRouter) scrollPanel(delta int) {
	if r.overlay.panelIndex < 0 || r.overlay.panelIndex >= len(r.overlay.panelItems) {
		return
	}
	panel := &r.overlay.panelItems[r.overlay.panelIndex]
	if panel.Type == "overview" {
		return
	}
	off := max(0, min(panel.ContentLines()-1, panel.ScrollOffset+delta))
	if off != panel.ScrollOffset {
		panel.ScrollOffset = off
		r.overlay.draw()
	}
}

// pageLines is the PageUp/PageDown step: the panel body less a line of
// overlap so the reader keeps their place.
func (r *InputRouter) pageLines() int {
	return max(1, r.overlay.height-8)
}

// openScriptPanel focuses the process panel for the named script and starts
// its output refresh. Must be called with overlay.mu held.
func (r *InputRouter) openScriptPanel(name string) {
	for i, p := range r.overlay.panelItems {
		if p.Type == "process" && p.ID == name {
			r.overlay.panelIndex = i
			panel := &r.overlay.panelItems[i]
			if r.outputFetcher != nil {
				r.startPanelRefresh(panel.ID)
			}
			r.overlay.renderer.ClearScreen()
			r.overlay.draw()
			return
		}
	}
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
	if script := r.selectedScript(); script != nil {
		r.openScriptPanel(script.Name)
	}
}

// overviewStopScript stops the selected script via the daemon.
// Must be called with overlay.mu held.
func (r *InputRouter) overviewStopScript() {
	r.overviewScriptAction("stop", func(c ScriptController, name string) error { return c.StopScript(name) })
}

// overviewRestartScript restarts the selected script via the daemon.
// Must be called with overlay.mu held.
func (r *InputRouter) overviewRestartScript() {
	r.overviewScriptAction("restart", func(c ScriptController, name string) error { return c.RestartScript(name) })
}

// overviewStartScript starts the selected stopped script via the daemon.
// Must be called with overlay.mu held.
func (r *InputRouter) overviewStartScript() {
	r.overviewScriptAction("start", func(c ScriptController, name string) error { return c.StartScript(name) })
}

// overviewScriptAction runs one a/s/r key against the selected script. The
// call does network I/O, so the overlay lock is released around it. A failure
// is reported the same way the palette's start/stop/restart report theirs: a
// key that did nothing visible was indistinguishable from a key that worked.
// Must be called with overlay.mu held.
func (r *InputRouter) overviewScriptAction(verb string, fn func(ScriptController, string) error) {
	if r.scriptController == nil {
		r.notifyActionError(verb + ": not connected to the daemon (try :reconnect)")
		return
	}
	script := r.selectedScript()
	if script == nil {
		return
	}
	name := script.Name
	r.overlay.mu.Unlock()
	err := fn(r.scriptController, name)
	if r.statusFetcher != nil {
		r.statusFetcher.Refresh()
	}
	r.overlay.mu.Lock()
	if err != nil {
		r.notifyActionError(fmt.Sprintf("%s %s failed: %v", verb, name, err))
	}
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

// isConfigPanel reports whether the .agnt.kdl editor is the active panel.
func (r *InputRouter) isConfigPanel() bool {
	if !r.overlay.panelMode || r.overlay.panelIndex >= len(r.overlay.panelItems) {
		return false
	}
	return r.overlay.panelItems[r.overlay.panelIndex].Type == "config"
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
	// The summarize runs for up to two minutes. If the user closed the panel
	// view meanwhile, reopening it (and clearing the screen) would wipe the
	// child's live output and leave panelMode stale; the summary stays
	// available as a panel the next time the view opens.
	if r.overlay.State() != StateMenu {
		return
	}
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
	args = strings.TrimSpace(args)
	// A required argument is written "<name>" in the registry; "[name]" is
	// optional. Refuse loudly rather than doing nothing.
	if strings.HasPrefix(c.Arg, "<") && args == "" {
		r.notifyActionError(fmt.Sprintf("%s: argument required: %s %s", c.Name, c.Name, c.Arg))
		return
	}

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
	case "mute", "unmute":
		r.overlay.mu.Unlock()
		r.setForwarding(c.Name == "mute")
		r.overlay.mu.Lock()
		return
	case "dismiss":
		n, err := strconv.Atoi(args)
		if err != nil {
			r.notifyActionError(fmt.Sprintf("dismiss: %q is not a notice number", args))
			return
		}
		if shown := len(r.overlay.VisibleNotices()); n < 1 || n > shown {
			r.notifyActionError(fmt.Sprintf("dismiss: no notice %d (%d shown)", n, shown))
			return
		}
		r.overlay.DismissNoticeByIndex(n)
		return
	case "dismiss-all":
		r.overlay.DismissAllNotices()
		return
	}

	if r.scriptController == nil {
		r.notifyActionError(c.Name + ": not connected to the daemon (try :reconnect)")
		return
	}
	var port int
	if c.Name == "kill-port" {
		p, err := strconv.Atoi(args)
		if err != nil || p < 1 || p > 65535 {
			r.notifyActionError(fmt.Sprintf("kill-port: %q is not a TCP port", args))
			return
		}
		port = p
	}

	// :config rewrites panelItems and configEditor, which the status refresh
	// rebuilds under the lock, so it does its file read here, lock held.
	if c.Name == "config" {
		if err := r.openConfigPanel(); err != nil {
			r.notifyActionError(fmt.Sprintf("config failed: %v", err))
		}
		return
	}

	r.overlay.mu.Unlock()
	// These are explicit user-invoked palette commands; a daemon-side failure
	// must not vanish. Surface it as an error notification.
	fail := func(what string, err error) {
		r.overlay.Notify(Notification{Level: LevelError, Text: fmt.Sprintf("%s failed: %v", what, err)})
	}
	switch c.Name {
	case "start":
		if err := r.scriptController.StartScript(args); err != nil {
			fail("start "+args, err)
		}
	case "stop":
		if err := r.scriptController.StopScript(args); err != nil {
			fail("stop "+args, err)
		}
	case "restart":
		if err := r.scriptController.RestartScript(args); err != nil {
			fail("restart "+args, err)
		}
	case "kill-port":
		if err := r.scriptController.KillPort(port); err != nil {
			fail(fmt.Sprintf("kill-port %d", port), err)
		}
	case "kill-orphans":
		if err := r.scriptController.CleanOrphans(); err != nil {
			fail("kill-orphans", err)
		}
	case "restart-proxy":
		if err := r.scriptController.RestartProxy(args); err != nil {
			fail("restart-proxy "+args, err)
		}
	case "stop-proxy":
		if err := r.scriptController.StopProxy(args); err != nil {
			fail("stop-proxy "+args, err)
		}
	case "tunnel":
		if err := r.runTunnelCommand(args); err != nil {
			fail("tunnel", err)
		}
	case "stop-tunnel":
		if err := r.scriptController.StopTunnel(args); err != nil {
			fail("stop-tunnel "+args, err)
		}
	case "tailscale":
		if err := r.runTailscaleCommand(args); err != nil {
			fail("tailscale", err)
		}
	case "as":
		if err := r.runAsCommand(args); err != nil {
			fail("as", err)
		}
	case "run":
		if err := r.scriptController.RunCommand(args); err != nil {
			fail("run "+args, err)
		}
	}
	if r.statusFetcher != nil {
		r.statusFetcher.Refresh()
	}
	r.overlay.mu.Lock()
}

// notifyActionError reports a user-invoked command that was refused or
// failed, as an error notification. Must be called with overlay.mu held (it
// goes through notifyLocked, which repaints in place when notices are shown).
func (r *InputRouter) notifyActionError(text string) {
	r.overlay.notifyLocked(Notification{Level: LevelError, Text: text})
}

// maxPanelLines is how many trailing lines of script output a process panel holds.
const maxPanelLines = 2000

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
//
// It fetches once immediately: the panel's first fill comes from here, never
// from the keyboard goroutine, because a daemon call there (30s timeout on the
// shared connection) froze typing and the agent's screen whenever the daemon
// was slow.
func (r *InputRouter) runPanelRefresh(stop <-chan struct{}, panelID string) {
	r.refreshPanelContent(panelID)
	ticker := time.NewTicker(panelRefreshInterval)
	defer ticker.Stop()
	r.panelRefreshLoop(stop, ticker.C, panelID)
}

// panelRefreshLoop refreshes the panel on every tick until stop closes. The tick
// source is a parameter so tests can drive refreshes without waiting on the
// production interval.
func (r *InputRouter) panelRefreshLoop(stop <-chan struct{}, ticks <-chan time.Time, panelID string) {
	for {
		select {
		case <-stop:
			return
		case <-ticks:
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

	// Submit the current view; the renderer owns change detection.
	r.overlay.draw()
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

	// Stepping back past the overview returns to the agent's screen, the
	// mirror of Ctrl+Right opening the view there. Forward stops at the log.
	newIndex := r.overlay.panelIndex + delta
	if newIndex < 0 {
		r.exitPanelMode()
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

// isPanelOpenShortcut reports whether a collected input buffer is Ctrl+Right,
// the one key that opens the panel view from the agent's screen.
func isPanelOpenShortcut(buf []byte) bool {
	return string(buf) == "\x1b[1;5C"
}

// forwardingHotkey maps Ctrl+Up → pause and Ctrl+Down → resume agent forwarding.
// Returns (paused, true) on a match, (false, false) otherwise. These sit in the
// same Ctrl+Arrow family as the panel shortcut (isPanelOpenShortcut), so they
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

// enterPanelBrowse opens the panel view on the overview, the one screen every
// other panel is reached from. Called when Ctrl+Right is pressed from the
// agent's screen.
func (r *InputRouter) enterPanelBrowse() {
	r.overlay.mu.Lock()
	defer r.overlay.mu.Unlock()

	r.overlay.showPanelDirect()
	debug.Log("input", "panel view opened (gate frozen=%v)", r.overlay.gate != nil && r.overlay.gate.IsFrozen())

	// Start at overview (index 0)
	r.overlay.panelMode = true
	r.overlay.panelIndex = 0
	r.overlay.renderer.ClearScreen()
	r.overlay.draw()
}

// readChunks yields each read of r as one chunk, unchanged: the bytes reach
// the agent exactly as the terminal delivered them.
func readChunks(r io.Reader) iter.Seq[[]byte] {
	return func(yield func([]byte) bool) {
		for {
			buf := make([]byte, 4096)
			n, err := r.Read(buf)
			if n > 0 && !yield(buf[:n]) {
				return
			}
			if err != nil {
				return
			}
		}
	}
}

// csiKeys maps a complete CSI sequence to the key name the overlay binds it
// to. A sequence that is not here is consumed and discarded, not reported.
var csiKeys = map[string]string{
	"\x1b[A":     "Up",
	"\x1b[B":     "Down",
	"\x1b[C":     "Right",
	"\x1b[D":     "Left",
	"\x1b[H":     "Home",
	"\x1b[F":     "End",
	"\x1b[1~":    "Home",
	"\x1b[4~":    "End",
	"\x1b[3~":    "Delete",
	"\x1b[5~":    "PageUp",
	"\x1b[6~":    "PageDown",
	"\x1b[Z":     "Shift+Tab",
	"\x1b[1;5C":  "Ctrl+Right",
	"\x1b[1;5D":  "Ctrl+Left",
	"\x1b[1;5A":  "Ctrl+Up",
	"\x1b[1;5B":  "Ctrl+Down",
	"\x1b[99;5u": "Ctrl+C", // kitty keyboard protocol
}

// ss3Keys maps the byte after ESC O, which a terminal in application cursor
// mode sends instead of the CSI forms above.
var ss3Keys = map[byte]string{
	'A': "Up", 'B': "Down", 'C': "Right", 'D': "Left", 'H': "Home", 'F': "End",
}

// Reader states.
const (
	escGround     = iota // no sequence in progress
	escAfterESC          // ESC seen, waiting for the sequence type
	escInCSI             // inside ESC [ ... , waiting for a final byte
	escAfterSS3          // ESC O seen, waiting for its single byte
	escX10Mouse          // inside the three raw bytes of an ESC [ M report
	escInString          // inside OSC/DCS/APC/PM/SOS, waiting for a string terminator
	escDiscardCSI        // past the CSI cap, waiting for the sequence's final byte
	escInPaste           // inside a bracketed paste, waiting for ESC [201~
)

// Bracketed paste: a terminal whose application enabled mode 2004 (the agent
// does) wraps pasted text in these markers.
const (
	bracketedPasteStart = "\x1b[200~"
	bracketedPasteEnd   = "\x1b[201~"
)

// pasteTimeout is how long a bracketed paste may go quiet before the router
// abandons it. Far longer than the bare-Escape window, because a large paste
// over a slow link arrives in reads spaced wider than 50ms, and each byte of a
// paste read as a key is a panel command.
const pasteTimeout = time.Second

// stringSequenceIntroducers are the bytes that follow ESC to open a sequence
// carrying a payload rather than a keypress: OSC (a colour or clipboard
// reply), DCS (a terminfo or DECRQSS reply), SOS, PM and APC. A terminal
// sends these on stdin in answer to a query, and their payload must never be
// read as keys. Each runs to a string terminator: ST (ESC \\) or, for OSC,
// BEL as well.
var stringSequenceIntroducers = map[byte]bool{
	']': true, // OSC
	'P': true, // DCS
	'X': true, // SOS
	'^': true, // PM
	'_': true, // APC
}

// csiMaxLen bounds a runaway CSI so a corrupt stream cannot wedge the reader
// mid-sequence and swallow every keypress after it.
const csiMaxLen = 32

// stringMaxLen bounds a string sequence, and a CSI already past csiMaxLen, so
// a stream that never sends a terminator is abandoned instead of swallowing
// every keypress after it. It is generous because a legitimate OSC 52
// clipboard reply carries base64 of whatever was copied.
const stringMaxLen = 8192

// pasteKeyPrefix marks a key that carries a whole bracketed paste: the reader
// reports a paste as one key, pasteKeyPrefix followed by the pasted text.
const pasteKeyPrefix = "Paste:"

// pasteMaxLen bounds the text held for one bracketed paste. A longer paste is
// still consumed to its end marker, then dropped whole: delivering a truncated
// paste would be worse than delivering none.
const pasteMaxLen = 64 << 10

// x10MouseBytes is the fixed payload length of an ESC [ M mouse report.
const x10MouseBytes = 3

// EscapeSequenceReader parses terminal input into key names for the overlay.
//
// Beyond key lookup it holds one rule: a sequence it does not recognise is
// consumed and discarded, never reported as Escape. Escape closes the open
// panel, and the bytes still to come from that sequence would then be passed
// through to the child process and land in its input field as typed text.
// That is how mouse motion over a panel typed fragments of mouse reports into
// the agent's prompt: a report is longer than any bound key, so the reader
// gave up partway and called it Escape. Only a bare Escape -- an ESC with
// nothing following it, reported by Timeout -- is Escape.
type EscapeSequenceReader struct {
	buffer []byte
	state  int
	// x10Remaining counts down the raw bytes left in an ESC [ M report.
	// They carry button and coordinates and may hold any value, including
	// ESC, '[' and 'M', so they are consumed by count and never scanned.
	x10Remaining int
	// stringESC records an ESC inside a string sequence, which ends that
	// sequence when the next byte is the backslash of ST.
	stringESC bool
	// discarded counts the bytes dropped since a sequence passed its cap, so
	// a stream that never produces a final byte is abandoned rather than
	// swallowing every later keypress.
	discarded int
	// paste holds a bracketed paste's text (plus any partial end marker);
	// pasteOverflow records that it outgrew pasteMaxLen and will be dropped.
	paste         []byte
	pasteOverflow bool
}

// NewEscapeSequenceReader creates a new escape sequence reader.
func NewEscapeSequenceReader() *EscapeSequenceReader {
	return &EscapeSequenceReader{
		buffer: make([]byte, 0, 8),
	}
}

// Feed feeds a byte into the reader. complete reports that the byte finished
// something; key names it, and is empty when what finished was a sequence the
// overlay does not bind.
func (r *EscapeSequenceReader) Feed(b byte) (key string, complete bool) {
	switch r.state {
	case escGround:
		if b == 0x1b {
			r.state = escAfterESC
			r.buffer = append(r.buffer[:0], b)
			return "", false
		}
		return string(b), true

	case escAfterESC:
		r.buffer = append(r.buffer, b)
		switch b {
		case '[':
			r.state = escInCSI
			return "", false
		case 'O':
			r.state = escAfterSS3
			return "", false
		}
		if stringSequenceIntroducers[b] {
			r.state = escInString
			r.stringESC = false
			return "", false
		}
		// ESC followed by anything else is Alt+key; the caller reads the
		// Escape+ prefix as Escape.
		next := b
		r.reset()
		return "Escape+" + string(next), true

	case escInString:
		// The payload is arbitrary and is never scanned for keys. Only the
		// string terminator ends it: ST (ESC \\), or BEL, which OSC also
		// accepts and which no other part of a reply may contain.
		if r.stringESC {
			r.stringESC = false
			if b == '\\' {
				r.reset()
				return "", true
			}
			// An ESC that was not ST restarts the sequence's own escape
			// tracking rather than ending it.
			if b == 0x1b {
				r.stringESC = true
			}
			return "", false
		}
		switch b {
		case 0x07:
			r.reset()
			return "", true
		case 0x1b:
			r.stringESC = true
		}
		r.discarded++
		if r.discarded > stringMaxLen {
			r.reset()
			return "", true
		}
		return "", false

	case escDiscardCSI:
		// Past the cap the rest of the sequence is dropped, but the reader
		// stays inside it: returning to ground here would read its remaining
		// parameters and its final byte as keypresses.
		r.discarded++
		if b >= 0x40 && b <= 0x7e {
			r.reset()
			return "", true
		}
		if r.discarded > stringMaxLen {
			r.reset()
			return "", true
		}
		return "", false

	case escInPaste:
		return r.feedPaste(b)

	case escAfterSS3:
		r.reset()
		if name, ok := ss3Keys[b]; ok {
			return name, true
		}
		return "", true

	case escX10Mouse:
		r.x10Remaining--
		if r.x10Remaining > 0 {
			return "", false
		}
		r.reset()
		return "", true

	default: // escInCSI
		// ESC [ M is the X10 mouse report: its payload is counted, because
		// scanning it for a terminator would stop on a coordinate byte.
		if b == 'M' && len(r.buffer) == 2 {
			r.state = escX10Mouse
			r.x10Remaining = x10MouseBytes
			return "", false
		}
		r.buffer = append(r.buffer, b)
		// A CSI ends at its first byte in 0x40-0x7e; parameter and
		// intermediate bytes come before it.
		if b < 0x40 || b > 0x7e {
			if len(r.buffer) > csiMaxLen {
				r.buffer = r.buffer[:0]
				r.state = escDiscardCSI
				r.discarded = 0
				return "", false
			}
			return "", false
		}
		seq := string(r.buffer)
		r.reset()
		if seq == bracketedPasteStart {
			r.state = escInPaste
			return "", false
		}
		return csiKeys[seq], true
	}
}

// feedPaste consumes one byte of a bracketed paste. The text is never read as
// keys: it is reported whole, as one pasteKeyPrefix key, when the end marker
// arrives.
func (r *EscapeSequenceReader) feedPaste(b byte) (key string, complete bool) {
	r.paste = append(r.paste, b)
	if b == '~' && strings.HasSuffix(string(r.paste[max(0, len(r.paste)-len(bracketedPasteEnd)):]), bracketedPasteEnd) {
		if !r.pasteOverflow {
			key = pasteKeyPrefix + string(r.paste[:len(r.paste)-len(bracketedPasteEnd)])
		}
		r.reset()
		return key, true
	}
	// Past the cap keep only enough of the tail to recognise the end marker.
	if keep := len(bracketedPasteEnd) - 1; len(r.paste) > pasteMaxLen+keep {
		r.pasteOverflow = true
		r.paste = append(r.paste[:0], r.paste[len(r.paste)-keep:]...)
	}
	return "", false
}

// Timeout should be called when no more input arrives after starting an escape
// sequence. This is what makes a lone Escape keypress an Escape.
//
// Only a bare ESC counts. A timeout partway through a longer sequence means
// the sequence was split across the window or truncated, and calling that
// Escape would close the panel on a mouse report that merely arrived slowly --
// the same failure the reader avoids when the report arrives whole. The
// partial is dropped instead.
func (r *EscapeSequenceReader) Timeout() (key string, hadPending bool) {
	if r.state == escGround {
		return "", false
	}
	bare := r.state == escAfterESC && len(r.buffer) == 1
	r.reset()
	if !bare {
		return "", false
	}
	return "Escape", true
}

// Reset resets the reader state.
func (r *EscapeSequenceReader) Reset() {
	r.reset()
}

func (r *EscapeSequenceReader) reset() {
	r.state = escGround
	r.buffer = r.buffer[:0]
	r.x10Remaining = 0
	r.stringESC = false
	r.discarded = 0
	r.paste = r.paste[:0]
	r.pasteOverflow = false
}

// IsPending returns true if we're in the middle of parsing an escape sequence.
func (r *EscapeSequenceReader) IsPending() bool {
	return r.state != escGround
}

// InPaste reports whether the reader is inside a bracketed paste, which the
// router gives pasteTimeout rather than the bare-Escape window.
func (r *EscapeSequenceReader) InPaste() bool {
	return r.state == escInPaste
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
