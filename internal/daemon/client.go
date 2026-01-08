package daemon

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/go-cli-server/client"
)

// Re-export error types from go-cli-server for backward compatibility.
var (
	ErrNotConnected = client.ErrNotConnected
	ErrServerError  = client.ErrServerError
	// Note: ErrSocketNotFound is already exported from socket_compat.go via socket package
)

// Client is a client for communicating with the daemon over the socket.
// This wraps go-cli-server/client.Conn with agnt-specific methods.
type Client struct {
	conn *client.Conn
}

// clientConfig holds options for creating a client.
type clientConfig struct {
	socketPath string
	timeout    time.Duration
}

// ClientOption configures a Client.
type ClientOption func(*clientConfig)

// WithSocketPath sets the socket path for the client.
func WithSocketPath(path string) ClientOption {
	return func(c *clientConfig) {
		c.socketPath = path
	}
}

// WithTimeout sets the default timeout for operations.
func WithTimeout(d time.Duration) ClientOption {
	return func(c *clientConfig) {
		c.timeout = d
	}
}

// NewClient creates a new daemon client.
func NewClient(opts ...ClientOption) *Client {
	cfg := &clientConfig{
		socketPath: DefaultSocketPath(),
		timeout:    30 * time.Second,
	}

	for _, opt := range opts {
		opt(cfg)
	}

	conn := client.NewConn(
		client.WithSocketPath(cfg.socketPath),
		client.WithTimeout(cfg.timeout),
	)

	return &Client{conn: conn}
}

// NewClientWithPath creates a new daemon client with a specific socket path.
func NewClientWithPath(socketPath string) *Client {
	if socketPath == "" {
		socketPath = DefaultSocketPath()
	}
	return &Client{
		conn: client.NewConn(
			client.WithSocketPath(socketPath),
			client.WithTimeout(30*time.Second),
		),
	}
}

// Connect connects to the daemon.
func (c *Client) Connect() error {
	debug.Log("client", "Connect: socket=%s", c.conn.SocketPath())
	err := c.conn.EnsureConnected()
	if err != nil {
		debug.Log("client", "Connect failed: %v", err)
	}
	return err
}

// Close closes the connection to the daemon.
func (c *Client) Close() error {
	return c.conn.Close()
}

// IsConnected returns whether the client is connected.
func (c *Client) IsConnected() bool {
	return c.conn.IsConnected()
}

// SocketPath returns the socket path.
func (c *Client) SocketPath() string {
	return c.conn.SocketPath()
}

// Ping sends a ping to the daemon and waits for a pong response.
func (c *Client) Ping() error {
	return c.conn.Ping()
}

// Info retrieves daemon information.
// Uses STATUS command to get full daemon info (Hub's INFO is minimal).
// Falls back to INFO if STATUS is not available (for backwards compatibility).
func (c *Client) Info() (*DaemonInfo, error) {
	debug.Log("client", "Info: requesting daemon status")
	// Try STATUS first (returns full daemon info)
	result, err := c.conn.Request(protocol.VerbStatus).JSON()
	if err != nil {
		debug.Log("client", "STATUS failed, falling back to INFO: %v", err)
		// Fall back to INFO for older daemons that don't have STATUS
		result, err = c.conn.Request(protocol.VerbInfo).JSON()
		if err != nil {
			debug.Log("client", "Info failed: %v", err)
			return nil, err
		}
	}

	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal result: %w", err)
	}

	var info DaemonInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("failed to unmarshal info: %w", err)
	}

	return &info, nil
}

// Shutdown requests the daemon to shut down.
func (c *Client) Shutdown() error {
	return c.conn.Request(protocol.VerbShutdown).OK()
}

// Detect detects the project type at the given path.
func (c *Client) Detect(path string) (map[string]interface{}, error) {
	req := c.conn.Request(protocol.VerbDetect)
	if path != "" && path != "." {
		req = c.conn.Request(protocol.VerbDetect, path)
	}
	return req.JSON()
}

