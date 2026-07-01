package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/standardbeagle/agnt/internal/agentadapter"
	"github.com/standardbeagle/agnt/internal/agntprompt"
	"github.com/standardbeagle/agnt/internal/aichannel"
	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/overlay"
	"github.com/standardbeagle/agnt/internal/pathutil"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/tools"
)

// portConflictInfo describes a port conflict detected during autostart.
type portConflictInfo struct {
	ScriptName  string
	Port        int
	PIDs        []int
	ProcessName string
}

// daemonSessionHandle manages the daemon connection and session registration.
// It encapsulates the resilient client, heartbeat goroutine, and session state.
type daemonSessionHandle struct {
	client            *daemon.ResilientClient
	heartbeatStop     chan struct{}
	sessionCode       string
	sessionRegistered bool

	// Completion signaling for async registration
	registrationDone chan struct{}
	// registrationErr records why the session failed to connect or register,
	// if it did. Written by the registration goroutine before closing
	// registrationDone (happens-before the WaitRegistered reader), read by
	// status printers. Surfaced to the user instead of being swallowed.
	registrationErr  error
	autostartScripts []string // scripts successfully started
	autostartProxies []string // proxies successfully started
	autostartErrors  []string // errors encountered during autostart
	portConflicts    []portConflictInfo
	portsCleared     []portConflictInfo
	projectPath      string
}

// Close cleans up daemon session resources.
// Stops heartbeat, unregisters session, and closes the client connection.
func (h *daemonSessionHandle) Close() {
	if h == nil {
		return
	}
	// Stop heartbeat
	if h.heartbeatStop != nil {
		close(h.heartbeatStop)
	}
	// Unregister session
	if h.client != nil && h.sessionRegistered {
		_ = h.client.SessionUnregister(h.sessionCode)
	}
	// Close client
	if h.client != nil {
		h.client.Close()
	}
}

// WaitRegistered blocks until the registration goroutine completes or the timeout expires.
// Returns true if registration completed within the timeout.
func (h *daemonSessionHandle) WaitRegistered(timeout time.Duration) bool {
	if h == nil || h.registrationDone == nil {
		return false
	}
	select {
	case <-h.registrationDone:
		return true
	case <-time.After(timeout):
		return false
	}
}

// IsConnected returns true if the daemon client is connected.
func (h *daemonSessionHandle) IsConnected() bool {
	return h != nil && h.client != nil && h.client.IsConnected()
}

// BroadcastActivity sends activity state to the daemon.
func (h *daemonSessionHandle) BroadcastActivity(active bool) {
	if h.IsConnected() {
		_ = h.client.BroadcastActivity(active)
	}
}

// SetForwarding tells the daemon to pause/resume agent-inbound push (incident
// digest pings) for this session. Best-effort; the in-process PTY gate is the
// primary path and does not depend on this succeeding.
func (h *daemonSessionHandle) SetForwarding(paused bool) {
	if h.IsConnected() {
		_ = h.client.SetForwarding(paused)
	}
}

// BroadcastOutputPreview sends output preview lines to the daemon.
func (h *daemonSessionHandle) BroadcastOutputPreview(lines []string) {
	if h.IsConnected() {
		_ = h.client.BroadcastOutputPreview(lines)
	}
}

// daemonSessionConfig contains configuration for daemon session registration.
type daemonSessionConfig struct {
	SessionCode       string
	OverlayEndpoint   string
	ProjectPath       string
	Command           string
	CmdArgs           []string
	SocketPath        string
	SkipAutostart     bool
	HeartbeatInterval time.Duration
	// SessionPGID is the POSIX process group ID of the PTY child (the
	// session leader). The daemon uses it at session cleanup time to
	// reap every descendant the coding agent spawned via non-interactive
	// bash (`npm run dev &` etc.) that the daemon has no explicit handle
	// on. Zero on Windows (Job Object cleanup is reported via
	// SessionJobHandle instead) or when the caller cannot determine a
	// pgid.
	SessionPGID int
	// SessionJobHandle is the Windows Job Object handle for the PTY
	// child subtree. Windows equivalent of SessionPGID: every process
	// assigned to this job (and its descendants) is killed when the
	// job is terminated via platform.KillSessionJobObject. Stored as
	// uint64 because windows.Handle is not available on non-Windows
	// builds — the protocol-level JSON field is also uint64. Zero on
	// Unix or when the caller cannot create a job.
	SessionJobHandle uint64
}

// startDaemonSession starts daemon connection and session registration in a goroutine.
// Returns a handle that can be used to interact with the daemon and must be closed when done.
// The registration happens asynchronously; use handle.WaitRegistered() to block until complete.
func startDaemonSession(ctx context.Context, cfg daemonSessionConfig) *daemonSessionHandle {
	handle := &daemonSessionHandle{
		sessionCode:      cfg.SessionCode,
		registrationDone: make(chan struct{}),
	}

	go func() {
		defer close(handle.registrationDone)

		config := daemon.DefaultResilientClientConfig()
		if cfg.SocketPath != "" {
			config.AutoStartConfig.SocketPath = cfg.SocketPath
		}

		// Re-register session when connection is restored after daemon restart.
		// Session registration handles per-project overlay endpoint scoping.
		config.OnReconnect = func(client *daemon.Client) error {
			_, _ = client.SessionRegisterWithContainment(cfg.SessionCode, cfg.OverlayEndpoint, cfg.ProjectPath, cfg.Command, cfg.CmdArgs, cfg.SessionPGID, cfg.SessionJobHandle)
			return nil
		}

		handle.client = daemon.NewResilientClient(config)
		if err := handle.client.Connect(); err != nil {
			// Connection is best-effort, but record why so the status
			// printer can tell the user rather than a bare "not available".
			handle.registrationErr = fmt.Errorf("connect: %w", err)
			debug.Log("daemon", "session connect failed: %v", err)
			return
		}

		// Register session with daemon (autostart and overlay scoping happen server-side)
		result, err := handle.client.SessionRegisterWithContainment(cfg.SessionCode, cfg.OverlayEndpoint, cfg.ProjectPath, cfg.Command, cfg.CmdArgs, cfg.SessionPGID, cfg.SessionJobHandle)
		if err != nil {
			handle.registrationErr = fmt.Errorf("register: %w", err)
			debug.Log("daemon", "session register failed: %v", err)
			return
		}

		handle.sessionRegistered = true
		handle.projectPath = cfg.ProjectPath

		// Capture autostart results
		if result != nil && !cfg.SkipAutostart {
			if autostart, ok := result["autostart"].(map[string]interface{}); ok {
				if scripts, ok := autostart["scripts"].([]interface{}); ok {
					for _, s := range scripts {
						if str, ok := s.(string); ok {
							handle.autostartScripts = append(handle.autostartScripts, str)
						}
					}
				}
				if proxies, ok := autostart["proxies"].([]interface{}); ok {
					for _, p := range proxies {
						if str, ok := p.(string); ok {
							handle.autostartProxies = append(handle.autostartProxies, str)
						}
					}
				}
				if errs, ok := autostart["errors"].([]interface{}); ok {
					for _, e := range errs {
						if str, ok := e.(string); ok {
							handle.autostartErrors = append(handle.autostartErrors, str)
						}
					}
				}
				if conflicts, ok := autostart["port_conflicts"].([]interface{}); ok {
					for _, c := range conflicts {
						if cm, ok := c.(map[string]interface{}); ok {
							info := portConflictInfo{}
							if s, ok := cm["script_name"].(string); ok {
								info.ScriptName = s
							}
							if p, ok := cm["port"].(float64); ok {
								info.Port = int(p)
							}
							if s, ok := cm["process_name"].(string); ok {
								info.ProcessName = s
							}
							if pids, ok := cm["pids"].([]interface{}); ok {
								for _, p := range pids {
									if pid, ok := p.(float64); ok {
										info.PIDs = append(info.PIDs, int(pid))
									}
								}
							}
							handle.portConflicts = append(handle.portConflicts, info)
						}
					}
				}
				if cleared, ok := autostart["ports_cleared"].([]interface{}); ok {
					for _, c := range cleared {
						if cm, ok := c.(map[string]interface{}); ok {
							info := portConflictInfo{}
							if s, ok := cm["script_name"].(string); ok {
								info.ScriptName = s
							}
							if p, ok := cm["port"].(float64); ok {
								info.Port = int(p)
							}
							if s, ok := cm["process_name"].(string); ok {
								info.ProcessName = s
							}
							handle.portsCleared = append(handle.portsCleared, info)
						}
					}
				}
			}
		}

		// Start heartbeat goroutine
		interval := cfg.HeartbeatInterval
		if interval == 0 {
			interval = 30 * time.Second
		}
		handle.heartbeatStop = make(chan struct{})
		go func() {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-handle.heartbeatStop:
					return
				case <-ctx.Done():
					return
				case <-ticker.C:
					if handle.IsConnected() {
						_ = handle.client.SessionHeartbeat(cfg.SessionCode)
					}
				}
			}
		}()
	}()

	return handle
}

