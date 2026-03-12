package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/overlay"
	"github.com/standardbeagle/agnt/internal/protocol"
)

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
			_, _ = client.SessionRegister(cfg.SessionCode, cfg.OverlayEndpoint, cfg.ProjectPath, cfg.Command, cfg.CmdArgs)
			return nil
		}

		handle.client = daemon.NewResilientClient(config)
		if err := handle.client.Connect(); err != nil {
			return // Daemon connection is best-effort, non-critical
		}

		// Register session with daemon (autostart and overlay scoping happen server-side)
		result, err := handle.client.SessionRegister(cfg.SessionCode, cfg.OverlayEndpoint, cfg.ProjectPath, cfg.Command, cfg.CmdArgs)
		if err != nil {
			return
		}

		handle.sessionRegistered = true

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
				fmt.Fprintf(w, "\x1b[2m  │ %s\x1b[0m\r\n", outputLine)
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
	agntCfg, _ := config.LoadAgntConfig(projectPath)
	if agntCfg == nil {
		agntCfg = config.DefaultAgntConfig()
	}

	// Check if alerts are explicitly disabled
	if agntCfg.Alerts != nil && !agntCfg.Alerts.IsEnabled() {
		return nil
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
