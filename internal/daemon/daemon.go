package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/agnt/internal/automation"
	"github.com/standardbeagle/agnt/internal/browser"
	"github.com/standardbeagle/agnt/internal/chromedp"
	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/project"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/store"
	"github.com/standardbeagle/agnt/internal/tunnel"
	"github.com/standardbeagle/agnt/internal/updater"
	"github.com/standardbeagle/go-cli-server/hub"
	"github.com/standardbeagle/go-cli-server/process"
	"github.com/standardbeagle/go-cli-server/script"
)

// Version is the daemon version.
// Can be overridden at build time with: -ldflags "-X github.com/standardbeagle/agnt/internal/daemon.Version=x.y.z"
var Version = "0.12.18"

// BuildTime is the build timestamp (RFC3339 format).
// Set at build time with: -ldflags "-X github.com/standardbeagle/agnt/internal/daemon.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
var BuildTime = ""

// GitCommit is the git commit hash.
// Set at build time with: -ldflags "-X github.com/standardbeagle/agnt/internal/daemon.GitCommit=$(git rev-parse HEAD)"
var GitCommit = ""

// ProxyEventType represents the type of proxy event.
type ProxyEventType int

const (
	// URLDetected indicates a URL was detected from script output
	URLDetected ProxyEventType = iota
	// ExplicitStart indicates a proxy should start with explicit config
	ExplicitStart
	// ScriptStopped indicates a script stopped and its proxies should be cleaned up
	ScriptStopped
)

// ProxyEvent represents an event that triggers proxy creation or cleanup.
type ProxyEvent struct {
	Type     ProxyEventType
	ScriptID string // Process/script ID that triggered the event
	URL      string // Detected URL (for URLDetected events)
	ProxyID  string // Specific proxy ID (for ExplicitStart events)
	Config   *config.ProxyConfig
	Path     string // Project path
}

// DaemonConfig holds configuration for the daemon.
type DaemonConfig struct {
	// Socket configuration
	SocketPath string

	// Process manager configuration
	ProcessConfig process.ManagerConfig

	// Max concurrent clients (0 = unlimited)
	MaxClients int

	// Connection read timeout (0 = no timeout)
	ReadTimeout time.Duration

	// Connection write timeout (0 = no timeout)
	WriteTimeout time.Duration

	// OverlayEndpoint is the URL of the agnt overlay server for forwarding events.
	// Example: "http://127.0.0.1:19191"
	// When set, proxies will forward panel messages, sketches, etc. to the overlay.
	OverlayEndpoint string

	// EnableStatePersistence enables persisting proxy configs for recovery.
	EnableStatePersistence bool

	// StatePath is the path to the state file.
	// If empty, uses default location.
	StatePath string

	// EnableUpdateCheck enables periodic update checking.
	// Default: true
	EnableUpdateCheck bool

	// UpdateCheckInterval is the interval between update checks.
	// Default: 24 hours
	UpdateCheckInterval time.Duration
}

// DefaultDaemonConfig returns sensible defaults.
func DefaultDaemonConfig() DaemonConfig {
	return DaemonConfig{
		SocketPath:             DefaultSocketPath(),
		ProcessConfig:          process.DefaultManagerConfig(),
		MaxClients:             100,
		ReadTimeout:            0, // No timeout for long-running commands
		WriteTimeout:           30 * time.Second,
		EnableStatePersistence: true,
		EnableUpdateCheck:      true,
		UpdateCheckInterval:    24 * time.Hour,
	}
}

// Daemon is the main daemon process that manages state across client connections.
// The daemon is built on top of go-cli-server Hub, which owns the ProcessManager
// and handles session/client lifecycle. Daemon adds agnt-specific functionality:
// proxy management, tunnel management, URL tracking, and scheduling.
type Daemon struct {
	config DaemonConfig

	// Core hub (owns ProcessManager, sessions, clients)
	hub *hub.Hub

	// agnt-specific managers
	proxym            *proxy.ProxyManager
	tunnelm           *tunnel.Manager
	browserm          *browser.Manager
	sessionm          *chromedp.SessionManager // chromedp automation sessions
	storem            *store.StoreManager
	automator         *automation.Processor
	autoRestarter     *ProcessAutoRestarter // Process auto-restart manager
	alertStore        *ProcessAlertStore    // Ring buffer store for process output alerts
	startupErrorStore *StartupLogStore      // Ring buffer for startup events
	scriptRegistry    *script.Registry      // Per-script state that persists across process restarts
	scriptConfigs     sync.Map              // processID -> *config.ScriptConfig (agnt-specific config)

	// Session and scheduling (agnt-specific extensions)
	sessionRegistry   *SessionRegistry
	scheduler         *Scheduler
	schedulerStateMgr *SchedulerStateManager

	// State persistence
	stateMgr   *StateManager
	pidTracker *process.FilePIDTracker

	// URL tracking for processes
	urlTracker *URLTracker

	// Readiness coordination for dependency-ordered autostart
	readySignaler *ReadySignaler

	// Proxy event system
	proxyEvents   chan ProxyEvent
	scriptProxies map[string][]string // scriptID -> []proxyID
	scriptProxyMu sync.RWMutex

	// Update checker
	updateChecker *updater.UpdateChecker

	// Overlay endpoint (can be set dynamically)
	overlayEndpoint atomic.Pointer[string]

	// Lifecycle
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	started    time.Time
	shutdownMu sync.Mutex
	shutdown   bool
}

