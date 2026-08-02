package daemonclient

import (
	"errors"
	"time"

	goclient "github.com/standardbeagle/go-cli-server/client"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
)

var (
	// ErrReconnecting is returned when an operation is attempted during reconnection.
	ErrReconnecting = goclient.ErrReconnecting
	// ErrShutdown is returned when an operation is attempted after shutdown.
	ErrShutdown = goclient.ErrShutdown
)

// ReconnectCallback is called after successful reconnection.
// It should restore any state that needs to be re-registered with the daemon.
type ReconnectCallback func(client *Client) error

// ResilientClientConfig configures a ResilientClient.
type ResilientClientConfig struct {
	// AutoStartConfig for daemon auto-start
	AutoStartConfig AutoStartConfig

	// HeartbeatInterval is how often to send heartbeats (0 disables)
	HeartbeatInterval time.Duration

	// HeartbeatTimeout is how long to wait for heartbeat response
	HeartbeatTimeout time.Duration

	// ReconnectBackoffMin is the minimum backoff between reconnection attempts
	ReconnectBackoffMin time.Duration

	// ReconnectBackoffMax is the maximum backoff between reconnection attempts
	ReconnectBackoffMax time.Duration

	// MaxReconnectAttempts limits reconnection attempts (0 = unlimited)
	MaxReconnectAttempts int

	// OnReconnect is called after successful reconnection
	OnReconnect ReconnectCallback

	// OnDisconnect is called when connection is lost
	OnDisconnect func(err error)

	// OnReconnectFailed is called when reconnection fails permanently
	OnReconnectFailed func(err error)

	// ClientVersion is the expected daemon version (strict matching).
	// If empty, version checking is skipped.
	ClientVersion string

	// OnVersionMismatch is called when client and daemon versions don't match.
	// If nil and versions mismatch, Connect() returns an error.
	// If non-nil, the callback can handle the mismatch (e.g., trigger upgrade).
	// Return nil to proceed with mismatched versions, or error to fail connection.
	OnVersionMismatch func(clientVer, daemonVer string) error
}

// DefaultResilientClientConfig returns sensible defaults.
func DefaultResilientClientConfig() ResilientClientConfig {
	return ResilientClientConfig{
		AutoStartConfig:      DefaultAutoStartConfig(),
		HeartbeatInterval:    10 * time.Second,
		HeartbeatTimeout:     5 * time.Second,
		ReconnectBackoffMin:  100 * time.Millisecond,
		ReconnectBackoffMax:  30 * time.Second,
		MaxReconnectAttempts: 0, // Unlimited
	}
}

// ResilientClient wraps client.ResilientConn with automatic reconnection and health monitoring.
// It provides agnt-specific wrapper methods for convenience.
type ResilientClient struct {
	config ResilientClientConfig
	rc     *goclient.ResilientConn
}

