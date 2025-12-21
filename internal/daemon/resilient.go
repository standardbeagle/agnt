package daemon

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
)

var (
	// ErrReconnecting is returned when an operation is attempted during reconnection.
	ErrReconnecting = errors.New("client is reconnecting")
	// ErrShutdown is returned when an operation is attempted after shutdown.
	ErrShutdown = errors.New("client has been shut down")
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

// ResilientClient wraps Client with automatic reconnection and health monitoring.
type ResilientClient struct {
	config ResilientClientConfig

	client   *Client
	clientMu sync.RWMutex

	connected    atomic.Bool
	reconnecting atomic.Bool
	shutdown     atomic.Bool

	// Heartbeat management
	heartbeatCtx    context.Context
	heartbeatCancel context.CancelFunc

	// Reconnection management
	reconnectCh chan struct{}

	// Statistics
	reconnectCount     atomic.Int64
	lastConnectTime    atomic.Pointer[time.Time]
	lastDisconnectTime atomic.Pointer[time.Time]
}

// NewResilientClient creates a new resilient client.
func NewResilientClient(config ResilientClientConfig) *ResilientClient {
	rc := &ResilientClient{
		config:      config,
		reconnectCh: make(chan struct{}, 1),
	}
	return rc
}

// Connect establishes the initial connection to the daemon.
func (rc *ResilientClient) Connect() error {
	if rc.shutdown.Load() {
		return ErrShutdown
	}

	rc.clientMu.Lock()
	defer rc.clientMu.Unlock()

	// Create new client and connect
	client, err := EnsureDaemonRunning(rc.config.AutoStartConfig)
	if err != nil {
		return err
	}

	// Check version compatibility if configured
	if rc.config.ClientVersion != "" {
		if err := rc.checkVersionCompatibility(client); err != nil {
			client.Close()
			return err
		}
	}

	rc.client = client
	rc.connected.Store(true)
	now := time.Now()
	rc.lastConnectTime.Store(&now)

	// Start heartbeat monitor
	rc.startHeartbeat()

	return nil
}

// checkVersionCompatibility verifies that the daemon version matches the client version.
func (rc *ResilientClient) checkVersionCompatibility(client *Client) error {
	// Get daemon info
	info, err := client.Info()
	if err != nil {
		return errors.New("failed to get daemon version: " + err.Error())
	}

	// Check if versions match
	if !VersionsMatch(rc.config.ClientVersion, info.Version) {
		// Versions don't match - call callback if configured
		if rc.config.OnVersionMismatch != nil {
			return rc.config.OnVersionMismatch(rc.config.ClientVersion, info.Version)
		}

		// No callback - stop the daemon so next connection uses new version
		_ = client.Shutdown() // Best effort

		return errors.New("version mismatch: client=" + rc.config.ClientVersion +
			" daemon=" + info.Version + " (daemon stopped, will restart with new version)")
	}

	return nil
}

// Close shuts down the resilient client.
func (rc *ResilientClient) Close() error {
	if rc.shutdown.Swap(true) {
		return nil // Already shut down
	}

	// Stop heartbeat
	if rc.heartbeatCancel != nil {
		rc.heartbeatCancel()
	}

	// Close underlying client
	rc.clientMu.Lock()
	defer rc.clientMu.Unlock()

	if rc.client != nil {
		return rc.client.Close()
	}
	return nil
}

// IsConnected returns whether the client is currently connected.
func (rc *ResilientClient) IsConnected() bool {
	return rc.connected.Load() && !rc.reconnecting.Load()
}

// IsReconnecting returns whether the client is currently reconnecting.
func (rc *ResilientClient) IsReconnecting() bool {
	return rc.reconnecting.Load()
}

// Stats returns connection statistics.
func (rc *ResilientClient) Stats() map[string]interface{} {
	stats := map[string]interface{}{
		"connected":       rc.connected.Load(),
		"reconnecting":    rc.reconnecting.Load(),
		"reconnect_count": rc.reconnectCount.Load(),
	}

	if t := rc.lastConnectTime.Load(); t != nil {
		stats["last_connect"] = *t
	}
	if t := rc.lastDisconnectTime.Load(); t != nil {
		stats["last_disconnect"] = *t
	}

	return stats
}

// Client returns the underlying client for direct access.
// Returns nil if not connected.
func (rc *ResilientClient) Client() *Client {
	rc.clientMu.RLock()
	defer rc.clientMu.RUnlock()
	return rc.client
}

// WithClient executes a function with the client, handling reconnection.
func (rc *ResilientClient) WithClient(fn func(*Client) error) error {
	if rc.shutdown.Load() {
		return ErrShutdown
	}

	if rc.reconnecting.Load() {
		return ErrReconnecting
	}

	rc.clientMu.RLock()
	client := rc.client
	rc.clientMu.RUnlock()

	if client == nil {
		return ErrNotConnected
	}

	err := fn(client)
	if err != nil {
		// Check if this is a connection error that should trigger reconnection
		if isConnectionError(err) {
			rc.triggerReconnect(err)
		}
	}
	return err
}

// startHeartbeat starts the heartbeat monitoring goroutine.
func (rc *ResilientClient) startHeartbeat() {
	if rc.config.HeartbeatInterval <= 0 {
		return
	}

	// Cancel any existing heartbeat
	if rc.heartbeatCancel != nil {
		rc.heartbeatCancel()
	}

	rc.heartbeatCtx, rc.heartbeatCancel = context.WithCancel(context.Background())

	go rc.heartbeatLoop()
}

// heartbeatLoop sends periodic heartbeats and detects connection failures.
func (rc *ResilientClient) heartbeatLoop() {
	ticker := time.NewTicker(rc.config.HeartbeatInterval)
	defer ticker.Stop()

	consecutiveFailures := 0
	maxConsecutiveFailures := 3

	for {
		select {
		case <-rc.heartbeatCtx.Done():
			return
		case <-ticker.C:
			if rc.reconnecting.Load() || rc.shutdown.Load() {
				continue
			}

			err := rc.sendHeartbeat()
			if err != nil {
				consecutiveFailures++
				if consecutiveFailures >= maxConsecutiveFailures {
					rc.triggerReconnect(err)
					consecutiveFailures = 0
				}
			} else {
				consecutiveFailures = 0
			}
		}
	}
}

// sendHeartbeat sends a single heartbeat ping.
func (rc *ResilientClient) sendHeartbeat() error {
	rc.clientMu.RLock()
	client := rc.client
	rc.clientMu.RUnlock()

	if client == nil {
		return ErrNotConnected
	}

	// Use a timeout for the ping
	done := make(chan error, 1)
	go func() {
		done <- client.Ping()
	}()

	select {
	case err := <-done:
		return err
	case <-time.After(rc.config.HeartbeatTimeout):
		return errors.New("heartbeat timeout")
	}
}

// triggerReconnect initiates the reconnection process.
func (rc *ResilientClient) triggerReconnect(err error) {
	// Only one reconnection at a time
	if !rc.reconnecting.CompareAndSwap(false, true) {
		return
	}

	rc.connected.Store(false)
	now := time.Now()
	rc.lastDisconnectTime.Store(&now)

	// Notify disconnect callback
	if rc.config.OnDisconnect != nil {
		go rc.config.OnDisconnect(err)
	}

	// Start reconnection in background
	go rc.reconnectLoop()
}

// reconnectLoop attempts to reconnect with exponential backoff.
func (rc *ResilientClient) reconnectLoop() {
	defer rc.reconnecting.Store(false)

	backoff := rc.config.ReconnectBackoffMin
	attempts := 0

	for {
		if rc.shutdown.Load() {
			return
		}

		attempts++

		// Close old connection
		rc.clientMu.Lock()
		if rc.client != nil {
			rc.client.Close()
			rc.client = nil
		}
		rc.clientMu.Unlock()

		// Attempt to connect
		client, err := EnsureDaemonRunning(rc.config.AutoStartConfig)
		if err == nil {
			rc.clientMu.Lock()
			rc.client = client
			rc.clientMu.Unlock()

			rc.connected.Store(true)
			rc.reconnectCount.Add(1)
			now := time.Now()
			rc.lastConnectTime.Store(&now)

			// Call reconnect callback to restore state
			if rc.config.OnReconnect != nil {
				// Ignore callback errors - state restoration is best-effort
				_ = rc.config.OnReconnect(client)
			}

			// Restart heartbeat
			rc.startHeartbeat()
			return
		}

		// Check if we've exceeded max attempts
		if rc.config.MaxReconnectAttempts > 0 && attempts >= rc.config.MaxReconnectAttempts {
			if rc.config.OnReconnectFailed != nil {
				rc.config.OnReconnectFailed(err)
			}
			return
		}

		// Exponential backoff
		time.Sleep(backoff)
		backoff = minDuration(backoff*2, rc.config.ReconnectBackoffMax)
	}
}

// isConnectionError checks if an error indicates a connection problem.
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}

	// Check for known connection errors
	if errors.Is(err, ErrNotConnected) {
		return true
	}

	// Check error message for common connection problems
	errStr := err.Error()
	connectionErrors := []string{
		"connection refused",
		"broken pipe",
		"connection reset",
		"EOF",
		"no such file or directory",
		"socket",
		"network",
	}

	for _, ce := range connectionErrors {
		if containsIgnoreCase(errStr, ce) {
			return true
		}
	}

	return false
}

