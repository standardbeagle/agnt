package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/standardbeagle/agnt/internal/updater"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/daemonclient"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"

	"github.com/standardbeagle/go-sdk/mcp"
)

// DaemonTools wraps a daemon client for MCP tool handlers.
type DaemonTools struct {
	client *daemonclient.ResilientClient
	// clientMu serialises the lazy create-and-assign of client. Tool calls are
	// dispatched concurrently by the MCP SDK and the initialized handler's
	// autostart now runs on its own goroutine, so two callers can reach
	// ensureConnected at once; without this they would each build a
	// ResilientClient and one would be leaked with its heartbeat goroutine
	// still running.
	clientMu sync.Mutex
	config   daemonclient.AutoStartConfig
	version  string // Client version for validation

	// Session management
	sessionCode     string     // Attached session code (empty if not attached)
	sessionMu       sync.Mutex // Protects sessionCode
	noAutoAttach    bool       // If true, skip auto-attach on connect
	attachAttempted bool       // Whether we've attempted auto-attach

	// Channel session management (agnt mcp in channel mode).
	//
	// Iter 18 race fix (Option B, single-owner ctx): the heartbeat goroutine
	// no longer reads a shared stop channel from DaemonTools. Instead,
	// RegisterChannelSession creates a child context and stores its
	// CancelFunc on the returned ChannelSessionHandle. The goroutine selects
	// only on its ticker and its own hbCtx.Done(); Close() cancels that ctx
	// from inside stopOnce.Do so the signal happens exactly once with no
	// shared-field read. channelSessionCode remains here because it is a
	// process-scoped identifier read by ChannelSessionCode(); that access
	// continues to go through sessionMu.
	channelSessionCode string // Session code for the channel session (empty if not registered)
	channelProjectPath string // Project the channel session registered under, for re-registration after a daemon restart
}

// NewDaemonTools creates a new daemon tools wrapper with auto-start and version checking.
// The version parameter should be the current binary version (e.g., "0.6.5").
func NewDaemonTools(config daemonclient.AutoStartConfig, version string) *DaemonTools {
	return &DaemonTools{
		config:  config,
		version: version,
	}
}

// SetNoAutoAttach disables automatic session attachment on connect.
// Call this before any tool calls if you want to operate globally.
func (dt *DaemonTools) SetNoAutoAttach(noAttach bool) {
	dt.sessionMu.Lock()
	defer dt.sessionMu.Unlock()
	dt.noAutoAttach = noAttach
}

// SetSessionCode sets the session code directly (useful for testing or explicit attachment).
func (dt *DaemonTools) SetSessionCode(code string) {
	dt.sessionMu.Lock()
	defer dt.sessionMu.Unlock()
	dt.sessionCode = code
}

// SessionCode returns the current attached session code.
func (dt *DaemonTools) SessionCode() string {
	dt.sessionMu.Lock()
	defer dt.sessionMu.Unlock()
	return dt.sessionCode
}

// tryAutoAttach attempts to attach to a session for the current directory.
// It runs once per daemon connection (the binding lives on the daemon's side
// of the connection, so a new connection needs a new attach — see
// forgetAttachment and reattach). It's non-fatal if no session is found.
func (dt *DaemonTools) tryAutoAttach() {
	dt.sessionMu.Lock()
	if dt.attachAttempted || dt.noAutoAttach || dt.sessionCode != "" {
		dt.sessionMu.Unlock()
		return
	}
	dt.attachAttempted = true
	dt.sessionMu.Unlock()

	dt.attach(dt.currentClient().SessionAttach)
}

// attach binds a daemon connection to the session for the current directory
// through sessionAttach (a SESSION ATTACH on that connection) and records the
// resulting session code. Reports whether a session was attached.
func (dt *DaemonTools) attach(sessionAttach func(directory string) (map[string]interface{}, error)) bool {
	// Get current working directory
	cwd, err := os.Getwd()
	if err != nil {
		// Best-effort: without a cwd we cannot resolve a session; log and skip.
		debug.Log("daemon-tools", "auto-attach skipped: getwd failed: %v", err)
		return false
	}

	// Try to attach via the daemon
	result, err := sessionAttach(cwd)
	if err != nil {
		// No session registered for this directory is the common case, not an
		// error; surface to the debug log for diagnosis but do not fail the tool.
		debug.Log("daemon-tools", "auto-attach: no session for %s: %v", cwd, err)
		return false
	}

	// Successfully attached
	code, ok := result["session_code"].(string)
	if !ok || code == "" {
		return false
	}
	dt.sessionMu.Lock()
	dt.sessionCode = code
	dt.sessionMu.Unlock()

	// Log the attachment (to stderr so it doesn't interfere with MCP)
	if projectPath, ok := result["project_path"].(string); ok {
		fmt.Fprintf(os.Stderr, "[agnt] Attached to session %s (project: %s)\n", code, projectPath)
	}
	return true
}

// forgetAttachment drops the recorded session binding because the daemon
// connection it was made on is gone. The next ensureConnected attaches the
// new connection; until then the wrapper must not report itself attached.
func (dt *DaemonTools) forgetAttachment() {
	dt.sessionMu.Lock()
	defer dt.sessionMu.Unlock()
	dt.attachAttempted = false
	dt.sessionCode = ""
}