// Run starts a process on the daemon.
func (c *Client) Run(config protocol.RunConfig) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbRunJSON).WithJSON(config).JSON()
}

// ProcStatus gets the status of a process.
func (c *Client) ProcStatus(processID string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbProc, protocol.SubVerbStatus, processID).JSON()
}

// ProcOutput gets the output of a process.
func (c *Client) ProcOutput(processID string, filter protocol.OutputFilter) (string, error) {
	args := []string{protocol.SubVerbOutput, processID}

	// Add filter args
	if filter.Stream != "" && filter.Stream != "combined" {
		args = append(args, fmt.Sprintf("stream=%s", filter.Stream))
	}
	if filter.Tail > 0 {
		args = append(args, fmt.Sprintf("tail=%d", filter.Tail))
	}
	if filter.Head > 0 {
		args = append(args, fmt.Sprintf("head=%d", filter.Head))
	}
	if filter.Grep != "" {
		args = append(args, fmt.Sprintf("grep=%s", filter.Grep))
	}
	if filter.GrepV {
		args = append(args, "grep_v")
	}

	return c.conn.Request(protocol.VerbProc, args...).String()
}

// ProcStop stops a process.
func (c *Client) ProcStop(processID string, force bool) (map[string]interface{}, error) {
	args := []string{protocol.SubVerbStop, processID}
	if force {
		args = append(args, "force")
	}
	return c.conn.Request(protocol.VerbProc, args...).JSON()
}

// ProcList lists all processes.
func (c *Client) ProcList(dirFilter protocol.DirectoryFilter) (map[string]interface{}, error) {
	req := c.conn.Request(protocol.VerbProc, protocol.SubVerbList)
	if dirFilter.Directory != "" || dirFilter.Global {
		req = req.WithJSON(dirFilter)
	}
	return req.JSON()
}

// ProcCleanupPort kills processes on a specific port.
func (c *Client) ProcCleanupPort(port int) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbProc, protocol.SubVerbCleanupPort, fmt.Sprintf("%d", port)).JSON()
}

// ProxyStartConfig holds configuration for starting a proxy.
type ProxyStartConfig struct {
	Path        string                 `json:"path,omitempty"`
	BindAddress string                 `json:"bind_address,omitempty"`
	PublicURL   string                 `json:"public_url,omitempty"`
	VerifyTLS   bool                   `json:"verify_tls,omitempty"`
	Tunnel      *protocol.TunnelConfig `json:"tunnel,omitempty"`
}

// ProxyStart starts a reverse proxy.
func (c *Client) ProxyStart(id, targetURL string, port, maxLogSize int, path string) (map[string]interface{}, error) {
	return c.ProxyStartWithConfig(id, targetURL, port, maxLogSize, ProxyStartConfig{Path: path})
}

// ProxyStartWithConfig starts a reverse proxy with extended configuration.
func (c *Client) ProxyStartWithConfig(id, targetURL string, port, maxLogSize int, config ProxyStartConfig) (map[string]interface{}, error) {
	args := []string{protocol.SubVerbStart, id, targetURL, fmt.Sprintf("%d", port)}
	if maxLogSize > 0 {
		args = append(args, fmt.Sprintf("%d", maxLogSize))
	}
	return c.conn.Request(protocol.VerbProxy, args...).WithJSON(config).JSON()
}

// ProxyStop stops a reverse proxy.
func (c *Client) ProxyStop(id string) error {
	return c.conn.Request(protocol.VerbProxy, protocol.SubVerbStop, id).OK()
}

// ProxyStatus gets the status of a proxy.
func (c *Client) ProxyStatus(id string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbProxy, protocol.SubVerbStatus, id).JSON()
}

// ProxyList lists all proxies.
func (c *Client) ProxyList(dirFilter protocol.DirectoryFilter) (map[string]interface{}, error) {
	req := c.conn.Request(protocol.VerbProxy, protocol.SubVerbList)
	if dirFilter.Directory != "" || dirFilter.Global {
		req = req.WithJSON(dirFilter)
	}
	return req.JSON()
}