// containsIgnoreCase checks if s contains substr (case-insensitive).
func containsIgnoreCase(s, substr string) bool {
	return len(s) >= len(substr) &&
		(s == substr ||
			len(substr) == 0 ||
			(len(s) > 0 && containsIgnoreCaseHelper(s, substr)))
}

func containsIgnoreCaseHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if equalFoldAt(s, i, substr) {
			return true
		}
	}
	return false
}

func equalFoldAt(s string, i int, substr string) bool {
	for j := 0; j < len(substr); j++ {
		c1 := s[i+j]
		c2 := substr[j]
		if c1 != c2 {
			// Simple ASCII case folding
			if c1 >= 'A' && c1 <= 'Z' {
				c1 += 'a' - 'A'
			}
			if c2 >= 'A' && c2 <= 'Z' {
				c2 += 'a' - 'A'
			}
			if c1 != c2 {
				return false
			}
		}
	}
	return true
}

// minDuration returns the smaller of two durations.
func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// Convenience methods that wrap common client operations with resilience

// Ping sends a ping to the daemon.
func (rc *ResilientClient) Ping() error {
	return rc.WithClient(func(c *Client) error {
		return c.Ping()
	})
}

// Info retrieves daemon information.
func (rc *ResilientClient) Info() (*DaemonInfo, error) {
	var info *DaemonInfo
	err := rc.WithClient(func(c *Client) error {
		var e error
		info, e = c.Info()
		return e
	})
	return info, err
}