// reattach restores the session binding on a connection the ResilientClient
// just re-established. The daemon binds a session to a connection, not to a
// client, so the fresh connection starts unbound even though this wrapper
// still holds a session code; commands that resolve the project from the
// connection (publish, PROC RUN without project_path) would otherwise fail
// with "no active session with project path".
//
// It never fails the reconnect: with no session to attach (daemon restarted,
// session ended) the stale code is dropped and the next tool call retries.
func (dt *DaemonTools) reattach(client *daemonclient.Client) error {
	dt.reregisterChannelSession(client)

	dt.sessionMu.Lock()
	skip := dt.noAutoAttach || !dt.attachAttempted
	dt.sessionMu.Unlock()
	if skip {
		// Never attached on the old connection: the first tool call does it.
		return nil
	}
	if !dt.attach(client.SessionAttach) {
		dt.forgetAttachment()
	}
	return nil
}

// reregisterChannelSession recreates the channel-owned session on a connection
// the ResilientClient just re-established. Unlike a cwd session, the channel
// session is created by this process, so a restarted daemon has no record of it
// and heartbeats would target a nonexistent session. Same code, so the incident
// digest stream and heartbeat loop keep working unchanged.
func (dt *DaemonTools) reregisterChannelSession(client *daemonclient.Client) {
	dt.sessionMu.Lock()
	code, projectPath := dt.channelSessionCode, dt.channelProjectPath
	dt.sessionMu.Unlock()
	if code == "" {
		return
	}
	if _, err := client.SessionRegisterWithPGID(code, "-", projectPath, "mcp", nil, 0); err != nil {
		fmt.Fprintf(os.Stderr, "[agnt] channel session: re-registration failed: %v\n", err)
	}
}

// ensureConnected ensures we have a connection to the daemon with automatic version checking and upgrade.
// It also attempts to auto-attach to a session on first connection.
func (dt *DaemonTools) ensureConnected() error {
	if err := dt.ensureClient(); err != nil {
		return err
	}

	// Auto-attach outside the client lock: it is a daemon round trip, and
	// holding clientMu across it would serialise every concurrent tool call
	// behind one session lookup. tryAutoAttach is idempotent on its own flag.
	dt.tryAutoAttach()
	return nil
}

// ensureClient lazily builds the ResilientClient, at most once across
// concurrent callers.
func (dt *DaemonTools) ensureClient() error {
	dt.clientMu.Lock()
	defer dt.clientMu.Unlock()

	if dt.client != nil && dt.client.IsConnected() {
		return nil
	}

	// A client that is present but not connected is replaced, not reused:
	// close it so its reconnect loop does not outlive it, and forget the
	// session binding that lived on its connection. It stays in dt.client
	// (closed) until a replacement connects, so a handler racing this never
	// reads nil — see currentClient.
	if dt.client != nil {
		dt.client.Close()
		dt.forgetAttachment()
	}

	// Create ResilientClient with version checking and auto-upgrade
	resilientConfig := daemonclient.DefaultResilientClientConfig()
	resilientConfig.OnReconnect = dt.reattach
	resilientConfig.AutoStartConfig = dt.config
	resilientConfig.ClientVersion = dt.version

	// Configure auto-upgrade callback for version mismatches
	resilientConfig.OnVersionMismatch = func(clientVer, daemonVer string) error {
		fmt.Fprintf(os.Stderr, "[agnt] Version mismatch detected: client=%s daemon=%s\n", clientVer, daemonVer)
		// Upgrade() is a no-op for a daemon that is newer than this binary, so
		// reporting "upgraded" would be false; say what happens instead.
		if newer, perr := updater.IsNewerVersion(daemonVer, clientVer); perr == nil && newer {
			fmt.Fprintf(os.Stderr, "[agnt] Daemon %s is newer than this client %s; not downgrading\n", daemonVer, clientVer)
			return nil
		}
		fmt.Fprintf(os.Stderr, "[agnt] Triggering automatic daemon upgrade...\n")

		// Create upgrader
		upgrader := daemonclient.NewDaemonUpgrader(daemonclient.UpgradeConfig{
			SocketPath:      dt.config.SocketPath,
			Timeout:         30 * time.Second,
			GracefulTimeout: 5 * time.Second,
			Verbose:         false, // Don't spam logs during auto-upgrade
		})

		// Run upgrade with timeout
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := upgrader.Upgrade(ctx); err != nil {
			return fmt.Errorf("auto-upgrade failed: %w", err)
		}

		fmt.Fprintf(os.Stderr, "[agnt] ✓ Daemon upgraded to %s\n", clientVer)
		return nil
	}

	// Create and connect ResilientClient. Name the socket and the recovery
	// step: this error is what an agent sees in place of every tool result, so
	// a bare "failed to connect" costs a round trip to find out where it even
	// looked.
	client := daemonclient.NewResilientClient(resilientConfig)
	if err := client.Connect(); err != nil {
		socketPath := dt.config.SocketPath
		if socketPath == "" {
			socketPath = daemonclient.DefaultSocketPath()
		}
		return fmt.Errorf("cannot reach the agnt daemon at %s: %w "+
			"(the daemon auto-starts on demand; if this persists, run `agnt doctor` "+
			"to inspect it or `agnt up` to start one explicitly)", socketPath, err)
	}

	dt.client = client
	return nil
}

