// Package daemon implements the agnt background service.
//
// # go-cli-server Boundary
//
// The daemon is built on top of github.com/standardbeagle/go-cli-server, which
// provides the core IPC hub, process management, and script registry. The boundary
// between the two packages is:
//
// go-cli-server owns:
//   - hub.Hub — socket server, client lifecycle, session management
//   - hub.Connection — per-client connection handler
//   - process.ProcessManager — process creation, monitoring, termination
//   - process.ManagedProcess — individual process state machine
//   - script.Registry — per-script state that persists across process restarts
//   - protocol.Command — parsed IPC command (Verb, SubVerb, Args)
//   - protocol.StructuredError — typed error responses
//   - client.Conn — client-side IPC connection
//   - socket.Manager — Unix socket / named pipe lifecycle
//
// agnt/daemon owns:
//   - Daemon — composes hub.Hub with agnt-specific managers (proxy, tunnel, browser, etc.)
//   - proxy.ProxyManager — reverse proxy lifecycle
//   - tunnel.Manager — cloudflare/ngrok tunnel management
//   - browser.Manager — browser instance management
//   - SessionRegistry — agnt session tracking (distinct from hub sessions)
//   - URLTracker — URL detection from process output
//   - All hub_*.go handlers — agnt-specific command implementations
//   - Conn / RequestBuilder — shared client connection wrapper (see conn.go)
//
// Import convention within this package:
//   - hubpkg  = go-cli-server/hub
//   - goprocess = go-cli-server/process
//   - hubproto  = go-cli-server/protocol
//   - goclient  = go-cli-server/client
//   - Direct imports for go-cli-server/script (no alias needed)
//
// No go-cli-server types are re-exported from this package. A small number of
// error sentinel values are re-exported for internal convenience (see
// socket_compat.go, conn.go). Consumers that need go-cli-server types should
// import go-cli-server directly.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/agnt/internal/automation"
	"github.com/standardbeagle/agnt/internal/browser"
	"github.com/standardbeagle/agnt/internal/chromedp"
	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/overlay"
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
var Version = "0.13.20"

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
	// FallbackPortCheck indicates a delayed check for script-linked proxies that
	// weren't created by URL detection — creates proxy using fallback-port if needed
	FallbackPortCheck
)

