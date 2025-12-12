package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"devtool-mcp/internal/protocol"
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

// ProxyStart starts a reverse proxy.
func (c *Client) ProxyStart(id, targetURL string, port, maxLogSize int, path string) (map[string]interface{}, error) {
	args := []string{protocol.SubVerbStart, id, targetURL, fmt.Sprintf("%d", port)}
	if maxLogSize > 0 {
		args = append(args, fmt.Sprintf("%d", maxLogSize))
	}

	// Encode path in JSON data
	data, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal path: %w", err)
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