// New creates a new daemon instance.
func New(config DaemonConfig) *Daemon {
	ctx, cancel := context.WithCancel(context.Background())

	// Create session registry with 60-second heartbeat timeout (agnt-specific)
	sessionRegistry := NewSessionRegistry(60 * time.Second)

	// Create scheduler state manager for per-project task persistence
	schedulerStateMgr := NewSchedulerStateManager()

	// Create scheduler
	scheduler := NewScheduler(DefaultSchedulerConfig(), sessionRegistry, schedulerStateMgr)

	// Create PID tracker for orphan cleanup
	pidTracker := process.NewFilePIDTracker(process.FilePIDTrackerConfig{
		AppName: "devtool-mcp",
	})

	// Configure Hub with ProcessManager enabled
	procConfig := config.ProcessConfig
	procConfig.PIDTracker = pidTracker

	hubConfig := hub.Config{
		SocketPath:        config.SocketPath,
		SocketName:        "devtool-mcp", // Keep existing socket name
		MaxClients:        config.MaxClients,
		ReadTimeout:       config.ReadTimeout,
		WriteTimeout:      config.WriteTimeout,
		EnableProcessMgmt: true,
		ProcessConfig:     procConfig,
		Version:           Version,
	}

	h := hub.New(hubConfig)

	d := &Daemon{
		config:            config,
		hub:               h,
		proxym:            proxy.NewProxyManager(),
		tunnelm:           tunnel.NewManager(),
		browserm:          browser.NewManager(),
		sessionm:          chromedp.NewSessionManager(),
		storem:            store.NewStoreManager(),
		alertStore:        NewProcessAlertStore(500),
		startupErrorStore: NewStartupLogStore(100),
		scriptRegistry:    script.NewRegistry(),
		sessionRegistry:   sessionRegistry,
		scheduler:         scheduler,
		schedulerStateMgr: schedulerStateMgr,
		pidTracker:        pidTracker,
		proxyEvents:       make(chan ProxyEvent, 10), // Buffer 10 events
		scriptProxies:     make(map[string][]string),
		ctx:               ctx,
		cancel:            cancel,
	}

	// Wire script registry into ProcessManager for automatic lifecycle updates
	h.ProcessManager().SetScriptRegistry(d.scriptRegistry)

	// Initialize process auto-restarter
	d.autoRestarter = NewProcessAutoRestarter(d)

	// Initialize ready signaler for dependency-ordered autostart
	d.readySignaler = NewReadySignaler()

	// Create URLTracker with callbacks to emit proxy events
	// Access ProcessManager through Hub
	urlTracker := NewURLTracker(h.ProcessManager(), DefaultURLTrackerConfig())
	urlTracker.onURLDetected = func(processID, url string) {
		// Signal readiness for dependency ordering
		d.readySignaler.SignalReady(processID)

		// Get project path from process
		var projectPath string
		if proc, err := h.ProcessManager().Get(processID); err == nil {
			projectPath = proc.ProjectPath
		}

		// Send event to proxy event handler (non-blocking send)
		select {
		case d.proxyEvents <- ProxyEvent{
			Type:     URLDetected,
			ScriptID: processID,
			URL:      url,
			Path:     projectPath,
		}:
		default:
			// Channel full, log warning
			debug.Warn("daemon", "Proxy event channel full, dropping URL detection event for %s: %s", processID, url)
		}
	}
	urlTracker.onProcessStopped = func(processID string) {
		// Send script stopped event (non-blocking send)
		select {
		case d.proxyEvents <- ProxyEvent{
			Type:     ScriptStopped,
			ScriptID: processID,
		}:
		default:
			// Channel full, log warning
			debug.Warn("daemon", "Proxy event channel full, dropping process stopped event for %s", processID)
		}
	}
	urlTracker.onProcessFirstSeen = func(processID string) {
		// Load URL matchers from config when a process is first detected
		d.LoadURLMatchersForProcess(processID)
	}
	d.urlTracker = urlTracker

	// Initialize state manager if persistence is enabled
	if config.EnableStatePersistence {
		d.stateMgr = NewStateManager(StateManagerConfig{
			StatePath: config.StatePath,
			AutoLoad:  true,
		})
	}

	// Set initial overlay endpoint from config or persisted state
	if config.OverlayEndpoint != "" {
		d.overlayEndpoint.Store(&config.OverlayEndpoint)
	} else if d.stateMgr != nil {
		if endpoint := d.stateMgr.GetOverlayEndpoint(); endpoint != "" {
			d.overlayEndpoint.Store(&endpoint)
		}
	}

	// Initialize update checker if enabled
	if config.EnableUpdateCheck {
		updateConfig := updater.Config{
			CurrentVersion: Version,
			CheckInterval:  config.UpdateCheckInterval,
			GitHubRepo:     updater.DefaultGitHubRepo,
			Enabled:        true,
		}
		d.updateChecker = updater.NewUpdateChecker(updateConfig)
	}

	return d
}

// registerCommands registers agnt-specific commands with the Hub.
// This delegates to registerAgntCommands() in hub_handlers.go.
func (d *Daemon) registerCommands() {
	d.registerAgntCommands()
}

// Start starts the daemon and begins accepting connections.
func (d *Daemon) Start() error {
	d.shutdownMu.Lock()
	if d.shutdown {
		d.shutdownMu.Unlock()
		debug.Log("daemon", "Start() called but daemon already shutdown")
		return errors.New("daemon already shutdown")
	}
	d.shutdownMu.Unlock()

	// Setup file-based logging for debugging (captures output even when daemon runs detached)
	setupDebugLogging()

	// Register agnt-specific commands with Hub before starting
	d.registerCommands()

	// Register session cleanup callback with Hub
	// This ensures processes/proxies are stopped when sessions disconnect
	d.hub.SetSessionCleanup(func(sessionCode string) {
		d.CleanupSessionResources(sessionCode)
	})

	// Start the Hub (handles socket creation, accept loop, client management)
	if err := d.hub.Start(); err != nil {
		debug.Error("daemon", "failed to start hub: %v", err)
		return fmt.Errorf("failed to start hub: %w", err)
	}
	d.started = time.Now()

	// Clean up orphaned processes from previous crash
	d.cleanupOrphans()

	// Restore proxies from persisted state
	d.restoreProxies()

	// Start the scheduler for scheduled message delivery
	if err := d.scheduler.Start(d.ctx); err != nil {
		debug.Log("daemon", "failed to start scheduler: %v", err)
	}

	// Start URL tracker for process URL detection
	d.urlTracker.Start(d.ctx)

	// Start proxy event handler for event-driven proxy creation
	d.wg.Add(1)
	go d.handleProxyEvents()

	// Start update checker if enabled
	if d.updateChecker != nil {
		d.updateChecker.Start()
	}

	return nil
}