// mergeAutostartResult updates the handle with results from the resumed autostart.
func mergeAutostartResult(handle *daemonSessionHandle, result map[string]interface{}) {
	if result == nil {
		return
	}
	if scripts, ok := result["scripts"].([]interface{}); ok {
		for _, s := range scripts {
			if str, ok := s.(string); ok {
				handle.autostartScripts = append(handle.autostartScripts, str)
			}
		}
	}
	if proxies, ok := result["proxies"].([]interface{}); ok {
		for _, p := range proxies {
			if str, ok := p.(string); ok {
				handle.autostartProxies = append(handle.autostartProxies, str)
			}
		}
	}
	if errs, ok := result["errors"].([]interface{}); ok {
		for _, e := range errs {
			if str, ok := e.(string); ok {
				handle.autostartErrors = append(handle.autostartErrors, str)
			}
		}
	}
}

// displayAutostartResults waits for daemon registration to complete and shows
// autostart results in the overlay status bar. Success messages appear as a
// transient status bar message that fades back to the normal indicator after 3s.
// Errors are written to the fallback writer since they need more space.
func displayAutostartResults(handle *daemonSessionHandle, ov *overlay.Overlay, w io.Writer, timeout time.Duration) {
	if handle == nil {
		return
	}
	if !handle.WaitRegistered(timeout) {
		return
	}

	// Show auto-cleared ports (auto-kill mode)
	for _, c := range handle.portsCleared {
		fmt.Fprintf(w, "\x1b[33m[agnt] cleared port %d (was: %s PID %v)\x1b[0m\r\n", c.Port, c.ProcessName, c.PIDs)
	}

	// Handle port conflicts (prompt mode)
	if len(handle.portConflicts) > 0 {
		fmt.Fprintf(w, "\x1b[33m[agnt] port conflicts detected:\x1b[0m\r\n")
		for _, c := range handle.portConflicts {
			fmt.Fprintf(w, "\x1b[33m  %d (%s) <- %s (PID %v)\x1b[0m\r\n", c.Port, c.ScriptName, c.ProcessName, c.PIDs)
		}
		fmt.Fprintf(w, "\x1b[33m  Kill all blocking processes? [Y/n] \x1b[0m")

		// Read single character from stdin (PTY is in raw mode)
		buf := make([]byte, 1)
		n, err := os.Stdin.Read(buf)
		answer := byte('Y')
		if err == nil && n > 0 && buf[0] != '\n' && buf[0] != '\r' {
			answer = buf[0]
		}

		if answer == 'n' || answer == 'N' {
			fmt.Fprintf(w, "\r\n\x1b[2m[agnt] proceeding without killing -- scripts may fail to bind\x1b[0m\r\n")
			if handle.IsConnected() {
				var result map[string]interface{}
				_ = handle.client.WithClient(func(c *daemon.Client) error {
					var err error
					result, err = c.AutostartContinue(handle.projectPath)
					return err
				})
				mergeAutostartResult(handle, result)
			}
		} else {
			fmt.Fprintf(w, "\r\n")
			if handle.IsConnected() {
				var result map[string]interface{}
				_ = handle.client.WithClient(func(c *daemon.Client) error {
					var err error
					result, err = c.AutostartClearPorts(handle.projectPath)
					return err
				})
				mergeAutostartResult(handle, result)
			}
		}
	}

	started := append(handle.autostartScripts, handle.autostartProxies...)
	hasErrors := len(handle.autostartErrors) > 0

	if len(started) > 0 && ov != nil {
		msg := "auto-started: " + strings.Join(started, ", ")
		if hasErrors {
			msg += fmt.Sprintf(" (%d errors)", len(handle.autostartErrors))
		}
		ov.DrawStatusBarMessage(msg)

		// Restore normal indicator after 3 seconds
		go func() {
			time.Sleep(3 * time.Second)
			ov.RedrawIndicator()
		}()
	}

	// Errors still go to the terminal since they need multiple lines
	for _, e := range handle.autostartErrors {
		lines := strings.SplitN(e, "\n", 2)
		summary := lines[0]
		fmt.Fprintf(w, "\x1b[31m[agnt] autostart error: %s\x1b[0m\r\n", summary)
		if len(lines) > 1 && strings.TrimSpace(lines[1]) != "" {
			for _, outputLine := range strings.Split(strings.TrimSpace(lines[1]), "\n") {
				fmt.Fprintf(w, "\x1b[2m  | %s\x1b[0m\r\n", outputLine)
			}
		}
	}
	if hasErrors {
		fmt.Fprintf(w, "\x1b[33m[agnt] tip: run the failed command directly to see full output, or use get_errors for details\x1b[0m\r\n")
	}
}

// setupAlertScanner creates an AlertScanner from .agnt.kdl config.
// Returns nil if alerts are disabled or config can't be loaded.
// If daemonHandle is non-nil, alerts are also pushed to the daemon's alert store
// so they can be queried by the get_errors MCP tool.
func setupAlertScanner(projectPath, sessionCode string, netOverlay *Overlay, daemonHandle *daemonSessionHandle, actState func() overlay.ActivityState) *overlay.AlertScanner {
	agntCfg, err := config.LoadAgntConfig(projectPath)
	if err != nil {
		debug.Log("alerts", "failed to load config for alert scanner: %v", err)
		agntCfg = config.DefaultAgntConfig()
	}

	// Check if alerts are explicitly disabled
	if agntCfg.Alerts != nil && !agntCfg.Alerts.IsEnabled() {
		return nil
	}

	// Configure auto-forward for browser/proxy errors
	if netOverlay != nil {
		var afCfg *config.AutoForwardConfig
		if agntCfg.Alerts != nil {
			afCfg = agntCfg.Alerts.AutoForward
		}
		netOverlay.SetAutoForwardConfig(afCfg)
	}

	scannerCfg := overlay.AlertScannerConfig{
		ActivityState: actState,
		OnAlert: func(batch *overlay.AlertBatch) {
			formatted := batch.Format()
			if formatted == "" {
				return
			}
			// Forwarding pause gates only the PUSH (PTY injection). The
			// AlertReport below still runs, so errors stay pullable via
			// get_errors/get_incidents while the agent is muted.
			if netOverlay != nil && !netOverlay.IsForwardingPaused() {
				netOverlay.typeText(TypeMessage{
					Text:    formatted,
					Enter:   true,
					Instant: true,
				})
			}
			// Push alert matches to daemon store for get_errors queries
			if daemonHandle != nil && daemonHandle.IsConnected() {
				for _, m := range batch.Matches {
					_ = daemonHandle.client.AlertReport(protocol.AlertReportPayload{
						PatternID:   m.Pattern.ID,
						Severity:    string(m.Pattern.Severity),
						Category:    m.Pattern.Category,
						Description: m.Pattern.Description,
						Line:        m.Line,
						ScriptID:    m.ScriptID,
						ProjectPath: projectPath,
						Timestamp:   m.Timestamp.Format(time.RFC3339),
					})
				}
			}
		},
	}

	// Apply config overrides
	if agntCfg.Alerts != nil {
		if agntCfg.Alerts.BatchWindow > 0 {
			scannerCfg.BatchWindow = time.Duration(agntCfg.Alerts.BatchWindow) * time.Second
		}
		if agntCfg.Alerts.DedupeWindow > 0 {
			scannerCfg.DedupeWindow = time.Duration(agntCfg.Alerts.DedupeWindow) * time.Second
		}
		scannerCfg.DisabledIDs = agntCfg.Alerts.Disable

		// Convert custom patterns from config
		for id, pcfg := range agntCfg.Alerts.Patterns {
			compiled, err := regexp.Compile(pcfg.Pattern)
			if err != nil {
				log.Printf("[alerts] invalid pattern %q: %v", id, err)
				continue
			}
			sev := overlay.AlertSeverityError
			switch pcfg.Severity {
			case "warning":
				sev = overlay.AlertSeverityWarning
			case "info":
				sev = overlay.AlertSeverityInfo
			}
			scannerCfg.Patterns = append(scannerCfg.Patterns, &overlay.AlertPattern{
				ID:       id,
				Pattern:  compiled,
				Severity: sev,
				Category: "custom",
			})
		}
	}

	return overlay.NewAlertScanner(scannerCfg)
}

