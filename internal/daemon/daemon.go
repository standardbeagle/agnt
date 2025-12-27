package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/project"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/tunnel"
	"github.com/standardbeagle/agnt/internal/updater"
	"github.com/standardbeagle/go-cli-server/process"
)

// Version is the daemon version.
// Can be overridden at build time with: -ldflags "-X github.com/standardbeagle/agnt/internal/daemon.Version=x.y.z"
var Version = "0.7.12"

// BuildTime is the build timestamp (RFC3339 format).
// Set at build time with: -ldflags "-X github.com/standardbeagle/agnt/internal/daemon.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
var BuildTime = ""

// GitCommit is the git commit hash.
// Set at build time with: -ldflags "-X github.com/standardbeagle/agnt/internal/daemon.GitCommit=$(git rev-parse HEAD)"
var GitCommit = ""

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
type Daemon struct {
	config DaemonConfig

	// Core managers
	pm      *process.ProcessManager
	proxym  *proxy.ProxyManager
	tunnelm *tunnel.Manager

	// Session and scheduling
	sessionRegistry   *SessionRegistry
	scheduler         *Scheduler
	schedulerStateMgr *SchedulerStateManager

	// State persistence
	stateMgr   *StateManager
	pidTracker *process.FilePIDTracker

	// Update checker
	updateChecker *updater.UpdateChecker

	// Socket management
	sockMgr  *SocketManager
	listener net.Listener

	// Client tracking
	clients     sync.Map // clientID -> *Connection
	clientCount atomic.Int64
	nextID      atomic.Int64

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

	// Create session registry with 60-second heartbeat timeout
	sessionRegistry := NewSessionRegistry(60 * time.Second)

	// Create scheduler state manager for per-project task persistence
	schedulerStateMgr := NewSchedulerStateManager()

	// Create scheduler
	scheduler := NewScheduler(DefaultSchedulerConfig(), sessionRegistry, schedulerStateMgr)

	// Create PID tracker for orphan cleanup
	pidTracker := process.NewFilePIDTracker(process.FilePIDTrackerConfig{
		AppName: "devtool-mcp",
	})

	// Configure process manager with PID tracking
	procConfig := config.ProcessConfig
	procConfig.PIDTracker = pidTracker

	d := &Daemon{
		config:            config,
		pm:                process.NewProcessManager(procConfig),
		proxym:            proxy.NewProxyManager(),
		tunnelm:           tunnel.NewManager(),
		sessionRegistry:   sessionRegistry,
		scheduler:         scheduler,
		schedulerStateMgr: schedulerStateMgr,
		pidTracker:        pidTracker,
		sockMgr:           NewSocketManager(SocketConfig{Path: config.SocketPath}),
		ctx:               ctx,
		cancel:            cancel,
	}

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

// Start starts the daemon and begins accepting connections.
func (d *Daemon) Start() error {
	d.shutdownMu.Lock()
	if d.shutdown {
		d.shutdownMu.Unlock()
		return errors.New("daemon already shutdown")
	}
	d.shutdownMu.Unlock()

	// Setup file-based logging for debugging (captures output even when daemon runs detached)
	setupDebugLogging()

	// Create socket
	listener, err := d.sockMgr.Listen()
	if err != nil {
		return fmt.Errorf("failed to create socket: %w", err)
	}
	d.listener = listener
	d.started = time.Now()

	log.Printf("Daemon started, listening on %s", d.sockMgr.Path())

	// Clean up orphaned processes from previous crash
	d.cleanupOrphans()

	// Restore proxies from persisted state
	d.restoreProxies()

	// Start the scheduler for scheduled message delivery
	if err := d.scheduler.Start(d.ctx); err != nil {
		log.Printf("[Daemon] failed to start scheduler: %v", err)
	}

	// Start update checker if enabled
	if d.updateChecker != nil {
		d.updateChecker.Start()
	}

	// Start accept loop
	d.wg.Add(1)
	go d.acceptLoop()

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

	log.Printf("[Daemon] restoring %d proxies from state", len(proxies))

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

		log.Printf("[Daemon] restored proxy %s -> %s on port %d",
			pc.ID, pc.TargetURL, pc.Port)
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

	// Close listener to unblock accept (sockMgr.Close() will handle cleanup)
	// We close it here first to unblock the accept loop before waiting for it
	if d.listener != nil {
		d.listener.Close()
		d.listener = nil // Mark as closed so sockMgr.Close() won't try again
	}

	// Close all client connections
	d.clients.Range(func(key, value any) bool {
		conn := value.(*Connection)
		conn.Close()
		return true
	})

	// Shutdown managers
	var errs []error

	// Stop scheduler
	d.scheduler.Stop()

	// Stop update checker
	if d.updateChecker != nil {
		d.updateChecker.Stop()
	}

	if err := d.tunnelm.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("tunnel manager: %w", err))
	}

	if err := d.proxym.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("proxy manager: %w", err))
	}

	if err := d.pm.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("process manager: %w", err))
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

	// Cleanup socket
	if err := d.sockMgr.Close(); err != nil {
		errs = append(errs, fmt.Errorf("socket cleanup: %w", err))
	}

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
		SocketPath:  d.sockMgr.Path(),
		Uptime:      time.Since(d.started),
		ClientCount: d.clientCount.Load(),
		ProcessInfo: ProcessInfo{
			Active:       d.pm.ActiveCount(),
			TotalStarted: d.pm.TotalStarted(),
			TotalFailed:  d.pm.TotalFailed(),
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
	return d.pm
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
		if err := d.pm.StopAll(cleanupCtx); err != nil {
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
		stoppedIDs, err := d.pm.StopByProjectPath(ctx, projectPath)
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

// acceptLoop accepts new client connections.
func (d *Daemon) acceptLoop() {
	defer d.wg.Done()

	// Keep local reference to avoid race with Stop() setting d.listener = nil
	listener := d.listener

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-d.ctx.Done():
				return // Shutting down
			default:
				log.Printf("Accept error: %v", err)
				continue
			}
		}

		// Check max clients
		if d.config.MaxClients > 0 && d.clientCount.Load() >= int64(d.config.MaxClients) {
			log.Printf("Max clients reached, rejecting connection")
			conn.Close()
			continue
		}

		// Create connection handler
		clientID := d.nextID.Add(1)
		clientConn := newConnection(clientID, conn, d)

		// Register client
		d.clients.Store(clientID, clientConn)
		d.clientCount.Add(1)

		// Handle in goroutine
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			defer func() {
				d.clients.Delete(clientID)
				d.clientCount.Add(-1)

				// If this connection registered a session, clean up its resources
				// This ensures processes/proxies started by this session are stopped
				// when the session ends, without affecting other sessions.
				if clientConn.sessionCode != "" {
					d.CleanupSessionResources(clientConn.sessionCode)
				}
			}()

			clientConn.Handle(d.ctx)
		}()
	}
}

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
		log.Printf("[DEBUG] RunAutostart: empty projectPath, skipping")
		return result
	}

	log.Printf("[DEBUG] RunAutostart: loading config from %s", projectPath)

	// Load .agnt.kdl config
	agntConfig, err := config.LoadAgntConfig(projectPath)
	if err != nil {
		log.Printf("[DEBUG] RunAutostart: config load error: %v", err)
		// No config or error loading - not an error, just nothing to autostart
		return result
	}

	if agntConfig == nil {
		log.Printf("[DEBUG] RunAutostart: config is nil")
		return result
	}

	// Start scripts
	autostartScripts := agntConfig.GetAutostartScripts()
	log.Printf("[DEBUG] RunAutostart: found %d autostart scripts: %v", len(autostartScripts), mapKeys(autostartScripts))
	for name, script := range autostartScripts {
		log.Printf("[DEBUG] RunAutostart: starting script %q", name)
		if err := d.autostartScript(ctx, name, script, projectPath); err != nil {
			log.Printf("[DEBUG] RunAutostart: script %q error: %v", name, err)
			result.Errors = append(result.Errors, fmt.Sprintf("script %s: %v", name, err))
		} else {
			log.Printf("[DEBUG] RunAutostart: script %q started successfully", name)
			result.Scripts = append(result.Scripts, name)
		}
	}

	// Start proxies
	autostartProxies := agntConfig.GetAutostartProxies()
	log.Printf("[DEBUG] RunAutostart: found %d autostart proxies: %v", len(autostartProxies), mapKeysProxy(autostartProxies))
	for name, proxyConfig := range autostartProxies {
		log.Printf("[DEBUG] RunAutostart: starting proxy %q (target: %s)", name, proxyConfig.Target)
		if err := d.autostartProxy(ctx, name, proxyConfig, projectPath); err != nil {
			log.Printf("[DEBUG] RunAutostart: proxy %q error: %v", name, err)
			result.Errors = append(result.Errors, fmt.Sprintf("proxy %s: %v", name, err))
		} else {
			log.Printf("[DEBUG] RunAutostart: proxy %q started successfully", name)
			result.Proxies = append(result.Proxies, name)
		}
	}

	return result
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