// restoreProxies restores proxy servers from persisted state.
func (d *Daemon) restoreProxies() {
	if d.stateMgr == nil {
		return
	}

	proxies := d.stateMgr.GetProxies()
	if len(proxies) == 0 {
		return
	}

	// Removed startup log: restoring %d proxies from state

	overlayEndpoint := d.OverlayEndpoint()

	for _, pc := range proxies {
		config := proxy.ProxyConfig{
			ID:          pc.ID,
			TargetURL:   pc.TargetURL,
			ListenPort:  pc.Port,
			MaxLogSize:  pc.MaxLogSize,
			AutoRestart: true,
			Path:        pc.Path,
		}

		proxyServer, err := d.proxym.Create(d.ctx, config)
		if err != nil {
			debug.Log("daemon", "failed to restore proxy %s: %v", pc.ID, err)
			// Remove from state if it can't be restored
			d.stateMgr.RemoveProxy(pc.ID)
			continue
		}

		// Configure overlay endpoint: prefer session-scoped, fall back to global
		if pc.Path != "" {
			if session, ok := d.sessionRegistry.FindByDirectory(pc.Path); ok && session.OverlayPath != "" {
				proxyServer.SetOverlayEndpoint(session.OverlayPath)
			} else if overlayEndpoint != "" {
				proxyServer.SetOverlayEndpoint(overlayEndpoint)
			}
		} else if overlayEndpoint != "" {
			proxyServer.SetOverlayEndpoint(overlayEndpoint)
		}

		// Removed startup log: restored proxy %s -> %s on port %d
	}
}

// cleanupOrphans cleans up orphaned processes from a previous daemon crash.
func (d *Daemon) cleanupOrphans() {
	if d.pidTracker == nil {
		return
	}

	killedCount, err := d.pidTracker.CleanupOrphans(os.Getpid())
	if err != nil {
		debug.Log("daemon", "failed to cleanup orphans: %v", err)
		return
	}

	if killedCount > 0 {
		debug.Log("daemon", "cleaned up %d orphaned process(es) from previous crash", killedCount)
	}

	// Set current daemon PID for future crash detection
	if err := d.pidTracker.SetDaemonPID(os.Getpid()); err != nil {
		debug.Log("daemon", "failed to set daemon PID: %v", err)
	}
}

// Stop gracefully shuts down the daemon.
func (d *Daemon) Stop(ctx context.Context) error {
	d.shutdownMu.Lock()
	if d.shutdown {
		d.shutdownMu.Unlock()
		return nil
	}
	d.shutdown = true
	d.shutdownMu.Unlock()

	debug.Log("daemon", "Daemon stopping")

	// Signal all goroutines to stop
	d.cancel()

	// Stop Hub (handles listener, clients, connections)
	if err := d.hub.Stop(ctx); err != nil {
		debug.Log("daemon", "error stopping hub: %v", err)
	}

	// Shutdown agnt-specific managers
	var errs []error

	// Stop scheduler
	d.scheduler.Stop(ctx)

	// Stop update checker
	if d.updateChecker != nil {
		d.updateChecker.Stop(ctx)
	}

	// Stop process auto-restarter first (before processes are stopped)
	if d.autoRestarter != nil {
		d.autoRestarter.Shutdown(ctx)
	}

	if err := d.tunnelm.Shutdown(ctx); err != nil {
		debug.Error("daemon", "tunnel manager shutdown error: %v", err)
		errs = append(errs, fmt.Errorf("tunnel manager: %w", err))
	}

	if err := d.browserm.Shutdown(ctx); err != nil {
		debug.Error("daemon", "browser manager shutdown error: %v", err)
		errs = append(errs, fmt.Errorf("browser manager: %w", err))
	}

	if err := d.sessionm.Shutdown(ctx); err != nil {
		debug.Error("daemon", "session manager shutdown error: %v", err)
		errs = append(errs, fmt.Errorf("session manager: %w", err))
	}

	if err := d.proxym.Shutdown(ctx); err != nil {
		debug.Error("daemon", "proxy manager shutdown error: %v", err)
		errs = append(errs, fmt.Errorf("proxy manager: %w", err))
	}

	// Clear PID tracking (clean shutdown)
	if d.pidTracker != nil {
		if err := d.pidTracker.Clear(); err != nil {
			debug.Log("daemon", "failed to clear PID tracking: %v", err)
		}
	}

	// Wait for goroutines with timeout
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Clean exit
	case <-ctx.Done():
		errs = append(errs, ctx.Err())
	}

	// Socket cleanup is handled by Hub.Stop()

	debug.Log("daemon", "Daemon stopped")

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// Wait blocks until the daemon stops.
func (d *Daemon) Wait() {
	<-d.ctx.Done()
	d.wg.Wait()
}

// Info returns daemon information.
func (d *Daemon) Info() DaemonInfo {
	info := DaemonInfo{
		Version:     Version,
		BuildTime:   BuildTime,
		GitCommit:   GitCommit,
		SocketPath:  d.hub.SocketPath(),
		Uptime:      time.Since(d.started),
		ClientCount: d.hub.ClientCount(),
		ProcessInfo: ProcessInfo{
			Active:       d.hub.ProcessManager().ActiveCount(),
			TotalStarted: d.hub.ProcessManager().TotalStarted(),
			TotalFailed:  d.hub.ProcessManager().TotalFailed(),
		},
		ProxyInfo: ProxyInfo{
			Active:       d.proxym.ActiveCount(),
			TotalStarted: d.proxym.TotalStarted(),
		},
		TunnelInfo: TunnelInfo{
			Active: int64(d.tunnelm.ActiveCount()),
		},
		BrowserInfo: BrowserInfo{
			Active:       int64(d.browserm.ActiveCount()),
			TotalStarted: d.browserm.TotalStarted(),
		},
		SessionInfo:   d.sessionRegistry.Info(),
		SchedulerInfo: d.scheduler.Info(),
	}

	// Include update info if update checker is enabled
	if d.updateChecker != nil {
		updateInfo := d.updateChecker.GetUpdateInfo()
		info.UpdateInfo = &updateInfo
	}

	return info
}

// ProcessManager returns the process manager.
func (d *Daemon) ProcessManager() *process.ProcessManager {
	return d.hub.ProcessManager()
}

// ProxyManager returns the proxy manager.
func (d *Daemon) ProxyManager() *proxy.ProxyManager {
	return d.proxym
}

// TunnelManager returns the tunnel manager.
func (d *Daemon) TunnelManager() *tunnel.Manager {
	return d.tunnelm
}

// BrowserManager returns the browser manager.
func (d *Daemon) BrowserManager() *browser.Manager {
	return d.browserm
}