// daemonClientAdapter wraps *daemon.Conn to implement overlay.DaemonClient.
// Defined in cmd/agnt because it bridges the daemon and overlay packages,
// which must not import each other.
type daemonClientAdapter struct {
	conn *daemon.Conn
}

// newDaemonClientAdapter wraps a *daemon.Conn as an overlay.DaemonClient.
func newDaemonClientAdapter(conn *daemon.Conn) overlay.DaemonClient {
	return &daemonClientAdapter{conn: conn}
}

func (a *daemonClientAdapter) SocketPath() string     { return a.conn.SocketPath() }
func (a *daemonClientAdapter) EnsureConnected() error { return a.conn.EnsureConnected() }
func (a *daemonClientAdapter) IsConnected() bool      { return a.conn.IsConnected() }
func (a *daemonClientAdapter) Close() error           { return a.conn.Close() }
func (a *daemonClientAdapter) Disconnect() error      { return a.conn.Disconnect() }
func (a *daemonClientAdapter) Ping() error            { return a.conn.Ping() }

func (a *daemonClientAdapter) RequestJSON(verb string, payload interface{}, args ...string) (map[string]interface{}, error) {
	req := a.conn.Request(verb, args...)
	if payload != nil {
		req.WithJSON(payload)
	}
	return req.JSON()
}

func (a *daemonClientAdapter) RequestString(verb string, args ...string) (string, error) {
	return a.conn.Request(verb, args...).String()
}

func (a *daemonClientAdapter) RequestOK(verb string, payload interface{}, args ...string) error {
	req := a.conn.Request(verb, args...)
	if payload != nil {
		req.WithJSON(payload)
	}
	return req.OK()
}

// daemonConnector implements overlay.DaemonConnector using a shared daemon connection.
// Defined in cmd/agnt because it requires daemon-specific auto-start functionality
// (CleanupZombieDaemons, AutoStartConfig) that the overlay package cannot import.
type daemonConnector struct {
	conn *daemon.Conn
}

// newDaemonConnector creates a new overlay.DaemonConnector using a shared connection.
func newDaemonConnector(conn *daemon.Conn) overlay.DaemonConnector {
	return &daemonConnector{conn: conn}
}

// Connect attempts to connect to the daemon, auto-starting it if needed.
func (c *daemonConnector) Connect() error {
	socketPath := c.conn.SocketPath()

	// First clean up any zombie daemons
	daemon.CleanupZombieDaemons(socketPath)

	// Use auto-start client to ensure daemon is running
	config := daemon.AutoStartConfig{
		SocketPath:    socketPath,
		StartTimeout:  5 * time.Second,
		RetryInterval: 100 * time.Millisecond,
		MaxRetries:    50,
	}
	autoClient := daemon.NewAutoStartClient(config)

	if err := autoClient.Connect(); err != nil {
		return err
	}
	autoClient.Close()

	// Now connect the shared connection
	return c.conn.EnsureConnected()
}

// IsConnected returns true if currently connected to the daemon.
func (c *daemonConnector) IsConnected() bool {
	return c.conn.IsConnected()
}

// ===========================================================================
// Shared `agnt run` command surface (cobra spec, flag parsing, helpers).
// The platform-specific PTY entry points runWithPTY (Unix) and
// runWithConPTY (Windows) live in run.go / run_windows.go and are dispatched
// by runCommand below. Everything in this section is identical across
// platforms — reviewers should NOT re-introduce divergent copies in the
// per-platform files. See task wkDDSC2FgNc6.
// ===========================================================================

var runCmd = &cobra.Command{
	Use:   "run <command> [args...]",
	Short: "Run an AI coding tool with overlay features",
	Long: `Run any AI coding tool (Claude, Gemini, Copilot, etc.) with overlay features.

The command is executed in a pseudo-terminal (PTY on Unix, ConPTY on Windows)
that allows:
- Capturing and forwarding all input/output
- Injecting synthetic input from external sources (like devtool proxy events)
- Terminal resize handling
- Session management for programmatic message injection and scheduling

Flags:
  --session <code>      Session code for identifying this run (auto-generated if not set)
  --overlay-socket      Custom socket path for overlay server
  --no-indicator        Disable the indicator bar
  --no-overlay          Disable terminal overlay entirely
  --no-autostart        Skip auto-starting scripts and proxies from .agnt.kdl
  --config <path>       Use a specific .agnt.kdl (default: <cwd>/.agnt.kdl, no walk-up)

Examples:
  agnt run claude --dangerously-skip-permissions
  agnt run claude --session dev
  agnt run claude
  agnt run claude --no-autostart    # Skip .agnt.kdl autostart
  agnt run gemini
  agnt run copilot
  agnt run opencode

Overlay Features:
- CTRL+←/→: Browse process, proxy, and log panels
- Status bar: Shows running services and proxy URLs for browser access
- Auto-start: Loads .agnt.kdl to auto-start configured dev scripts and proxies

The overlay listens on port 19191 for WebSocket connections from devtool-mcp
to receive events that can be injected as user input. Sessions can receive
scheduled messages via MCP tools, CLI commands, or the devtools API.`,
	DisableFlagParsing: true,
	Args:               cobra.MinimumNArgs(1),
	Run:                runCommand,
}

var (
	overlaySocketPath string
	showIndicator     bool = true
	useTermOverlay    bool = true
	sessionCode       string
	skipAutostart     bool = false
	// setupOnlyMode is set by `agnt init` to run a single setup phase
	// (configure the project, no relaunch) regardless of the first-run gate.
	setupOnlyMode bool = false
	// configOverride is the value of the --config flag. Empty means "use
	// <cwd>/.agnt.kdl"; a non-empty value points the config resolution at a
	// specific file (see config.ResolveConfigPath). Config scope is otherwise
	// the cwd of execution only — there is no walk-up.
	configOverride string
)