// autostartScript starts a single script from config.
func (d *Daemon) autostartScript(ctx context.Context, name string, script *config.ScriptConfig, projectPath string) error {
	log.Printf("[DEBUG] autostartScript: name=%q projectPath=%q", name, projectPath)

	// Check if already running
	if existing, err := d.pm.Get(name); err == nil {
		log.Printf("[DEBUG] autostartScript: script %q already running (state=%d)", name, existing.State())
		return nil // Already running
	}

	var command string
	var args []string

	if script.Command != "" {
		// Explicit command specified
		command = script.Command
		args = script.Args
		log.Printf("[DEBUG] autostartScript: using explicit command %q %v", command, args)
	} else {
		// No command - run as package.json script via detected package manager
		log.Printf("[DEBUG] autostartScript: detecting project type for path %q", projectPath)
		proj, err := project.Detect(projectPath)
		if err != nil {
			log.Printf("[DEBUG] autostartScript: project detection failed: %v", err)
			return fmt.Errorf("project detection failed: %v", err)
		}

		log.Printf("[DEBUG] autostartScript: detected project type=%q package_manager=%q", proj.Type, proj.PackageManager)

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
			return fmt.Errorf("cannot run script %q: unknown project type and no command specified", name)
		}
		log.Printf("[DEBUG] autostartScript: will run %q %v", command, args)
	}

	// Create and start process using StartOrReuse for idempotent behavior
	log.Printf("[DEBUG] autostartScript: starting process id=%q path=%q cmd=%q args=%v", name, projectPath, command, args)
	result, err := d.pm.StartOrReuse(ctx, process.ProcessConfig{
		ID:          name,
		ProjectPath: projectPath,
		Command:     command,
		Args:        args,
		Env:         os.Environ(),
	})
	if err != nil {
		log.Printf("[DEBUG] autostartScript: StartOrReuse failed: %v", err)
		return err
	}

	log.Printf("[DEBUG] autostartScript: started successfully (reused=%v)", result.Reused)
	return nil
}