// resetClient closes the daemon client so the next ensureConnected rebuilds
// it. Used by the daemon stop/restart tools, which invalidate the connection
// out from under every other in-flight tool call.
//
// The closed client is left in place rather than set to nil: a handler that
// passed ensureConnected just before this ran then gets the client's
// "shut down" error from its request instead of a nil-pointer panic.
// ensureClient replaces it because a closed client never reports connected.
func (dt *DaemonTools) resetClient() {
	dt.clientMu.Lock()
	defer dt.clientMu.Unlock()

	if dt.client != nil {
		dt.client.Close()
	}
	// The session binding went with the connection.
	dt.forgetAttachment()
}

// currentClient returns the daemon client as of now, read under clientMu so
// it cannot race resetClient or ensureClient swapping it. Every read of
// dt.client outside those two goes through here. It is nil only before the
// first successful ensureConnected; after that it may be a closed client
// (resetClient), whose requests fail with an error rather than panic.
func (dt *DaemonTools) currentClient() *daemonclient.ResilientClient {
	dt.clientMu.Lock()
	defer dt.clientMu.Unlock()
	return dt.client
}

// Close closes the daemon client connection.
func (dt *DaemonTools) Close() error {
	if c := dt.currentClient(); c != nil {
		return c.Close()
	}
	return nil
}

// RunAutostart triggers a non-interactive autostart for the given project
// directory via the daemon. Used by the MCP InitializedHandler in channel
// mode. Returns the raw autostart result map from the daemon.
func (dt *DaemonTools) RunAutostart(projectDir string) (map[string]interface{}, error) {
	if err := dt.ensureConnected(); err != nil {
		return nil, err
	}
	return dt.currentClient().Client().AutostartRun(projectDir)
}

// ReplaytestLogClient lazily resolves a connected daemon client for the
// replaytest record/stop actions, ensuring the daemon connection is live before
// returning it. The returned *daemonclient.Client satisfies replaytestLogClient.
func (dt *DaemonTools) ReplaytestLogClient() (replaytestLogClient, error) {
	if err := dt.ensureConnected(); err != nil {
		return nil, err
	}
	return dt.currentClient().Client(), nil
}

