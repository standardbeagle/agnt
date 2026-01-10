package daemon

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/agnt/internal/automation"
	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/project"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/store"
	"github.com/standardbeagle/agnt/internal/tunnel"
	"github.com/standardbeagle/agnt/internal/updater"
	"github.com/standardbeagle/go-cli-server/hub"
	"github.com/standardbeagle/go-cli-server/process"
)

// Version is the daemon version.
// Can be overridden at build time with: -ldflags "-X github.com/standardbeagle/agnt/internal/daemon.Version=x.y.z"
var Version = "0.8.0"

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
	proxym    *proxy.ProxyManager
	tunnelm   *tunnel.Manager
	storem    *store.StoreManager
	automator *automation.Processor

	// Session and scheduling (agnt-specific extensions)
	sessionRegistry   *SessionRegistry
	scheduler         *Scheduler
	schedulerStateMgr *SchedulerStateManager

	// State persistence
	stateMgr   *StateManager
	pidTracker *process.FilePIDTracker

	// URL tracking for processes
	urlTracker *URLTracker

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
		storem:            store.NewStoreManager(),
		sessionRegistry:   sessionRegistry,
		scheduler:         scheduler,
		schedulerStateMgr: schedulerStateMgr,
		pidTracker:        pidTracker,
		proxyEvents:       make(chan ProxyEvent, 10), // Buffer 10 events
		scriptProxies:     make(map[string][]string),
		ctx:               ctx,
		cancel:            cancel,
	}

	// Create URLTracker with callbacks to emit proxy events
	// Access ProcessManager through Hub
	urlTracker := NewURLTracker(h.ProcessManager(), DefaultURLTrackerConfig())
	urlTracker.onURLDetected = func(processID, url string) {
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
			log.Printf("[WARN] Proxy event channel full, dropping URL detection event for %s: %s", processID, url)
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
			log.Printf("[WARN] Proxy event channel full, dropping process stopped event for %s", processID)
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
		log.Printf("[Daemon] failed to start scheduler: %v", err)
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
			log.Printf("[Daemon] failed to restore proxy %s: %v", pc.ID, err)
			// Remove from state if it can't be restored
			d.stateMgr.RemoveProxy(pc.ID)
			continue
		}

		// Configure overlay endpoint
		if overlayEndpoint != "" {
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
		log.Printf("[Daemon] failed to cleanup orphans: %v", err)
		return
	}

	if killedCount > 0 {
		log.Printf("[Daemon] cleaned up %d orphaned process(es) from previous crash", killedCount)
	}

	// Set current daemon PID for future crash detection
	if err := d.pidTracker.SetDaemonPID(os.Getpid()); err != nil {
		log.Printf("[Daemon] failed to set daemon PID: %v", err)
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

	log.Println("Daemon stopping...")

	// Signal all goroutines to stop
	d.cancel()

	// Stop Hub (handles listener, clients, connections)
	if err := d.hub.Stop(ctx); err != nil {
		log.Printf("[Daemon] error stopping hub: %v", err)
	}

	// Shutdown agnt-specific managers
	var errs []error

	// Stop scheduler
	d.scheduler.Stop()

	// Stop update checker
	if d.updateChecker != nil {
		d.updateChecker.Stop()
	}

	if err := d.tunnelm.Shutdown(ctx); err != nil {
		debug.Error("daemon", "tunnel manager shutdown error: %v", err)
		errs = append(errs, fmt.Errorf("tunnel manager: %w", err))
	}

	if err := d.proxym.Shutdown(ctx); err != nil {
		debug.Error("daemon", "proxy manager shutdown error: %v", err)
		errs = append(errs, fmt.Errorf("proxy manager: %w", err))
	}

	// Clear PID tracking (clean shutdown)
	if d.pidTracker != nil {
		if err := d.pidTracker.Clear(); err != nil {
			log.Printf("[Daemon] failed to clear PID tracking: %v", err)
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

	log.Println("Daemon stopped")

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
		log.Printf("[DEBUG] LoadURLMatchersForProcess: process %s not found", processID)
		return
	}

	projectPath := proc.ProjectPath
	if projectPath == "" {
		log.Printf("[DEBUG] LoadURLMatchersForProcess: process %s has no project path", processID)
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
		log.Printf("[DEBUG] LoadURLMatchersForProcess: failed to load config from %s: %v", projectPath, err)
		return // No config or error - skip URL matchers
	}

	// Find script config
	script, ok := agntConfig.Scripts[scriptName]
	if !ok || script == nil {
		log.Printf("[DEBUG] LoadURLMatchersForProcess: script %s not found in config", scriptName)
		return // Script not found in config
	}

	// Set URL matchers if specified
	if len(script.URLMatchers) > 0 {
		d.urlTracker.SetURLMatchers(processID, script.URLMatchers)
		log.Printf("[DEBUG] Set URL matchers for %s: %v", processID, script.URLMatchers)
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
			log.Printf("[Daemon] error stopping tunnels: %v", err)
		}
	}()

	// Stop all proxies and update state
	wg.Add(1)
	go func() {
		defer wg.Done()
		stoppedIDs, err := d.proxym.StopAll(cleanupCtx)
		if err != nil {
			log.Printf("[Daemon] error stopping proxies: %v", err)
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
			log.Printf("[Daemon] error stopping processes: %v", err)
		}
	}()

	wg.Wait()

	// Clear overlay endpoint since no clients are connected
	d.SetOverlayEndpoint("")

	log.Println("[Daemon] all resources stopped (last client disconnected)")
}