// ProxyEvent represents an event that triggers proxy creation or cleanup.
type ProxyEvent struct {
	Type      ProxyEventType
	ScriptID  string // Process/script ID that triggered the event
	URL       string // Detected URL (for URLDetected events)
	ProxyID   string // Specific proxy ID (for ExplicitStart events)
	ProxyName string // Config proxy name (for FallbackPortCheck events)
	Config    *config.ProxyConfig
	Path      string // Project path
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

	// CleanupGracePeriod is how long to wait before cleaning up session
	// resources after a connection drops. Allows ResilientClient reconnects
	// to cancel the cleanup. Set to 0 for immediate cleanup (tests).
	// Default: 5s
	CleanupGracePeriod time.Duration

	// StartupMonitorTimeout is how long monitorStartupFailure watches a newly
	// started process for early exit before declaring it healthy. Set to a
	// short value in tests to avoid the 3s × N-scripts wall-clock cost.
	// Default: 3s
	StartupMonitorTimeout time.Duration

	// OrphanScanEnabled gates startupOrphanPGIDScan. Default zero-value
	// (false) is the test-safe default: tests must NEVER walk host /proc
	// or issue real kill(2) syscalls against pgids owned by other host
	// processes under the same uid.
	//
	// Production sets this to true explicitly in cmd/agnt/daemon.go.
	// One production site vs 40+ test constructors made positive naming +
	// explicit-production the better trade than negative naming +
	// explicit-tests.
	//
	// Replaces the legacy AGNT_DISABLE_ORPHAN_SCAN env var fence; see iter 15
	// (task 6btkGG5QUGTL) for the migration. Never expose this field to end
	// users — it is an internal test-safety knob, not a product feature.
	OrphanScanEnabled bool
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
		CleanupGracePeriod:     5 * time.Second,
		StartupMonitorTimeout:  3 * time.Second,
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
	alertScanner      *overlay.AlertScanner // Scans daemon-managed process output for errors
	processExitInfo   *processExitInfoStore // In-memory death records (proc status + get_errors)
	startupErrorStore *StartupLogStore      // Ring buffer for startup events
	eventHub          *EventHub             // Routes alerts to overlay/MCP/stream sinks
	incidentBus       *incident.MPSCBus     // Incident pipeline event bus (L8+)
	hookRing          *hookRingBuffer       // Claude Code hook event ring buffer (phase 1 scope)

	// swallowDetector runs the chaos "swallowed error" heuristic. It is always
	// allocated (cheap) but only fed when a fault entry is stamped
	// ChaosSwallowWatch — i.e. the proxy's runtime swallow-detect panel toggle
	// was on. The sweeper goroutine starts lazily on the first watched fault,
	// so projects that never enable it pay nothing.
	swallowDetector       *incident.SwallowDetector
	swallowSweeperStarted atomic.Bool
	scriptRegistry        *script.Registry // Per-script state that persists across process restarts
	scriptConfigs         sync.Map         // processID -> *config.ScriptConfig (agnt-specific config)

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

	// readinessWatchdogGrace bounds how long a proxy may sit gated on its
	// `wait-for` dependencies before the daemon surfaces a warning (Silent
	// Failure Prohibition). Set once at construction; tests override it on
	// their own instance to keep the watchdog deterministic. Zero falls back
	// to defaultReadinessWatchdogGrace.
	readinessWatchdogGrace time.Duration

	// pendingProcs tracks PROC RUN / PROC RUN-GROUP processes that are
	// waiting on declared `depends_on` dependencies before launching. The
	// tracker is the single surface that PROC STATUS / PROC LIST consult
	// to populate the `waiting_for` field while a process is gated. See
	// internal/daemon/proc_pending.go for the lifecycle contract.
	pendingProcs *PendingProcessTracker

	// Proxy event system
	proxyEvents chan ProxyEvent
	// fallbackCheckDelay is how long scheduleFallbackPortChecks waits after
	// autostart before emitting a FallbackPortCheck for a script-linked proxy
	// whose URL detection never fired. Production is always 30s (set in New);
	// this is an internal test seam so tests can exercise the scheduling path
	// without a 30s wait. Never expose in .agnt.kdl.
	fallbackCheckDelay time.Duration
	scriptProxies      map[string][]string // scriptID -> []proxyID
	proxyToScript      map[string]string   // proxyID -> scriptID (reverse index for suppression lookup)
	scriptProxyMu      sync.RWMutex

	// proxyEntries is the admin-surface shim for explicit proxies — proxies
	// started without a linked script (MCP tool path or standalone-proxy
	// autostart). The vendored script.Registry only tracks process-kind
	// entries, so without this shim explicit proxies never appear in
	// SCRIPT LIST and therefore never get a status-bar indicator. Entries
	// are keyed by (projectPath, proxyName) and merged into
	// hubHandleScriptList's response with kind="proxy". Cleanup of these
	// entries is T5 scope.
	proxyEntries *proxyEntryStore

	// healthTracker observes process state edges to drive proxy error
	// stream suppression during rebuild/restart windows. See
	// internal/daemon/health_tracker.go for the suppression contract.
	healthTracker *HealthTracker

	// outageClassifier wraps healthTracker with rebuild-vs-crash
	// classification. The proxy broadcast gate consults its
	// SuppressionMode() on every log entry. See
	// internal/daemon/outage_classifier.go for the rules.
	outageClassifier *OutageClassifier

	// holdBuffer holds transport / browser-JS errors during synthetic
	// transport outage and replays them on window expiry, dropping
	// transport-cascade noise that arrived during a successful recovery.
	// See internal/daemon/hold_buffer.go for semantics.
	//
	// atomic.Pointer because ApplyAlertsConfig swaps the buffer (on every
	// session connect, from an async autostart worker) while hub-handler
	// goroutines read it on every proxy log entry and Stop reads it on
	// shutdown — an unsynchronised field swap was a data race. All
	// HoldBuffer methods are nil/closed-safe, so Load → call is lock-free
	// and correct even against a concurrent Swap+Stop.
	holdBuffer atomic.Pointer[HoldBuffer]

	// Update checker
	updateChecker *updater.UpdateChecker

	// Duplicate process scanner
	dupScanner *DuplicateScanner

	// Overlay endpoint (can be set dynamically)
	overlayEndpoint atomic.Pointer[string]

	// Pending two-phase autostarts (prompt mode)
	pendingAutostarts sync.Map // projectPath → *pendingAutostart

	// Non-interactive config override: when set, RunAutostartAsync uses this
	// config instead of loading from disk. Set by RunAutostartNonInteractive
	// to override port-conflict "prompt" with "skip" in channel mode.
	nonInteractiveConfigOverride sync.Map // projectPath → *config.AgntConfig

	// Pending session cleanups — deferred to allow reconnection to cancel them.
	// Key: session code, Value: *time.Timer (Stop cancels, fire runs cleanup).
	pendingCleanups sync.Map

	// autostartManager coordinates at-most-one autostart run per project path.
	// Session registration delegates autostart to GetOrCreate so multiple
	// concurrent registrations for the same project share a single run.
	autostartManager *AutostartManager

	// Lifecycle
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	started    time.Time
	shutdownMu sync.Mutex
	shutdown   bool
}