// NewResilientClient creates a new resilient client.
func NewResilientClient(config ResilientClientConfig) *ResilientClient {
	// Map our config to go-cli-server config
	// Route through toLibraryConfig so the spawn argv (HubArgs =
	// "daemon start --socket <path>") stays in one place; the library's generic
	// default would launch agnt with no subcommand and the hub would never bind.
	autoStartCfg := config.AutoStartConfig.toLibraryConfig()

	resilientCfg := goclient.ResilientConfig{
		AutoStartConfig:      autoStartCfg,
		HeartbeatInterval:    config.HeartbeatInterval,
		HeartbeatTimeout:     config.HeartbeatTimeout,
		ReconnectBackoffMin:  config.ReconnectBackoffMin,
		ReconnectBackoffMax:  config.ReconnectBackoffMax,
		MaxReconnectAttempts: config.MaxReconnectAttempts,
		OnDisconnect:         config.OnDisconnect,
		OnReconnectFailed:    config.OnReconnectFailed,
	}

	// Set up version checking if configured
	if config.ClientVersion != "" {
		resilientCfg.VersionCheck = func(conn *goclient.Conn) error {
			debug.Log("client", "checking daemon version (client=%s)", config.ClientVersion)
			// Get daemon info
			var info DaemonInfo
			if err := conn.Request("INFO").JSONInto(&info); err != nil {
				debug.Error("client", "failed to get daemon version: %v", err)
				return errors.New("failed to get daemon version: " + err.Error())
			}

			// Check if versions match
			if !goclient.VersionsMatch(config.ClientVersion, info.Version) {
				debug.Log("client", "version mismatch: client=%s daemon=%s", config.ClientVersion, info.Version)
				// Versions don't match - call callback if configured
				if config.OnVersionMismatch != nil {
					return config.OnVersionMismatch(config.ClientVersion, info.Version)
				}

				// No callback - stop the daemon so next connection uses new version.
				// Best-effort: if the old daemon is already gone or unresponsive
				// the SHUTDOWN fails harmlessly (the version-mismatch error below
				// still triggers a restart), but log it so a wedged daemon shows up.
				if err := conn.Request("SHUTDOWN").OK(); err != nil {
					debug.Log("client", "best-effort SHUTDOWN of version-mismatched daemon failed: %v", err)
				}

				return errors.New("version mismatch: client=" + config.ClientVersion +
					" daemon=" + info.Version + " (daemon stopped, will restart with new version)")
			}
			debug.Log("client", "version check passed: %s", info.Version)

			return nil
		}
	}

	// Set up reconnect callback wrapper if configured
	if config.OnReconnect != nil {
		resilientCfg.OnReconnect = func(conn *goclient.Conn) error {
			// Wrap the connection in a Client for the callback
			client := &Client{conn: conn}
			return config.OnReconnect(client)
		}
	}

	return &ResilientClient{
		config: config,
		rc:     goclient.NewResilientConn(resilientCfg),
	}
}

// Connect establishes the initial connection to the daemon.
func (rc *ResilientClient) Connect() error {
	return rc.rc.Connect()
}

// Close shuts down the resilient client.
func (rc *ResilientClient) Close() error {
	return rc.rc.Close()
}

// IsConnected returns whether the client is currently connected.
func (rc *ResilientClient) IsConnected() bool {
	return rc.rc.IsConnected()
}

// IsReconnecting returns whether the client is currently reconnecting.
func (rc *ResilientClient) IsReconnecting() bool {
	return rc.rc.IsReconnecting()
}

// Stats returns connection statistics.
func (rc *ResilientClient) Stats() map[string]interface{} {
	return rc.rc.Stats()
}

// Client returns the underlying client for direct access.
// Returns nil if not connected.
func (rc *ResilientClient) Client() *Client {
	conn := rc.rc.Conn()
	if conn == nil {
		return nil
	}
	return &Client{conn: conn}
}

// WithClient executes a function with the client, handling reconnection.
func (rc *ResilientClient) WithClient(fn func(*Client) error) error {
	return rc.rc.WithConn(func(conn *goclient.Conn) error {
		client := &Client{conn: conn}
		return fn(client)
	})
}

// withResult runs fn against the current client (via WithClient, so it routes
// to the live connection across reconnects) and returns its result. Every
// single-result wrapper below is one line on top of this — before it, each
// spelled out the same nine-line capture-the-result closure by hand and the
// copies had already begun to drift.
func withResult[T any](rc *ResilientClient, fn func(*Client) (T, error)) (T, error) {
	var result T
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = fn(c)
		return e
	})
	return result, err
}

// Convenience methods that wrap common client operations with resilience

// Ping sends a ping to the daemon.
func (rc *ResilientClient) Ping() error {
	return rc.rc.Ping()
}

// Info retrieves daemon information.
func (rc *ResilientClient) Info() (*DaemonInfo, error) {
	return withResult(rc, (*Client).Info)
}

// OverlaySet sets the overlay endpoint.
func (rc *ResilientClient) OverlaySet(endpoint string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.OverlaySet(endpoint) })
}

// ProxyStart starts a reverse proxy.
func (rc *ResilientClient) ProxyStart(id, targetURL string, port, maxLogSize int, path string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) {
		return c.ProxyStart(id, targetURL, port, maxLogSize, path)
	})
}

// ProxyStop stops a reverse proxy.
func (rc *ResilientClient) ProxyStop(id string) error {
	return rc.WithClient(func(c *Client) error { return c.ProxyStop(id) })
}

// ProxyList lists all proxies.
func (rc *ResilientClient) ProxyList(dirFilter protocol.DirectoryFilter) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.ProxyList(dirFilter) })
}

// Detect detects the project type at the given path.
func (rc *ResilientClient) Detect(path string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.Detect(path) })
}