// CleanupSessionResources stops all processes and proxies for a specific session.
// This is called when a connection that registered a session disconnects.
func (d *Daemon) CleanupSessionResources(sessionCode string) {
	// Get session to find project path
	session, ok := d.sessionRegistry.Get(sessionCode)
	if !ok {
		log.Printf("[Daemon] session %s not found for cleanup", sessionCode)
		return
	}

	projectPath := session.ProjectPath
	if projectPath == "" {
		log.Printf("[Daemon] session %s has no project path, skipping resource cleanup", sessionCode)
		// Still unregister the session
		d.sessionRegistry.Unregister(sessionCode)
		return
	}

	log.Printf("[Daemon] cleaning up resources for session %s (project: %s)", sessionCode, projectPath)

	// Use a reasonable timeout for cleanup
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	// Stop proxies for this project
	wg.Add(1)
	go func() {
		defer wg.Done()
		stoppedIDs, err := d.proxym.StopByProjectPath(ctx, projectPath)
		if err != nil {
			log.Printf("[Daemon] error stopping proxies for project %s: %v", projectPath, err)
		}
		if len(stoppedIDs) > 0 {
			log.Printf("[Daemon] stopped proxies: %v", stoppedIDs)
			// Remove from persisted state
			if d.stateMgr != nil {
				for _, id := range stoppedIDs {
					d.stateMgr.RemoveProxy(id)
				}
			}
		}
	}()

	// Stop processes for this project
	wg.Add(1)
	go func() {
		defer wg.Done()
		stoppedIDs, err := d.hub.ProcessManager().StopByProjectPath(ctx, projectPath)
		if err != nil {
			log.Printf("[Daemon] error stopping processes for project %s: %v", projectPath, err)
		}
		if len(stoppedIDs) > 0 {
			log.Printf("[Daemon] stopped processes: %v", stoppedIDs)
		}
	}()

	wg.Wait()

	// Unregister the session
	if err := d.sessionRegistry.Unregister(sessionCode); err != nil {
		log.Printf("[Daemon] error unregistering session %s: %v", sessionCode, err)
	}

	log.Printf("[Daemon] session %s cleanup complete", sessionCode)
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
// Returns the list of started scripts/proxies and any errors encountered.
func (d *Daemon) RunAutostart(ctx context.Context, projectPath string) *AutostartResult {
	result := &AutostartResult{}

	if projectPath == "" {
		log.Printf("[DEBUG] RunAutostart: projectPath is empty")
		return result
	}

	log.Printf("[DEBUG] RunAutostart: loading config from %s", projectPath)

	// Load .agnt.kdl config
	agntConfig, err := config.LoadAgntConfig(projectPath)
	if err != nil {
		// No config or error loading - not an error, just nothing to autostart
		log.Printf("[DEBUG] RunAutostart: config load error: %v", err)
		return result
	}

	if agntConfig == nil {
		log.Printf("[DEBUG] RunAutostart: config is nil")
		return result
	}

	log.Printf("[DEBUG] RunAutostart: config loaded, scripts=%d proxies=%d",
		len(agntConfig.Scripts), len(agntConfig.Proxies))

	// Start scripts (pass proxy configs for port detection)
	autostartScripts := agntConfig.GetAutostartScripts()
	proxyConfigs := agntConfig.Proxies // All proxies, not just autostart ones
	log.Printf("[DEBUG] RunAutostart: found %d autostart scripts: %v", len(autostartScripts), mapKeys(autostartScripts))
	for name, script := range autostartScripts {
		log.Printf("[DEBUG] RunAutostart: starting script %s", name)
		if err := d.autostartScript(ctx, name, script, projectPath, proxyConfigs); err != nil {
			log.Printf("[DEBUG] RunAutostart: script %s failed: %v", name, err)
			result.Errors = append(result.Errors, fmt.Sprintf("script %s: %v", name, err))
		} else {
			log.Printf("[DEBUG] RunAutostart: script %s started successfully", name)
			result.Scripts = append(result.Scripts, name)
		}
	}

	// Start proxies
	autostartProxies := agntConfig.GetAutostartProxies()
	log.Printf("[DEBUG] RunAutostart: found %d autostart proxies: %v", len(autostartProxies), mapKeysProxy(autostartProxies))
	for name, proxyConfig := range autostartProxies {
		log.Printf("[DEBUG] RunAutostart: starting proxy %s (script=%s port=%d)", name, proxyConfig.Script, proxyConfig.Port)
		if err := d.autostartProxy(ctx, name, proxyConfig, projectPath); err != nil {
			log.Printf("[DEBUG] RunAutostart: proxy %s failed: %v", name, err)
			result.Errors = append(result.Errors, fmt.Sprintf("proxy %s: %v", name, err))
		} else {
			log.Printf("[DEBUG] RunAutostart: proxy %s started successfully", name)
			result.Proxies = append(result.Proxies, name)
		}
	}

	return result
}

// makeProcessID creates a unique process ID scoped to a project path.
// This prevents process ID collisions when multiple sessions from different
// projects use the same script name (e.g., "dev").
// Format: <basename>-<hash>:<name> (e.g., "my-project-a1b2:<name>")
// The hash is derived from the full path to ensure uniqueness even when
// different directories have the same basename.
func makeProcessID(projectPath, name string) string {
	if projectPath == "" {
		return name
	}
	// Use basename + short hash of full path for uniqueness
	basename := filepath.Base(projectPath)
	hash := shortPathHash(projectPath)
	return fmt.Sprintf("%s-%s:%s", basename, hash, name)
}

// shortPathHash returns a short (4 char) hex hash of a path for ID uniqueness.
func shortPathHash(path string) string {
	h := fnv.New32a()
	h.Write([]byte(path))
	return fmt.Sprintf("%04x", h.Sum32()&0xFFFF)
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

// autostartScript starts a single script from config with automatic EADDRINUSE recovery.
func (d *Daemon) autostartScript(ctx context.Context, name string, script *config.ScriptConfig, projectPath string, proxyConfigs map[string]*config.ProxyConfig) error {

	// Make process ID unique per project to avoid collisions between sessions
	processID := makeProcessID(projectPath, name)

	// Check if already running
	if _, err := d.hub.ProcessManager().Get(processID); err == nil {
		return nil // Already running
	}

	var command string
	var args []string

	if script.Run != "" {
		// Shell command string - execute via sh -c
		command = "sh"
		args = []string{"-c", script.Run}
	} else if script.Command != "" {
		// Explicit command specified
		command = script.Command
		args = script.Args
	} else {
		// No command - run as package.json script via detected package manager
		proj, err := project.Detect(projectPath)
		if err != nil {
			debug.Error("daemon", "project detection failed for %s: %v", projectPath, err)
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
			return fmt.Errorf("cannot run script %q: unknown project type and no command specified", name)
		}
	}

	// Determine expected port for pre-flight cleanup and EADDRINUSE recovery
	expectedPort := d.getExpectedPortForScript(name, script, proxyConfigs, projectPath, command, args)

	// Start with automatic EADDRINUSE recovery
	_, startupErr := d.startScriptWithRetry(ctx, processID, projectPath, command, args, expectedPort)
	if startupErr != nil {
		return startupErr
	}

	// Load and set URL matchers for this process
	d.LoadURLMatchersForProcess(processID)

	return nil
}

// autostartProxy starts a single proxy from config.
// Only handles fully-specified proxies (explicit URL or port).
// Script-linked proxies are created automatically by the event system when URLs are detected.
func (d *Daemon) autostartProxy(ctx context.Context, name string, proxyConfig *config.ProxyConfig, projectPath string) error {
	// Skip script-linked proxies - they're handled by URLDetected events
	if proxyConfig.Script != "" {
		log.Printf("[DEBUG] Proxy %s is script-linked, skipping auto-start (will be created when URLs detected)", name)
		return nil
	}

	// Only auto-start if explicitly requested
	if !proxyConfig.Autostart {
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
		log.Printf("[DEBUG] Proxy %s has no explicit target URL, skipping", name)
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
		log.Printf("[DEBUG] Queued explicit proxy %s for auto-start", name)
	default:
		log.Printf("[WARN] Proxy event channel full, cannot queue proxy %s for auto-start", name)
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

// DebugLogPath is the path to the daemon debug log file.
const DebugLogPath = "/tmp/agnt-daemon.log"

// setupDebugLogging configures file-based logging for the daemon.
// This allows debugging even when the daemon runs detached (auto-started).
func setupDebugLogging() {
	// Open log file (append mode, create if not exists)
	f, err := os.OpenFile(DebugLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		// Can't open log file, continue with default stderr logging
		return
	}

	// Configure log to write to file
	log.SetOutput(f)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
}