// OverlaySet sets the overlay endpoint.
func (rc *ResilientClient) OverlaySet(endpoint string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.OverlaySet(endpoint)
		return e
	})
	return result, err
}

// ProxyStart starts a reverse proxy.
func (rc *ResilientClient) ProxyStart(id, targetURL string, port, maxLogSize int, path string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.ProxyStart(id, targetURL, port, maxLogSize, path)
		return e
	})
	return result, err
}

// ProxyStop stops a reverse proxy.
func (rc *ResilientClient) ProxyStop(id string) error {
	return rc.WithClient(func(c *Client) error {
		return c.ProxyStop(id)
	})
}

// ProxyList lists all proxies.
func (rc *ResilientClient) ProxyList(dirFilter protocol.DirectoryFilter) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.ProxyList(dirFilter)
		return e
	})
	return result, err
}

// Detect detects the project type at the given path.
func (rc *ResilientClient) Detect(path string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.Detect(path)
		return e
	})
	return result, err
}

// Run starts a process on the daemon.
func (rc *ResilientClient) Run(config protocol.RunConfig) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.Run(config)
		return e
	})
	return result, err
}

// ProcStatus gets the status of a process.
func (rc *ResilientClient) ProcStatus(processID string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.ProcStatus(processID)
		return e
	})
	return result, err
}

// ProcOutput gets the output of a process.
func (rc *ResilientClient) ProcOutput(processID string, filter protocol.OutputFilter) (string, error) {
	var output string
	err := rc.WithClient(func(c *Client) error {
		var e error
		output, e = c.ProcOutput(processID, filter)
		return e
	})
	return output, err
}

// ProcStop stops a process.
func (rc *ResilientClient) ProcStop(processID string, force bool) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.ProcStop(processID, force)
		return e
	})
	return result, err
}

