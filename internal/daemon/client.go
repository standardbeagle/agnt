package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
)

var (
	// ErrNotConnected is returned when trying to use a closed client.
	ErrNotConnected = errors.New("not connected to daemon")
	// ErrServerError is returned when the daemon returns an error response.
	ErrServerError = errors.New("daemon error")
)

// Client is a client for communicating with the daemon over the socket.
type Client struct {
	conn   net.Conn
	parser *protocol.Parser
	writer *protocol.Writer

	mu     sync.Mutex // Protects connection state
	closed bool

	// Options
	socketPath string
	timeout    time.Duration
}

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithSocketPath sets the socket path for the client.
func WithSocketPath(path string) ClientOption {
	return func(c *Client) {
		c.socketPath = path
	}
}

// WithTimeout sets the default timeout for operations.
func WithTimeout(d time.Duration) ClientOption {
	return func(c *Client) {
		c.timeout = d
	}
}

// NewClient creates a new daemon client.
func NewClient(opts ...ClientOption) *Client {
	c := &Client{
		socketPath: DefaultSocketPath(),
		timeout:    30 * time.Second,
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// Connect connects to the daemon.
func (c *Client) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil && !c.closed {
		return nil // Already connected
	}

	conn, err := Connect(c.socketPath)
	if err != nil {
		return err
	}

	c.conn = conn
	c.parser = protocol.NewParser(conn)
	c.writer = protocol.NewWriter(conn)
	c.closed = false

	return nil
}

// Close closes the connection to the daemon.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed || c.conn == nil {
		return nil
	}

	c.closed = true
	return c.conn.Close()
}

// IsConnected returns whether the client is connected.
func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil && !c.closed
}

// Ping sends a ping to the daemon and waits for a pong response.
func (c *Client) Ping() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed || c.conn == nil {
		return ErrNotConnected
	}

	// Send PING
	if err := c.writer.WriteCommand(protocol.VerbPing, nil, nil); err != nil {
		return fmt.Errorf("failed to send ping: %w", err)
	}

	// Read response
	resp, err := c.parser.ParseResponse()
	if err != nil {
		return fmt.Errorf("failed to read pong: %w", err)
	}

	if resp.Type != protocol.ResponsePong {
		return fmt.Errorf("expected PONG, got %s", resp.Type)
	}

	return nil
}

// Info retrieves daemon information.
func (c *Client) Info() (*DaemonInfo, error) {
	data, err := c.sendCommand(protocol.VerbInfo, nil, nil, nil)
	if err != nil {
		return nil, err
	}

	var info DaemonInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("failed to unmarshal info: %w", err)
	}

	return &info, nil
}

// Shutdown requests the daemon to shut down.
func (c *Client) Shutdown() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed || c.conn == nil {
		return ErrNotConnected
	}

	// Send SHUTDOWN
	if err := c.writer.WriteCommand(protocol.VerbShutdown, nil, nil); err != nil {
		return fmt.Errorf("failed to send shutdown: %w", err)
	}

	// Read response
	resp, err := c.parser.ParseResponse()
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.Type == protocol.ResponseErr {
		return fmt.Errorf("%w: %s", ErrServerError, resp.Message)
	}

	return nil
}

// Detect detects the project type at the given path.
func (c *Client) Detect(path string) (map[string]interface{}, error) {
	var args []string
	if path != "" && path != "." {
		args = []string{path}
	}

	data, err := c.sendCommand(protocol.VerbDetect, args, nil, nil)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
}

// Run starts a process on the daemon.
func (c *Client) Run(config protocol.RunConfig) (map[string]interface{}, error) {
	// Use JSON mode for complex config
	data, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config: %w", err)
	}

	result, err := c.sendCommand(protocol.VerbRunJSON, nil, nil, data)
	if err != nil {
		return nil, err
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return resp, nil
}

// ProcStatus gets the status of a process.
func (c *Client) ProcStatus(processID string) (map[string]interface{}, error) {
	data, err := c.sendCommand(protocol.VerbProc, []string{protocol.SubVerbStatus, processID}, nil, nil)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
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

	// Output uses chunked responses
	output, err := c.sendCommandChunked(protocol.VerbProc, args, nil, nil)
	if err != nil {
		return "", err
	}

	return string(output), nil
}

// ProcStop stops a process.
func (c *Client) ProcStop(processID string, force bool) (map[string]interface{}, error) {
	args := []string{protocol.SubVerbStop, processID}
	if force {
		args = append(args, "force")
	}

	data, err := c.sendCommand(protocol.VerbProc, args, nil, nil)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
}