// Run starts a process on the daemon.
// The config is marshaled to JSON and can be protocol.RunConfig or any struct
// that embeds it (e.g., with additional fields like no_auto_restart).
func (rc *ResilientClient) Run(config interface{}) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.Run(config) })
}

// ProcRun starts an admin-aware process via PROC RUN. Optionally gates
// on declared dependencies before launching. See Client.ProcRun for the
// full contract.
func (rc *ResilientClient) ProcRun(name string, cfg ProcRunConfig) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.ProcRun(name, cfg) })
}

// ProcRunGroup launches a multi-process startup group via PROC RUN-GROUP.
// Performs cycle detection before any process launches. See
// Client.ProcRunGroup for the full contract.
func (rc *ResilientClient) ProcRunGroup(cfg ProcRunGroupConfig) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.ProcRunGroup(cfg) })
}

// ProcStatus gets the status of a process.
func (rc *ResilientClient) ProcStatus(processID string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.ProcStatus(processID) })
}

// ProcOutput gets the output of a process.
func (rc *ResilientClient) ProcOutput(processID string, filter protocol.OutputFilter) (string, error) {
	return withResult(rc, func(c *Client) (string, error) { return c.ProcOutput(processID, filter) })
}

// ProcStop stops a process.
func (rc *ResilientClient) ProcStop(processID string, force bool) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.ProcStop(processID, force) })
}

// ProcList lists all processes.
func (rc *ResilientClient) ProcList(dirFilter protocol.DirectoryFilter) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.ProcList(dirFilter) })
}

// ResolveQueryScope resolves the effective query scope through the daemon.
func (rc *ResilientClient) ResolveQueryScope(filter protocol.DirectoryFilter) (bool, error) {
	return withResult(rc, func(c *Client) (bool, error) { return c.ResolveQueryScope(filter) })
}

// ProcCleanupPort kills processes on a specific port.
func (rc *ResilientClient) ProcCleanupPort(port int) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.ProcCleanupPort(port) })
}

// ProxyStartWithConfig starts a reverse proxy with extended configuration.
func (rc *ResilientClient) ProxyStartWithConfig(id, targetURL string, port, maxLogSize int, config ProxyStartConfig) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) {
		return c.ProxyStartWithConfig(id, targetURL, port, maxLogSize, config)
	})
}

// ProxyStatus gets the status of a proxy.
func (rc *ResilientClient) ProxyStatus(id string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.ProxyStatus(id) })
}

// ProxyExec executes JavaScript in connected browsers.
func (rc *ResilientClient) ProxyExec(id, code string, frameID ...string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.ProxyExec(id, code, frameID...) })
}

// ProxyToast sends a toast notification to connected browsers.
func (rc *ResilientClient) ProxyToast(id string, toast protocol.ToastConfig) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.ProxyToast(id, toast) })
}

// ProxyLogQuery queries proxy logs.
func (rc *ResilientClient) ProxyLogQuery(proxyID string, filter protocol.LogQueryFilter) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.ProxyLogQuery(proxyID, filter) })
}

// ProxyLogQueryFull queries proxy logs and decodes typed entries plus the total
// available and dropped counts, for handlers that reuse the shared compact/raw
// formatters.
func (rc *ResilientClient) ProxyLogQueryFull(proxyID string, filter protocol.LogQueryFilter) ([]proxy.LogEntry, int64, int64, error) {
	var entries []proxy.LogEntry
	var total, dropped int64
	err := rc.WithClient(func(c *Client) error {
		var e error
		entries, total, dropped, e = c.ProxyLogQueryFull(proxyID, filter)
		return e
	})
	return entries, total, dropped, err
}

// ProxyLogClear clears proxy logs.
func (rc *ResilientClient) ProxyLogClear(proxyID string) error {
	return rc.WithClient(func(c *Client) error { return c.ProxyLogClear(proxyID) })
}

// ProxyLogStats gets proxy log statistics.
func (rc *ResilientClient) ProxyLogStats(proxyID string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.ProxyLogStats(proxyID) })
}

// CurrentPageList lists active page sessions.
func (rc *ResilientClient) CurrentPageList(proxyID string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.CurrentPageList(proxyID) })
}