// ProcList lists all processes.
func (rc *ResilientClient) ProcList(dirFilter protocol.DirectoryFilter) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.ProcList(dirFilter)
		return e
	})
	return result, err
}

// ProcCleanupPort kills processes on a specific port.
func (rc *ResilientClient) ProcCleanupPort(port int) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.ProcCleanupPort(port)
		return e
	})
	return result, err
}

// ProxyStartWithConfig starts a reverse proxy with extended configuration.
func (rc *ResilientClient) ProxyStartWithConfig(id, targetURL string, port, maxLogSize int, config ProxyStartConfig) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.ProxyStartWithConfig(id, targetURL, port, maxLogSize, config)
		return e
	})
	return result, err
}

// ProxyStatus gets the status of a proxy.
func (rc *ResilientClient) ProxyStatus(id string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.ProxyStatus(id)
		return e
	})
	return result, err
}

// ProxyExec executes JavaScript in connected browsers.
func (rc *ResilientClient) ProxyExec(id, code string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.ProxyExec(id, code)
		return e
	})
	return result, err
}

// ProxyToast sends a toast notification to connected browsers.
func (rc *ResilientClient) ProxyToast(id string, toast protocol.ToastConfig) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.ProxyToast(id, toast)
		return e
	})
	return result, err
}

// ProxyLogQuery queries proxy logs.
func (rc *ResilientClient) ProxyLogQuery(proxyID string, filter protocol.LogQueryFilter) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.ProxyLogQuery(proxyID, filter)
		return e
	})
	return result, err
}

// ProxyLogClear clears proxy logs.
func (rc *ResilientClient) ProxyLogClear(proxyID string) error {
	return rc.WithClient(func(c *Client) error {
		return c.ProxyLogClear(proxyID)
	})
}

// ProxyLogStats gets proxy log statistics.
func (rc *ResilientClient) ProxyLogStats(proxyID string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.ProxyLogStats(proxyID)
		return e
	})
	return result, err
}

// CurrentPageList lists active page sessions.
func (rc *ResilientClient) CurrentPageList(proxyID string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.CurrentPageList(proxyID)
		return e
	})
	return result, err
}

// CurrentPageGet gets details for a specific page session.
func (rc *ResilientClient) CurrentPageGet(proxyID, sessionID string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.CurrentPageGet(proxyID, sessionID)
		return e
	})
	return result, err
}

// CurrentPageClear clears page sessions.
func (rc *ResilientClient) CurrentPageClear(proxyID string) error {
	return rc.WithClient(func(c *Client) error {
		return c.CurrentPageClear(proxyID)
	})
}

// Chaos methods

// ChaosEnable enables chaos injection on a proxy.
func (rc *ResilientClient) ChaosEnable(proxyID string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.ChaosEnable(proxyID)
		return e
	})
	return result, err
}

// ChaosDisable disables chaos injection on a proxy.
func (rc *ResilientClient) ChaosDisable(proxyID string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.ChaosDisable(proxyID)
		return e
	})
	return result, err
}

// ChaosStatus gets the chaos status of a proxy.
func (rc *ResilientClient) ChaosStatus(proxyID string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.ChaosStatus(proxyID)
		return e
	})
	return result, err
}

// ChaosPreset applies a preset chaos configuration to a proxy.
func (rc *ResilientClient) ChaosPreset(proxyID, preset string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.ChaosPreset(proxyID, preset)
		return e
	})
	return result, err
}

// ChaosSet sets the full chaos configuration on a proxy.
func (rc *ResilientClient) ChaosSet(proxyID string, config protocol.ChaosConfigPayload) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.ChaosSet(proxyID, config)
		return e
	})
	return result, err
}

// ChaosAddRule adds a single rule to a proxy's chaos engine.
func (rc *ResilientClient) ChaosAddRule(proxyID string, rule protocol.ChaosRuleConfig) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.ChaosAddRule(proxyID, rule)
		return e
	})
	return result, err
}

// ChaosRemoveRule removes a rule from a proxy's chaos engine.
func (rc *ResilientClient) ChaosRemoveRule(proxyID, ruleID string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.ChaosRemoveRule(proxyID, ruleID)
		return e
	})
	return result, err
}

// ChaosListRules lists all chaos rules for a proxy.
func (rc *ResilientClient) ChaosListRules(proxyID string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.ChaosListRules(proxyID)
		return e
	})
	return result, err
}

