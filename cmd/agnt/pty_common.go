package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/overlay"
	"github.com/standardbeagle/agnt/internal/protocol"
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

// BroadcastOutputPreview sends output preview lines to the daemon.
func (h *daemonSessionHandle) BroadcastOutputPreview(lines []string) {
	if h.IsConnected() {
		_ = h.client.BroadcastOutputPreview(lines)
	}
}

// terminalOverlayComponents contains all overlay-related components.
// These are initialized together and need coordinated cleanup.
type terminalOverlayComponents struct {
	overlay       *overlay.Overlay
	inputRouter   *overlay.InputRouter
	statusFetcher *overlay.StatusFetcher
	outputFilter  *overlay.ProtectedWriter
	outputGate    *overlay.OutputGate
	daemonConn    *daemon.Conn
}

// Cleanup stops all overlay components in the correct order.
func (c *terminalOverlayComponents) Cleanup() {
	if c == nil {
		return
	}
	if c.inputRouter != nil {
		c.inputRouter.Stop()
	}
	if c.outputFilter != nil {
		c.outputFilter.Stop()
	}
	if c.statusFetcher != nil {
		c.statusFetcher.Stop()
	}
	if c.daemonConn != nil {
		c.daemonConn.Close()
	}
}

// ioGoroutineHandles contains channels and sync primitives for I/O goroutines.
type ioGoroutineHandles struct {
	done            chan struct{}
	wg              *sync.WaitGroup
	activityMonitor *overlay.ActivityMonitor
}

// Wait waits for all I/O goroutines to complete.
func (h *ioGoroutineHandles) Wait() {
	if h != nil && h.wg != nil {
		h.wg.Wait()
	}
}

// StopActivityMonitor stops the activity monitor if running.
func (h *ioGoroutineHandles) StopActivityMonitor() {
	if h != nil && h.activityMonitor != nil {
		h.activityMonitor.Stop()
	}
}

// terminalCleanupConfig contains parameters for terminal cleanup.
type terminalCleanupConfig struct {
	height      int
	resetScroll bool
	showCursor  bool
	clearBottom bool
}

// defaultTerminalCleanupConfig returns the default cleanup configuration.
func defaultTerminalCleanupConfig(height int) terminalCleanupConfig {
	return terminalCleanupConfig{
		height:      height,
		resetScroll: true,
		showCursor:  true,
		clearBottom: true,
	}
}

// heartbeatConfig contains configuration for the daemon session heartbeat.
type heartbeatConfig struct {
	interval    time.Duration
	sessionCode string
}

// defaultHeartbeatConfig returns the default heartbeat configuration.
func defaultHeartbeatConfig(sessionCode string) heartbeatConfig {
	return heartbeatConfig{
		interval:    30 * time.Second,
		sessionCode: sessionCode,
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
			return // Daemon connection is best-effort, non-critical
		}

		// Register session with daemon (autostart and overlay scoping happen server-side)
		result, err := handle.client.SessionRegisterWithContainment(cfg.SessionCode, cfg.OverlayEndpoint, cfg.ProjectPath, cfg.Command, cfg.CmdArgs, cfg.SessionPGID, cfg.SessionJobHandle)
		if err != nil {
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
			if netOverlay != nil {
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
