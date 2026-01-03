package daemon

import (
	"errors"
	"time"

	goclient "github.com/standardbeagle/go-cli-server/client"

	"github.com/standardbeagle/agnt/internal/protocol"
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
	autoStartCfg := goclient.AutoStartConfig{
		SocketPath:     config.AutoStartConfig.SocketPath,
		HubPath:        config.AutoStartConfig.DaemonPath,
		StartTimeout:   config.AutoStartConfig.StartTimeout,
		RetryInterval:  config.AutoStartConfig.RetryInterval,
		MaxRetries:     config.AutoStartConfig.MaxRetries,
		ProcessMatcher: isAgntDaemonProcess,
	}

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
			// Get daemon info
			var info DaemonInfo
			if err := conn.Request("INFO").JSONInto(&info); err != nil {
				return errors.New("failed to get daemon version: " + err.Error())
			}

			// Check if versions match
			if !goclient.VersionsMatch(config.ClientVersion, info.Version) {
				// Versions don't match - call callback if configured
				if config.OnVersionMismatch != nil {
					return config.OnVersionMismatch(config.ClientVersion, info.Version)
				}

				// No callback - stop the daemon so next connection uses new version
				_ = conn.Request("SHUTDOWN").OK() // Best effort

				return errors.New("version mismatch: client=" + config.ClientVersion +
					" daemon=" + info.Version + " (daemon stopped, will restart with new version)")
			}

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

// Convenience methods that wrap common client operations with resilience

// Ping sends a ping to the daemon.
func (rc *ResilientClient) Ping() error {
	return rc.rc.Ping()
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
func (rc *ResilientClient) TunnelList(dirFilter protocol.DirectoryFilter) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.TunnelList(dirFilter)
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

// Store methods

// StoreGet retrieves a value from the key-value store.
func (rc *ResilientClient) StoreGet(req protocol.StoreGetRequest) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.StoreGet(req)
		return e
	})
	return result, err
}

// StoreSet stores a value in the key-value store.
func (rc *ResilientClient) StoreSet(req protocol.StoreSetRequest) error {
	return rc.WithClient(func(c *Client) error {
		return c.StoreSet(req)
	})
}

// StoreDelete deletes a value from the key-value store.
func (rc *ResilientClient) StoreDelete(req protocol.StoreDeleteRequest) error {
	return rc.WithClient(func(c *Client) error {
		return c.StoreDelete(req)
	})
}

// StoreList lists all keys in a scope.
func (rc *ResilientClient) StoreList(req protocol.StoreListRequest) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.StoreList(req)
		return e
	})
	return result, err
}

// StoreClear clears all entries in a scope.
func (rc *ResilientClient) StoreClear(req protocol.StoreClearRequest) error {
	return rc.WithClient(func(c *Client) error {
		return c.StoreClear(req)
	})
}

// StoreGetAll retrieves all key-value pairs in a scope.
func (rc *ResilientClient) StoreGetAll(req protocol.StoreGetAllRequest) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := rc.WithClient(func(c *Client) error {
		var e error
		result, e = c.StoreGetAll(req)
		return e
	})
	return result, err
}