// isShuttingDown reports whether Stop has begun. Used by ApplyAlertsConfig to
// avoid leaking a hold-buffer goroutine when it races daemon shutdown.
func (d *Daemon) isShuttingDown() bool {
	d.shutdownMu.Lock()
	defer d.shutdownMu.Unlock()
	return d.shutdown
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

	incBus := incident.NewMPSCBus(nil)

	d := &Daemon{
		config:             config,
		hub:                h,
		proxym:             proxy.NewProxyManager(),
		tunnelm:            tunnel.NewManager(),
		browserm:           browser.NewManager(),
		sessionm:           chromedp.NewSessionManager(),
		storem:             store.NewStoreManager(),
		alertStore:         NewProcessAlertStore(500),
		processExitInfo:    newProcessExitInfoStore(defaultExitInfoRetention),
		startupErrorStore:  NewStartupLogStore(100),
		eventHub:           NewEventHub(),
		incidentBus:        incBus,
		swallowDetector:    incident.NewSwallowDetector(2 * time.Second),
		hookRing:           newHookRingBuffer(hookRingCapacity),
		scriptRegistry:     script.NewRegistry(),
		sessionRegistry:    sessionRegistry,
		scheduler:          scheduler,
		schedulerStateMgr:  schedulerStateMgr,
		pidTracker:         pidTracker,
		proxyEvents:        make(chan ProxyEvent, 10), // Buffer 10 events
		fallbackCheckDelay: 30 * time.Second,
		scriptProxies:      make(map[string][]string),
		proxyToScript:      make(map[string]string),
		proxyEntries:       newProxyEntryStore(),
		ctx:                ctx,
		cancel:             cancel,
	}

	// Wire proxy manager into EventHub so crash alerts are broadcast to
	// connected browser overlays as toast notifications.
	d.eventHub.SetProxyBroadcaster(newProxyManagerBroadcaster(d.proxym))

	// HealthTracker is initialised after the struct so it can capture `d`
	// in its lookup closures. Its emitDiagnostic routes back through the
	// EventHub directly so suppression markers always bypass the gate.
	d.healthTracker = NewHealthTracker(
		func(processID string) (*process.ManagedProcess, error) {
			return d.hub.ProcessManager().Get(processID)
		},
		func(entry proxy.LogEntry, proxyID string) {
			if d.eventHub == nil {
				return
			}
			d.eventHub.BroadcastLogEntry(entry, proxyID)
		},
	)

	// HoldBuffer suppresses transport-cascade noise during synthetic
	// transport outages. The emit callback fans replays through both the
	// EventHub (legacy stream sinks) and the incident bus (get_incidents).
	// Construction uses default config until the per-project AgntConfig is
	// loaded; ApplyAlertsConfig replaces it on session connect.
	d.holdBuffer.Store(NewHoldBuffer(nil, d.fireHoldEmit))
	d.healthTracker.SetTransportConfig(DefaultTransportConfig)

	// Wire transport-recovery callback so HealthTracker exits to the buffer
	// flush path on every recovery signal.
	d.healthTracker.SetOnTransportRecovery(func(proxyID string) {
		if hb := d.holdBuffer.Load(); hb != nil {
			hb.OnRecovery(proxyID)
		}
	})

	// OutageClassifier extends HealthTracker with rebuild/crash
	// classification. It needs the same process lookup, an emitter for
	// long-rebuild heartbeats and the expired-rebuild warning, and a
	// reverse lookup from processID → proxyID so heartbeat diagnostics
	// can be addressed to the right proxy.
	d.outageClassifier = NewOutageClassifier(
		d.healthTracker,
		func(processID string) (*process.ManagedProcess, error) {
			return d.hub.ProcessManager().Get(processID)
		},
		func(entry proxy.LogEntry, proxyID string) {
			if d.eventHub == nil {
				return
			}
			d.eventHub.BroadcastLogEntry(entry, proxyID)
		},
		func(processID string) string {
			proxies := d.getProxiesForScript(processID)
			if len(proxies) == 0 {
				return ""
			}
			return proxies[0]
		},
	)

	// alertScanner scans daemon-managed process output (proc run) for error
	// patterns and routes matches into alertStore + eventHub — the same
	// surfaces get_errors and agnt monitor query. OnAlert fires after the
	// scanner's batch window (default 3s) to avoid per-line churn.
	d.alertScanner = overlay.NewAlertScanner(overlay.AlertScannerConfig{
		OnAlert: func(batch *overlay.AlertBatch) {
			ts := time.Now()
			for i := range batch.Matches {
				d.ingestProcessAlert(batch.Matches[i], ts)
			}
			formatted := batch.Format()
			if formatted != "" {
				d.eventHub.Deliver(string(batch.MaxSeverity()), formatted)
			}
		},
	})

	// Autostart manager fans every progress event out to the alert hub so
	// that monitor/MCP watch subscribers see real-time autostart phases.
	// The closure captures `d` lazily; eventHub is set on the line above
	// via the struct literal so it is safe to dereference inside the
	// callback.
	d.autostartManager = NewAutostartManagerWithBroadcast(func(projectPath string, ev AutostartProgress) {
		d.broadcastAutostartProgress(projectPath, ev)
	})

	// Wire script registry into ProcessManager for automatic lifecycle updates
	h.ProcessManager().SetScriptRegistry(d.scriptRegistry)

	// Initialize process auto-restarter
	d.autoRestarter = NewProcessAutoRestarter(d)

	// Initialize ready signaler for dependency-ordered autostart
	d.readySignaler = NewReadySignaler()
	d.readinessWatchdogGrace = defaultReadinessWatchdogGrace

	// Initialize pending-process tracker for PROC RUN dep-gated launches.
	d.pendingProcs = NewPendingProcessTracker()

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
			debug.Warn("daemon", "Proxy event channel full, dropping URL detection event for %s: %s", processID, url)
			if d.startupErrorStore != nil {
				d.startupErrorStore.Add(&StartupLogEntry{
					ProcessID: processID, ScriptName: stripProcessPrefix(processID),
					Level: "warning", EventType: "proxy_event_dropped",
					Message:   fmt.Sprintf("proxy event channel full: URL %s detected but event dropped — proxy may not be created", url),
					Timestamp: time.Now(),
				})
			}
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
			debug.Warn("daemon", "Proxy event channel full, dropping process stopped event for %s", processID)
			if d.startupErrorStore != nil {
				d.startupErrorStore.Add(&StartupLogEntry{
					ProcessID: processID, ScriptName: stripProcessPrefix(processID),
					Level: "warning", EventType: "proxy_event_dropped",
					Message:   fmt.Sprintf("proxy event channel full: script stopped event dropped for %s — proxies may not be cleaned up", processID),
					Timestamp: time.Now(),
				})
			}
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

	// Initialize duplicate process scanner
	d.dupScanner = NewDuplicateScanner(d)

	return d
}