// validateResolvedConfig resolves the config path for projectPath (honoring
// the --config override) and parses it early, before any PTY/terminal setup.
// A missing --config file or a parse error is fatal — the user has a config
// they expect to work. A bare cwd with no config is fine (returns nil). Shared
// by the Unix and Windows run orchestrators so the validation cannot drift.
func validateResolvedConfig(projectPath string) error {
	configPath, err := config.ResolveConfigPath(projectPath, configOverride)
	if err != nil {
		return err
	}
	if configPath == "" {
		return nil
	}
	if _, err := config.LoadAgntConfigFile(configPath); err != nil {
		return fmt.Errorf("%s: %w", configPath, err)
	}
	return nil
}

// loadResolvedConfig loads the config that applies for projectPath, honoring
// the --config override. With no config file present it returns
// DefaultAgntConfig (never nil), matching config.LoadAgntConfig semantics. A
// missing --config file or a parse error is surfaced to the caller.
func loadResolvedConfig(projectPath string) (*config.AgntConfig, error) {
	p, err := config.ResolveConfigPath(projectPath, configOverride)
	if err != nil {
		return nil, err
	}
	if p == "" {
		return config.DefaultAgntConfig(), nil
	}
	return config.LoadAgntConfigFile(p)
}

// hasResolvedConfig reports whether a config file applies for projectPath,
// honoring the --config override. A missing --config file resolves to an error
// here (treated as "no config" for the gate); runWithPTY surfaces that error
// loudly during validation so the user is not left guessing.
func hasResolvedConfig(projectPath string) bool {
	p, err := config.ResolveConfigPath(projectPath, configOverride)
	return err == nil && p != ""
}

func init() {
	// We use DisableFlagParsing so flags are parsed manually
	// to allow passing all flags to the wrapped command.
}

// runCommand parses our private flags out of args and dispatches to the
// platform-specific PTY entry point (runPlatformPTY, defined in run.go /
// run_windows.go).
func runCommand(cmd *cobra.Command, args []string) {
	// Handle help manually since DisableFlagParsing prevents cobra from doing
	// it. Only leading agnt-run help should be consumed here; child help such
	// as `agnt run claude --help` must pass through to the wrapped command.
	if runHelpRequested(args) {
		cmd.Help()
		return
	}

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Error: command is required")
		cmd.Help()
		os.Exit(1)
	}

	// Parse our own flags from args.
	overlaySocketPath = "" // will use default
	commandArgs, err := parseRunCommandArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if len(commandArgs) == 0 {
		fmt.Fprintln(os.Stderr, "Error: command is required")
		os.Exit(1)
	}

	// If debug was enabled (via env var or flag) but no log file was set,
	// force file-only logging. In PTY context, stderr output corrupts the terminal.
	if debug.IsEnabled() && debug.GetLogFilePath() == "" {
		if err := debug.SetLogFile("agnt-run.log"); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to set debug log file: %v\n", err)
		}
	}

	if err := runPlatformPTY(commandArgs, overlaySocketPath, sessionCode); err != nil {
		log.Fatalf("Error: %v", err)
	}
}

func runHelpRequested(args []string) bool {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help", "help":
			return true
		case "--overlay-socket", "--session", "--debug-log", "--config":
			if i+1 >= len(args) {
				return false
			}
			i++
		case "--no-indicator", "--no-overlay", "--no-autostart", "--debug", "-d":
			// Keep scanning leading agnt-run/global flags.
		default:
			// The first non-agnt flag is the wrapped command. From this point
			// onward, arguments belong to that command.
			return false
		}
	}
	return false
}

func parseRunCommandArgs(args []string) ([]string, error) {
	commandArgs := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--overlay-socket":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("%s requires a value", args[i])
			}
			overlaySocketPath = args[i+1]
			i++
		case "--session":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("%s requires a value", args[i])
			}
			sessionCode = args[i+1]
			i++
		case "--config":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("%s requires a value", args[i])
			}
			configOverride = args[i+1]
			i++
		case "--no-indicator":
			showIndicator = false
		case "--no-overlay":
			useTermOverlay = false
		case "--no-autostart":
			skipAutostart = true
		case "--debug", "-d":
			// Handle global debug flag (since DisableFlagParsing prevents cobra from parsing it).
			debug.Enable()
			if debug.GetLogFilePath() == "" {
				// In PTY context, stderr corrupts terminal output; force file logging.
				if err := debug.SetLogFile("agnt-run.log"); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to set debug log file: %v\n", err)
				}
			}
		case "--debug-log":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("%s requires a value", args[i])
			}
			debug.Enable()
			if err := debug.SetLogFile(args[i+1]); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to set debug log file: %v\n", err)
			}
			i++
		default:
			commandArgs = append(commandArgs, args[i])
		}
	}
	return commandArgs, nil
}

// spinner displays a loading animation and returns a stop function.
// Uses braille frames — Windows Terminal and modern macOS/Linux terminals
// all support unicode. Falls back gracefully if not supported (frames
// just render as box characters, the rotation effect still works).
func spinner(message string) func() {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		i := 0
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-done:
				// No need to clear - screen clear after PTY start handles it
				return
			case <-ticker.C:
				fmt.Printf("\r%s %s", frames[i%len(frames)], message)
				i++
			}
		}
	}()

	return func() {
		close(done)
		wg.Wait()
	}
}

// detectAIAgent detects the first available AI agent in PATH.
// Returns empty string if none found.
func detectAIAgent() string {
	agents := aichannel.DetectAvailableAgents()
	if len(agents) > 0 {
		return string(agents[0])
	}
	return ""
}

// generateSessionCode generates a unique session code based on the command name.
// Format: <command>-<sequence> (e.g., "claude-1", "gemini-2"). Strips path
// components (handles both / and \), and trailing .exe so Windows paths
// like `C:\Program Files\claude.exe` and Unix paths like `/usr/bin/claude`
// produce the same base name.
func generateSessionCode(command string) string {
	// Extract base command name (handle paths like /usr/bin/claude or
	// C:\Program Files\claude.exe). Strip both separators so the same
	// helper handles Unix paths under WSL and Windows paths.
	base := command
	if idx := strings.LastIndex(command, "/"); idx >= 0 {
		base = command[idx+1:]
	}
	if idx := strings.LastIndex(base, "\\"); idx >= 0 {
		base = base[idx+1:]
	}
	// Strip the .exe extension (no-op on Unix, important on Windows so
	// "claude.exe" and "claude" share a session series).
	base = strings.TrimSuffix(base, ".exe")

	// Connect to daemon to get a unique sequence number
	socketPath, _ := rootCmd.Flags().GetString("socket")
	if socketPath == "" {
		socketPath = daemon.DefaultSocketPath()
	}

	client := daemon.NewClient(daemon.WithSocketPath(socketPath))
	if err := client.Connect(); err == nil {
		defer client.Close()
		// Use the daemon's session registry to generate a unique code
		code, err := client.SessionGenerateCode(base)
		if err == nil {
			return code
		}
	}

	// Fallback: use timestamp-based code if daemon unavailable
	return fmt.Sprintf("%s-%d", base, time.Now().UnixNano()%10000)
}

// cleanupTerminal resets the terminal state to prevent display corruption on exit.
// This ensures the scroll region is reset and the cursor is visible. The
// `?9001l` (disable win32-input-mode) escape is harmless on Unix terminals
// — they ignore unknown sequences — so we emit it unconditionally.
func cleanupTerminal(height int) {
	// Disable extended keyboard/input modes that the child process may have enabled
	// These cause garbage output (semicolons, escape sequences) if left enabled
	fmt.Fprint(os.Stdout, "\x1b[?9001l") // Disable win32-input-mode (extended key reporting)
	fmt.Fprint(os.Stdout, "\x1b[?1004l") // Disable focus event reporting ([I and [O sequences)
	fmt.Fprint(os.Stdout, "\x1b[?2004l") // Disable bracketed paste mode
	fmt.Fprint(os.Stdout, "\x1b[?1l")    // Disable application cursor keys (DECCKM)
	fmt.Fprint(os.Stdout, "\x1b[?25h")   // Show cursor (might have been hidden)

	// Reset scroll region to full screen (removes protected area)
	fmt.Fprint(os.Stdout, "\x1b[r")

	// Reset all text attributes
	fmt.Fprint(os.Stdout, "\x1b[0m")

	// Move to the bottom row and clear it (remove status bar remnants)
	fmt.Fprintf(os.Stdout, "\x1b[%d;1H\x1b[2K", height)

	// Move cursor to a reasonable position (bottom-left)
	fmt.Fprintf(os.Stdout, "\x1b[%d;1H", height)
}