// CurrentPageGet gets details for a specific page session.
func (rc *ResilientClient) CurrentPageGet(proxyID, sessionID string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.CurrentPageGet(proxyID, sessionID) })
}

// CurrentPageClear clears page sessions.
func (rc *ResilientClient) CurrentPageClear(proxyID string) error {
	return rc.WithClient(func(c *Client) error { return c.CurrentPageClear(proxyID) })
}

// Chaos methods

// ChaosEnable enables chaos injection on a proxy.
func (rc *ResilientClient) ChaosEnable(proxyID string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.ChaosEnable(proxyID) })
}

// ChaosDisable disables chaos injection on a proxy.
func (rc *ResilientClient) ChaosDisable(proxyID string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.ChaosDisable(proxyID) })
}

// ChaosStatus gets the chaos status of a proxy.
func (rc *ResilientClient) ChaosStatus(proxyID string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.ChaosStatus(proxyID) })
}

// ChaosPreset applies a preset chaos configuration to a proxy.
func (rc *ResilientClient) ChaosPreset(proxyID, preset string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.ChaosPreset(proxyID, preset) })
}

// ChaosSet sets the full chaos configuration on a proxy.
func (rc *ResilientClient) ChaosSet(proxyID string, config protocol.ChaosConfigPayload) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.ChaosSet(proxyID, config) })
}

// ChaosAddRule adds a single rule to a proxy's chaos engine.
func (rc *ResilientClient) ChaosAddRule(proxyID string, rule protocol.ChaosRuleConfig) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.ChaosAddRule(proxyID, rule) })
}

// ChaosRemoveRule removes a rule from a proxy's chaos engine.
func (rc *ResilientClient) ChaosRemoveRule(proxyID, ruleID string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.ChaosRemoveRule(proxyID, ruleID) })
}

// ChaosListRules lists all chaos rules for a proxy.
func (rc *ResilientClient) ChaosListRules(proxyID string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.ChaosListRules(proxyID) })
}

// ChaosStats gets chaos statistics for a proxy.
func (rc *ResilientClient) ChaosStats(proxyID string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.ChaosStats(proxyID) })
}

// ChaosClear clears all chaos rules and resets stats for a proxy.
func (rc *ResilientClient) ChaosClear(proxyID string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.ChaosClear(proxyID) })
}

// ChaosListPresets returns the list of available chaos presets.
func (rc *ResilientClient) ChaosListPresets() (map[string]interface{}, error) {
	return withResult(rc, (*Client).ChaosListPresets)
}

// Tunnel methods

// TunnelStart starts a tunnel for a local port.
func (rc *ResilientClient) TunnelStart(config protocol.TunnelStartConfig) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.TunnelStart(config) })
}

// TunnelStop stops a running tunnel.
func (rc *ResilientClient) TunnelStop(id string) error {
	return rc.WithClient(func(c *Client) error { return c.TunnelStop(id) })
}

// TunnelStatus gets the status of a tunnel.
func (rc *ResilientClient) TunnelStatus(id string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.TunnelStatus(id) })
}

// TunnelList lists all active tunnels.
func (rc *ResilientClient) TunnelList(dirFilter protocol.DirectoryFilter) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.TunnelList(dirFilter) })
}

// Browser methods

// BrowserStart starts a browser instance.
func (rc *ResilientClient) BrowserStart(config protocol.BrowserStartConfig) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.BrowserStart(config) })
}

// BrowserStop stops a running browser instance.
func (rc *ResilientClient) BrowserStop(id string) error {
	return rc.WithClient(func(c *Client) error { return c.BrowserStop(id) })
}

// BrowserStatus gets the status of a browser instance.
func (rc *ResilientClient) BrowserStatus(id string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.BrowserStatus(id) })
}

// BrowserList lists all active browser instances.
func (rc *ResilientClient) BrowserList(dirFilter protocol.DirectoryFilter) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.BrowserList(dirFilter) })
}

// Automation methods (chromedp sessions)

// AutomationStart starts a chromedp automation session.
func (rc *ResilientClient) AutomationStart(config protocol.AutomationStartConfig) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.AutomationStart(config) })
}

// AutomationStop stops an automation session.
func (rc *ResilientClient) AutomationStop(id string) error {
	return rc.WithClient(func(c *Client) error { return c.AutomationStop(id) })
}