// ProcList lists all processes.
func (c *Client) ProcList(dirFilter protocol.DirectoryFilter) (map[string]interface{}, error) {
	var data []byte
	var err error

	// If we have a directory filter, encode it as JSON
	if dirFilter.Directory != "" || dirFilter.Global {
		data, err = json.Marshal(dirFilter)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal directory filter: %w", err)
		}
	}

	resultData, err := c.sendCommand(protocol.VerbProc, []string{protocol.SubVerbList}, nil, data)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resultData, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
}

// ProcCleanupPort kills processes on a specific port.
func (c *Client) ProcCleanupPort(port int) (map[string]interface{}, error) {
	data, err := c.sendCommand(protocol.VerbProc, []string{protocol.SubVerbCleanupPort, fmt.Sprintf("%d", port)}, nil, nil)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
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

	// Encode config in JSON data
	data, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config: %w", err)
	}

	resultData, err := c.sendCommand(protocol.VerbProxy, args, nil, data)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resultData, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
}

// ProxyStop stops a reverse proxy.
func (c *Client) ProxyStop(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed || c.conn == nil {
		return ErrNotConnected
	}

	// Send command
	if err := c.writer.WriteCommand(protocol.VerbProxy, []string{protocol.SubVerbStop, id}, nil); err != nil {
		return fmt.Errorf("failed to send command: %w", err)
	}

	// Read response
	resp, err := c.parser.ParseResponse()
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.Type == protocol.ResponseErr {
		return fmt.Errorf("%w: [%s] %s", ErrServerError, resp.Code, resp.Message)
	}

	return nil
}

// ProxyStatus gets the status of a proxy.
func (c *Client) ProxyStatus(id string) (map[string]interface{}, error) {
	data, err := c.sendCommand(protocol.VerbProxy, []string{protocol.SubVerbStatus, id}, nil, nil)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
}

// ProxyList lists all proxies.
func (c *Client) ProxyList(dirFilter protocol.DirectoryFilter) (map[string]interface{}, error) {
	var data []byte
	var err error

	// If we have a directory filter, encode it as JSON
	if dirFilter.Directory != "" || dirFilter.Global {
		data, err = json.Marshal(dirFilter)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal directory filter: %w", err)
		}
	}

	resultData, err := c.sendCommand(protocol.VerbProxy, []string{protocol.SubVerbList}, nil, data)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resultData, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
}

// ProxyExec executes JavaScript in connected browsers.
func (c *Client) ProxyExec(id, code string) (map[string]interface{}, error) {
	data, err := c.sendCommand(protocol.VerbProxy, []string{protocol.SubVerbExec, id}, nil, []byte(code))
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
}

// ProxyToast sends a toast notification to connected browsers.
func (c *Client) ProxyToast(id string, toast protocol.ToastConfig) (map[string]interface{}, error) {
	toastData, err := json.Marshal(toast)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal toast config: %w", err)
	}

	data, err := c.sendCommand(protocol.VerbProxy, []string{protocol.SubVerbToast, id}, nil, toastData)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
}

// ProxyLogQuery queries proxy logs.
func (c *Client) ProxyLogQuery(proxyID string, filter protocol.LogQueryFilter) (map[string]interface{}, error) {
	filterData, err := json.Marshal(filter)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal filter: %w", err)
	}

	data, err := c.sendCommand(protocol.VerbProxyLog, []string{protocol.SubVerbQuery, proxyID}, nil, filterData)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
}

// ProxyLogClear clears proxy logs.
func (c *Client) ProxyLogClear(proxyID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed || c.conn == nil {
		return ErrNotConnected
	}

	// Send command
	if err := c.writer.WriteCommand(protocol.VerbProxyLog, []string{protocol.SubVerbClear, proxyID}, nil); err != nil {
		return fmt.Errorf("failed to send command: %w", err)
	}

	// Read response
	resp, err := c.parser.ParseResponse()
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.Type == protocol.ResponseErr {
		return fmt.Errorf("%w: [%s] %s", ErrServerError, resp.Code, resp.Message)
	}

	return nil
}

// ProxyLogStats gets proxy log statistics.
func (c *Client) ProxyLogStats(proxyID string) (map[string]interface{}, error) {
	data, err := c.sendCommand(protocol.VerbProxyLog, []string{protocol.SubVerbStats, proxyID}, nil, nil)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
}

// CurrentPageList lists active page sessions.
func (c *Client) CurrentPageList(proxyID string) (map[string]interface{}, error) {
	data, err := c.sendCommand(protocol.VerbCurrentPage, []string{protocol.SubVerbList, proxyID}, nil, nil)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
}