// ProxyExec executes JavaScript in connected browsers.
func (c *Client) ProxyExec(id, code string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbProxy, protocol.SubVerbExec, id).WithData([]byte(code)).JSON()
}

// ProxyToast sends a toast notification to connected browsers.
func (c *Client) ProxyToast(id string, toast protocol.ToastConfig) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbProxy, protocol.SubVerbToast, id).WithJSON(toast).JSON()
}

// ProxyLogQuery queries proxy logs.
func (c *Client) ProxyLogQuery(proxyID string, filter protocol.LogQueryFilter) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbProxyLog, protocol.SubVerbQuery, proxyID).WithJSON(filter).JSON()
}

// ProxyLogClear clears proxy logs.
func (c *Client) ProxyLogClear(proxyID string) error {
	return c.conn.Request(protocol.VerbProxyLog, protocol.SubVerbClear, proxyID).OK()
}

// ProxyLogStats gets proxy log statistics.
func (c *Client) ProxyLogStats(proxyID string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbProxyLog, protocol.SubVerbStats, proxyID).JSON()
}

// CurrentPageList lists active page sessions.
func (c *Client) CurrentPageList(proxyID string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbCurrentPage, protocol.SubVerbList, proxyID).JSON()
}

// CurrentPageGet gets details for a specific page session.
func (c *Client) CurrentPageGet(proxyID, sessionID string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbCurrentPage, protocol.SubVerbGet, proxyID, sessionID).JSON()
}

// CurrentPageClear clears page sessions.
func (c *Client) CurrentPageClear(proxyID string) error {
	return c.conn.Request(protocol.VerbCurrentPage, protocol.SubVerbClear, proxyID).OK()
}

// OverlaySet sets the overlay endpoint URL.
func (c *Client) OverlaySet(endpoint string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbOverlay, protocol.SubVerbSet, endpoint).JSON()
}

// OverlayGet gets the current overlay endpoint configuration.
func (c *Client) OverlayGet() (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbOverlay, protocol.SubVerbGet).JSON()
}

// OverlayClear clears the overlay endpoint configuration.
func (c *Client) OverlayClear() error {
	return c.conn.Request(protocol.VerbOverlay, protocol.SubVerbClear).OK()
}

// BroadcastActivity broadcasts an activity state update to connected browsers via specified proxies.
func (c *Client) BroadcastActivity(active bool, proxyIDs ...string) error {
	activeStr := "false"
	if active {
		activeStr = "true"
	}
	args := append([]string{protocol.SubVerbActivity, activeStr}, proxyIDs...)
	_, err := c.conn.Request(protocol.VerbOverlay, args...).JSON()
	return err
}

// BroadcastOutputPreview broadcasts output preview lines to connected browsers via proxies.
func (c *Client) BroadcastOutputPreview(lines []string, proxyIDs ...string) error {
	payload := map[string]interface{}{
		"lines":     lines,
		"proxy_ids": proxyIDs,
	}
	_, err := c.conn.Request(protocol.VerbOverlay, protocol.SubVerbOutputPreview).WithJSON(payload).JSON()
	return err
}

// TunnelStart starts a tunnel for a local port.
func (c *Client) TunnelStart(config protocol.TunnelStartConfig) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbTunnel, protocol.SubVerbStart).WithJSON(config).JSON()
}

// TunnelStop stops a running tunnel.
func (c *Client) TunnelStop(id string) error {
	return c.conn.Request(protocol.VerbTunnel, protocol.SubVerbStop, id).OK()
}

// TunnelStatus gets the status of a tunnel.
func (c *Client) TunnelStatus(id string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbTunnel, protocol.SubVerbStatus, id).JSON()
}

