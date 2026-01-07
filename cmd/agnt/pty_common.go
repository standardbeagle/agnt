package main

import (
	"context"
	"sync"
	"time"

	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/overlay"
)

// daemonSessionHandle manages the daemon connection and session registration.
// It encapsulates the resilient client, heartbeat goroutine, and session state.
type daemonSessionHandle struct {
	client            *daemon.ResilientClient
	heartbeatStop     chan struct{}
	sessionCode       string
	sessionRegistered bool
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
// The registration happens asynchronously; use handle.IsConnected() to check status.
func startDaemonSession(ctx context.Context, cfg daemonSessionConfig, onAutostartError func(errs []string)) *daemonSessionHandle {
	handle := &daemonSessionHandle{
		sessionCode: cfg.SessionCode,
	}

	go func() {
		config := daemon.DefaultResilientClientConfig()
		if cfg.SocketPath != "" {
			config.AutoStartConfig.SocketPath = cfg.SocketPath
		}

		// Re-register overlay and session when connection is restored after daemon restart
		config.OnReconnect = func(client *daemon.Client) error {
			_, err := client.OverlaySet(cfg.OverlayEndpoint)
			if err != nil {
				return err
			}
			// Re-register session
			_, _ = client.SessionRegister(cfg.SessionCode, cfg.OverlayEndpoint, cfg.ProjectPath, cfg.Command, cfg.CmdArgs)
			return nil
		}

		handle.client = daemon.NewResilientClient(config)
		if err := handle.client.Connect(); err != nil {
			return // Daemon connection is best-effort, non-critical
		}

		// Register overlay endpoint on initial connect (best-effort)
		_, _ = handle.client.OverlaySet(cfg.OverlayEndpoint)

		// Register session with daemon (autostart happens server-side)
		result, err := handle.client.SessionRegister(cfg.SessionCode, cfg.OverlayEndpoint, cfg.ProjectPath, cfg.Command, cfg.CmdArgs)
		if err != nil {
			return
		}

		handle.sessionRegistered = true

		// Process autostart results
		if result != nil && !cfg.SkipAutostart && onAutostartError != nil {
			if autostart, ok := result["autostart"].(map[string]interface{}); ok {
				if errs, ok := autostart["errors"].([]interface{}); ok && len(errs) > 0 {
					var errStrs []string
					for _, e := range errs {
						if str, ok := e.(string); ok {
							errStrs = append(errStrs, str)
						}
					}
					if len(errStrs) > 0 {
						onAutostartError(errStrs)
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