// registerCommands registers agnt-specific commands with the Hub.
// This delegates to registerAgntCommands() in hub_handlers.go.
func (d *Daemon) registerCommands() {
	d.registerAgntCommands()
}

// bootstrap performs the minimum wiring shared by Start() and NewForTest():
// registers commands, wires hub session cleanup, starts the hub socket,
// and kicks off the scheduler, URL tracker, proxy event loop, and hook
// drain goroutine. It deliberately excludes orphan scans, port cleanup,
// proxy restoration, and the update checker — those belong to the
// production Start() path only.
//
// The split exists so tests can spin up a live daemon in <100ms without
// walking /proc, touching persisted state, or issuing kill(2) syscalls.
// Production byte-for-byte behavior lives in Start(); bootstrap() is
// called from both paths to keep them in sync. See the "Test startup
// contract" section of .claude/rules/daemon-architecture.md.
func (d *Daemon) bootstrap() error {
	// Register agnt-specific commands with Hub before starting
	d.registerCommands()

	// Register session cleanup callback with Hub
	// Uses deferred cleanup so ResilientClient reconnects don't kill processes.
	d.hub.SetSessionCleanup(func(sessionCode string) {
		d.CleanupSessionResourcesDeferred(sessionCode)
	})

	// Start the Hub (handles socket creation, accept loop, client management)
	if err := d.hub.Start(); err != nil {
		debug.Error("daemon", "failed to start hub: %v", err)
		return fmt.Errorf("failed to start hub: %w", err)
	}
	d.started = time.Now()

	// Start the scheduler for scheduled message delivery
	if err := d.scheduler.Start(d.ctx); err != nil {
		debug.Log("daemon", "failed to start scheduler: %v", err)
	}

	// Start URL tracker for process URL detection
	d.urlTracker.Start(d.ctx)

	// Start proxy event handler for event-driven proxy creation
	d.wg.Add(1)
	go d.handleProxyEvents()

	// Start the hook event drain goroutine. It pops events from the
	// hookRing (pushed by the HOOK verb handler on each `agnt hook`
	// RPC) and fans them out through the eventHub to any registered
	// HookEventSinks. Exits when d.ctx is cancelled.
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.drainHooks(d.ctx)
	}()

	return nil
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

	// Shared wiring: commands, hub, scheduler, URL tracker, proxy events, hook drain.
	if err := d.bootstrap(); err != nil {
		return err
	}

	// Clean up orphaned processes from previous crash.
	// Note: port-based cleanup happens lazily in preflightPortCleanup when
	// scripts are started via StartScript/RunAutostart, not at daemon startup.
	d.cleanupOrphans()

	// Clean up orphaned port bindings from persisted proxy state.
	// This handles the race where orphaned processes were killed above but
	// child/zombie processes still hold the ports.
	d.startupPortCleanup(d.ctx)

	// Scan /proc for orphaned POSIX process groups whose leader PID is
	// dead but whose members are still running. These are leftovers from
	// a daemon crash mid-session: the session-pgid tracker from Slice A
	// was in memory, so it could not fire. No project path is known yet
	// at daemon startup -- the scan runs with default config (enabled).
	// Per-project overrides via session.orphan-pgid-scan still apply if
	// the daemon is later reinvoked with a projectPath.
	d.startupOrphanPGIDScan("")

	// Restore proxies from persisted state
	d.restoreProxies()

	// Start update checker if enabled
	if d.updateChecker != nil {
		d.updateChecker.Start()
	}

	// Periodic duplicate process scanner is intentionally NOT started.
	// The pre-autostart scan in makeAutostartStartFn handles orphans from
	// previous sessions; running a periodic scan during normal operation
	// races against managed dotnet/node child process trees and kills
	// legitimate descendants. dupScanner is still constructed because
	// makeAutostartStartFn calls ScanForProject directly.

	return nil
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