// CurrentPageGet gets details for a specific page session.
func (c *Client) CurrentPageGet(proxyID, sessionID string) (map[string]interface{}, error) {
	data, err := c.sendCommand(protocol.VerbCurrentPage, []string{protocol.SubVerbGet, proxyID, sessionID}, nil, nil)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
}

// CurrentPageClear clears page sessions.
func (c *Client) CurrentPageClear(proxyID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed || c.conn == nil {
		return ErrNotConnected
	}

	// Send command
	if err := c.writer.WriteCommand(protocol.VerbCurrentPage, []string{protocol.SubVerbClear, proxyID}, nil); err != nil {
		return fmt.Errorf("failed to send command: %w", err)
	}

	// Read response
	resp, err := c.parser.ParseResponse()
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.Type == protocol.ResponseErr {
		return fmt.Errorf("%w: [%s] %s", ErrServerError, resp.Code, resp.Message)
	}

	return nil
}

// OverlaySet sets the overlay endpoint URL.
// The endpoint should be the full URL, e.g., "http://127.0.0.1:19191".
func (c *Client) OverlaySet(endpoint string) (map[string]interface{}, error) {
	data, err := c.sendCommand(protocol.VerbOverlay, []string{protocol.SubVerbSet, endpoint}, nil, nil)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
}

// OverlayGet gets the current overlay endpoint configuration.
func (c *Client) OverlayGet() (map[string]interface{}, error) {
	data, err := c.sendCommand(protocol.VerbOverlay, []string{protocol.SubVerbGet}, nil, nil)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
}

// OverlayClear clears the overlay endpoint configuration.
func (c *Client) OverlayClear() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed || c.conn == nil {
		return ErrNotConnected
	}

	// Send command
	if err := c.writer.WriteCommand(protocol.VerbOverlay, []string{protocol.SubVerbClear}, nil); err != nil {
		return fmt.Errorf("failed to send command: %w", err)
	}

	// Read response
	resp, err := c.parser.ParseResponse()
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.Type == protocol.ResponseErr {
		return fmt.Errorf("%w: [%s] %s", ErrServerError, resp.Code, resp.Message)
	}

	return nil
}

// BroadcastActivity broadcasts an activity state update to connected browsers via specified proxies.
// If proxyIDs is empty, broadcasts to all proxies (backward compatibility).
func (c *Client) BroadcastActivity(active bool, proxyIDs ...string) error {
	activeStr := "false"
	if active {
		activeStr = "true"
	}
	args := append([]string{protocol.SubVerbActivity, activeStr}, proxyIDs...)
	_, err := c.sendCommand(protocol.VerbOverlay, args, nil, nil)
	return err
}

// TunnelStart starts a tunnel for a local port.
func (c *Client) TunnelStart(config protocol.TunnelStartConfig) (map[string]interface{}, error) {
	data, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config: %w", err)
	}

	resultData, err := c.sendCommand(protocol.VerbTunnel, []string{protocol.SubVerbStart}, nil, data)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resultData, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
}

// TunnelStop stops a running tunnel.
func (c *Client) TunnelStop(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed || c.conn == nil {
		return ErrNotConnected
	}

	// Send command
	if err := c.writer.WriteCommand(protocol.VerbTunnel, []string{protocol.SubVerbStop, id}, nil); err != nil {
		return fmt.Errorf("failed to send command: %w", err)
	}

	// Read response
	resp, err := c.parser.ParseResponse()
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.Type == protocol.ResponseErr {
		return fmt.Errorf("%w: [%s] %s", ErrServerError, resp.Code, resp.Message)
	}

	return nil
}

// TunnelStatus gets the status of a tunnel.
func (c *Client) TunnelStatus(id string) (map[string]interface{}, error) {
	data, err := c.sendCommand(protocol.VerbTunnel, []string{protocol.SubVerbStatus, id}, nil, nil)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
}

// TunnelList lists all active tunnels.
func (c *Client) TunnelList() (map[string]interface{}, error) {
	data, err := c.sendCommand(protocol.VerbTunnel, []string{protocol.SubVerbList}, nil, nil)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
}

// ChaosEnable enables chaos injection on a proxy.
func (c *Client) ChaosEnable(proxyID string) (map[string]interface{}, error) {
	data, err := c.sendCommand(protocol.VerbChaos, []string{protocol.SubVerbEnable, proxyID}, nil, nil)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
}

// ChaosDisable disables chaos injection on a proxy.
func (c *Client) ChaosDisable(proxyID string) (map[string]interface{}, error) {
	data, err := c.sendCommand(protocol.VerbChaos, []string{protocol.SubVerbDisable, proxyID}, nil, nil)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
}