// SessionManager returns the chromedp session manager.
func (d *Daemon) SessionManager() *chromedp.SessionManager {
	return d.sessionm
}

// AutoRestarter returns the process auto-restart manager.
func (d *Daemon) AutoRestarter() *ProcessAutoRestarter {
	return d.autoRestarter
}

// AlertStore returns the process alert store.
func (d *Daemon) AlertStore() *ProcessAlertStore {
	return d.alertStore
}

// StartupLogStore returns the startup log store.
func (d *Daemon) StartupLogStore() *StartupLogStore {
	return d.startupErrorStore
}

// ScriptRegistry returns the script registry.
func (d *Daemon) ScriptRegistry() *script.Registry {
	return d.scriptRegistry
}

// SessionRegistry returns the session registry.
func (d *Daemon) SessionRegistry() *SessionRegistry {
	return d.sessionRegistry
}

// Scheduler returns the message scheduler.
func (d *Daemon) Scheduler() *Scheduler {
	return d.scheduler
}

// GetSession retrieves a session by code.
func (d *Daemon) GetSession(code string) (*Session, bool) {
	return d.sessionRegistry.Get(code)
}

// SetOverlayEndpoint sets the overlay endpoint URL and updates all existing proxies.
// The endpoint should be the full URL, e.g., "http://127.0.0.1:19191".
// Pass an empty string to disable overlay forwarding.
func (d *Daemon) SetOverlayEndpoint(endpoint string) {
	if endpoint == "" {
		d.overlayEndpoint.Store(nil)
	} else {
		d.overlayEndpoint.Store(&endpoint)
	}

	// Persist to state
	if d.stateMgr != nil {
		d.stateMgr.SetOverlayEndpoint(endpoint)
	}

	// Update all existing proxies
	for _, p := range d.proxym.List() {
		p.SetOverlayEndpoint(endpoint)
	}
}

// StateManager returns the state manager (may be nil if persistence is disabled).
func (d *Daemon) StateManager() *StateManager {
	return d.stateMgr
}

// OverlayEndpoint returns the current overlay endpoint URL, or empty string if not set.
func (d *Daemon) OverlayEndpoint() string {
	ptr := d.overlayEndpoint.Load()
	if ptr == nil {
		return ""
	}
	return *ptr
}

// LoadURLMatchersForProcess loads URL matchers from agnt.kdl for a process and sets them on the URL tracker.
// Process ID format: {basename}:{scriptName} (e.g., "my-project:dev")
// The project path is retrieved from the process's ProjectPath field.
func (d *Daemon) LoadURLMatchersForProcess(processID string) {
	// Get process to retrieve its project path
	proc, err := d.hub.ProcessManager().Get(processID)
	if err != nil {
		debug.Log("daemon", "LoadURLMatchersForProcess: process %s not found", processID)
		return
	}

	projectPath := proc.ProjectPath
	if projectPath == "" {
		debug.Log("daemon", "LoadURLMatchersForProcess: process %s has no project path", processID)
		return
	}

	// Parse process ID to extract script name (second part after colon)
	parts := strings.SplitN(processID, ":", 2)
	if len(parts) < 2 {
		return // Invalid process ID format
	}
	scriptName := parts[1]

	// Load agnt config
	agntConfig, err := config.LoadAgntConfig(projectPath)
	if err != nil {
		debug.Log("daemon", "LoadURLMatchersForProcess: failed to load config from %s: %v", projectPath, err)
		return // No config or error - skip URL matchers
	}

	// Find script config
	script, ok := agntConfig.Scripts[scriptName]
	if !ok || script == nil {
		debug.Log("daemon", "LoadURLMatchersForProcess: script %s not found in config", scriptName)
		return // Script not found in config
	}

	// Set URL matchers if specified
	if len(script.URLMatchers) > 0 {
		d.urlTracker.SetURLMatchers(processID, script.URLMatchers)
		debug.Log("daemon", "Set URL matchers for %s: %v", processID, script.URLMatchers)
	}
}

// StopAllResources stops all processes, proxies, and tunnels without shutting down the daemon.
// Unlike Shutdown, this does NOT prevent new resources from being created afterward.
// This is typically called explicitly via the daemon management tool, not automatically.
func (d *Daemon) StopAllResources(ctx context.Context) {
	// Use a reasonable timeout for cleanup
	cleanupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	// Stop all tunnels
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := d.tunnelm.StopAll(cleanupCtx); err != nil {
			debug.Log("daemon", "error stopping tunnels: %v", err)
		}
	}()

	// Stop all browsers
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := d.browserm.StopAll(cleanupCtx); err != nil {
			debug.Log("daemon", "error stopping browsers: %v", err)
		}
	}()

	// Stop all proxies and update state
	wg.Add(1)
	go func() {
		defer wg.Done()
		stoppedIDs, err := d.proxym.StopAll(cleanupCtx)
		if err != nil {
			debug.Log("daemon", "error stopping proxies: %v", err)
		}
		// Remove stopped proxies from persisted state
		if d.stateMgr != nil {
			for _, id := range stoppedIDs {
				d.stateMgr.RemoveProxy(id)
			}
		}
	}()

	// Stop all processes
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := d.hub.ProcessManager().StopAll(cleanupCtx); err != nil {
			debug.Log("daemon", "error stopping processes: %v", err)
		}
	}()

	wg.Wait()

	// Clear overlay endpoint since no clients are connected
	d.SetOverlayEndpoint("")

	debug.Log("daemon", "all resources stopped (last client disconnected)")
}