// ingestProcessAlert routes a single AlertScanner match to every agent-facing
// surface: the legacy alertStore (get_errors) AND the incident bus
// (get_incidents). Before this, process alerts reached only alertStore + the
// browser toast (EventHub.Deliver), so a session running with the incident
// pipeline enabled saw process errors pop as toasts but never in its inbox.
// fallbackTS is used when the match carries no timestamp.
func (d *Daemon) ingestProcessAlert(m *overlay.AlertMatch, fallbackTS time.Time) {
	mts := m.Timestamp
	if mts.IsZero() {
		mts = fallbackTS
	}
	// Stamp the owning project path at ingest so the session-scope chokepoint
	// can filter alerts by project. The scanner only fires for daemon-managed
	// processes, so the script ID resolves to a managed process whose
	// ProjectPath is the source of truth. Fail loud (not silent empty) when it
	// can't be resolved.
	projectPath := ""
	if m.ScriptID != "" {
		if proc, err := d.hub.ProcessManager().Get(m.ScriptID); err == nil {
			projectPath = proc.ProjectPath
		}
		if projectPath == "" {
			debug.Warn("daemon", "alert ingest: unresolved project path for script %q; storing alert unscoped", m.ScriptID)
		}
	}
	d.alertStore.Add(&AlertEntry{
		PatternID:   m.Pattern.ID,
		Severity:    string(m.Pattern.Severity),
		Category:    m.Pattern.Category,
		Description: m.Pattern.Description,
		Line:        m.Line,
		ScriptID:    m.ScriptID,
		ProjectPath: projectPath,
		Timestamp:   mts,
	})
	if m.Pattern.Category == "rebuild" && m.ScriptID != "" {
		d.healthTracker.RecordRebuildSignal(m.ScriptID)
	}
	// Mirror into the incident pipeline. The bus is a no-op when no session has
	// the pipeline enabled, so this is safe to call unconditionally — it matches
	// the dual-write pattern fireToIncidentBus already uses for proxy signals.
	if d.incidentBus != nil {
		d.incidentBus.Publish(incident.FromAlertMatch(m, m.ScriptID))
	}
}