// buildAgntSystemPrompt queries the daemon for running services and builds
// a system prompt to inject into the AI agent (Claude, Gemini, etc.) with
// context about agnt and auto-started services. Loads configuration from
// .agnt.kdl to include configured scripts/proxies and any custom system
// prompt overrides.
// phaseCmdArgsAndPrompt selects the system prompt for a run phase and returns
// the adapter-built cmdArgs alongside the prompt (the prompt is also handed to
// runOverlayPipeline for stdin-delivery adapters). The setup phase uses the
// setup-mode prompt; the coding phase uses the normal agnt prompt. With a nil
// adapter both are passthrough/empty. Shared by both platform children to keep
// run.go / run_windows.go under their line budget (TestRunFilesUnderBudget).
func phaseCmdArgsAndPrompt(adapter agentadapter.Adapter, cmdArgs []string, setupPhase bool, socketPath string) ([]string, string) {
	if adapter == nil {
		return cmdArgs, ""
	}
	var prompt string
	if setupPhase {
		prompt = buildSetupSystemPrompt(adapter.Name())
	} else {
		prompt = buildAgntSystemPrompt(socketPath)
		// Persist the steering block into the agent's always-loaded context
		// file (AGENTS.md, GEMINI.md, …) so non-Claude agents keep the agnt
		// tool guidance every turn rather than from the one-shot stdin nudge.
		// No-op for Claude and when disabled in config. cwd is the project dir
		// under `agnt run`. Best-effort: never blocks launch.
		if cwd, err := os.Getwd(); err == nil {
			writePersistentContext(adapter.Name(), cwd, prompt)
		}
	}
	return adapter.BuildArgs(cmdArgs, prompt), prompt
}

// suppressAutostartDuringSetup forces skipAutostart on for the setup phase and
// returns a restore func; the coding phase gets a no-op. Phases run
// sequentially, so this package-global mutation is race-free. (Setup has no
// config yet so autostart is moot — this is belt-and-braces.) Call as:
// defer suppressAutostartDuringSetup(setupPhase)().
func suppressAutostartDuringSetup(setupPhase bool) func() {
	if !setupPhase {
		return func() {}
	}
	prev := skipAutostart
	skipAutostart = true
	return func() { skipAutostart = prev }
}

// firstRunOrCoding is the shared run-orchestration wiring for both platform
// entrypoints. It drives the optional first-run two-phase setup→relaunch flow
// for ANY agent (the gate is verb-driven, not Claude-specific). Only the launch
// + reap callbacks — which own the platform-specific PTY backend — differ
// between Unix and Windows, so they are injected here rather than duplicating
// the firstRunDeps construction in run.go and run_windows.go (enforced by
// TestRunFilesUnderBudget).
func firstRunOrCoding(projectPath string, args []string,
	launch func(setupPhase bool, args []string) (int, error), reap func(int)) error {
	// Deterministic auto-config first: a simple project (package.json web app,
	// dotnet site, Go/Python repo) gets a generated .agnt.kdl with no LLM
	// round-trip. After this writes, the gate below sees a config and goes
	// straight to the coding launch — the LLM is only involved for projects
	// agnt cannot confidently configure on its own.
	autoConfigured := false
	if !hasResolvedConfig(projectPath) && configOverride == "" {
		if tryAutoConfig(projectPath) {
			autoConfigured = true
			// Best-effort: a failed marker write only re-shows the setup nudge
			// next run. debug.Log is the only safe channel on the PTY path.
			if err := writeFirstRunMarker(firstRunStatePath(projectPath), setupOutcomeMarker(true, time.Now())); err != nil {
				debug.Log("firstrun", "marker write failed (will re-nudge): %v", err)
			}
		}
	}

	// `agnt init`: run one setup phase for any agent, no relaunch. On success
	// record a permanent marker so a later `agnt run` skips the setup nudge.
	if setupOnlyMode {
		// If auto-config just wrote the config, the project is configured with
		// no LLM round-trip — nothing left for init to do. (A pre-existing
		// config still runs setup: an explicit `agnt init` means reconfigure.)
		if autoConfigured {
			return nil
		}
		if _, err := launch(true, args); err != nil {
			return err
		}
		if hasResolvedConfig(projectPath) {
			// Best-effort: a failed marker write only re-shows the setup nudge
			// next run. debug.Log is the only safe channel on the PTY path.
			if err := writeFirstRunMarker(firstRunStatePath(projectPath), setupOutcomeMarker(true, time.Now())); err != nil {
				debug.Log("firstrun", "marker write failed (will re-nudge): %v", err)
			}
		}
		return nil
	}
	// Verb-driven: the first-run gate fires for any agent, not just Claude. The
	// gate (config presence + marker/TTL) decides whether to run setup; the
	// adapter only dictates HOW the setup prompt is injected (flag vs stdin),
	// resolved per-phase inside the launch callback.
	return runFirstRunFlow(firstRunDeps{
		now:         time.Now(),
		ttl:         renudgeTTLForProject(projectPath),
		hasConfig:   func() bool { return hasResolvedConfig(projectPath) },
		readMarker:  func() (*firstRunMarker, error) { return readFirstRunMarker(firstRunStatePath(projectPath)) },
		writeMarker: func(m firstRunMarker) error { return writeFirstRunMarker(firstRunStatePath(projectPath), m) },
		args:        args,
		launch:      launch,
		reap:        reap,
	})
}