// CleanupSessionResources stops all processes and proxies for a specific session.
// This is called when a connection that registered a session disconnects.
func (d *Daemon) CleanupSessionResources(sessionCode string) {
	// Get session to find project path
	session, ok := d.sessionRegistry.Get(sessionCode)
	if !ok {
		debug.Log("daemon", "session %s not found for cleanup", sessionCode)
		return
	}

	projectPath := session.ProjectPath
	if projectPath == "" {
		debug.Log("daemon", "session %s has no project path, skipping resource cleanup", sessionCode)
		// Still unregister the session
		d.sessionRegistry.Unregister(sessionCode)
		return
	}

	debug.Log("daemon", "cleaning up resources for session %s (project: %s)", sessionCode, projectPath)

	// Handle script ownership transfer or cleanup.
	// Remove the session as observer first, then check ownership.
	var orphanedProcessIDs []string
	for _, entry := range d.scriptRegistry.List(projectPath) {
		entry.RemoveSession(sessionCode)

		if entry.Owner() != sessionCode {
			continue
		}

		// This session owns this script — transfer or stop
		newOwner := entry.TransferOwnership()
		if newOwner != "" {
			debug.Log("daemon", "transferred ownership of script %s from %s to %s", entry.Name, sessionCode, newOwner)
		} else {
			debug.Log("daemon", "no observers for script %s, marking for stop", entry.Name)
			entry.SetState(script.StateStopped)
			orphanedProcessIDs = append(orphanedProcessIDs, entry.ProcessID)
		}
	}

	// Use a reasonable timeout for cleanup
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Check if any other sessions remain for this project
	hasOtherSessions := false
	for _, s := range d.sessionRegistry.List(projectPath, false) {
		if s.Code != sessionCode {
			hasOtherSessions = true
			break
		}
	}

	var wg sync.WaitGroup

	if !hasOtherSessions {
		// Last session for this project — stop all proxies and browsers
		wg.Add(1)
		go func() {
			defer wg.Done()
			stoppedIDs, err := d.proxym.StopByProjectPath(ctx, projectPath)
			if err != nil {
				debug.Log("daemon", "error stopping proxies for project %s: %v", projectPath, err)
			}
			if len(stoppedIDs) > 0 {
				debug.Log("daemon", "stopped proxies: %v", stoppedIDs)
				if d.stateMgr != nil {
					for _, id := range stoppedIDs {
						d.stateMgr.RemoveProxy(id)
					}
				}
			}
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			stoppedIDs, err := d.browserm.StopByProjectPath(ctx, projectPath)
			if err != nil {
				debug.Log("daemon", "error stopping browsers for project %s: %v", projectPath, err)
			}
			if len(stoppedIDs) > 0 {
				debug.Log("daemon", "stopped browsers: %v", stoppedIDs)
			}
		}()
	}

	// Stop only orphaned script processes (no remaining observers)
	if len(orphanedProcessIDs) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pm := d.hub.ProcessManager()
			for _, pid := range orphanedProcessIDs {
				if err := pm.Stop(ctx, pid); err != nil {
					debug.Log("daemon", "error stopping orphaned process %s: %v", pid, err)
				} else {
					debug.Log("daemon", "stopped orphaned process %s", pid)
					if d.autoRestarter != nil {
						d.autoRestarter.Unregister(pid)
					}
				}
			}
		}()
	} else if !hasOtherSessions {
		// No orphaned scripts but last session — stop all remaining processes
		wg.Add(1)
		go func() {
			defer wg.Done()
			stoppedIDs, err := d.hub.ProcessManager().StopByProjectPath(ctx, projectPath)
			if err != nil {
				debug.Log("daemon", "error stopping processes for project %s: %v", projectPath, err)
			}
			if len(stoppedIDs) > 0 {
				debug.Log("daemon", "stopped processes: %v", stoppedIDs)
				if d.autoRestarter != nil {
					for _, id := range stoppedIDs {
						d.autoRestarter.Unregister(id)
					}
				}
			}
		}()
	}

	wg.Wait()

	// Unregister the session
	if err := d.sessionRegistry.Unregister(sessionCode); err != nil {
		debug.Log("daemon", "error unregistering session %s: %v", sessionCode, err)
	}

	debug.Log("daemon", "session %s cleanup complete", sessionCode)
}

// NOTE: acceptLoop is now handled by Hub - removed from Daemon.
// Session cleanup is registered with Hub via SetSessionCleanup() in Start().

// DaemonInfo holds daemon status information.
type DaemonInfo struct {
	Version       string              `json:"version"`
	BuildTime     string              `json:"build_time,omitempty"` // Build timestamp (RFC3339)
	GitCommit     string              `json:"git_commit,omitempty"` // Git commit hash
	SocketPath    string              `json:"socket_path"`
	Uptime        time.Duration       `json:"uptime"`
	ClientCount   int64               `json:"client_count"`
	ProcessInfo   ProcessInfo         `json:"process_info"`
	ProxyInfo     ProxyInfo           `json:"proxy_info"`
	TunnelInfo    TunnelInfo          `json:"tunnel_info"`
	BrowserInfo   BrowserInfo         `json:"browser_info"`
	SessionInfo   SessionInfo         `json:"session_info"`
	SchedulerInfo SchedulerInfo       `json:"scheduler_info"`
	UpdateInfo    *updater.UpdateInfo `json:"update_info,omitempty"` // Update availability info
}

// ProcessInfo holds process manager statistics.
type ProcessInfo struct {
	Active       int64 `json:"active"`
	TotalStarted int64 `json:"total_started"`
	TotalFailed  int64 `json:"total_failed"`
}

// ProxyInfo holds proxy manager statistics.
type ProxyInfo struct {
	Active       int64 `json:"active"`
	TotalStarted int64 `json:"total_started"`
}

// TunnelInfo holds tunnel manager statistics.
type TunnelInfo struct {
	Active int64 `json:"active"`
}

// BrowserInfo holds browser manager statistics.
type BrowserInfo struct {
	Active       int64 `json:"active"`
	TotalStarted int64 `json:"total_started"`
}

// Note: SessionInfo is defined in session.go
// Note: SchedulerInfo is defined in scheduler.go

// AutostartResult holds the results of an autostart operation.
type AutostartResult struct {
	Scripts []string `json:"scripts,omitempty"`
	Proxies []string `json:"proxies,omitempty"`
	Errors  []string `json:"errors,omitempty"`
}