// TunnelList lists all active tunnels.
func (c *Client) TunnelList(dirFilter protocol.DirectoryFilter) (map[string]interface{}, error) {
	req := c.conn.Request(protocol.VerbTunnel, protocol.SubVerbList)
	if dirFilter.Directory != "" || dirFilter.Global {
		req = req.WithJSON(dirFilter)
	}
	return req.JSON()
}

// ChaosEnable enables chaos injection on a proxy.
func (c *Client) ChaosEnable(proxyID string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbChaos, protocol.SubVerbEnable, proxyID).JSON()
}

// ChaosDisable disables chaos injection on a proxy.
func (c *Client) ChaosDisable(proxyID string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbChaos, protocol.SubVerbDisable, proxyID).JSON()
}

// ChaosStatus gets the chaos status of a proxy.
func (c *Client) ChaosStatus(proxyID string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbChaos, protocol.SubVerbStatus, proxyID).JSON()
}

// ChaosPreset applies a preset chaos configuration to a proxy.
func (c *Client) ChaosPreset(proxyID, preset string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbChaos, protocol.SubVerbPreset, proxyID, preset).JSON()
}

// ChaosSet sets the full chaos configuration on a proxy.
func (c *Client) ChaosSet(proxyID string, config protocol.ChaosConfigPayload) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbChaos, protocol.SubVerbSet, proxyID).WithJSON(config).JSON()
}

// ChaosAddRule adds a single rule to a proxy's chaos engine.
func (c *Client) ChaosAddRule(proxyID string, rule protocol.ChaosRuleConfig) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbChaos, protocol.SubVerbAddRule, proxyID).WithJSON(rule).JSON()
}

// ChaosRemoveRule removes a rule from a proxy's chaos engine.
func (c *Client) ChaosRemoveRule(proxyID, ruleID string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbChaos, protocol.SubVerbRemoveRule, proxyID, ruleID).JSON()
}

// ChaosListRules lists all chaos rules for a proxy.
func (c *Client) ChaosListRules(proxyID string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbChaos, protocol.SubVerbListRules, proxyID).JSON()
}

// ChaosStats gets chaos statistics for a proxy.
func (c *Client) ChaosStats(proxyID string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbChaos, protocol.SubVerbStats, proxyID).JSON()
}

// ChaosClear clears all chaos rules and resets stats for a proxy.
func (c *Client) ChaosClear(proxyID string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbChaos, protocol.SubVerbClear, proxyID).JSON()
}

// ChaosListPresets returns the list of available chaos presets.
func (c *Client) ChaosListPresets() (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbChaos, "LIST-PRESETS").JSON()
}

// SessionRegister registers a new session with the daemon.
func (c *Client) SessionRegister(code string, overlayPath string, projectPath string, command string, args []string) (map[string]interface{}, error) {
	metadata := protocol.SessionRegisterConfig{
		OverlayPath: overlayPath,
		ProjectPath: projectPath,
		Command:     command,
		Args:        args,
	}
	return c.conn.Request(protocol.VerbSession, protocol.SubVerbRegister, code, overlayPath).WithJSON(metadata).JSON()
}

// SessionUnregister unregisters a session from the daemon.
func (c *Client) SessionUnregister(code string) error {
	return c.conn.Request(protocol.VerbSession, protocol.SubVerbUnregister, code).OK()
}

// SessionHeartbeat sends a heartbeat for a session.
func (c *Client) SessionHeartbeat(code string) error {
	return c.conn.Request(protocol.VerbSession, protocol.SubVerbHeartbeat, code).OK()
}

// SessionList lists active sessions.
func (c *Client) SessionList(dirFilter protocol.DirectoryFilter) (map[string]interface{}, error) {
	req := c.conn.Request(protocol.VerbSession, protocol.SubVerbList)
	if dirFilter.Directory != "" || dirFilter.Global {
		req = req.WithJSON(dirFilter)
	}
	return req.JSON()
}

// SessionGet retrieves a specific session.
func (c *Client) SessionGet(code string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbSession, protocol.SubVerbGet, code).JSON()
}