func buildAgntSystemPrompt(socketPath string) string {
	if socketPath == "" {
		socketPath = daemon.DefaultSocketPath()
	}

	// Load agnt config from current directory.
	// Config was already validated at startup; errors here are unexpected.
	cwd, _ := os.Getwd()
	agntConfig, err := config.LoadAgntConfig(cwd)
	if err != nil {
		debug.Log("run", "unexpected config load error in buildAgntSystemPrompt: %v", err)
		agntConfig = config.DefaultAgntConfig()
	}

	// If full system prompt override is set, use it directly
	if agntConfig.AI != nil && agntConfig.AI.SystemPrompt != "" {
		return agntConfig.AI.SystemPrompt
	}

	// Build the base prompt from config (includes configured scripts/proxies).
	// The cheat sheet is appended here (rather than inside config.BuildSystemPrompt)
	// because internal/tools imports internal/config — the reverse import would be
	// a cycle. Every agent-prompt consumer (Claude flag, stdin adapters, `agnt ai`
	// REPL) flows through buildAgntSystemPrompt, so one site covers all delivery
	// paths with identical content.
	basePrompt := agntConfig.BuildSystemPrompt()
	if agntConfig.AI.CheatSheetEnabled() {
		basePrompt = basePrompt + "\n" + agntprompt.BuildCheatSheet(tools.DevToolAPIFunctions)
	}

	// Try to connect to a running daemon to add runtime state.
	// Use the lightweight non-auto-start client: if the daemon is already
	// running (e.g. a previous session left processes alive) we get their
	// state. If not, the config-based prompt is sufficient — startDaemonSession
	// will start the daemon and trigger autostart after the PTY launches.
	client := daemon.NewClient(daemon.WithSocketPath(socketPath))
	if err := client.Connect(); err != nil {
		return basePrompt
	}
	defer client.Close()

	var sb strings.Builder
	sb.WriteString(basePrompt)

	// Add current runtime state: processes/proxies already running.
	// Scoped to cwd so we don't list another project's services.
	var hasRuntime bool

	procFilter := protocol.DirectoryFilter{Directory: cwd}
	procs, err := client.ProcList(procFilter)
	if err == nil {
		if processes, ok := procs["processes"].([]interface{}); ok && len(processes) > 0 {
			if !hasRuntime {
				sb.WriteString("\n## Current Runtime State\n")
				sb.WriteString("These processes and proxies are already running — do NOT start them again.\n")
				hasRuntime = true
			}
			sb.WriteString("\n**Running processes**:\n")
			for _, p := range processes {
				if pm, ok := p.(map[string]interface{}); ok {
					id := pm["id"]
					state := pm["state"]
					cmd := pm["command"]
					sb.WriteString(fmt.Sprintf("- **%s**: `%s` (state: %s)\n", id, cmd, state))
					sb.WriteString(fmt.Sprintf("  - Output: `proc {action: \"output\", id: \"%s\"}`\n", id))
					sb.WriteString(fmt.Sprintf("  - Errors: `get_errors {process_id: \"%s\"}`\n", id))
				}
			}
		}
	}

	proxyFilter := protocol.DirectoryFilter{Directory: cwd}
	proxies, err := client.ProxyList(proxyFilter)
	if err == nil {
		if proxyList, ok := proxies["proxies"].([]interface{}); ok && len(proxyList) > 0 {
			if !hasRuntime {
				sb.WriteString("\n## Current Runtime State\n")
				sb.WriteString("These processes and proxies are already running — do NOT start them again.\n")
				hasRuntime = true
			}
			sb.WriteString("\n**Running proxies** (browser traffic being captured):\n")
			for _, p := range proxyList {
				if pm, ok := p.(map[string]interface{}); ok {
					id := pm["id"]
					target := pm["target_url"]
					listen := pm["listen_addr"]
					sb.WriteString(fmt.Sprintf("- **%s**: %s → %s\n", id, listen, target))
					sb.WriteString(fmt.Sprintf("  - Logs: `proxylog {action: \"query\", proxy_id: \"%s\"}`\n", id))
					sb.WriteString(fmt.Sprintf("  - Errors: `get_errors {proxy_id: \"%s\"}`\n", id))
				}
			}
		}
	}

	return sb.String()
}

// ===========================================================================
// Shared overlay/runtime pipeline.
// runOverlayPipeline handles everything between "PTY started" and "PTY exited".
// Both the Unix (run.go) and Windows (run_windows.go) entry points construct
// a ptyHandle, call runOverlayPipeline, and then handle the platform-specific
// child cleanup.
// ===========================================================================

// ptyHandle bundles the platform-specific PTY backend with the metadata
// required to drive the shared overlay pipeline. Each field is set by the
// per-platform PTY entrypoint before calling runOverlayPipeline.
type ptyHandle struct {
	// Backend is the underlying PTY (creack/pty *os.File on Unix,
	// aymanbagabas/go-pty Pty on Windows). Both satisfy io.ReadWriter
	// and overlay.PtyReadWriter so the same overlay/InputRouter code
	// drives both backends without conditional compilation.
	Backend overlay.PtyReadWriter

	// Width/Height are the host terminal dimensions at PTY start.
	Width, Height int

	// SessionPGID is the POSIX process group leader's PID on Unix
	// (zero on Windows). The daemon uses it for cleanup. See
	// daemonSessionConfig.SessionPGID.
	SessionPGID int

	// SessionJobHandle is the Windows Job Object handle as a uint64
	// (zero on Unix). See daemonSessionConfig.SessionJobHandle.
	SessionJobHandle uint64

	// Resize is invoked by the resize watcher (SIGWINCH on Unix,
	// polling on Windows) when the host terminal changes size. childRows
	// is already adjusted for the protected indicator row.
	Resize func(childCols, childRows int) error

	// Interrupt sends Ctrl+C / SIGINT to the entire PTY process group
	// (Unix) or the Job Object root (Windows). Called when the
	// shared loop receives ctx.Done().
	Interrupt func()

	// PreRedraw is called whenever the gate unfreezes (menu closes).
	// On Unix it sends SIGWINCH to the child to force a redraw; on
	// Windows it is typically a no-op (the gate-unfreeze callback
	// itself handles scroll-region re-enforcement). Optional.
	PreRedraw func()

	// ForceRepaint forces a child repaint after a menu close when the
	// child is in the alternate screen buffer. A bare same-size SIGWINCH
	// (PreRedraw) is ignored by newer TUI renderers (Claude Code,
	// opencode) which diff the winsize and skip the repaint — leaving the
	// screen blank after the overlay drew panels into their alt buffer. A
	// transient winsize change ("jiggle") forces the repaint. Optional;
	// when nil the gate-unfreeze path falls back to PreRedraw.
	ForceRepaint func()

	// WrapOutput optionally wraps the activity-monitor output writer.
	// Used by Windows to inject the BrowserHelper that auto-opens
	// OAuth URLs ConPTY can't surface. Returns dest unchanged when
	// nil.
	WrapOutput func(dest io.Writer) io.Writer

	// EnableSplash controls whether the startup-tip splash is shown.
	// Unix enables it; Windows does not (ConPTY clears the screen).
	EnableSplash bool
}

// pipelineRuntime holds the runtime handles produced by runOverlayPipeline
// so the per-platform exit path can stop them cleanly. Caller is responsible
// for invoking Stop() before terminal cleanup.
type pipelineRuntime struct {
	done            chan struct{}
	wg              *sync.WaitGroup
	netOverlay      *Overlay
	daemonHandle    *daemonSessionHandle
	termOverlay     *overlay.Overlay
	inputRouter     *overlay.InputRouter
	statusFetcher   *overlay.StatusFetcher
	outputFilter    *overlay.ProtectedWriter
	outputGate      *overlay.OutputGate
	startupSplash   *overlay.StartupSplash
	activityMonitor *overlay.ActivityMonitor
	alertScanner    *overlay.AlertScanner
	daemonConn      *daemon.Conn
}

// Done returns the channel that closes when the PTY output goroutine
// finishes (i.e. the child process exited and io.Copy returned).
func (r *pipelineRuntime) Done() <-chan struct{} { return r.done }

// Stop tears down all overlay components in dependency order. Safe to call
// multiple times. The per-platform exit path must call Stop before
// cleanupTerminal so escape-sequence emission doesn't race with overlay
// redraws.
func (r *pipelineRuntime) Stop() {
	if r == nil {
		return
	}
	if r.inputRouter != nil {
		r.inputRouter.Stop()
	}
	if r.startupSplash != nil {
		r.startupSplash.Stop()
	}
	if r.outputFilter != nil {
		r.outputFilter.Stop()
	}
	if r.activityMonitor != nil {
		r.activityMonitor.Stop()
	}
	if r.alertScanner != nil {
		r.alertScanner.Stop()
	}
	if r.statusFetcher != nil {
		r.statusFetcher.Stop()
	}
	if r.netOverlay != nil {
		r.netOverlay.Stop()
	}
	if r.daemonHandle != nil {
		r.daemonHandle.Close()
	}
	if r.daemonConn != nil {
		r.daemonConn.Close()
	}
	// Wait for goroutines after stopping their inputs.
	if r.wg != nil {
		r.wg.Wait()
	}
}