// StartChannelSink starts a goroutine that subscribes to daemon StreamEvents
// and forwards matching entries as MCP channel notifications. Error-class
// events the incident pipeline ingests are not forwarded here (ChannelSink
// drops them); they arrive through StartIncidentDigestSink instead. Returns a cancel
// function to stop the sink. No-op and returns nil if channel is not enabled.
// projectPath scopes the subscription to proxies for that project directory
// (never global; see ChannelSinkFilter); "" falls back to getProjectPath().
func (dt *DaemonTools) StartChannelSink(server *mcp.Server, cfg *config.ChannelConfig, projectPath string) context.CancelFunc {
	if cfg == nil || !cfg.IsEnabled() {
		return func() {}
	}

	ctx, cancel := context.WithCancel(context.Background())

	notify := func(ctx context.Context, method string, params any) error {
		for session := range server.Sessions() {
			// Best-effort fan-out: one slow/broken session must not block the
			// others, so a failed Notify is logged, not propagated.
			if err := session.Notify(ctx, method, params); err != nil {
				debug.Log("channel-sink", "notify failed: %v", err)
			}
		}
		return nil
	}

	sink := NewChannelSink(cfg, notify)

	filter := ChannelSinkFilter(cfg, projectPath)

	go func() {
		for {
			if ctx.Err() != nil {
				return
			}
			// Create a raw client for streaming (dedicated socket per stream).
			client := daemonclient.NewClient(daemonclient.WithSocketPath(dt.config.SocketPath))
			err := client.StreamEvents(ctx, filter, func(entry proxy.LogEntry) error {
				sink.HandleEntry(ctx, entry)
				return nil
			})
			if err != nil {
				debug.Log("channel-sink", "stream ended: %v", err)
			}
			// Reconnect after brief pause unless cancelled.
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}()

	return cancel
}

// ChannelSinkFilter builds the STREAM-EVENTS filter for the channel sink: the
// event types the channel cares about, scoped to this MCP process's project so
// two simultaneous MCP clients don't receive each other's browser events.
// Global is pinned to an explicit false (as in IncidentDigestFilter): an omitted
// global falls back to the project's `scope.default-global`, which would widen
// the subscription to every project's proxies. Empty projectPath falls back to
// getProjectPath().
func ChannelSinkFilter(cfg *config.ChannelConfig, projectPath string) protocol.StreamEventFilter {
	if projectPath == "" {
		projectPath = getProjectPath()
	}
	filter := protocol.StreamEventFilter{
		ProjectPath:    projectPath,
		GlobalOverride: protocol.Bool(false),
	}
	if cfg != nil {
		if events := cfg.GetEvents(); len(events) > 0 {
			filter.Types = events
		}
	}
	return filter
}

// IncidentDigestFilter builds the STREAM-EVENTS filter for the digest sink.
//
// It is ALWAYS project-scoped and never global: the daemon stamps each digest
// with its owning session's project (broadcastIncidentDigest), and a global
// subscription disables that filter, so every `agnt mcp` process would forward
// every other project's "you have incidents" ping to its own agent. The scope
// is named the same way every other tool names it (sessionScope): the attached
// session code when there is one, else the project directory. Global is pinned
// to an explicit false so a project's `scope.default-global` cannot widen it.
//
// Type-only (no severity) so the digest is never dropped by the stream's
// severity gate.
func IncidentDigestFilter(sessionCode, projectPath string) protocol.StreamEventFilter {
	filter := protocol.StreamEventFilter{
		Types:          []string{string(proxy.LogTypeIncidentDigest)},
		GlobalOverride: protocol.Bool(false),
	}
	if sessionCode != "" {
		filter.SessionCode = sessionCode
	} else {
		filter.Directory = projectPath
	}
	return filter
}

// StartIncidentDigestSink subscribes an always-on goroutine to the daemon event
// stream filtered to incident_digest entries and delivers each to all connected
// sessions on one transport: a claude/channel event when channel mode is
// enabled, an MCP Log notification otherwise (ForwardIncidentDigest). It is the
// single push path for errors — the channel sink no longer forwards them raw —
// and it runs whether or not channel mode is on. Returns a cancel function.
//
// projectPath scopes the subscription to this MCP process's project (see
// IncidentDigestFilter); empty falls back to getProjectPath().
func (dt *DaemonTools) StartIncidentDigestSink(server *mcp.Server, projectPath string, channelCfg *config.ChannelConfig) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())

	channelOn := channelCfg != nil && channelCfg.IsEnabled()
	// Best-effort fan-out on both transports: a failed send to one session
	// must not block the digest reaching the others.
	logFn := func(ctx context.Context, level, message string) error {
		for session := range server.Sessions() {
			if err := session.Log(ctx, &mcp.LoggingMessageParams{
				Level:  mcp.LoggingLevel(level),
				Logger: "agnt-incidents",
				Data:   message,
			}); err != nil {
				debug.Log("incident-digest-sink", "log notify failed: %v", err)
			}
		}
		return nil
	}
	notify := func(ctx context.Context, method string, params any) error {
		for session := range server.Sessions() {
			if err := session.Notify(ctx, method, params); err != nil {
				debug.Log("incident-digest-sink", "channel notify failed: %v", err)
			}
		}
		return nil
	}

	if projectPath == "" {
		projectPath = getProjectPath()
	}

	go func() {
		// A session code that has since gone stale makes the daemon reject the
		// subscription; fall back to the project directory on the next attempt
		// rather than retrying a dead code forever.
		sessionRejected := false
		for {
			if ctx.Err() != nil {
				return
			}
			sessionCode := ""
			if !sessionRejected {
				sessionCode = dt.SessionCode()
			}
			filter := IncidentDigestFilter(sessionCode, projectPath)
			client := daemonclient.NewClient(daemonclient.WithSocketPath(dt.config.SocketPath))
			err := client.StreamEvents(ctx, filter, func(entry proxy.LogEntry) error {
				ForwardIncidentDigest(ctx, entry, channelOn, logFn, notify)
				return nil
			})
			if err != nil {
				debug.Log("incident-digest-sink", "stream ended: %v", err)
				sessionRejected = sessionCode != "" && !sessionRejected
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}()

	return cancel
}

// ChannelSessionHandle manages the lifecycle of a daemon session registered
// by agnt mcp in channel mode. Call Close to unregister the session and stop
// the heartbeat. The handle owns the heartbeat goroutine's cancel function
// so Close() signals the goroutine via context cancellation — no shared
// stop-channel field on DaemonTools, no Close-vs-Init race.
type ChannelSessionHandle struct {
	dt       *DaemonTools
	cancelHB context.CancelFunc // Cancels the heartbeat goroutine's context.
	stopOnce sync.Once
}

// Close unregisters the channel session from the daemon and stops the
// heartbeat goroutine. stopOnce guarantees the ctx cancel + daemon
// unregister pair fire exactly once, even under concurrent Close calls.
func (h *ChannelSessionHandle) Close() {
	if h == nil || h.dt == nil {
		return
	}
	h.stopOnce.Do(func() {
		if h.cancelHB != nil {
			h.cancelHB()
		}
		h.dt.closeChannelSession()
	})
}

// RegisterChannelSession registers a daemon session for the MCP process when
// running in channel mode. In channel mode there is no PTY child, so
// SessionPGID is 0 (pgid cleanup is a no-op; managed processes still clean up
// normally via the daemon's ProcessManager). Returns a handle that must be
// closed on shutdown to unregister the session and stop the heartbeat.
// Returns nil if channel mode is disabled.
func (dt *DaemonTools) RegisterChannelSession(ctx context.Context, cfg *config.ChannelConfig, projectPath string) *ChannelSessionHandle {
	if cfg == nil || !cfg.IsEnabled() {
		return nil
	}

	if err := dt.ensureConnected(); err != nil {
		fmt.Fprintf(os.Stderr, "[agnt] channel session: failed to connect to daemon: %v\n", err)
		return nil
	}

	client := dt.currentClient()
	if client == nil {
		fmt.Fprintf(os.Stderr, "[agnt] channel session: daemon client was reset before registration\n")
		return nil
	}

	// Generate a session code for this MCP instance the way classic sessions
	// do (crypto/rand suffix): SESSION REGISTER treats an existing code as a
	// reconnect, so a colliding code would take over another session.
	sessionCode, err := client.SessionGenerateCode("mcp")
	if err != nil {
		fmt.Fprintf(os.Stderr, "[agnt] channel session: %v\n", err)
		return nil
	}

	// Register with SessionPGID=0 (no PTY child in channel mode).
	// Overlay path is "-" because there is no overlay socket in channel mode.
	_, err = client.SessionRegisterWithPGID(sessionCode, "-", projectPath, "mcp", nil, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[agnt] channel session: registration failed: %v\n", err)
		return nil
	}

	dt.sessionMu.Lock()
	dt.channelSessionCode = sessionCode
	dt.channelProjectPath = projectPath
	dt.sessionMu.Unlock()

	// Child context owned by the returned handle. Close() cancels it;
	// the goroutine selects only on ticker + hbCtx.Done() so there is no
	// shared-state read from DaemonTools inside the hot loop.
	hbCtx, cancelHB := context.WithCancel(ctx)

	// Start heartbeat goroutine.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				// Read the client under clientMu each tick: the daemon
				// stop/restart tools swap it out via resetClient.
				if c := dt.currentClient(); c != nil && c.IsConnected() {
					// Best-effort: a missed heartbeat is recovered by the next
					// tick; log so a persistent failure is diagnosable.
					if err := c.SessionHeartbeat(sessionCode); err != nil {
						debug.Log("channel-session", "heartbeat failed: %v", err)
					}
				}
			case <-hbCtx.Done():
				return
			}
		}
	}()

	return &ChannelSessionHandle{dt: dt, cancelHB: cancelHB}
}