// AutomationStatus gets the status of an automation session.
func (rc *ResilientClient) AutomationStatus(id string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.AutomationStatus(id) })
}

// AutomationList lists all active automation sessions.
func (rc *ResilientClient) AutomationList(dirFilter protocol.DirectoryFilter) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.AutomationList(dirFilter) })
}

// AutomationScreenshot takes a screenshot in an automation session.
func (rc *ResilientClient) AutomationScreenshot(config protocol.AutomationScreenshotConfig) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.AutomationScreenshot(config) })
}

// AutomationNavigate navigates to a URL in an automation session.
func (rc *ResilientClient) AutomationNavigate(config protocol.AutomationNavigateConfig) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.AutomationNavigate(config) })
}

// AutomationEvaluate evaluates JavaScript in an automation session.
func (rc *ResilientClient) AutomationEvaluate(config protocol.AutomationEvaluateConfig) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.AutomationEvaluate(config) })
}

// BroadcastActivity sends an activity state update to connected browsers via specified proxies.
// If proxyIDs is empty, broadcasts to all proxies (backward compatibility).
func (rc *ResilientClient) BroadcastActivity(active bool, proxyIDs ...string) error {
	return rc.WithClient(func(c *Client) error { return c.BroadcastActivity(active, proxyIDs...) })
}

// SetForwarding pauses/resumes agent-inbound push for this session.
func (rc *ResilientClient) SetForwarding(paused bool) error {
	return rc.WithClient(func(c *Client) error { return c.SetForwarding(paused) })
}

// BroadcastOutputPreview sends output preview lines to connected browsers via proxies.
func (rc *ResilientClient) BroadcastOutputPreview(lines []string, throbber string, proxyIDs ...string) error {
	return rc.WithClient(func(c *Client) error { return c.BroadcastOutputPreview(lines, throbber, proxyIDs...) })
}

// Session methods

// SessionRegister registers a new session with the daemon.
func (rc *ResilientClient) SessionRegister(code, overlayPath, projectPath, command string, args []string) (map[string]interface{}, error) {
	return rc.SessionRegisterWithPGID(code, overlayPath, projectPath, command, args, 0)
}

// SessionRegisterWithPGID registers a new session and reports the POSIX
// process group ID of the PTY child (the session leader). See Client's
// equivalent method for details.
func (rc *ResilientClient) SessionRegisterWithPGID(code, overlayPath, projectPath, command string, args []string, sessionPGID int) (map[string]interface{}, error) {
	return rc.SessionRegisterWithContainment(code, overlayPath, projectPath, command, args, sessionPGID, 0)
}

// SessionRegisterWithContainment registers a new session and reports
// both the Unix session pgid and the Windows Job Object handle for the
// PTY child subtree. See Client.SessionRegisterWithContainment for
// platform semantics.
func (rc *ResilientClient) SessionRegisterWithContainment(code, overlayPath, projectPath, command string, args []string, sessionPGID int, sessionJobHandle uint64) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) {
		return c.SessionRegisterWithContainment(code, overlayPath, projectPath, command, args, sessionPGID, sessionJobHandle)
	})
}

// SessionUnregister unregisters a session.
func (rc *ResilientClient) SessionUnregister(code string) error {
	return rc.WithClient(func(c *Client) error { return c.SessionUnregister(code) })
}

// SessionHeartbeat sends a heartbeat for a session.
func (rc *ResilientClient) SessionHeartbeat(code string) error {
	return rc.WithClient(func(c *Client) error { return c.SessionHeartbeat(code) })
}

// SessionList lists sessions, optionally filtered by directory.
func (rc *ResilientClient) SessionList(dirFilter protocol.DirectoryFilter) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.SessionList(dirFilter) })
}

// SessionGet retrieves details for a specific session.
func (rc *ResilientClient) SessionGet(code string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.SessionGet(code) })
}

// SessionSend sends a message to a session immediately.
func (rc *ResilientClient) SessionSend(code, message string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.SessionSend(code, message) })
}

// SessionSchedule schedules a message for future delivery.
func (rc *ResilientClient) SessionSchedule(code, duration, message string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.SessionSchedule(code, duration, message) })
}

// SessionCancel cancels a scheduled task.
func (rc *ResilientClient) SessionCancel(taskID string) error {
	return rc.WithClient(func(c *Client) error { return c.SessionCancel(taskID) })
}