// AlertStore returns the process alert store.
func (d *Daemon) AlertStore() *ProcessAlertStore {
	return d.alertStore
}

// EventHub returns the alert hub for event routing.
func (d *Daemon) EventHub() *EventHub {
	return d.eventHub
}

// IncidentBus returns the incident event bus.
func (d *Daemon) IncidentBus() *incident.MPSCBus {
	return d.incidentBus
}

// ApplyAlertsConfig pushes the alerts block from a project's AgntConfig
// into the daemon's runtime alert subsystems: the HoldBuffer's per-proxy
// hold window and cascade patterns, and the HealthTracker's transport
// outage thresholds. Safe to call multiple times — the latest values win.
// A nil cfg restores defaults.
func (d *Daemon) ApplyAlertsConfig(cfg *config.AlertsConfig) {
	if d == nil {
		return
	}

	var holdCfg *config.OutageHoldConfig
	if cfg != nil {
		holdCfg = cfg.OutageHold
	}

	// Swap atomically, then stop the old buffer. Concurrent readers either
	// see the old buffer (its methods are closed-safe) or the new one.
	newHB := NewHoldBuffer(holdCfg, d.fireHoldEmit)
	if old := d.holdBuffer.Swap(newHB); old != nil {
		old.Stop()
	}
	// Shutdown race: if Stop() ran concurrently it may have already torn down
	// the hold buffer (it swaps to nil under d.shutdown). In that case the
	// buffer just installed would never be stopped and its goroutine would
	// leak. Re-check and reclaim our own buffer. CompareAndSwap ensures we only
	// stop it if it is still the installed one (if Stop already claimed it via
	// Swap(nil), the CAS fails and Stop owns the teardown).
	if d.isShuttingDown() {
		if d.holdBuffer.CompareAndSwap(newHB, nil) {
			newHB.Stop()
		}
	}

	if d.healthTracker != nil {
		d.healthTracker.SetTransportConfig(TransportConfig{
			Threshold:        holdCfg.GetTransportErrThreshold(),
			Window:           holdCfg.GetTransportErrWindow(),
			RecoveryDebounce: holdCfg.GetRecoveryDebounce(),
		})
	}
}

// startSwallowSweeperOnce launches the single sweeper goroutine that drains the
// swallow detector and publishes any swallowed-error incidents. It is started
// lazily on the first ChaosSwallowWatch fault (so a daemon whose proxies never
// turn the panel toggle on pays nothing) and exits on context cancel.
func (d *Daemon) startSwallowSweeperOnce() {
	if !d.swallowSweeperStarted.CompareAndSwap(false, true) {
		return
	}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-d.ctx.Done():
				return
			case <-ticker.C:
				if d.swallowDetector == nil || d.incidentBus == nil {
					continue
				}
				for _, ev := range d.swallowDetector.Sweep(time.Now()) {
					d.incidentBus.Publish(ev)
				}
			}
		}
	}()
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

// SetOverlayEndpoint records the daemon-wide default overlay endpoint in
// persistent state. It deliberately does NOT push the endpoint onto existing
// proxies: overlay binding is per-project and owned by rebindProxyOverlays,
// which matches a proxy to the session that owns its project path. The old
// behavior — blasting one endpoint onto every proxy across all projects — was
// the cause of cross-project message leaks (a message from project A's browser
// reaching project B's agent), so it has been removed.
//
// Per-proxy binding goes through ProxyServer.SetOverlayEndpoint via
// rebindProxyOverlays / overlayEndpointForProject; this method only persists
// the default for OVERLAY GET and state restore.
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

// DaemonInfo holds daemon status information.
type DaemonInfo struct {
	Version       string              `json:"version"`
	BuildTime     string              `json:"build_time,omitempty"`
	GitCommit     string              `json:"git_commit,omitempty"`
	SocketPath    string              `json:"socket_path"`
	Uptime        time.Duration       `json:"uptime"`
	ClientCount   int64               `json:"client_count"`
	ProcessInfo   ProcessInfo         `json:"process_info"`
	ProxyInfo     ProxyInfo           `json:"proxy_info"`
	TunnelInfo    TunnelInfo          `json:"tunnel_info"`
	BrowserInfo   BrowserInfo         `json:"browser_info"`
	SessionInfo   SessionInfo         `json:"session_info"`
	SchedulerInfo SchedulerInfo       `json:"scheduler_info"`
	UpdateInfo    *updater.UpdateInfo `json:"update_info,omitempty"`
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