// runOverlayPipeline owns everything between PTY start and PTY exit.
// It wires daemon session, overlay (network + terminal), input/output
// goroutines, resize handler, alert scanner, and activity monitor. The
// returned runtime exposes Done() to block on PTY exit and Stop() to tear
// everything down. ctx cancellation triggers handle.Interrupt() so the
// caller does not need to handle SIGINT separately.
func runOverlayPipeline(
	ctx context.Context,
	handle *ptyHandle,
	command string,
	cmdArgs []string,
	adapter agentadapter.Adapter,
	adapterPrompt string,
	projectPath string,
	sessionCodeArg string,
) *pipelineRuntime {
	sessionCode := sessionCodeArg
	rt := &pipelineRuntime{
		done: make(chan struct{}),
		wg:   &sync.WaitGroup{},
	}

	// Per-session overlay socket path so concurrent sessions don't collide.
	resolvedOverlayPath := overlaySessionSocketPath(sessionCode)

	// Network overlay (browser → agent message channel).
	rt.netOverlay = newOverlay(resolvedOverlayPath, handle.Backend)
	if err := rt.netOverlay.Start(ctx); err != nil {
		debug.Warn("overlay", "socket failed to start: %v (proxy→agent messages will not work)", err)
	}

	// Daemon session registration (autostart + overlay endpoint scoping).
	daemonSocketPath, _ := rootCmd.Flags().GetString("socket")
	rt.daemonHandle = startDaemonSession(ctx, daemonSessionConfig{
		SessionCode:      sessionCode,
		OverlayEndpoint:  rt.netOverlay.SocketPath(),
		ProjectPath:      projectPath,
		Command:          command,
		CmdArgs:          cmdArgs,
		SocketPath:       daemonSocketPath,
		SkipAutostart:    skipAutostart,
		SessionPGID:      handle.SessionPGID,
		SessionJobHandle: handle.SessionJobHandle,
	})

	// Terminal overlay (indicator bar, menus, status fetcher, input router).
	if useTermOverlay {
		setupTerminalOverlay(ctx, handle, rt)
	}

	// Initial indicator draw after a short delay so the child has a chance
	// to clear/redraw first.
	if rt.termOverlay != nil && showIndicator {
		rt.wg.Add(1)
		go func() {
			defer rt.wg.Done()
			select {
			case <-ctx.Done():
				return
			case <-rt.done:
				return
			case <-time.After(50 * time.Millisecond):
				if rt.outputFilter != nil {
					rt.outputFilter.EnforceScrollRegion()
				}
				rt.termOverlay.Redraw()
			}
		}()
	}

	// Splash text fills the gap between PTY start and first child output.
	if handle.EnableSplash && rt.outputGate != nil {
		rt.startupSplash = overlay.NewStartupSplash(rt.outputGate, handle.Width, handle.Height)
		rt.startupSplash.Start()
	}

	// Display autostart results in the status bar (errors fall back to
	// the gate since they need multi-line output).
	rt.wg.Add(1)
	go func() {
		defer rt.wg.Done()
		var autostartOut io.Writer = os.Stdout
		if rt.outputGate != nil {
			autostartOut = rt.outputGate
		}
		displayAutostartResults(rt.daemonHandle, rt.termOverlay, autostartOut, 10*time.Second)
	}()

	// Resize watcher — platform-specific dispatcher (SIGWINCH on Unix,
	// polling on Windows) lives in run.go / run_windows.go and feeds
	// dimensions to the shared dispatcher below.
	rt.wg.Add(1)
	go func() {
		defer rt.wg.Done()
		watchResize(ctx, rt.done, handle, rt)
	}()

	// Stdin → PTY (via input router when overlay is enabled, raw copy
	// otherwise). The router handles hotkeys, menu navigation, and
	// daemon RPCs.
	rt.wg.Add(1)
	go func() {
		defer rt.wg.Done()
		if rt.inputRouter != nil {
			rt.inputRouter.Run()
		} else {
			_, _ = io.Copy(handle.Backend, os.Stdin)
		}
	}()

	// outputPulse fires on every chunk the child writes. The initial-stdin
	// injector uses it to detect "the agent rendered and went quiet" — a far
	// better ready-for-input signal than a fixed delay from launch.
	outputPulse := make(chan struct{}, 1)

	// PTY → activity monitor → filter → gate → stdout.
	// Activity monitor detects when the agent is working and surfaces
	// state to the daemon (which forwards to the browser indicator).
	rt.wg.Add(1)
	go func() {
		defer rt.wg.Done()
		var outputDest io.Writer = os.Stdout
		if rt.outputFilter != nil {
			outputDest = rt.outputFilter
		} else if rt.outputGate != nil {
			outputDest = rt.outputGate
		}
		if handle.WrapOutput != nil {
			outputDest = handle.WrapOutput(outputDest)
		}

		activityCfg := overlay.DefaultActivityMonitorConfig()
		activityCfg.OnStateChange = func(state overlay.ActivityState) {
			rt.daemonHandle.BroadcastActivity(state == overlay.ActivityActive)
			if state == overlay.ActivityActive && rt.netOverlay != nil {
				rt.netOverlay.NotifyActivity()
			}
		}
		activityCfg.OnOutputPreview = func(lines []string) {
			rt.daemonHandle.BroadcastOutputPreview(lines)
		}
		if rt.startupSplash != nil {
			activityCfg.OnFirstActivity = rt.startupSplash.OnFirstActivity()
		}

		// alertScanner routes browser/HTTP proxy events through the
		// canonical dedup/batch/activity-defer queue. Stored on the
		// runtime so Stop() can call alertScanner.Stop() deterministically.
		rt.alertScanner = setupAlertScanner(projectPath, sessionCode, rt.netOverlay, rt.daemonHandle, func() overlay.ActivityState {
			if rt.activityMonitor != nil {
				return rt.activityMonitor.State()
			}
			return overlay.ActivityIdle
		})
		if rt.alertScanner != nil {
			// Wire the canonical queue into the auto-forward path so
			// browser/HTTP errors share dedup, batch, and activity-defer
			// with process alerts from the daemon scanner.
			if rt.netOverlay != nil {
				cascade := config.DefaultJSCascadePatterns
				if agntCfg, err := config.LoadAgntConfig(projectPath); err == nil &&
					agntCfg != nil && agntCfg.Alerts != nil && agntCfg.Alerts.OutageHold != nil {
					cascade = agntCfg.Alerts.OutageHold.GetJSCascadePatterns()
				}
				rt.netOverlay.SetAlertScanner(rt.alertScanner, cascade)
			}
		}

		rt.activityMonitor = overlay.NewActivityMonitor(outputDest, activityCfg)
		_, _ = io.Copy(io.MultiWriter(newActivityPulse(outputPulse), rt.activityMonitor), handle.Backend)
		close(rt.done)
	}()

	// For stdin-based adapters (everything except Claude by default),
	// inject the initial context message as if the user typed it — but only
	// once the agent is actually reading stdin. Injecting on a fixed delay
	// races slow agent startup: the PTY is still in cooked mode, so the line
	// discipline echoes the bytes to stdout and the agent's stdin gets flushed
	// on init — the message lands on the terminal, never in the agent.
	if adapter != nil {
		if stdin := adapter.InitialStdin(adapterPrompt); len(stdin) > 0 {
			go injectInitialStdin(ctx, handle.Backend, stdin, outputPulse, adapter.StdinDelay())
		}
	}

	// Forward ctx cancellation to the platform-specific interrupt handler.
	go func() {
		select {
		case <-ctx.Done():
			if handle.Interrupt != nil {
				handle.Interrupt()
			}
		case <-rt.done:
		}
	}()

	return rt
}

// maxStdinReadyWait bounds how long injectInitialStdin waits for the agent
// to become ready before injecting anyway. A silent agent (no startup banner)
// gives no readiness signal, so the message is delivered best-effort after
// this ceiling rather than never. Var (not const) so tests can shorten it.
var maxStdinReadyWait = 8 * time.Second