// closeChannelSession unregisters the channel session from the daemon.
// The heartbeat goroutine is stopped by ChannelSessionHandle.Close via
// context cancellation before this runs, so there is no stop channel to
// touch here.
func (dt *DaemonTools) closeChannelSession() {
	dt.sessionMu.Lock()
	code := dt.channelSessionCode
	dt.channelSessionCode = ""
	dt.channelProjectPath = ""
	dt.sessionMu.Unlock()

	if code == "" {
		return
	}

	// Unregister from daemon so CleanupSessionResources fires.
	if c := dt.currentClient(); c != nil && c.IsConnected() {
		// Best-effort cleanup on shutdown: the daemon also reaps the session on
		// connection drop, so a failed unregister is logged, not fatal.
		if err := c.SessionUnregister(code); err != nil {
			debug.Log("channel-session", "unregister failed: %v", err)
		}
	}
}

// ChannelSessionCode returns the session code for the active channel session,
// or empty string if none is registered.
func (dt *DaemonTools) ChannelSessionCode() string {
	dt.sessionMu.Lock()
	defer dt.sessionMu.Unlock()
	return dt.channelSessionCode
}

// RegisterDaemonTools adds all MCP tools that communicate with the daemon.
func RegisterDaemonTools(server *mcp.Server, dt *DaemonTools) {
	// Project tools
	addLenientTool(server, &mcp.Tool{
		Name: "detect",
		Description: `Detect project type and available scripts.
Returns ready-to-paste proc run / proc wait invocations and likely_signals
heuristics for each script, so agents can chain detect → proc run → proc wait
without hand-constructing commands.

Examples:
  detect {}
  detect {path: "."}
  detect {raw: true}   // skip compact text, return JSON only`,
	}, dt.makeDetectHandler())

	// Process tools
	addLenientTool(server, &mcp.Tool{
		Name: "run",
		Description: `Run a project script or raw command.

Modes:
  background (default): Returns process_id immediately for tracking via proc tool
  foreground: Waits for completion, returns exit_code/state/runtime (output via proc)
  foreground-raw: Waits for completion, returns exit_code/state/runtime + stdout/stderr

A script declared in .agnt.kdl runs with everything it declares there: env, cwd,
shell, ports, url-matchers, depends-on, auto-restart, and lifecycle hooks.

IDs: the returned process_id is project-scoped ("myapp-1a2b3c4d:dev"); name is the
short name ("dev" - the script name, or the id you passed). proc accepts either
one from this project. Running an id that is already running returns that process.

Auto-restart: run does not auto-restart by default. A process restarts on crash only
when its script declares auto-restart in .agnt.kdl, or after it is enabled with
proc {action: "autorestart"} (rate-limited: max 5 restarts/minute by default).
no_auto_restart: true turns a declared script's auto-restart off for this launch.
  run {script_name: "dev"}                         # Restarts on crash only if .agnt.kdl says so
  run {script_name: "dev", no_auto_restart: true}  # Ignore the configured auto-restart this time
  proc {action: "autorestart", process_id: "dev", auto_restart_enable: true}  # Enable afterwards

Restarting: Use proc restart (preferred) or stop then run again:
  proc {action: "restart", process_id: "dev"}
Never use pkill or external commands - always use proc stop/restart for clean shutdown.

Examples:
  run {script_name: "test"}
  run {script_name: "dev"}
  run {script_name: "test", mode: "foreground"}
  run {script_name: "test", mode: "foreground-raw"}
  run {raw: true, command: "go", args: ["mod", "tidy"], mode: "foreground-raw"}`,
	}, dt.makeRunHandler())

	addLenientTool(server, &mcp.Tool{
		Name: "proc",
		Description: `Manage running processes.

Actions:
  list: List all running processes (use global: true for all directories)
  status: Get process status and info
  output: Get process output (tail/grep supported, falls back to script output history)
  stop: Gracefully stop a process (use force: true for immediate kill)
  restart: Restart a running process (stop then start with same config). For .agnt.kdl autostart scripts, process_id = script name.
  cleanup_port: Kill any process using a specific port
  autorestart: Enable/disable automatic restart when process exits
  scripts: List all registered scripts with state and start/fail counts
  script_output: Get script output history across restarts
  script_history: Get script state transition history
  snapshot: Unified dev-environment status (processes + proxies + recent errors + URLs + suggested next actions) in a single call. Use raw:true for structured JSON.
  find: Extract build warnings, compile errors, and test failures from a process's output buffer (framework-aware classification).
  run: Start a single admin-aware process; visible in proc list and SCRIPT LIST. Optionally gate on depends_on:[...] (default 30s timeout). Process state is "pending" while waiting; "starting" once deps clear; "failed" on dependency_timeout.
  run_group: Start a multi-process stack in one call. Cycle detection runs before any process launches; on cycle the call returns an error and no process is started. Each process may declare depends_on referencing other processes in the group; topo order is enforced via the readiness signaler.
  wait: Block until a named signal appears in a process's output. Polls (default 200ms, override with poll_ms) until match or timeout (default 30000ms). Returns {signal, matched_line, elapsed_ms} on hit, {timeout: true} on timeout — never errors. Pass signals:[...] to wait for whichever comes first.

Multi-stream output and signals:
  proc {action:"output", process_ids:["build","server","test"]}
  Pulls output from N processes in one call. Each line tagged "[id]" in compact output, NDJSON in raw mode.
  Add extract:["url","error","warning","ready","port"] to scan for structured signals — surfaced per-process under multi_stream[*].signals (or top-level signals when single process_id is used).
  Coordinates with proc snapshot: snapshot uses the unified-error classification pipeline (framework-aware), extract scans raw line buffers (regex, framework-agnostic).

Restarting dev servers:
  proc {action: "restart", process_id: "dev"}
Never use pkill or external commands - always use proc stop/restart for clean shutdown.

Auto-restart: off unless the script declares auto-restart in .agnt.kdl or it is enabled
here (rate-limited to max 5 restarts/minute by default). Use the autorestart action to
enable or disable it:
  proc {action: "autorestart", process_id: "dev", auto_restart_enable: true}   # Enable
  proc {action: "autorestart", process_id: "dev", auto_restart_enable: false}  # Disable

Examples:
  proc {action: "list"}
  proc {action: "status", process_id: "test"}
  proc {action: "output", process_id: "test", tail: 20}
  proc {action: "output", process_id: "test", grep: "FAIL"}
  proc {action: "stop", process_id: "test"}
  proc {action: "stop", process_id: "test", force: true}
  proc {action: "restart", process_id: "dev"}
  proc {action: "cleanup_port", port: 3000}
  proc {action: "autorestart", process_id: "dev", auto_restart_enable: false}
  proc {action: "scripts"}
  proc {action: "script_output", script_name: "dev", tail: 50}
  proc {action: "script_history", script_name: "dev"}
  proc {action: "snapshot"}                  # Compact dev environment summary
  proc {action: "snapshot", raw: true}       # Full JSON of processes, proxies, errors, urls, suggested_next
  proc {action: "output", process_ids: ["build","server"], extract: ["error","ready"]}  # Multi-stream + signals
  proc {action: "wait", process_id: "server", signal: "ready", timeout: 30000}          # Block until ready
  proc {action: "wait", process_id: "build", signals: ["ready", "error"], timeout: 60000}  # First wins
  proc {action: "run", id: "api", run: "go run ./cmd/api", depends_on: ["db"]}
  proc {action: "run_group", processes: [
    {id: "db",  run: "docker compose up db"},
    {id: "api", run: "go run ./cmd/api", depends_on: ["db"]},
    {id: "web", run: "npm run dev", depends_on: ["api"]}
  ]}`,
	}, dt.makeProcHandler())

	// Proxy tools
	addLenientTool(server, &mcp.Tool{
		Name:        "proxy",
		Description: ProxyToolDescription,
	}, dt.makeProxyHandler())

	addLenientTool(server, &mcp.Tool{
		Name: "proxylog",
		Description: `Query and analyze proxy traffic logs.

Actions:
  query: Search logs with filters (default, may be large)
  summary: Get compact aggregated summary (recommended for large logs)
  clear: Clear all logs for a proxy
  stats: Get log statistics

Log Types:
  http: HTTP request/response pairs
  error: Frontend JavaScript errors with stack traces
  performance: Page load and resource timing metrics
  custom: Custom log messages from __devtool.log()
  screenshot: Screenshots captured via __devtool.screenshot()
  execution: Results of executed JavaScript code
  response: JavaScript execution responses
  interaction: User interactions (clicks, keyboard, scroll)
  mutation: DOM mutations (added, removed, modified elements)
  diagnostic: Framework/runtime diagnostics (severity: error, warning, info)
  panel_message: Messages sent from the floating indicator panel
  sketch: Sketches/wireframes from sketch mode (includes JSON data and PNG image path)

Summary Action (Recommended for Large Logs):
  The summary action aggregates logs by type and provides:
  - Counts by type (errors, http, performance, etc.)
  - Deduplicated error summaries (top 10 unique errors)
  - HTTP status/method breakdown
  - Average performance metrics
  - Recent entries for each type (last 5)

  Progressive reveal with detail parameter:
  - detail: ["errors"] - include compact error list (truncated stacks)
  - detail: ["http"] - include HTTP request list
  - detail: ["performance"] - include performance metrics
  - detail: ["interactions"] - include user interactions
  - detail: ["mutations"] - include DOM mutations
  - detail: ["other"] - include custom/panel/sketch logs
  - limit: N - max items per detailed section (default: 10, max: 100)

  All data is automatically compacted to prevent token overflow:
  - Error stack traces limited to first 3 lines
  - Messages truncated to 500 chars max
  - URLs truncated to 100 chars
  - Individual stack lines capped at 120 chars

Query Examples:
  proxylog {proxy_id: "dev", types: ["http"], methods: ["GET"]}
  proxylog {proxy_id: "dev", types: ["error"], limit: 5}
  proxylog {proxy_id: "dev", since: "5m", limit: 50}

Summary Examples (Recommended):
  proxylog {proxy_id: "dev", action: "summary"}
  proxylog {proxy_id: "dev", action: "summary", detail: ["errors"]}
  proxylog {proxy_id: "dev", action: "summary", detail: ["errors", "http"], limit: 20}
  proxylog {proxy_id: "dev", action: "summary", types: ["error"]}

Other Actions:
  proxylog {proxy_id: "dev", action: "stats"}
  proxylog {proxy_id: "dev", action: "clear"}

Each proxy maintains its own separate log storage.`,
	}, dt.makeProxyLogHandler())

	addLenientTool(server, &mcp.Tool{
		Name: "currentpage",
		Description: `Inspect the current page: framework diagnostics, layout issues, and session resources/metrics.

Actions:
  triage: (default) Classify React/Next/Vue/Svelte/Solid runtime problems (hydration, key,
          infinite-loop, reactivity) into category + fix + anti-pattern. Start here.
  layout: Diagnose containing-block traps, ineffective z-index, click interception, and
          clipped descendants — names the offending ancestor and the fix.
  list: List all active page sessions
  get: Get detailed information for a specific session (may be large)
  summary: Get a compact summary optimized for long/complex pages
  clear: Clear all page sessions

A page session groups together:
  - The initial HTML document request
  - All associated resource requests (JS, CSS, images, etc.)
  - Frontend JavaScript errors from that page
  - Performance metrics (page load time, paint timing, etc.)
  - User interactions (clicks, keyboard, scroll, etc.)
  - DOM mutations (added, removed, modified elements)

Examples:
  currentpage {proxy_id: "dev"}                       // triage (default)
  currentpage {proxy_id: "dev", action: "layout"}     // layout diagnosis
  currentpage {proxy_id: "dev", action: "summary", session_id: "page-1"}
  currentpage {proxy_id: "dev", action: "summary", session_id: "page-1", detail: ["interactions"], limit: 20}
  currentpage {proxy_id: "dev", action: "summary", session_id: "page-1", detail: ["interactions", "mutations"]}
  currentpage {proxy_id: "dev", action: "get", session_id: "page-1"}
  currentpage {proxy_id: "dev", action: "clear"}

The list action returns summary counts (interaction_count, mutation_count).
The summary action returns aggregated data (errors by type, interactions by type,
  last 5 interactions/mutations) - best for long pages to avoid context overflow.
  Use detail parameter to get full data for specific sections:
  - detail: ["interactions"] - include full interaction list
  - detail: ["mutations"] - include full mutation list
  - detail: ["errors"] - include compact error list (truncated stacks/messages)
  - detail: ["resources"] - include full resource URL list

Error format is automatically compacted to prevent token overflow:
  - Stack traces limited to first 3 lines
  - Messages truncated to 500 chars max
  - Source paths reduced to filename only
  - Individual stack lines capped at 120 chars
  - limit: N - max items per detailed section (default: 5, max: 100)
The get action returns full interaction and mutation history (may be large).

This provides a high-level view of active pages and their resources.`,
	}, dt.makeCurrentPageHandler())

	// Demo tool - narrated demo-video authoring surface. Shells to the in-repo
	// engine (docs-site/screenshots/engine/demo.mjs) as a daemon-managed
	// process so long recordings survive and report like any managed script.
	addLenientTool(server, &mcp.Tool{
		Name: "demo",
		Description: `Author narrated demo videos with the in-repo demo engine.

This is a repo-checkout capability: it drives docs-site/screenshots/engine/demo.mjs,
which ships in the agnt repository, not the installed binary. Running it from a
project without that engine tree returns a loud error naming the requirement.

Actions:
  list:     Enumerate demos under docs-site/screenshots/demos with a per-demo
            segment and narration summary.
  record:   Start a recording as a daemon-managed process; returns a process_id
            immediately. Observe/stop it via the proc tool (output/status/stop).
            Pass only:"seg1,seg2" to record specific segments instead of the whole demo.
  assemble: Re-mux an already-recorded demo from its segment captures (engine
            --assemble-only) as a managed process; returns a process_id.
  inspect:  Cut-point authoring aid: renders each recorded take into a contact
            sheet (one thumbnail/second, marks on a timeline strip) under
            demos/<name>/out/inspect. Read-only over the takes. Pass only:"seg"
            for a single segment. Managed process id: demo-inspect-<name>.
  publish:  Promote an already-rendered demo into docs-site/static (clip,
            captions when present, poster, animated webp) and print the
            <ModeVideo/> embed snippet. Needs ffmpeg and a prior record or
            assemble; writes nothing otherwise. Managed process id:
            demo-publish-<name>.

Examples:
  demo {action: "list"}
  demo {action: "record", name: "incident-inbox"}
  demo {action: "record", name: "incident-inbox", only: "card-intro,fix"}
  demo {action: "assemble", name: "incident-inbox"}
  demo {action: "inspect", name: "incident-inbox", only: "fix"}
  demo {action: "publish", name: "incident-inbox"}
  proc {action: "output", process_id: "demo-incident-inbox"}
  proc {action: "stop", process_id: "demo-incident-inbox"}`,
	}, dt.makeDemoHandler())

	// Watch tool - returns monitor command for Claude's Monitor tool
	addLenientTool(server, &mcp.Tool{
		Name: "watch",
		Description: `Get the shell command for streaming agnt events via Monitor.

Returns a command string that can be passed to Claude's Monitor tool.
Bridges the gap between the MCP client (which knows the daemon socket) and Monitor.

Targets:
  errors: Browser JS errors, HTTP errors, diagnostics
  interactions: Panel messages, clicks, keyboard, sketch events
  process: Process output for a specific process
  all: All agnt events (default)

Examples:
  watch {target: "errors", proxy_id: "dev"}
  watch {target: "interactions", proxy_id: "dev"}
  watch {target: "process", process_id: "app"}
  watch {target: "all"}
  watch {}`,
	}, dt.makeWatchHandler())

	// Session tool - register via separate function for organization
	RegisterSessionTool(server, dt)

	// Store tool - register via separate function for organization
	RegisterStoreTool(server, dt)

	// Publish tool - public walkthrough share-token lifecycle (control plane)
	RegisterPublishTool(server, dt)

	// External error queue tool
	RegisterErrorQueueTool(server, dt)
}