// SessionTasks lists scheduled tasks, optionally filtered by directory.
func (rc *ResilientClient) SessionTasks(dirFilter protocol.DirectoryFilter) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.SessionTasks(dirFilter) })
}

// SessionGenerateCode generates a unique session code for a command.
func (rc *ResilientClient) SessionGenerateCode(command string) (string, error) {
	return withResult(rc, func(c *Client) (string, error) { return c.SessionGenerateCode(command) })
}

// SessionFind finds a session by directory ancestry.
func (rc *ResilientClient) SessionFind(directory string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.SessionFind(directory) })
}

// SessionAttach attaches to a session found by directory ancestry.
func (rc *ResilientClient) SessionAttach(directory string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.SessionAttach(directory) })
}

// Store methods

// StoreGet retrieves a value from the key-value store.
func (rc *ResilientClient) StoreGet(req protocol.StoreGetRequest) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.StoreGet(req) })
}

// StoreSet stores a value in the key-value store.
func (rc *ResilientClient) StoreSet(req protocol.StoreSetRequest) error {
	return rc.WithClient(func(c *Client) error { return c.StoreSet(req) })
}

// StoreDelete deletes a value from the key-value store.
func (rc *ResilientClient) StoreDelete(req protocol.StoreDeleteRequest) error {
	return rc.WithClient(func(c *Client) error { return c.StoreDelete(req) })
}

// StoreList lists all keys in a scope.
func (rc *ResilientClient) StoreList(req protocol.StoreListRequest) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.StoreList(req) })
}

// StoreClear clears all entries in a scope.
func (rc *ResilientClient) StoreClear(req protocol.StoreClearRequest) error {
	return rc.WithClient(func(c *Client) error { return c.StoreClear(req) })
}

// StoreGetAll retrieves all key-value pairs in a scope.
func (rc *ResilientClient) StoreGetAll(req protocol.StoreGetAllRequest) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.StoreGetAll(req) })
}

// Restart and StopAll methods

// ProcRestart restarts a process.
func (rc *ResilientClient) ProcRestart(processID string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.ProcRestart(processID) })
}

// ProcAutoRestart enables, disables, or queries auto-restart for a process.
func (rc *ResilientClient) ProcAutoRestart(processID, action string, config *ProcAutoRestartConfig) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.ProcAutoRestart(processID, action, config) })
}

// ProxyRestart restarts a proxy.
func (rc *ResilientClient) ProxyRestart(id string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.ProxyRestart(id) })
}

// StopAll stops the processes and proxies owned by the connection's bound
// session project (empty filter → daemon resolves scope from the session).
func (rc *ResilientClient) StopAll() (map[string]interface{}, error) {
	return rc.StopAllScoped(protocol.DirectoryFilter{})
}

// StopAllScoped stops resources for the project resolved by filter (the MCP
// daemon connection is not session-bound, so it names the project or passes
// global explicitly).
func (rc *ResilientClient) StopAllScoped(filter protocol.DirectoryFilter) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.StopAllScoped(filter) })
}

// RestartAll restarts the processes and proxies owned by the connection's
// bound session project. See StopAll for the scoping contract.
func (rc *ResilientClient) RestartAll() (map[string]interface{}, error) {
	return rc.RestartAllScoped(protocol.DirectoryFilter{})
}

// RestartAllScoped restarts resources for the project resolved by filter.
func (rc *ResilientClient) RestartAllScoped(filter protocol.DirectoryFilter) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.RestartAllScoped(filter) })
}

// Alert methods

// AlertReport sends an alert report to the daemon.
func (rc *ResilientClient) AlertReport(payload protocol.AlertReportPayload) error {
	return rc.WithClient(func(c *Client) error { return c.AlertReport(payload) })
}

// AlertQuery queries alerts from the daemon.
func (rc *ResilientClient) AlertQuery(filter protocol.AlertQueryFilter) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.AlertQuery(filter) })
}

// IncidentQuery queries the incident inbox for the current session.
func (rc *ResilientClient) IncidentQuery(filter protocol.IncidentQueryFilter) (*protocol.IncidentQueryResult, error) {
	return withResult(rc, func(c *Client) (*protocol.IncidentQueryResult, error) { return c.IncidentQuery(filter) })
}