// ChaosStatus gets the chaos status of a proxy.
func (c *Client) ChaosStatus(proxyID string) (map[string]interface{}, error) {
	data, err := c.sendCommand(protocol.VerbChaos, []string{protocol.SubVerbStatus, proxyID}, nil, nil)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
}

// ChaosPreset applies a preset chaos configuration to a proxy.
func (c *Client) ChaosPreset(proxyID, preset string) (map[string]interface{}, error) {
	data, err := c.sendCommand(protocol.VerbChaos, []string{protocol.SubVerbPreset, proxyID, preset}, nil, nil)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
}

// ChaosSet sets the full chaos configuration on a proxy.
func (c *Client) ChaosSet(proxyID string, config protocol.ChaosConfigPayload) (map[string]interface{}, error) {
	configData, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config: %w", err)
	}

	data, err := c.sendCommand(protocol.VerbChaos, []string{protocol.SubVerbSet, proxyID}, nil, configData)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
}

// ChaosAddRule adds a single rule to a proxy's chaos engine.
func (c *Client) ChaosAddRule(proxyID string, rule protocol.ChaosRuleConfig) (map[string]interface{}, error) {
	ruleData, err := json.Marshal(rule)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal rule: %w", err)
	}

	data, err := c.sendCommand(protocol.VerbChaos, []string{protocol.SubVerbAddRule, proxyID}, nil, ruleData)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
}

// ChaosRemoveRule removes a rule from a proxy's chaos engine.
func (c *Client) ChaosRemoveRule(proxyID, ruleID string) (map[string]interface{}, error) {
	data, err := c.sendCommand(protocol.VerbChaos, []string{protocol.SubVerbRemoveRule, proxyID, ruleID}, nil, nil)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
}

// ChaosListRules lists all chaos rules for a proxy.
func (c *Client) ChaosListRules(proxyID string) (map[string]interface{}, error) {
	data, err := c.sendCommand(protocol.VerbChaos, []string{protocol.SubVerbListRules, proxyID}, nil, nil)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
}

// ChaosStats gets chaos statistics for a proxy.
func (c *Client) ChaosStats(proxyID string) (map[string]interface{}, error) {
	data, err := c.sendCommand(protocol.VerbChaos, []string{protocol.SubVerbStats, proxyID}, nil, nil)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
}

// ChaosClear clears all chaos rules and resets stats for a proxy.
func (c *Client) ChaosClear(proxyID string) (map[string]interface{}, error) {
	data, err := c.sendCommand(protocol.VerbChaos, []string{protocol.SubVerbClear, proxyID}, nil, nil)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
}

// ChaosListPresets returns the list of available chaos presets.
func (c *Client) ChaosListPresets() (map[string]interface{}, error) {
	data, err := c.sendCommand(protocol.VerbChaos, []string{"LIST-PRESETS"}, nil, nil)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result, nil
}

// sendCommand sends a command and expects a JSON response.
func (c *Client) sendCommand(verb string, args []string, subVerb *string, data []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed || c.conn == nil {
		return nil, ErrNotConnected
	}

	// Send command
	if err := c.writer.WriteCommandWithData(verb, args, subVerb, data); err != nil {
		return nil, fmt.Errorf("failed to send command: %w", err)
	}

	// Read response
	resp, err := c.parser.ParseResponse()
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.Type == protocol.ResponseErr {
		return nil, fmt.Errorf("%w: [%s] %s", ErrServerError, resp.Code, resp.Message)
	}

	if resp.Type != protocol.ResponseJSON {
		return nil, fmt.Errorf("expected JSON response, got %s", resp.Type)
	}

	return resp.Data, nil
}

// sendCommandChunked sends a command and collects chunked response data.
func (c *Client) sendCommandChunked(verb string, args []string, subVerb *string, data []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed || c.conn == nil {
		return nil, ErrNotConnected
	}

	// Send command
	if err := c.writer.WriteCommandWithData(verb, args, subVerb, data); err != nil {
		return nil, fmt.Errorf("failed to send command: %w", err)
	}

	// Read chunked response
	var result []byte
	for {
		resp, err := c.parser.ParseResponse()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("failed to read response: %w", err)
		}

		switch resp.Type {
		case protocol.ResponseChunk:
			result = append(result, resp.Data...)
		case protocol.ResponseEnd:
			return result, nil
		case protocol.ResponseErr:
			return nil, fmt.Errorf("%w: [%s] %s", ErrServerError, resp.Code, resp.Message)
		default:
			return nil, fmt.Errorf("unexpected response type: %s", resp.Type)
		}
	}

	return result, nil
}