// makeDetectHandler creates a handler for the detect tool.
//
// Daemon mode uses a hybrid path: the daemon roundtrip confirms project type
// and package manager (so the daemon's view stays authoritative for any
// daemon-side state), but the script enrichment (CommandDef → DetectScript
// with proc_run / proc_wait / likely_signals) is built locally from the same
// `project.Detect` call. This avoids a protocol bump while keeping the
// daemon-mode output identical to the legacy-mode output.
func (dt *DaemonTools) makeDetectHandler() func(context.Context, *mcp.CallToolRequest, DetectInput) (*mcp.CallToolResult, DetectOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input DetectInput) (*mcp.CallToolResult, DetectOutput, error) {
		// Empty output with non-nil Scripts to avoid null in JSON schema validation.
		emptyOutput := DetectOutput{Scripts: []DetectScript{}, ScriptNames: []string{}}

		if err := validateDetectInput(input); err != nil {
			return errorResult(validationError("detect", err)), emptyOutput, nil
		}

		if err := dt.ensureConnected(); err != nil {
			return errorResult(err.Error()), emptyOutput, nil
		}

		// Resolve path to absolute to ensure daemon uses correct directory.
		// Use session project path (from AGNT_PROJECT_PATH) when path is not specified.
		path := input.Path
		if path == "" {
			path = getProjectPath()
		}
		absPath, err := filepath.Abs(path)
		if err != nil {
			return errorResult(fmt.Sprintf("failed to resolve path: %v", err)), emptyOutput, nil
		}

		// Daemon roundtrip — keeps the daemon authoritative for type/package_manager
		// and lets it surface any daemon-side errors (permission, missing path, ...).
		if _, err := dt.currentClient().Detect(absPath); err != nil {
			return formatDaemonError(err, "detect"), emptyOutput, nil
		}

		// Re-detect locally to enrich with the full CommandDef list.
		// `project.Detect` is a pure filesystem read — same call the daemon
		// just made — so this stays consistent without a protocol bump. The
		// enrichment/formatting is shared with the detect unit tests.
		return detectAndFormat(absPath, input.Raw)
	}
}

// makeRunHandler creates a handler for the run tool.

func (o PageSessionOutput) MarshalJSON() ([]byte, error) {
	type Alias PageSessionOutput
	return json.Marshal(&struct {
		Alias
	}{
		Alias: Alias(o),
	})
}

// normalizeAddr replaces 0.0.0.0 or [::] with localhost for display.