// IncidentPin pins an incident in the caller session's inbox so it survives
// eviction and every retention clear until unpinned.
func (rc *ResilientClient) IncidentPin(payload protocol.IncidentPinPayload) (*protocol.IncidentPinResult, error) {
	return withResult(rc, func(c *Client) (*protocol.IncidentPinResult, error) { return c.IncidentPin(payload) })
}

// IncidentUnpin releases a pin previously created with IncidentPin.
func (rc *ResilientClient) IncidentUnpin(payload protocol.IncidentPinPayload) (*protocol.IncidentPinResult, error) {
	return withResult(rc, func(c *Client) (*protocol.IncidentPinResult, error) { return c.IncidentUnpin(payload) })
}

// IncidentClear retires the caller session's unpinned incidents.
func (rc *ResilientClient) IncidentClear() (*protocol.IncidentClearResult, error) {
	return withResult(rc, func(c *Client) (*protocol.IncidentClearResult, error) { return c.IncidentClear() })
}

// PublishCreate validates + publishes a walkthrough, returning the share id and
// the plaintext token (returned once).
func (rc *ResilientClient) PublishCreate(req protocol.PublishCreateRequest) (*protocol.PublishCreateResult, error) {
	return withResult(rc, func(c *Client) (*protocol.PublishCreateResult, error) { return c.PublishCreate(req) })
}

// PublishRotate mints a fresh token for a share (old token dies immediately).
func (rc *ResilientClient) PublishRotate(id string) (*protocol.PublishRotateResult, error) {
	return withResult(rc, func(c *Client) (*protocol.PublishRotateResult, error) { return c.PublishRotate(id) })
}

// PublishRevoke tombstones a share.
func (rc *ResilientClient) PublishRevoke(id string) error {
	return rc.WithClient(func(c *Client) error { return c.PublishRevoke(id) })
}

// PublishStatus returns the redaction-safe status of a share.
func (rc *ResilientClient) PublishStatus(id string) (*protocol.PublishShareInfo, error) {
	return withResult(rc, func(c *Client) (*protocol.PublishShareInfo, error) { return c.PublishStatus(id) })
}

// PublishList lists the caller's project-scoped shares.
func (rc *ResilientClient) PublishList() (*protocol.PublishListResult, error) {
	return withResult(rc, (*Client).PublishList)
}

// PublishFeedback reads the owner-scoped feedback rows for a share (never the
// token). Ownership is enforced daemon-side.
func (rc *ResilientClient) PublishFeedback(id, cursor string, limit int) (*protocol.PublishFeedbackResult, error) {
	return withResult(rc, func(c *Client) (*protocol.PublishFeedbackResult, error) { return c.PublishFeedback(id, cursor, limit) })
}

// AlertClear clears alerts, scoped by the filter. Pinned errors are kept.
func (rc *ResilientClient) AlertClear(filter protocol.AlertClearFilter) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.AlertClear(filter) })
}

// AlertPin pins an error so it survives automatic retention clears.
func (rc *ResilientClient) AlertPin(payload protocol.AlertPinPayload) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.AlertPin(payload) })
}

// AlertUnpin removes a pin previously created with AlertPin.
func (rc *ResilientClient) AlertUnpin(payload protocol.AlertPinPayload) error {
	return rc.WithClient(func(c *Client) error { return c.AlertUnpin(payload) })
}

// StartupLog queries the startup log from the daemon.
func (rc *ResilientClient) StartupLog(limit int, dirFilter protocol.DirectoryFilter) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.StartupLog(limit, dirFilter) })
}

// Script methods

// ScriptList lists all scripts for a project directory.
func (rc *ResilientClient) ScriptList(projectPath string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.ScriptList(projectPath) })
}

// ScriptGet retrieves full detail for a named script.
func (rc *ResilientClient) ScriptGet(name, projectPath string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.ScriptGet(name, projectPath) })
}

// ScriptOutput retrieves output history for a named script.
func (rc *ResilientClient) ScriptOutput(name, projectPath string, tail int) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.ScriptOutput(name, projectPath, tail) })
}

// Doctor runs health checks and returns a diagnostic report.
func (rc *ResilientClient) Doctor(projectPath string) (map[string]interface{}, error) {
	return withResult(rc, func(c *Client) (map[string]interface{}, error) { return c.Doctor(projectPath) })
}