// RunAutostart loads .agnt.kdl config from projectPath and starts configured processes/proxies.
// This is called during SESSION REGISTER to ensure autostart happens once per project.
// Scripts are started in dependency order using topological sort:
//   - Layer 0 scripts (no dependencies) start concurrently
//   - Layer 1+ scripts wait for all their dependencies to become ready
//   - Readiness is signaled by URL detection or TCP port probe
//   - Timeout on dependency wait logs a warning and starts the script anyway
//
// Returns the list of started scripts/proxies and any errors encountered.
func (d *Daemon) RunAutostart(ctx context.Context, projectPath string) *AutostartResult {
	result := &AutostartResult{}
	log := d.startupErrorStore // short alias

	// Step 1: Validate input
	if projectPath == "" {
		log.Error("", "", "autostart", "projectPath is empty")
		return result
	}
	log.Info("", "", "autostart", fmt.Sprintf("starting autostart for %s", projectPath))

	// Step 2: Load .agnt.kdl
	agntConfig, err := config.LoadAgntConfig(projectPath)
	if err != nil {
		log.Error("", "", "config_error", fmt.Sprintf("failed to load .agnt.kdl from %s: %v", projectPath, err))
		return result
	}
	if agntConfig == nil {
		log.Info("", "", "no_config", fmt.Sprintf("no .agnt.kdl in %s", projectPath))
		return result
	}
	log.Info("", "", "config_loaded", fmt.Sprintf("%d scripts, %d proxies from %s", len(agntConfig.Scripts), len(agntConfig.Proxies), projectPath))

	// Step 3: Register ALL scripts (autostart and manual) so the overlay can see them
	for name, scriptCfg := range agntConfig.Scripts {
		processID := makeProcessID(projectPath, name)
		d.scriptConfigs.Store(processID, scriptCfg)
		if _, err := d.scriptRegistry.Register(name, projectPath, scriptConfigToEntry(scriptCfg)); err != nil {
			log.Error(processID, name, "register_failed", fmt.Sprintf("failed to register script: %v", err))
		}
	}

	// Step 4: Start autostart scripts in dependency order
	failedScripts := d.startAutostartScripts(ctx, agntConfig, projectPath, result)

	// Step 5: Start proxies (skip those depending on failed scripts)
	d.startAutostartProxies(ctx, agntConfig, projectPath, failedScripts, result)

	return result
}

// startAutostartScripts starts scripts in topological dependency order.
// Returns a set of script names that failed to start.
func (d *Daemon) startAutostartScripts(ctx context.Context, cfg *config.AgntConfig, projectPath string, result *AutostartResult) map[string]bool {
	log := d.startupErrorStore
	autostartScripts := cfg.GetAutostartScripts()
	proxyConfigs := cfg.Proxies
	failedScripts := make(map[string]bool)

	if len(autostartScripts) == 0 {
		return failedScripts
	}

	layers, sortErr := config.TopologicalSort(autostartScripts)
	if sortErr != nil {
		log.Error("", "", "dependency_sort", fmt.Sprintf("topological sort failed: %v", sortErr))
		result.Errors = append(result.Errors, fmt.Sprintf("dependency sort: %v", sortErr))
		return failedScripts
	}

	var resultMu sync.Mutex

	for layerIdx, layer := range layers {
		var layerWg sync.WaitGroup
		for _, name := range layer {
			scriptCfg := autostartScripts[name]
			if scriptCfg == nil {
				continue
			}

			layerWg.Add(1)
			go func(name string, scriptCfg *config.ScriptConfig) {
				defer layerWg.Done()
				processID := makeProcessID(projectPath, name)

				// Wait for dependencies (layer 1+)
				d.waitForDependencies(ctx, name, scriptCfg, projectPath, layerIdx)

				// Start the script
				log.Info(processID, name, "starting", fmt.Sprintf("starting %s (layer %d)", name, layerIdx))
				if err := d.autostartScript(ctx, name, scriptCfg, projectPath, proxyConfigs); err != nil {
					log.Error(processID, name, "start_failed", err.Error())
					resultMu.Lock()
					result.Errors = append(result.Errors, fmt.Sprintf("script %s: %v", name, err))
					failedScripts[name] = true
					resultMu.Unlock()
					return
				}

				log.Info(processID, name, "started", fmt.Sprintf("%s started", name))
				resultMu.Lock()
				result.Scripts = append(result.Scripts, name)
				resultMu.Unlock()

				// Port probe for dependency readiness signaling
				probePorts := d.getExpectedPortsForScript(name, scriptCfg, proxyConfigs,
					resolveWorkingDir(projectPath, scriptCfg.Cwd), "", nil)
				if len(probePorts) > 0 {
					d.readySignaler.StartPortProbe(processID, probePorts[0], ctx)
				}
			}(name, scriptCfg)
		}
		layerWg.Wait()
	}

	// Cleanup signaler channels
	for name := range autostartScripts {
		d.readySignaler.Cleanup(makeProcessID(projectPath, name))
	}

	return failedScripts
}

// waitForDependencies blocks until all dependencies for a script are ready.
func (d *Daemon) waitForDependencies(ctx context.Context, name string, scriptCfg *config.ScriptConfig, projectPath string, layerIdx int) {
	if layerIdx == 0 {
		return
	}
	for _, dep := range scriptCfg.DependsOn {
		depProcessID := makeProcessID(projectPath, dep.Name)
		timeout := dep.Timeout
		if timeout == 0 {
			timeout = config.DefaultDependencyTimeout
		}
		if err := d.readySignaler.WaitReady(depProcessID, timeout); err != nil {
			d.startupErrorStore.Add(&StartupLogEntry{
				ProcessID: makeProcessID(projectPath, name), ScriptName: name,
				Level: "warning", EventType: "dependency_wait",
				Message:   fmt.Sprintf("timeout waiting for %s: %v (starting anyway)", dep.Name, err),
				Timestamp: time.Now(),
			})
		}
	}
}

// startAutostartProxies starts proxies, skipping those that depend on failed scripts.
func (d *Daemon) startAutostartProxies(ctx context.Context, cfg *config.AgntConfig, projectPath string, failedScripts map[string]bool, result *AutostartResult) {
	log := d.startupErrorStore

	for proxyName, proxyConfig := range cfg.Proxies {
		if proxyConfig.Script != "" && failedScripts[proxyConfig.Script] {
			msg := fmt.Sprintf("proxy %q skipped: depends on failed script %q", proxyName, proxyConfig.Script)
			log.Error("", proxyName, "proxy_skipped", msg)
			result.Errors = append(result.Errors, msg)
		}
	}

	for name, proxyConfig := range cfg.GetAutostartProxies() {
		log.Info("", name, "proxy_starting", fmt.Sprintf("starting proxy %s", name))
		if err := d.autostartProxy(ctx, name, proxyConfig, projectPath); err != nil {
			log.Error("", name, "proxy_failed", err.Error())
			result.Errors = append(result.Errors, fmt.Sprintf("proxy %s: %v", name, err))
		} else {
			log.Info("", name, "proxy_started", fmt.Sprintf("proxy %s started", name))
			result.Proxies = append(result.Proxies, name)
		}
	}
}