// autostartProxy starts a single proxy from config.
func (d *Daemon) autostartProxy(ctx context.Context, name string, proxyConfig *config.ProxyConfig, projectPath string) error {
	// Check if already running
	if _, err := d.proxym.Get(name); err == nil {
		return nil // Already running
	}

	// Determine target URL
	targetURL := proxyConfig.Target

	// If linked to a script, detect the port
	if proxyConfig.Script != "" && targetURL == "" {
		detectedPort, err := d.detectPortForScript(ctx, proxyConfig.Script, proxyConfig)
		if err != nil {
			// Use fallback port if available
			if proxyConfig.FallbackPort > 0 {
				detectedPort = proxyConfig.FallbackPort
			} else {
				return fmt.Errorf("port detection failed and no fallback: %w", err)
			}
		}

		host := proxyConfig.Host
		if host == "" {
			host = "localhost"
		}
		targetURL = fmt.Sprintf("http://%s:%d", host, detectedPort)
	}

	if targetURL == "" {
		return nil // No target URL and no script for port detection
	}

	// Create proxy server using the same config format as handler.go
	proxyServerConfig := proxy.ProxyConfig{
		ID:          name,
		TargetURL:   targetURL,
		ListenPort:  -1,   // Auto-assign port
		MaxLogSize:  1000, // Default
		AutoRestart: true,
		Path:        projectPath,
	}

	server, err := d.proxym.Create(ctx, proxyServerConfig)
	if err != nil {
		if err == proxy.ErrProxyExists {
			return nil // Already exists, not an error
		}
		return err
	}

	// Set overlay endpoint if configured
	overlayEndpoint := d.OverlayEndpoint()
	if overlayEndpoint != "" {
		server.SetOverlayEndpoint(overlayEndpoint)
	}

	return nil
}

// detectPortForScript waits for a script to start and detects its listening port.
func (d *Daemon) detectPortForScript(ctx context.Context, scriptName string, proxyConfig *config.ProxyConfig) (int, error) {
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
			proc, err := d.pm.Get(scriptName)
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
	log.Printf("=== Daemon starting (pid=%d) ===", os.Getpid())
}