// activityPulse is an io.Writer that pulses a channel on every non-empty
// write. Teed into the child-output copy so the stdin injector can observe
// when the agent is producing output and when it has gone quiet. The send is
// non-blocking with a 1-slot buffer: callers only need "did output happen
// since I last looked," not every chunk.
type activityPulse struct{ ch chan<- struct{} }

func newActivityPulse(ch chan<- struct{}) *activityPulse { return &activityPulse{ch: ch} }

func (a *activityPulse) Write(p []byte) (int, error) {
	if len(p) > 0 {
		select {
		case a.ch <- struct{}{}:
		default:
		}
	}
	return len(p), nil
}

// injectInitialStdin writes the agent's initial context message to its stdin
// once the agent is ready to read it. Readiness = the agent has produced
// output (it is alive and past exec) and then gone quiet for `settle` (it
// finished its initial render and is waiting for input). This is the
// deterministic alternative to a fixed delay from launch, which fires while
// the agent is still starting and loses the message to cooked-mode echo +
// stdin flush. A silent agent that never outputs is handled by the
// maxStdinReadyWait ceiling.
func injectInitialStdin(ctx context.Context, w io.Writer, data []byte, pulse <-chan struct{}, settle time.Duration) {
	if settle <= 0 {
		settle = agentadapter.DefaultStdinDelay
	}
	deadline := time.NewTimer(maxStdinReadyWait)
	defer deadline.Stop()

	// Wait for the first sign of life (or the ceiling, or cancel).
	select {
	case <-pulse:
	case <-deadline.C:
		writeIfLive(ctx, w, data)
		return
	case <-ctx.Done():
		return
	}

	// Wait for output to settle: each new chunk restarts the quiet window, so
	// we inject only after the agent stops rendering. The overall deadline
	// still bounds a chatty agent that never pauses.
	quiet := time.NewTimer(settle)
	defer quiet.Stop()
	for {
		select {
		case <-pulse:
			if !quiet.Stop() {
				<-quiet.C
			}
			quiet.Reset(settle)
		case <-quiet.C:
			writeIfLive(ctx, w, data)
			return
		case <-deadline.C:
			writeIfLive(ctx, w, data)
			return
		case <-ctx.Done():
			return
		}
	}
}

// writeIfLive writes data unless the context is already cancelled.
func writeIfLive(ctx context.Context, w io.Writer, data []byte) {
	if ctx.Err() != nil {
		return
	}
	_, _ = w.Write(data)
}

// setupTerminalOverlay constructs the overlay components (gate, filter,
// indicator, input router, status fetcher, daemon adapters) and stores
// them on the pipelineRuntime. Extracted from runOverlayPipeline so the
// outer flow stays readable.
func setupTerminalOverlay(ctx context.Context, handle *ptyHandle, rt *pipelineRuntime) {
	cfg := overlay.DefaultConfig()
	cfg.ShowIndicator = showIndicator
	cfg.Version = appVersion
	cfg.OnAction = func(action overlay.Action) error {
		switch action {
		case overlay.ActionRefreshStatus:
			if rt.statusFetcher != nil {
				rt.statusFetcher.Refresh()
			}
		}
		return nil
	}

	// Gate is the final stage before stdout — when the menu is open it
	// freezes (discards) PTY output to prevent corruption.
	rt.outputGate = overlay.NewOutputGate(os.Stdout)
	rt.termOverlay = overlay.New(handle.Backend, handle.Width, handle.Height, cfg)
	rt.termOverlay.SetGate(rt.outputGate)
	rt.inputRouter = overlay.NewInputRouter(handle.Backend, rt.termOverlay)

	// Shared daemon connection for all overlay components.
	socketPath, _ := rootCmd.Flags().GetString("socket")
	rt.daemonConn = daemon.NewConn(socketPath)
	daemonClient := newDaemonClientAdapter(rt.daemonConn)
	rt.inputRouter.SetOutputFetcher(overlay.NewDaemonOutputFetcher(daemonClient))
	rt.inputRouter.SetDaemonConnector(newDaemonConnector(rt.daemonConn))
	rt.inputRouter.SetScriptController(overlay.NewDaemonScriptController(daemonClient))

	// Detect first available AI agent for summarizer.
	if agent := detectAIAgent(); agent != "" {
		summarizer := overlay.NewSummarizer(daemonClient, overlay.SummarizerConfig{
			Agent:       aichannel.AgentType(agent),
			Timeout:     2 * time.Minute,
			ProjectPath: projectPathFromHandle(handle),
		})
		rt.inputRouter.SetSummarizer(summarizer)
	}

	// Output filter protects the indicator bar. Filter writes to gate
	// (not directly to stdout) so the gate can freeze for menus.
	if showIndicator {
		filterCfg := overlay.FilterConfig{
			ProtectBottomRows: 1,
			RedrawInterval:    200 * time.Millisecond,
			OnRedraw: func() {
				if rt.termOverlay != nil {
					rt.termOverlay.Redraw()
				}
			},
		}
		rt.outputFilter = overlay.NewProtectedWriter(rt.outputGate, handle.Width, handle.Height, filterCfg)
		rt.termOverlay.SetAltScreenChecker(rt.outputFilter.InAltScreen)
	}

	// Gate-unfreeze callback re-enforces the scroll region and on Unix
	// also kicks the child with SIGWINCH to force a redraw clearing
	// any artifacts the menu left behind. Windows skips the SIGWINCH —
	// see run_windows.go for the rationale.
	rt.outputGate.SetCallbacks(nil, func() {
		// When the child is in the alternate screen buffer, the overlay drew
		// panels directly into that buffer, so the child must repaint. Newer
		// renderers ignore a same-size SIGWINCH (PreRedraw), so force the
		// repaint with a winsize jiggle. On the main screen, ExitAltScreen
		// already restored the child's content, so a plain SIGWINCH suffices.
		if rt.outputFilter != nil && rt.outputFilter.InAltScreen() && handle.ForceRepaint != nil {
			handle.ForceRepaint()
		} else if handle.PreRedraw != nil {
			handle.PreRedraw()
		}
		if rt.outputFilter != nil {
			rt.outputFilter.EnforceScrollRegion()
		}
	})

	rt.statusFetcher = overlay.NewStatusFetcher(daemonClient, rt.termOverlay, 2*time.Second)
	rt.statusFetcher.Start(ctx)
	rt.inputRouter.SetStatusFetcher(rt.statusFetcher)

	// Forwarding pause/resume: gate the in-process PTY injection and tell the
	// daemon to drop incident-digest pings for this session. rt.netOverlay and
	// rt.daemonHandle are captured lazily (set earlier in the run flow).
	rt.inputRouter.SetForwardingToggle(func(paused bool) {
		if rt.netOverlay != nil {
			rt.netOverlay.SetForwardingPaused(paused)
		}
		if rt.daemonHandle != nil {
			rt.daemonHandle.SetForwarding(paused)
		}
	})
}

// projectPathFromHandle returns a project-path string. We don't carry it
// on ptyHandle (it's only used by the summarizer), so look it up here from
// the working directory.
func projectPathFromHandle(_ *ptyHandle) string {
	cwd, _ := os.Getwd()
	return cwd
}

// overlaySessionSocketPath returns the per-session overlay socket path,
// preferring the user-supplied --overlay-socket value when set. The
// session code is sanitized so it can safely appear in a path component.
func overlaySessionSocketPath(sessionCode string) string {
	if overlaySocketPath != "" {
		return overlaySocketPath
	}
	defaultPath := DefaultOverlaySocketPath()
	if defaultPath == "" {
		return ""
	}
	dir := filepath.Dir(defaultPath)
	safeCode := pathutil.SafePathComponent(sessionCode)
	return filepath.Join(dir, fmt.Sprintf("devtool-overlay-%s.sock", safeCode))
}