// makeProcessID delegates to script.MakeProcessID for process ID generation.
func makeProcessID(projectPath, name string) string {
	return script.MakeProcessID(projectPath, name)
}

// stripProcessPrefix extracts the script name from a process ID.
// Process IDs use the format "project-hash:name"; this returns just "name".
func stripProcessPrefix(processID string) string {
	if idx := strings.Index(processID, ":"); idx >= 0 {
		return processID[idx+1:]
	}
	return processID
}

// mapKeys extracts keys from a script config map for logging.
func mapKeys(m map[string]*config.ScriptConfig) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// mapKeysProxy extracts keys from a proxy config map for logging.
func mapKeysProxy(m map[string]*config.ProxyConfig) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// resolveWorkingDir resolves the working directory for a script.
// If cwd is empty, returns projectPath.
// If cwd is absolute, returns it directly.
// If cwd is relative, joins it with projectPath and cleans the result.
func resolveWorkingDir(projectPath, cwd string) string {
	if cwd == "" {
		return projectPath
	}
	if filepath.IsAbs(cwd) {
		return cwd
	}
	return filepath.Clean(filepath.Join(projectPath, cwd))
}

// envMapToSlice converts a map of environment variables to a slice of "KEY=VALUE" strings.
func envMapToSlice(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	result := make([]string, 0, len(env))
	for k, v := range env {
		result = append(result, k+"="+v)
	}
	return result
}

// scriptConfigToEntry converts agnt's ScriptConfig to go-cli-server's script.Config.
func scriptConfigToEntry(cfg *config.ScriptConfig) *script.Config {
	return &script.Config{
		Run:       cfg.Run,
		Command:   cfg.Command,
		Args:      cfg.Args,
		Shell:     cfg.Shell,
		ShellArgs: cfg.ShellArgs,
		Autostart: cfg.Autostart,
		Env:       cfg.Env,
		Cwd:       cfg.Cwd,
	}
}

// autostartScript starts a single script from config with automatic EADDRINUSE recovery.
func (d *Daemon) autostartScript(ctx context.Context, name string, scriptCfg *config.ScriptConfig, projectPath string, proxyConfigs map[string]*config.ProxyConfig) error {

	// Make process ID unique per project to avoid collisions between sessions
	processID := makeProcessID(projectPath, name)

	// Store agnt-specific config for later use (e.g., restart with URLMatchers)
	d.scriptConfigs.Store(processID, scriptCfg)

	// Register in ScriptRegistry before starting (idempotent)
	entry, regErr := d.scriptRegistry.Register(name, projectPath, scriptConfigToEntry(scriptCfg))
	if regErr != nil {
		return fmt.Errorf("script registry: %w", regErr)
	}

	// Check ScriptRegistry state: if already running/starting, skip
	if state := entry.State(); state == script.StateRunning || state == script.StateStarting {
		debug.Log("daemon", "autostartScript: script %s already %s, skipping", name, state)
		return nil
	}

	// Clean up stale ProcessManager entry if it exists but isn't running
	if existing, err := d.hub.ProcessManager().Get(processID); err == nil {
		state := existing.State()
		if state != process.StateRunning && state != process.StateStarting {
			debug.Log("daemon", "autostartScript: removing stale process %s (state=%s)", processID, state)
			d.hub.ProcessManager().RemoveByPath(processID, projectPath)
		}
	}

	// Resolve working directory and environment
	workingDir := resolveWorkingDir(projectPath, scriptCfg.Cwd)
	envSlice := envMapToSlice(scriptCfg.Env)

	var command string
	var args []string

	if scriptCfg.Run != "" {
		// Shell command string - resolve via config or platform default
		command, args = scriptCfg.ResolveShell()
	} else if scriptCfg.Command != "" {
		// Explicit command specified
		command = scriptCfg.Command
		args = scriptCfg.Args
	} else {
		// No command - run as package.json script via detected package manager
		// Use workingDir for detection so monorepo subdirectories find their package.json
		proj, err := project.Detect(workingDir)
		if err != nil {
			debug.Error("daemon", "project detection failed for %s: %v", workingDir, err)
			entry.SetState(script.StateFailed)
			entry.SetLastError(fmt.Sprintf("project detection failed: %v", err))
			entry.IncrementFailCount()
			return fmt.Errorf("project detection failed: %v", err)
		}

		switch proj.Type {
		case project.ProjectNode:
			pm := proj.PackageManager
			if pm == "" {
				pm = "npm"
			}
			command = pm
			// pnpm and yarn don't need "run" prefix for scripts
			if pm == "npm" || pm == "bun" {
				args = []string{"run", name}
			} else {
				args = []string{name}
			}
		case project.ProjectGo:
			command = "go"
			args = []string{"run", name}
		case project.ProjectPython:
			command = "python"
			args = []string{"-m", name}
		default:
			debug.Error("daemon", "cannot run script %q: unknown project type %s", name, proj.Type)
			entry.SetState(script.StateFailed)
			entry.SetLastError(fmt.Sprintf("unknown project type: %s", proj.Type))
			entry.IncrementFailCount()
			return fmt.Errorf("cannot run script %q: unknown project type and no command specified", name)
		}
	}

	// Record resolved command in ScriptEntry
	entry.SetResolvedCommand(command, args)

	// Transition to starting (ProcessManager lifecycle handles Running state and StartCount)
	entry.SetState(script.StateStarting)

	// Determine expected ports for pre-flight cleanup and EADDRINUSE recovery
	expectedPorts := d.getExpectedPortsForScript(name, scriptCfg, proxyConfigs, workingDir, command, args)

	_, err := d.StartScript(ctx, StartScriptConfig{
		ProcessID:     processID,
		ProjectPath:   projectPath,
		WorkingDir:    workingDir,
		Command:       command,
		Args:          args,
		Env:           envSlice,
		ExpectedPorts: expectedPorts,
		URLMatchers:   scriptCfg.URLMatchers,
		AutoRestart:   true,
	})
	if err != nil {
		entry.SetState(script.StateFailed)
		entry.SetLastError(err.Error())
		entry.IncrementFailCount()

		// Include resolved command and process output in error for debugging
		cmdStr := command
		if len(args) > 0 {
			cmdStr = fmt.Sprintf("%s %s", command, strings.Join(args, " "))
		}
		msg := fmt.Sprintf("%s (resolved command: %s, cwd: %s)", err.Error(), cmdStr, workingDir)

		// Include process output if available (from StartupError)
		if startupErr, ok := err.(*StartupError); ok && startupErr.Output != "" {
			msg += "\n" + startupErr.Output
		}
		return fmt.Errorf("%s", msg)
	}

	// Success: ProcessManager lifecycle sets StateRunning automatically
	return nil
}