// SessionSend sends an immediate message to a session.
func (c *Client) SessionSend(code string, message string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbSession, protocol.SubVerbSend, code).WithData([]byte(message)).JSON()
}

// SessionSchedule schedules a message for future delivery.
func (c *Client) SessionSchedule(code string, duration string, message string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbSession, protocol.SubVerbSchedule, code, duration).WithData([]byte(message)).JSON()
}

// SessionCancel cancels a scheduled task.
func (c *Client) SessionCancel(taskID string) error {
	return c.conn.Request(protocol.VerbSession, protocol.SubVerbCancel, taskID).OK()
}

// SessionTasks lists scheduled tasks.
func (c *Client) SessionTasks(dirFilter protocol.DirectoryFilter) (map[string]interface{}, error) {
	req := c.conn.Request(protocol.VerbSession, protocol.SubVerbTasks)
	if dirFilter.Directory != "" || dirFilter.Global {
		req = req.WithJSON(dirFilter)
	}
	return req.JSON()
}

// SessionGenerateCode requests a new session code from the daemon.
func (c *Client) SessionGenerateCode(command string) (string, error) {
	return fmt.Sprintf("%s-%d", command, time.Now().UnixNano()%10000), nil
}

// SessionFind finds a session by directory ancestry.
func (c *Client) SessionFind(directory string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbSession, protocol.SubVerbFind, directory).JSON()
}

// SessionAttach attaches to a session found by directory ancestry.
func (c *Client) SessionAttach(directory string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbSession, protocol.SubVerbAttach, directory).JSON()
}

// SessionURL reports a detected URL from an agnt run session.
// This triggers proxy creation for any matching proxy configurations.
func (c *Client) SessionURL(code string, url string, scriptName string) (map[string]interface{}, error) {
	req := c.conn.Request(protocol.VerbSession, protocol.SubVerbURL, code, url)
	if scriptName != "" {
		req = req.WithJSON(map[string]string{"script": scriptName})
	}
	return req.JSON()
}

// StoreGet retrieves a value from the key-value store.
func (c *Client) StoreGet(req protocol.StoreGetRequest) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbStore, protocol.SubVerbGet).WithJSON(req).JSON()
}

// StoreSet stores a value in the key-value store.
func (c *Client) StoreSet(req protocol.StoreSetRequest) error {
	return c.conn.Request(protocol.VerbStore, protocol.SubVerbSet).WithJSON(req).OK()
}

// StoreDelete deletes a value from the key-value store.
func (c *Client) StoreDelete(req protocol.StoreDeleteRequest) error {
	return c.conn.Request(protocol.VerbStore, protocol.SubVerbDelete).WithJSON(req).OK()
}

// StoreList lists all keys in a scope.
func (c *Client) StoreList(req protocol.StoreListRequest) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbStore, protocol.SubVerbList).WithJSON(req).JSON()
}

// StoreClear clears all entries in a scope.
func (c *Client) StoreClear(req protocol.StoreClearRequest) error {
	return c.conn.Request(protocol.VerbStore, protocol.SubVerbClear).WithJSON(req).OK()
}

// StoreGetAll retrieves all key-value pairs in a scope.
func (c *Client) StoreGetAll(req protocol.StoreGetAllRequest) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbStore, protocol.SubVerbGetAll).WithJSON(req).JSON()
}

// StopAll stops all running processes, proxies, and tunnels.
func (c *Client) StopAll() (map[string]interface{}, error) {
	return c.conn.Request("STOP-ALL").JSON()
}

// RestartAll restarts all running processes and proxies with their original configurations.
func (c *Client) RestartAll() (map[string]interface{}, error) {
	return c.conn.Request("RESTART-ALL").JSON()
}

// ProcRestart restarts a single process by ID.
func (c *Client) ProcRestart(processID string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbProc, protocol.SubVerbRestart, processID).JSON()
}

// ProxyRestart restarts a single proxy by ID.
func (c *Client) ProxyRestart(id string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbProxy, protocol.SubVerbRestart, id).JSON()
}