// ChaosStats gets chaos statistics for a proxy.
func (rc *ResilientClient) ChaosStats(proxyID string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.ChaosStats(proxyID)
		return e
	})
	return result, err
}

// ChaosClear clears all chaos rules and resets stats for a proxy.
func (rc *ResilientClient) ChaosClear(proxyID string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.ChaosClear(proxyID)
		return e
	})
	return result, err
}

// ChaosListPresets returns the list of available chaos presets.
func (rc *ResilientClient) ChaosListPresets() (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.ChaosListPresets()
		return e
	})
	return result, err
}

// Tunnel methods

// TunnelStart starts a tunnel for a local port.
func (rc *ResilientClient) TunnelStart(config protocol.TunnelStartConfig) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.TunnelStart(config)
		return e
	})
	return result, err
}

// TunnelStop stops a running tunnel.
func (rc *ResilientClient) TunnelStop(id string) error {
	return rc.WithClient(func(c *Client) error {
		return c.TunnelStop(id)
	})
}

// TunnelStatus gets the status of a tunnel.
func (rc *ResilientClient) TunnelStatus(id string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.TunnelStatus(id)
		return e
	})
	return result, err
}

// TunnelList lists all active tunnels.
func (rc *ResilientClient) TunnelList() (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.TunnelList()
		return e
	})
	return result, err
}

// BroadcastActivity sends an activity state update to connected browsers via specified proxies.
// If proxyIDs is empty, broadcasts to all proxies (backward compatibility).
func (rc *ResilientClient) BroadcastActivity(active bool, proxyIDs ...string) error {
	return rc.WithClient(func(c *Client) error {
		return c.BroadcastActivity(active, proxyIDs...)
	})
}

// Session methods

// SessionRegister registers a new session with the daemon.
func (rc *ResilientClient) SessionRegister(code, overlayPath, projectPath, command string, args []string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.SessionRegister(code, overlayPath, projectPath, command, args)
		return e
	})
	return result, err
}

// SessionUnregister unregisters a session.
func (rc *ResilientClient) SessionUnregister(code string) error {
	return rc.WithClient(func(c *Client) error {
		return c.SessionUnregister(code)
	})
}

// SessionHeartbeat sends a heartbeat for a session.
func (rc *ResilientClient) SessionHeartbeat(code string) error {
	return rc.WithClient(func(c *Client) error {
		return c.SessionHeartbeat(code)
	})
}

// SessionList lists sessions, optionally filtered by directory.
func (rc *ResilientClient) SessionList(dirFilter protocol.DirectoryFilter) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.SessionList(dirFilter)
		return e
	})
	return result, err
}

// SessionGet retrieves details for a specific session.
func (rc *ResilientClient) SessionGet(code string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.SessionGet(code)
		return e
	})
	return result, err
}

// SessionSend sends a message to a session immediately.
func (rc *ResilientClient) SessionSend(code, message string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.SessionSend(code, message)
		return e
	})
	return result, err
}

// SessionSchedule schedules a message for future delivery.
func (rc *ResilientClient) SessionSchedule(code, duration, message string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.SessionSchedule(code, duration, message)
		return e
	})
	return result, err
}

// SessionCancel cancels a scheduled task.
func (rc *ResilientClient) SessionCancel(taskID string) error {
	return rc.WithClient(func(c *Client) error {
		return c.SessionCancel(taskID)
	})
}

// SessionTasks lists scheduled tasks, optionally filtered by directory.
func (rc *ResilientClient) SessionTasks(dirFilter protocol.DirectoryFilter) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.SessionTasks(dirFilter)
		return e
	})
	return result, err
}

// SessionGenerateCode generates a unique session code for a command.
func (rc *ResilientClient) SessionGenerateCode(command string) (string, error) {
	var code string
	err := rc.WithClient(func(c *Client) error {
		var e error
		code, e = c.SessionGenerateCode(command)
		return e
	})
	return code, err
}

// SessionFind finds a session by directory ancestry.
func (rc *ResilientClient) SessionFind(directory string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.SessionFind(directory)
		return e
	})
	return result, err
}

// SessionAttach attaches to a session found by directory ancestry.
func (rc *ResilientClient) SessionAttach(directory string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.SessionAttach(directory)
		return e
	})
	return result, err
}