// autostartProxy starts a single proxy from config.
// Called by RunAutostart with proxies from GetAutostartProxies (explicit target or Autostart flag).
// Script-linked proxies are skipped here — they're created by the event system when URLs are detected.
func (d *Daemon) autostartProxy(ctx context.Context, name string, proxyConfig *config.ProxyConfig, projectPath string) error {
	// Skip script-linked proxies - they're handled by URLDetected events
	if proxyConfig.Script != "" {
		debug.Log("daemon", "Proxy %s is script-linked, skipping auto-start (will be created when URLs detected)", name)
		return nil
	}

	// Make proxy ID unique per project
	proxyID := makeProcessID(projectPath, name)

	// Determine target URL (must be explicitly specified)
	var targetURL string
	if proxyConfig.URL != "" {
		targetURL = proxyConfig.URL
	} else if proxyConfig.Port > 0 {
		host := proxyConfig.Host
		if host == "" {
			host = "localhost"
		}
		targetURL = fmt.Sprintf("http://%s:%d", host, proxyConfig.Port)
	} else if proxyConfig.Target != "" {
		// Legacy target field
		targetURL = proxyConfig.Target
	}

	if targetURL == "" {
		debug.Log("daemon", "Proxy %s has no explicit target URL, skipping", name)
		return nil
	}

	// Send ExplicitStart event to create the proxy
	select {
	case d.proxyEvents <- ProxyEvent{
		Type:    ExplicitStart,
		ProxyID: proxyID,
		Config:  proxyConfig,
		Path:    projectPath,
	}:
		debug.Log("daemon", "Queued explicit proxy %s for auto-start", name)
	default:
		debug.Warn("daemon", "Proxy event channel full, cannot queue proxy %s for auto-start", name)
	}

	return nil
}

// detectPortForScript is deprecated and no longer used.
// Port detection is now handled by URLTracker emitting URLDetected events.
func (d *Daemon) detectPortForScript(ctx context.Context, scriptName string, proxyConfig *config.ProxyConfig) (int, error) {
	return 0, fmt.Errorf("deprecated: use event-driven proxy creation instead")
}

// Removed old autostartProxy implementation that did synchronous port detection.
// Now using event-driven approach:
// 1. URLTracker detects URLs from script output
// 2. Emits URLDetected events
// 3. handleURLDetected creates proxies for matching configs

// Old implementation kept detectPortForScript stub for reference, but it's no longer called.
func (d *Daemon) _old_detectPortForScript(ctx context.Context, scriptName string, proxyConfig *config.ProxyConfig) (int, error) {
	detector := config.NewPortDetector()

	// Create a timeout context for port detection (30 seconds)
	detectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Poll for port detection
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-detectCtx.Done():
			return 0, fmt.Errorf("timeout waiting for port detection")

		case <-ticker.C:
			// Get process to check if running
			proc, err := d.hub.ProcessManager().Get(scriptName)
			if err != nil {
				continue // Process may not be registered yet
			}

			// Check if process is running
			if !proc.IsRunning() {
				continue
			}

			// Try to get output and detect port from it
			output, _ := proc.CombinedOutput()
			if port := detector.DetectFromOutput(string(output)); port > 0 {
				return port, nil
			}

			// Try PID-based detection
			pid := proc.PID()
			if pid > 0 {
				if ports := detector.DetectFromPID(detectCtx, pid); len(ports) > 0 {
					return ports[0], nil
				}
			}
		}
	}
}

// MaxLogSize is the maximum log file size before rotation (5MB).
const MaxLogSize = 5 * 1024 * 1024

// MaxLogBackups is the number of rotated log files to keep.
const MaxLogBackups = 3

// GetLogPath returns the path to the daemon log file.
// Uses XDG_STATE_HOME or ~/.local/state/agnt/daemon.log
func GetLogPath() string {
	// Check XDG_STATE_HOME first
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		// Fall back to ~/.local/state
		home, err := os.UserHomeDir()
		if err != nil {
			// Last resort: OS temp directory
			return filepath.Join(os.TempDir(), "agnt-daemon.log")
		}
		stateHome = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(stateHome, "agnt", "daemon.log")
}

// rotateLogIfNeeded rotates the log file if it exceeds MaxLogSize.
func rotateLogIfNeeded(logPath string) {
	info, err := os.Stat(logPath)
	if err != nil {
		return // File doesn't exist, nothing to rotate
	}

	if info.Size() < MaxLogSize {
		return // Below threshold
	}

	// Rotate: shift existing backups
	for i := MaxLogBackups - 1; i >= 1; i-- {
		oldPath := fmt.Sprintf("%s.%d", logPath, i)
		newPath := fmt.Sprintf("%s.%d", logPath, i+1)
		os.Rename(oldPath, newPath) // Ignore errors, files may not exist
	}

	// Move current log to .1
	os.Rename(logPath, logPath+".1")
}

// setupDebugLogging configures file-based logging for the daemon.
// This allows debugging even when the daemon runs detached (auto-started).
// Log files are rotated when they exceed MaxLogSize.
func setupDebugLogging() {
	logPath := GetLogPath()

	// Create log directory if needed
	logDir := filepath.Dir(logPath)
	if err := os.MkdirAll(logDir, 0755); err != nil {
		// Can't create dir, continue with default stderr logging
		return
	}

	// Rotate if needed before opening
	rotateLogIfNeeded(logPath)

	// Open log file (append mode, create if not exists)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		// Can't open log file, continue with default stderr logging
		return
	}

	// Configure log to write to file
	log.SetOutput(f)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	log.Printf("[INFO] Daemon log started at %s", logPath)
}
